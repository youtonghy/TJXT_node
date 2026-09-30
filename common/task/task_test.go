package task

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedAndTimedOutRunsDoNotStopTheLoop(t *testing.T) {
	var runs atomic.Int32
	task := &Task{
		Name:     "flaky",
		Interval: 20 * time.Millisecond,
		Execute: func(ctx context.Context) error {
			switch runs.Add(1) {
			case 1:
				return errors.New("boom")
			case 2:
				<-ctx.Done() // runs into the timeout
				return ctx.Err()
			default:
				return nil
			}
		},
	}
	if err := task.Start(true); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for runs.Load() < 4 {
		select {
		case <-deadline:
			t.Fatalf("loop stopped after %d runs", runs.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	task.Close()
}

func TestCloseCancelsAndWaitsForTheRunningExecution(t *testing.T) {
	started := make(chan struct{})
	var finished atomic.Bool
	task := &Task{
		Name:     "slow",
		Interval: time.Hour,
		Execute: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			time.Sleep(20 * time.Millisecond)
			finished.Store(true)
			return ctx.Err()
		},
	}
	if err := task.Start(true); err != nil {
		t.Fatal(err)
	}
	<-started
	task.Close()
	if !finished.Load() {
		t.Fatal("Close returned while the execution was still running")
	}
	task.Close() // idempotent
}

func TestStartRejectsNonPositiveInterval(t *testing.T) {
	task := &Task{Name: "zero", Execute: func(context.Context) error { return nil }}
	if err := task.Start(false); err == nil {
		t.Fatal("expected an error for a zero interval")
	}
}
