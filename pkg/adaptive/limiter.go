// Package adaptive provides an AIMD (additive-increase / multiplicative-decrease)
// concurrency limiter. It adapts how many operations may run in parallel based on
// observed latency and errors — a congestion-control loop for RPC calls.
//
// The configured concurrency acts as a ceiling: the limiter multiplicatively
// backs off when calls get slow or fail (an overloaded / rate-limited RPC), and
// additively recovers back toward the ceiling once calls are fast again. It never
// exceeds the ceiling, so it can only ever do the same or better than a static
// semaphore of the same size.
package adaptive

import (
	"context"
	"sync"
	"time"
)

// Config tunes the limiter. Zero values are replaced with sane defaults.
type Config struct {
	Min            int           // floor for concurrency (clamped to >= 1)
	Max            int           // ceiling for concurrency (the configured concurrency)
	Start          int           // initial limit (defaults to Max)
	HighLatency    time.Duration // a success at/above this latency counts as congestion
	LowLatency     time.Duration // a success at/below this latency is eligible to grow
	AdjustInterval time.Duration // minimum time between limit changes (damping)
	GrowStreak     int           // consecutive good samples required before +1
}

func (c *Config) withDefaults() {
	if c.Max < 1 {
		c.Max = 1
	}
	if c.Min < 1 {
		c.Min = 1
	}
	if c.Min > c.Max {
		c.Min = c.Max
	}
	if c.Start <= 0 || c.Start > c.Max {
		c.Start = c.Max
	}
	if c.Start < c.Min {
		c.Start = c.Min
	}
	if c.HighLatency <= 0 {
		c.HighLatency = 2500 * time.Millisecond
	}
	if c.LowLatency <= 0 || c.LowLatency >= c.HighLatency {
		c.LowLatency = c.HighLatency / 2
	}
	if c.AdjustInterval <= 0 {
		c.AdjustInterval = time.Second
	}
	if c.GrowStreak <= 0 {
		c.GrowStreak = 10
	}
}

// Limiter is a concurrency limiter whose active limit moves between [Min, Max].
type Limiter struct {
	cfg  Config
	mu   sync.Mutex
	cond *sync.Cond

	limit      int
	inflight   int
	goodStreak int
	lastAdjust time.Time
}

// New returns a limiter with the given config (defaults applied).
func New(cfg Config) *Limiter {
	cfg.withDefaults()
	l := &Limiter{cfg: cfg, limit: cfg.Start}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// Acquire blocks until a slot is free under the current limit, or ctx is done.
// A nil return means a slot was acquired and the caller must call Release.
func (l *Limiter) Acquire(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for {
		// Context wins over an available slot, and is re-checked after every wake.
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.inflight < l.limit {
			l.inflight++
			return nil
		}
		// sync.Cond.Wait does not observe ctx, so register a wake on
		// cancellation: AfterFunc broadcasts to re-evaluate the loop, and stop()
		// unregisters it once this waiter proceeds normally.
		stop := context.AfterFunc(ctx, func() {
			l.mu.Lock()
			l.cond.Broadcast()
			l.mu.Unlock()
		})
		l.cond.Wait()
		stop()
	}
}

// Release returns a slot. Must be called exactly once per successful Acquire.
func (l *Limiter) Release() {
	l.mu.Lock()
	if l.inflight > 0 {
		l.inflight--
	}
	l.mu.Unlock()
	// A slot freed up: wake one waiter.
	l.cond.Signal()
}

// Observe feeds the outcome of one call back into the controller: its latency and
// whether it succeeded. Errors and high latency shrink the limit (multiplicative
// decrease); sustained fast successes grow it (additive increase). Changes are
// rate-limited by AdjustInterval to avoid thrashing on a burst of samples.
func (l *Limiter) Observe(latency time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	congested := !ok || latency >= l.cfg.HighLatency
	if congested {
		l.goodStreak = 0
		if l.limit > l.cfg.Min && time.Since(l.lastAdjust) >= l.cfg.AdjustInterval {
			l.limit = maxInt(l.cfg.Min, l.limit/2)
			l.lastAdjust = time.Now()
		}
		return
	}

	if latency > l.cfg.LowLatency {
		// Healthy but not fast enough to justify growing; hold steady.
		return
	}

	l.goodStreak++
	if l.goodStreak >= l.cfg.GrowStreak &&
		l.limit < l.cfg.Max &&
		time.Since(l.lastAdjust) >= l.cfg.AdjustInterval {
		l.limit++
		l.goodStreak = 0
		l.lastAdjust = time.Now()
		// A new slot became available.
		l.cond.Signal()
	}
}

// Limit returns the current concurrency limit (for logging / tests).
func (l *Limiter) Limit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
