package task

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-co-op/gocron/v2"

	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// Scheduler wraps gocron and manages task jobs.
type Scheduler struct {
	inner  gocron.Scheduler
	runner *Runner

	mu   sync.Mutex
	jobs map[string]gocron.Job // task_id -> gocron Job
}

// NewScheduler creates a Scheduler.
func NewScheduler(runner *Runner) (*Scheduler, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("create gocron scheduler: %w", err)
	}
	return &Scheduler{
		inner:  s,
		runner: runner,
		jobs:   make(map[string]gocron.Job),
	}, nil
}

// Start begins the scheduler event loop.
func (s *Scheduler) Start() {
	s.inner.Start()
	slog.Info("task scheduler started")
}

// Stop shuts down the scheduler.
func (s *Scheduler) Stop() {
	_ = s.inner.Shutdown()
}

// Add registers a new cron job for the given task.
func (s *Scheduler) Add(ctx context.Context, task *model.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing job if any.
	if job, ok := s.jobs[task.ID]; ok {
		_ = s.inner.RemoveJob(job.ID())
		delete(s.jobs, task.ID)
	}

	t := task // capture for closure
	job, err := s.inner.NewJob(
		gocron.CronJob(task.CronExpr, false),
		gocron.NewTask(func() {
			s.runner.Run(ctx, t)
		}),
		gocron.WithName(task.Name),
	)
	if err != nil {
		return fmt.Errorf("add job %s: %w", task.ID, err)
	}

	s.jobs[task.ID] = job
	slog.Info("task scheduler: registered job", "task_id", task.ID, "cron", task.CronExpr)
	return nil
}

// AddFunc registers a named cron function not tied to a model.Task.
func (s *Scheduler) AddFunc(name, cronExpr string, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.inner.NewJob(
		gocron.CronJob(cronExpr, false),
		gocron.NewTask(fn),
		gocron.WithName(name),
	)
	if err != nil {
		return fmt.Errorf("add func job %q: %w", name, err)
	}
	slog.Info("task scheduler: registered func job", "name", name, "cron", cronExpr)
	return nil
}

// Remove unregisters the job for the given task ID.
func (s *Scheduler) Remove(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[taskID]
	if !ok {
		return
	}
	_ = s.inner.RemoveJob(job.ID())
	delete(s.jobs, taskID)
	slog.Info("task scheduler: removed job", "task_id", taskID)
}
