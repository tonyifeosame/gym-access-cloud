package maintenance

import (
	"context"
	"log"
	"sync"
	"time"
)

// A small in-process scheduler for periodic background work.
//
// Deliberately not a cron library and not a queue. The jobs here are idempotent,
// cheap, and safe to skip or repeat, so the failure mode of a missed tick is a
// slightly stale gauge -- not lost work. Anything that must not be missed
// belongs in sync_jobs, which is durable, not here.
//
// Multi-instance note: with several API instances running, every instance runs
// these tasks. That is safe because each task is an idempotent set-based UPDATE
// or DELETE -- two instances sweeping at once converge on the same result. It is
// wasteful rather than wrong. If the work ever stops being idempotent it needs a
// database advisory lock first.

// Task is a unit of periodic work
type Task struct {
	Name     string
	Interval time.Duration
	// Run reports a short human-readable summary of what it did.
	Run func(context.Context) (string, error)
}

// TaskStats is the observable history of one task
type TaskStats struct {
	Name         string        `json:"name"`
	Interval     string        `json:"interval"`
	Runs         int64         `json:"runs"`
	Failures     int64         `json:"failures"`
	LastRunAt    *time.Time    `json:"last_run_at,omitempty"`
	LastDuration string        `json:"last_duration,omitempty"`
	LastSummary  string        `json:"last_summary,omitempty"`
	LastError    string        `json:"last_error,omitempty"`
	duration     time.Duration // retained for metrics in seconds
}

// LastDurationSeconds exposes the last run duration for metrics
func (s TaskStats) LastDurationSeconds() float64 { return s.duration.Seconds() }

// Scheduler runs registered tasks until stopped
type Scheduler struct {
	tasks  []Task
	mu     sync.RWMutex
	stats  map[string]*TaskStats
	wg     sync.WaitGroup
	cancel context.CancelFunc
	// started guards against Start being called twice
	started bool
}

// NewScheduler creates a scheduler for the given tasks
func NewScheduler(tasks ...Task) *Scheduler {
	s := &Scheduler{
		tasks: tasks,
		stats: make(map[string]*TaskStats, len(tasks)),
	}
	for _, t := range tasks {
		s.stats[t.Name] = &TaskStats{Name: t.Name, Interval: t.Interval.String()}
	}
	return s
}

// Start launches each task on its own ticker. Every task runs once immediately
// so that a restart does not leave stale state sitting for a full interval --
// after a crash, devices may already have been unreachable for some time.
func (s *Scheduler) Start(parent context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel

	for _, task := range s.tasks {
		s.wg.Add(1)
		go func(t Task) {
			defer s.wg.Done()

			s.runOnce(ctx, t)

			ticker := time.NewTicker(t.Interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.runOnce(ctx, t)
				}
			}
		}(task)
	}

	log.Printf("maintenance: started %d background task(s)", len(s.tasks))
}

// runOnce executes a task and records the outcome. A panic in one task must not
// take down the process or stop the other tasks.
func (s *Scheduler) runOnce(ctx context.Context, t Task) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("maintenance: task %s panicked: %v", t.Name, r)
			s.record(t.Name, 0, "", errPanic{r})
		}
	}()

	if ctx.Err() != nil {
		return
	}

	start := time.Now()
	summary, err := t.Run(ctx)
	elapsed := time.Since(start)

	// A cancelled context during shutdown is not a task failure
	if ctx.Err() != nil && err != nil {
		return
	}

	s.record(t.Name, elapsed, summary, err)

	if err != nil {
		log.Printf("maintenance: task %s failed after %s: %v", t.Name, elapsed, err)
	} else if summary != "" {
		log.Printf("maintenance: task %s: %s (%s)", t.Name, summary, elapsed)
	}
}

func (s *Scheduler) record(name string, elapsed time.Duration, summary string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stat, ok := s.stats[name]
	if !ok {
		return
	}

	now := time.Now().UTC()
	stat.Runs++
	stat.LastRunAt = &now
	stat.duration = elapsed
	stat.LastDuration = elapsed.String()

	if err != nil {
		stat.Failures++
		stat.LastError = err.Error()
		return
	}
	stat.LastError = ""
	stat.LastSummary = summary
}

// Stats returns a copy of the current task statistics
func (s *Scheduler) Stats() []TaskStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]TaskStats, 0, len(s.stats))
	for _, t := range s.tasks {
		if stat, ok := s.stats[t.Name]; ok {
			out = append(out, *stat)
		}
	}
	return out
}

// Stop signals the tasks to finish and waits for them, up to timeout.
// Returns false if they did not all stop in time.
func (s *Scheduler) Stop(timeout time.Duration) bool {
	if s.cancel == nil {
		return true
	}
	s.cancel()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// errPanic adapts a recovered panic value to the error interface
type errPanic struct{ value any }

func (e errPanic) Error() string {
	return "panic: " + toString(e.value)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if err, ok := v.(error); ok {
		return err.Error()
	}
	return "unknown"
}
