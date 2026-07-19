package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestRunnerRunsExpiryAndStopsOnCancellation(t *testing.T) {
	job := &recordingExpiryJob{}
	metrics := &recordingExpiryMetrics{}
	runner := NewRunner(
		noOpChecker{}, job, slog.New(slog.NewTextHandler(io.Discard, nil)),
		20*time.Millisecond, 5*time.Millisecond, 3*time.Millisecond, metrics,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	job.mu.Lock()
	calls := job.calls
	job.mu.Unlock()
	if calls < 2 || calls > 8 {
		t.Fatalf("expiry calls = %d, want bounded periodic runs", calls)
	}
	metrics.mu.Lock()
	observations := metrics.observations
	metrics.mu.Unlock()
	if observations != calls {
		t.Fatalf("metric observations = %d, calls = %d", observations, calls)
	}
}

func TestRunnerAppliesPerRunTimeout(t *testing.T) {
	job := &recordingExpiryJob{waitForContext: true}
	runner := NewRunner(
		noOpChecker{}, job, slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Hour, time.Hour, 2*time.Millisecond, nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	job.mu.Lock()
	timedOut := job.timedOut
	job.mu.Unlock()
	if !timedOut {
		t.Fatal("expiry job did not observe per-run timeout")
	}
}

type noOpChecker struct{}

func (noOpChecker) Check(context.Context) error { return nil }

type recordingExpiryJob struct {
	mu             sync.Mutex
	calls          int
	timedOut       bool
	waitForContext bool
}

func (j *recordingExpiryJob) RunOnce(ctx context.Context) (int, error) {
	j.mu.Lock()
	j.calls++
	wait := j.waitForContext
	j.mu.Unlock()
	if wait {
		<-ctx.Done()
		j.mu.Lock()
		j.timedOut = true
		j.mu.Unlock()
		return 0, ctx.Err()
	}
	return 1, nil
}

type recordingExpiryMetrics struct {
	mu           sync.Mutex
	observations int
}

func (m *recordingExpiryMetrics) ObserveExpiryRun(string, int, time.Duration) {
	m.mu.Lock()
	m.observations++
	m.mu.Unlock()
}
