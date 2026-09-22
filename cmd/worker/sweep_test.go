package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

type recordingSweeper struct {
	mu      sync.Mutex
	calls   int
	removed int64
	err     error
}

func (s *recordingSweeper) DeleteExpired(context.Context, time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.removed, s.err
}

func (s *recordingSweeper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func sweepConfig() config.Config {
	return config.Config{
		Idempotency: config.IdempotencyConfig{SweepInterval: 5 * time.Millisecond},
	}
}

// Housekeeping that fails must not stop the worker: the keys expire logically
// whether or not their rows are gone, and a worker that exits over a sweep takes
// the publisher and the consumer down with it.
func TestSweepKeepsRunningWhenTheDatabaseRefuses(t *testing.T) {
	t.Parallel()

	sweeper := &recordingSweeper{err: errors.New("connection refused")}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- sweepIdempotencyKeys(ctx, sweeper, sweepConfig(), logging.NewJSON(io.Discard, slog.LevelError))
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && sweeper.count() < 3 {
		time.Sleep(2 * time.Millisecond)
	}
	if sweeper.count() < 3 {
		t.Errorf("the sweep stopped after %d failures", sweeper.count())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the sweep returned %v on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the sweep did not return after cancellation")
	}
}

func TestSweepStopsOnCancellation(t *testing.T) {
	t.Parallel()

	sweeper := &recordingSweeper{removed: 3}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := sweepIdempotencyKeys(ctx, sweeper, sweepConfig(), logging.NewJSON(io.Discard, slog.LevelError)); err != nil {
		t.Errorf("sweepIdempotencyKeys returned %v", err)
	}
	if sweeper.count() != 0 {
		t.Errorf("the sweep ran %d times on an already cancelled context", sweeper.count())
	}
}
