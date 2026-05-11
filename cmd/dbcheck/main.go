// dbcheck is a CLI utility for inspecting task and session state.
//
// Modes:
//
//	--orphan-yaml   list tasks/*.yaml files that are NOT registered in DB
//	                (parse failure, disabled, or soft-deleted). Use this when
//	                you suspect a task "isn't running" — if the file is
//	                orphaned, the server logs will say why.
//
//	--stale-tasks   list enabled tasks whose last_run_at is more than one
//	                cron interval behind the expected next run. Catches the
//	                "registered but stopped executing" class of failure.
//
//	(default)       legacy behaviour: dump yzk_worker sessions + messages.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/db"
	"github.com/kid0317/cc-workspace-bot/internal/model"
	"github.com/kid0317/cc-workspace-bot/internal/task"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	orphan := flag.Bool("orphan-yaml", false, "list YAML files not registered in DB")
	stale := flag.Bool("stale-tasks", false, "list enabled tasks whose last_run_at is overdue")
	flag.Parse()

	database, err := db.Open("bot.db")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open bot.db:", err)
		os.Exit(1)
	}
	// Silence gorm's default "record not found" warnings — the report already
	// explains what missing means; the duplicate log line is noise.
	database.Logger = database.Logger.LogMode(logger.Silent)

	switch {
	case *orphan:
		cfg := mustLoadConfig(*cfgPath)
		runOrphanYAML(database, cfg)
	case *stale:
		runStaleTasks(database)
	default:
		runLegacyDump(database)
	}
}

func mustLoadConfig(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}
	return cfg
}

// ── orphan-yaml mode ─────────────────────────────────────────────────────────

func runOrphanYAML(database *gorm.DB, cfg *config.Config) {
	fmt.Println("=== Orphan YAML Report ===")
	fmt.Println("Every *.yaml in a tasks/ directory is cross-checked against the tasks table.")
	fmt.Println()

	total := 0
	orphaned := 0
	for _, app := range cfg.Apps {
		tasksDir := filepath.Join(app.WorkspaceDir, "tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Printf("[%s] error reading %s: %v\n", app.ID, tasksDir, err)
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			total++
			path := filepath.Join(tasksDir, e.Name())
			reason := diagnoseYAML(database, app.ID, path)
			if reason == "" {
				continue // healthy; registered and enabled
			}
			orphaned++
			fmt.Printf("  [%s] %s\n    reason: %s\n", app.ID, path, reason)
		}
	}
	fmt.Println()
	fmt.Printf("Checked %d YAML files, %d orphaned.\n", total, orphaned)
}

// diagnoseYAML returns an empty string if the YAML is healthy or intentionally
// disabled (enabled=false in both YAML and DB). Otherwise returns a short
// human-readable reason that indicates a real problem.
//
// The goal is "find silently failing tasks", not "report every YAML state".
// Intentionally disabled tasks are user intent, not bugs.
func diagnoseYAML(database *gorm.DB, appID, path string) string {
	t, err := task.LoadYAML(path, appID)
	if err != nil {
		return "parse failed: " + err.Error()
	}

	var existing model.Task
	dbErr := database.Unscoped().Where("id = ?", t.ID).First(&existing).Error
	if dbErr != nil {
		// File parses but no DB row — e.g. a template bug that the old server
		// rejected and never registered.
		if !t.Enabled {
			return "" // YAML says disabled and it isn't registered: consistent, not an orphan
		}
		return "not in DB (file parses but was never registered)"
	}
	if existing.DeletedAt.Valid && t.Enabled {
		return fmt.Sprintf("soft-deleted at %s but YAML still enabled", existing.DeletedAt.Time.Format(time.RFC3339))
	}
	// YAML and DB disagree about enabled — a silent-failure-class mismatch.
	if t.Enabled != existing.Enabled {
		return fmt.Sprintf("enabled mismatch: YAML=%v DB=%v", t.Enabled, existing.Enabled)
	}
	return ""
}

// ── stale-tasks mode ─────────────────────────────────────────────────────────

// runStaleTasks lists enabled tasks that appear to have stopped running. A
// task is flagged stale if more than one full cron interval has elapsed past
// its expected next run, i.e. now > schedule.Next(schedule.Next(last_run)).
// Never-run tasks (last_run_at IS NULL) are reported separately so the
// operator can tell "registered but never fired" from "used to work".
func runStaleTasks(database *gorm.DB) {
	fmt.Println("=== Stale Task Report ===")
	fmt.Println("Tasks whose last_run_at is more than one cron interval behind schedule.")
	fmt.Println()

	var tasks []model.Task
	if err := database.Where("enabled = ?", true).Find(&tasks).Error; err != nil {
		fmt.Fprintln(os.Stderr, "query tasks:", err)
		os.Exit(1)
	}

	now := time.Now()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	var neverRun, stale []model.Task
	for _, t := range tasks {
		if t.CronExpr == "" {
			continue
		}
		if t.LastRunAt == nil {
			neverRun = append(neverRun, t)
			continue
		}
		sched, err := parser.Parse(t.CronExpr)
		if err != nil {
			fmt.Printf("  [%s] invalid cron %q: %v\n", t.ID, t.CronExpr, err)
			continue
		}
		firstNext := sched.Next(*t.LastRunAt)
		secondNext := sched.Next(firstNext)
		if now.After(secondNext) {
			stale = append(stale, t)
		}
	}

	if len(stale) == 0 {
		fmt.Println("✓ No stale tasks.")
	} else {
		fmt.Printf("⚠ %d stale task(s):\n", len(stale))
		for _, t := range stale {
			fmt.Printf("  [%s] cron=%q last_run=%s\n", t.ID, t.CronExpr,
				t.LastRunAt.Format(time.RFC3339))
		}
	}

	if len(neverRun) > 0 {
		fmt.Printf("\n… %d task(s) enabled but never executed:\n", len(neverRun))
		for _, t := range neverRun {
			fmt.Printf("  [%s] cron=%q created=%s\n", t.ID, t.CronExpr,
				t.CreatedAt.Format(time.RFC3339))
		}
	}
}

// ── legacy dump (preserved for backwards compatibility) ───────────────────────

func runLegacyDump(database *gorm.DB) {
	fmt.Println("=== All YZK Worker Sessions ===")
	var sessions []model.Session
	database.Where("channel_key LIKE ?", "%yzk_worker%").
		Order("updated_at DESC").Find(&sessions)
	for _, s := range sessions {
		fmt.Printf("  ID=%s  channel=%s  status=%s  claude_sid=%q  created=%s  updated=%s\n",
			s.ID, s.ChannelKey, s.Status, s.ClaudeSessionID,
			s.CreatedAt.Format("15:04:05"), s.UpdatedAt.Format("15:04:05"))

		var msgs []model.Message
		database.Where("session_id = ?", s.ID).Order("created_at ASC").Find(&msgs)
		for _, m := range msgs {
			content := m.Content
			if len(content) > 120 {
				content = content[:120] + "..."
			}
			fmt.Printf("    [%s] role=%-9s time=%s  [%d chars] %s\n",
				m.ID[:8], m.Role, m.CreatedAt.Format("15:04:05"),
				len(m.Content), content)
		}
	}
}
