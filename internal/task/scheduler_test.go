package task

import (
	"context"
	"testing"

	"github.com/kid0317/cc-workspace-bot/internal/model"
)

func newTestScheduler(t *testing.T) *Scheduler {
	t.Helper()
	sched, err := NewScheduler(&Runner{})
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}
	return sched
}

func TestScheduler_AddFunc_Success(t *testing.T) {
	sched := newTestScheduler(t)
	sched.Start()
	defer sched.Stop()

	err := sched.AddFunc("test-job", "* * * * *", func() {})
	if err != nil {
		t.Errorf("AddFunc() error = %v", err)
	}
}

func TestScheduler_AddFunc_InvalidCron(t *testing.T) {
	sched := newTestScheduler(t)
	sched.Start()
	defer sched.Stop()

	err := sched.AddFunc("bad-job", "not-a-cron", func() {})
	if err == nil {
		t.Error("AddFunc() with invalid cron should return error")
	}
}

func TestScheduler_Add_AndRemove(t *testing.T) {
	sched := newTestScheduler(t)
	sched.Start()
	defer sched.Stop()

	task := &model.Task{
		ID:       "task-1",
		Name:     "test",
		CronExpr: "0 9 * * *",
	}

	if err := sched.Add(context.Background(), task); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	sched.mu.Lock()
	_, ok := sched.jobs[task.ID]
	sched.mu.Unlock()
	if !ok {
		t.Error("job should be registered after Add()")
	}

	sched.Remove(task.ID)

	sched.mu.Lock()
	_, ok = sched.jobs[task.ID]
	sched.mu.Unlock()
	if ok {
		t.Error("job should be removed after Remove()")
	}
}

func TestScheduler_Add_ReplacesExisting(t *testing.T) {
	sched := newTestScheduler(t)
	sched.Start()
	defer sched.Stop()

	task := &model.Task{ID: "task-dup", Name: "dup", CronExpr: "0 9 * * *"}
	ctx := context.Background()

	if err := sched.Add(ctx, task); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}
	if err := sched.Add(ctx, task); err != nil {
		t.Fatalf("second Add() error = %v", err)
	}

	sched.mu.Lock()
	count := 0
	for id := range sched.jobs {
		if id == task.ID {
			count++
		}
	}
	sched.mu.Unlock()

	if count != 1 {
		t.Errorf("expected exactly 1 job for task-dup, got %d", count)
	}
}

func TestScheduler_Remove_NonExistent(t *testing.T) {
	sched := newTestScheduler(t)
	sched.Start()
	defer sched.Stop()

	// Should not panic.
	sched.Remove("nonexistent-id")
}
