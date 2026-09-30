package task

import (
	"context"
	"errors"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// maxTimeout caps how long one execution may run before its context expires.
const maxTimeout = 5 * time.Minute

// Task runs Execute every Interval until Close. Each run gets a context that is
// cancelled on timeout or Close; a failed or timed-out run is logged and the
// next tick runs again.
type Task struct {
	Name     string
	Interval time.Duration
	Execute  func(context.Context) error

	access  sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// Start launches the loop; with first set, it runs once immediately.
func (t *Task) Start(first bool) error {
	t.access.Lock()
	defer t.access.Unlock()
	if t.running {
		return nil
	}
	if t.Interval <= 0 {
		return errors.New("task " + t.Name + ": interval must be positive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.running = true
	t.cancel = cancel
	t.done = make(chan struct{})
	go t.loop(ctx, first, t.done)
	return nil
}

func (t *Task) loop(ctx context.Context, first bool, done chan struct{}) {
	defer close(done)
	if first {
		t.runOnce(ctx)
	}
	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.runOnce(ctx)
		}
	}
}

// runOnce executes the task with a timeout derived from the interval.
func (t *Task) runOnce(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, min(5*t.Interval, maxTimeout))
	defer cancel()
	err := t.Execute(ctx)
	switch {
	case parent.Err() != nil:
		// Closed while running; nothing to report.
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		log.Errorf("Task %s execution timed out, retrying on the next tick", t.Name)
	case err != nil:
		log.Errorf("Task %s execution error: %v", t.Name, err)
	}
}

// Close stops the loop, cancels a running execution and waits for it to return,
// so no execution overlaps whatever the caller does next.
func (t *Task) Close() {
	t.access.Lock()
	if !t.running {
		t.access.Unlock()
		return
	}
	t.running = false
	t.cancel()
	done := t.done
	t.access.Unlock()
	<-done
	log.Warningf("Task %s stopped", t.Name)
}
