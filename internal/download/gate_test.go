package download

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

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

func TestGateWaitBlocksUntilPauseElapses(t *testing.T) {
	g := NewGate()
	g.Pause(50 * time.Millisecond)

	start := time.Now()
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("Wait returned after %v, want at least ~50ms", elapsed)
	}
}

func TestGateWaitReturnsImmediatelyWhenOpen(t *testing.T) {
	g := NewGate()
	start := time.Now()
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("Wait took %v with no pause, want immediate", elapsed)
	}
}

func TestGateWaitReturnsOnCancellation(t *testing.T) {
	g := NewGate()
	g.Pause(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}
}

func TestGatePauseNeverShortens(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	g := GateWithClock(clock.Now, nil)

	g.Pause(5 * time.Second)
	g.Pause(time.Second)

	got, want := GateUntil(g), time.Unix(0, 0).Add(5*time.Second)
	if !got.Equal(want) {
		t.Errorf("pause until = %v, want %v", got, want)
	}
}

func TestGatePauseIgnoresNonPositiveDurations(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	g := GateWithClock(clock.Now, nil)

	g.Pause(0)
	g.Pause(-time.Second)

	if until := GateUntil(g); !until.IsZero() {
		t.Errorf("pause until = %v, want zero", until)
	}
}

// GateWithClock returns a Gate using the provided clock. A nil sleep falls
// back to the real context-aware sleep.
func GateWithClock(now func() time.Time, sleep func(context.Context, time.Duration) error) *Gate {
	g := NewGate()
	if now != nil {
		g.now = now
	}
	if sleep != nil {
		g.sleep = sleep
	}
	return g
}

func GateUntil(g *Gate) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.until
}
