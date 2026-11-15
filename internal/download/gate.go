package download

import (
	"context"
	"sync"
	"time"
)

// Gate is a shared pause that every worker honours before an attempt. It is
// used to turn a host-wide 429 into a crawl-wide cool-down.
type Gate struct {
	mu    sync.Mutex
	until time.Time

	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// NewGate returns a gate that starts open.
func NewGate() *Gate {
	return &Gate{now: time.Now, sleep: sleepContext}
}

// Wait blocks until the current pause has elapsed or ctx is cancelled.
func (g *Gate) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		remaining := g.until.Sub(g.now())
		g.mu.Unlock()
		if remaining <= 0 {
			return nil
		}
		if err := g.sleep(ctx, remaining); err != nil {
			return err
		}
	}
}

// Pause extends the current pause so it lasts at least d from now. A shorter
// request never shortens an existing pause.
func (g *Gate) Pause(d time.Duration) {
	if d <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := g.now().Add(d); until.After(g.until) {
		g.until = until
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
