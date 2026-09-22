package app_test

import (
	"math"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/app"
)

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	t.Parallel()

	policy := app.BackoffPolicy{Base: 100 * time.Millisecond, Cap: 2 * time.Second}

	for attempt := 1; attempt <= 12; attempt++ {
		delay := policy.Delay(attempt)
		if delay < 0 {
			t.Fatalf("attempt %d produced a negative delay %v", attempt, delay)
		}
		if delay > policy.Cap {
			t.Errorf("attempt %d produced %v, which exceeds the cap %v", attempt, delay, policy.Cap)
		}
	}
}

// Full jitter means the delay is drawn from the whole window, not offset by a
// fixed amount. Without it, every failing publisher retries in the same
// millisecond and the broker gets a synchronised thundering herd on recovery.
func TestBackoffIsFullyJittered(t *testing.T) {
	t.Parallel()

	policy := app.BackoffPolicy{Base: 100 * time.Millisecond, Cap: 30 * time.Second}
	const samples = 2000
	const attempt = 6

	window := policy.Window(attempt)
	buckets := make([]int, 4)
	var sum float64

	for range samples {
		delay := policy.Delay(attempt)
		if delay > window {
			t.Fatalf("delay %v exceeds the window %v", delay, window)
		}
		sum += delay.Seconds()
		index := int(float64(len(buckets)) * float64(delay) / float64(window))
		if index >= len(buckets) {
			index = len(buckets) - 1
		}
		buckets[index]++
	}

	// Every quarter of the window has to be visited. A fixed offset, or a
	// multiplier applied to a constant, would leave at least one empty.
	for i, count := range buckets {
		if count == 0 {
			t.Errorf("quarter %d of the jitter window was never sampled: %v", i, buckets)
		}
	}

	mean := sum / samples
	expected := window.Seconds() / 2
	if math.Abs(mean-expected) > expected*0.15 {
		t.Errorf("mean delay %.3fs differs from the expected %.3fs by more than 15 percent", mean, expected)
	}
}

func TestBackoffHandlesDegenerateInput(t *testing.T) {
	t.Parallel()

	zero := app.BackoffPolicy{}
	if delay := zero.Delay(3); delay < 0 {
		t.Errorf("an unconfigured policy must not produce a negative delay, got %v", delay)
	}

	capped := app.BackoffPolicy{Base: time.Minute, Cap: time.Second}
	if delay := capped.Delay(1); delay > time.Second {
		t.Errorf("a base larger than the cap must still respect the cap, got %v", delay)
	}

	// A very large attempt count must not overflow into a negative or absurd
	// duration, which is what a naive shift would do.
	if delay := (app.BackoffPolicy{Base: time.Second, Cap: time.Minute}).Delay(1000); delay > time.Minute {
		t.Errorf("attempt 1000 produced %v, which exceeds the cap", delay)
	}
}
