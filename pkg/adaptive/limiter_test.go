package adaptive

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigDefaults(t *testing.T) {
	l := New(Config{Max: 16})
	assert.Equal(t, 16, l.Limit(), "start defaults to max")
	assert.Equal(t, 1, l.cfg.Min)
	assert.Equal(t, 2500*time.Millisecond, l.cfg.HighLatency)
	assert.Equal(t, 1250*time.Millisecond, l.cfg.LowLatency)
}

func TestObserveMultiplicativeDecreaseOnError(t *testing.T) {
	// A short AdjustInterval lets each spaced-out failure halve the limit.
	l := New(Config{Max: 16, Min: 1, AdjustInterval: time.Millisecond})
	for _, want := range []int{8, 4, 2, 1, 1} {
		l.Observe(time.Second, false)
		assert.Equal(t, want, l.Limit())
		time.Sleep(2 * time.Millisecond) // pass the adjust interval
	}
}

func TestObserveDecreaseOnHighLatency(t *testing.T) {
	l := New(Config{Max: 10, Min: 2, HighLatency: 2 * time.Second, AdjustInterval: 0})
	l.Observe(3*time.Second, true) // success but slow -> congestion
	assert.Equal(t, 5, l.Limit())
}

func TestObserveAdditiveIncreaseOnFastSuccess(t *testing.T) {
	l := New(Config{
		Max: 16, Min: 1, Start: 4,
		LowLatency: time.Second, HighLatency: 2 * time.Second,
		AdjustInterval: 0, GrowStreak: 3,
	})
	// Fewer than GrowStreak good samples: no growth yet.
	l.Observe(100*time.Millisecond, true)
	l.Observe(100*time.Millisecond, true)
	assert.Equal(t, 4, l.Limit())
	// Third good sample crosses the streak threshold -> +1.
	l.Observe(100*time.Millisecond, true)
	assert.Equal(t, 5, l.Limit())
}

func TestObserveNeverExceedsMax(t *testing.T) {
	l := New(Config{Max: 3, Min: 1, Start: 3, LowLatency: time.Second, AdjustInterval: 0, GrowStreak: 1})
	for i := 0; i < 20; i++ {
		l.Observe(10*time.Millisecond, true)
	}
	assert.Equal(t, 3, l.Limit(), "cannot grow past Max")
}

func TestAdjustIntervalDampsBurst(t *testing.T) {
	// A burst of failures within one interval collapses the limit only once.
	l := New(Config{Max: 16, Min: 1, AdjustInterval: time.Hour})
	for i := 0; i < 10; i++ {
		l.Observe(time.Second, false)
	}
	assert.Equal(t, 8, l.Limit(), "only one decrease per AdjustInterval")
}

func TestAcquireRespectsLimitAndReleases(t *testing.T) {
	l := New(Config{Max: 2, Min: 1, Start: 2})
	ctx := context.Background()
	require.NoError(t, l.Acquire(ctx))
	require.NoError(t, l.Acquire(ctx))

	// Third acquire must block until a Release happens.
	acquired := make(chan struct{})
	go func() {
		_ = l.Acquire(ctx)
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("acquire should block when at limit")
	case <-time.After(50 * time.Millisecond):
	}

	l.Release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("acquire should proceed after release")
	}
}

func TestAcquireCancelledContext(t *testing.T) {
	l := New(Config{Max: 1, Min: 1, Start: 1})
	require.NoError(t, l.Acquire(context.Background())) // fill the only slot

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- l.Acquire(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel() // cancellation alone must wake the waiter — no Release needed

	select {
	case err := <-errc:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancelled acquire should return")
	}
}

// TestAcquireWakesAllWaitersOnCancel covers the shutdown case: many goroutines
// blocked in Acquire must all return when ctx is cancelled, even though no slot
// is ever released (sync.Cond.Wait does not observe ctx on its own).
func TestAcquireWakesAllWaitersOnCancel(t *testing.T) {
	l := New(Config{Max: 2, Min: 1, Start: 2})
	require.NoError(t, l.Acquire(context.Background()))
	require.NoError(t, l.Acquire(context.Background())) // both slots held, never released

	ctx, cancel := context.WithCancel(context.Background())
	const waiters = 20
	done := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() { done <- l.Acquire(ctx) }()
	}

	time.Sleep(30 * time.Millisecond) // let them all park in Wait
	cancel()

	timeout := time.After(2 * time.Second)
	for i := 0; i < waiters; i++ {
		select {
		case err := <-done:
			assert.ErrorIs(t, err, context.Canceled)
		case <-timeout:
			t.Fatalf("waiter %d did not wake on cancel", i)
		}
	}
}

func TestConcurrentAcquireReleaseNoLeak(t *testing.T) {
	l := New(Config{Max: 4, Min: 1, Start: 4})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Acquire(ctx) == nil {
				time.Sleep(time.Millisecond)
				l.Release()
			}
		}()
	}
	wg.Wait()
	l.mu.Lock()
	inflight := l.inflight
	l.mu.Unlock()
	assert.Equal(t, 0, inflight, "all slots released")
}
