// Package adaptive provides an AIMD concurrency limiter: the configured
// concurrency is a ceiling the limiter backs off from under latency/errors and
// recovers toward when calls are fast.
package adaptive

import (
	"context"
	"sync"
	"time"
)

type Config struct {
	Min            int
	Max            int
	Start          int
	HighLatency    time.Duration
	LowLatency     time.Duration
	AdjustInterval time.Duration
	GrowStreak     int
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

type Limiter struct {
	cfg  Config
	mu   sync.Mutex
	cond *sync.Cond

	limit      int
	inflight   int
	goodStreak int
	lastAdjust time.Time
}

func New(cfg Config) *Limiter {
	cfg.withDefaults()
	l := &Limiter{cfg: cfg, limit: cfg.Start}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *Limiter) Acquire(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.inflight < l.limit {
			l.inflight++
			return nil
		}
		// sync.Cond.Wait ignores ctx; broadcast on cancel to re-evaluate.
		stop := context.AfterFunc(ctx, func() {
			l.mu.Lock()
			l.cond.Broadcast()
			l.mu.Unlock()
		})
		l.cond.Wait()
		stop()
	}
}

func (l *Limiter) Release() {
	l.mu.Lock()
	if l.inflight > 0 {
		l.inflight--
	}
	l.mu.Unlock()
	l.cond.Signal()
}

func (l *Limiter) Observe(latency time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !ok || latency >= l.cfg.HighLatency {
		l.goodStreak = 0
		if l.limit > l.cfg.Min && time.Since(l.lastAdjust) >= l.cfg.AdjustInterval {
			l.limit = maxInt(l.cfg.Min, l.limit/2)
			l.lastAdjust = time.Now()
		}
		return
	}

	if latency > l.cfg.LowLatency {
		return
	}

	l.goodStreak++
	if l.goodStreak >= l.cfg.GrowStreak &&
		l.limit < l.cfg.Max &&
		time.Since(l.lastAdjust) >= l.cfg.AdjustInterval {
		l.limit++
		l.goodStreak = 0
		l.lastAdjust = time.Now()
		l.cond.Signal()
	}
}

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
