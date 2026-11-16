package ratelimit

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestControllerDecreasesOnBackPressure(t *testing.T) {
	c, clock := newTestController(t, 10)
	c.observe(outcomeFailure)
	clock.Advance(adjustInterval)
	c.observe(outcomeFailure)

	if got, want := c.lim.Limit(), rate.Limit(5); got != want {
		t.Errorf("limit = %v, want %v", got, want)
	}
}

func TestControllerClampsToMinimum(t *testing.T) {
	c, clock := newTestController(t, 10)
	for i := 0; i < 50; i++ {
		clock.Advance(adjustInterval)
		c.observe(outcomeFailure)
	}
	if got := c.lim.Limit(); got != c.min {
		t.Errorf("limit = %v, want floor %v", got, c.min)
	}
}

func TestControllerIncreasesAfterDecrease(t *testing.T) {
	c, clock := newTestController(t, 10)
	clock.Advance(adjustInterval)
	c.observe(outcomeFailure)
	decreased := c.lim.Limit()

	for i := 0; i < 100; i++ {
		clock.Advance(adjustInterval)
		c.observe(outcomeSuccess)
	}
	if got := c.lim.Limit(); got != c.max {
		t.Errorf("limit = %v, want ceiling %v", got, c.max)
	}
	if decreased >= c.max {
		t.Errorf("expected a decrease first, got %v", decreased)
	}
}

func TestControllerIgnoresOutcomesWithinInterval(t *testing.T) {
	c, _ := newTestController(t, 10)
	c.observe(outcomeFailure)
	c.observe(outcomeFailure)

	if got, want := c.lim.Limit(), rate.Limit(10); got != want {
		t.Errorf("limit = %v, want unchanged %v before interval elapsed", got, want)
	}
}

func TestControllerHoldsInsideDeadband(t *testing.T) {
	c, clock := newTestController(t, 10)
	// Fill one window: 1 failure + 10 successes -> ratio ~0.09, inside the deadband.
	c.observe(outcomeFailure)
	for i := 0; i < 9; i++ {
		c.observe(outcomeSuccess)
	}
	clock.Advance(adjustInterval)
	c.observe(outcomeSuccess) // triggers the adjustment

	if got, want := c.lim.Limit(), rate.Limit(10); got != want {
		t.Errorf("limit = %v, want unchanged %v inside deadband", got, want)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestController(t *testing.T, max float64) (*Controller, *fakeClock) {
	t.Helper()
	lim := rate.NewLimiter(rate.Limit(max), 1)
	c := newController(lim, rate.Limit(max), discardLogger())
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c.now = clock.Now
	c.last = clock.Now()
	return c, clock
}
