// sanitize_jsonl scans all workspaces declared in config.yaml and removes
// foreign-provider pollution (kimi/qwen via the bailian anthropic-compatible
// bridge) from the corresponding ~/.claude/projects/.../*.jsonl resume files.
//
// Each modified file is backed up as <path>.bak. Run with -dry-run to preview.
//
// Usage:
//
//	go run ./cmd/sanitize_jsonl                  # apply, default config.yaml
//	go run ./cmd/sanitize_jsonl -dry-run         # report only
//	go run ./cmd/sanitize_jsonl -config x.yaml   # custom config
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kid0317/cc-workspace-bot/internal/claude"
	"github.com/kid0317/cc-workspace-bot/internal/config"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	dryRun := flag.Bool("dry-run", false, "report what would change without modifying files")
	verbose := flag.Bool("v", false, "list every scanned file even if unchanged")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "user home: %v\n", err)
		os.Exit(1)
	}
	projectsRoot := filepath.Join(home, ".claude", "projects")

	projectEntries, err := os.ReadDir(projectsRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", projectsRoot, err)
		os.Exit(1)
	}
	dirNames := make([]string, 0, len(projectEntries))
	for _, e := range projectEntries {
		if e.IsDir() {
			dirNames = append(dirNames, e.Name())
		}
	}

	totals := struct {
		filesScanned, filesChanged int
		linesDropped, linesRewritten int
	}{}

	mode := "APPLY"
	if *dryRun {
		mode = "DRY-RUN"
	}
	fmt.Printf("=== sanitize_jsonl (%s) ===\n", mode)
	fmt.Printf("config:        %s\n", *cfgPath)
	fmt.Printf("projects root: %s\n", projectsRoot)
	fmt.Printf("apps in cfg:   %d\n\n", len(cfg.Apps))

	for _, app := range cfg.Apps {
		ws := strings.TrimSpace(app.WorkspaceDir)
		if ws == "" {
			continue
		}
		absWs, err := filepath.Abs(ws)
		if err != nil {
			fmt.Printf("[%s] abs(%s) failed: %v\n", app.ID, ws, err)
			continue
		}
		prefix := flatten(absWs) + "-sessions-"

		matched := matchDirs(dirNames, prefix)
		if len(matched) == 0 {
			if *verbose {
				fmt.Printf("[%s] no claude project dirs (prefix=%s)\n", app.ID, prefix)
			}
			continue
		}

		var appFiles, appChanged, appDropped, appRewritten int
		for _, dirName := range matched {
			dirPath := filepath.Join(projectsRoot, dirName)
			ents, err := os.ReadDir(dirPath)
			if err != nil {
				fmt.Printf("[%s]   read %s: %v\n", app.ID, dirPath, err)
				continue
			}
			for _, e := range ents {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
					continue
				}
				path := filepath.Join(dirPath, e.Name())
				appFiles++
				totals.filesScanned++

				stats, err := analyzeOrSanitize(path, *dryRun)
				if err != nil {
					fmt.Printf("[%s]   ERROR %s: %v\n", app.ID, path, err)
					continue
				}
				if stats.Affected() == 0 {
					if *verbose {
						fmt.Printf("[%s]   clean: %s\n", app.ID, path)
					}
					continue
				}
				appChanged++
				appDropped += stats.Dropped
				appRewritten += stats.Rewritten
				totals.filesChanged++
				totals.linesDropped += stats.Dropped
				totals.linesRewritten += stats.Rewritten
				fmt.Printf("[%s]   %s %d dropped, %d rewritten: %s\n",
					app.ID, actionLabel(*dryRun), stats.Dropped, stats.Rewritten, path)
			}
		}
		if appChanged > 0 {
			fmt.Printf("[%s] subtotal: %d files affected, %d dropped + %d rewritten (of %d files scanned)\n\n",
				app.ID, appChanged, appDropped, appRewritten, appFiles)
		} else if *verbose {
			fmt.Printf("[%s] %d files scanned, all clean\n\n", app.ID, appFiles)
		}
	}

	fmt.Println("=== Summary ===")
	fmt.Printf("Files scanned:  %d\n", totals.filesScanned)
	fmt.Printf("Files changed:  %d\n", totals.filesChanged)
	fmt.Printf("Lines dropped:  %d\n", totals.linesDropped)
	fmt.Printf("Lines rewritten:%d  (text preserved, model retagged as <synthetic>)\n", totals.linesRewritten)
	if !*dryRun && totals.filesChanged > 0 {
		fmt.Printf("\nOriginals preserved as <path>.bak — restore with `mv <path>.bak <path>`.\n")
	}
}

// flatten mirrors the path-flattening rule used by Claude CLI when picking
// the directory under ~/.claude/projects: replace each '/' and '_' with '-'.
func flatten(absPath string) string {
	s := strings.ReplaceAll(absPath, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	return s
}

func matchDirs(all []string, prefix string) []string {
	var out []string
	for _, n := range all {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func analyzeOrSanitize(path string, dryRun bool) (claude.SanitizeStats, error) {
	if dryRun {
		return claude.AnalyzeJSONL(path)
	}
	return claude.SanitizeJSONL(path)
}

// actionLabel returns the user-facing verb to describe action counts.
func actionLabel(dryRun bool) string {
	if dryRun {
		return "would"
	}
	return "did"
}
