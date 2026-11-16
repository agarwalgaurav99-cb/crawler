package ratelimit

import (
	"log/slog"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// AIMD tuning. Adjustments happen at most once per adjustInterval, using the
// error ratio of that window; the gap between the thresholds is a deadband that
// stops the limit oscillating around a single boundary.
const (
	downThreshold  = 0.20
	upThreshold    = 0.05
	decreaseFactor = 0.5
	adjustInterval = time.Second
)

type outcome int

const (
	outcomeNeutral outcome = iota
	outcomeSuccess
	outcomeFailure
)

// Controller adapts a shared rate.Limiter with additive increase and
// multiplicative decrease.
type Controller struct {
	mu     sync.Mutex
	lim    *rate.Limiter
	max    rate.Limit
	min    rate.Limit
	step   rate.Limit
	last   time.Time
	ok     int
	fail   int
	now    func() time.Time
	logger *slog.Logger
}

func newController(lim *rate.Limiter, max rate.Limit, logger *slog.Logger) *Controller {
	min := rate.Limit(math.Max(float64(max)/10, 0.5))
	if min > max {
		min = max
	}
	return &Controller{
		lim:    lim,
		max:    max,
		min:    min,
		step:   rate.Limit(math.Max(float64(max)/20, 0.1)),
		last:   time.Now(),
		now:    time.Now,
		logger: logger,
	}
}

// observe records one outcome and adjusts the limit once per interval.
func (c *Controller) observe(o outcome) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch o {
	case outcomeSuccess:
		c.ok++
	case outcomeFailure:
		c.fail++
	default:
		return
	}

	now := c.now()
	if now.Sub(c.last) < adjustInterval {
		return
	}
	c.last = now
	c.adjust()
	c.ok, c.fail = 0, 0
}

func (c *Controller) adjust() {
	total := c.ok + c.fail
	if total == 0 {
		return
	}
	ratio := float64(c.fail) / float64(total)
	cur := c.lim.Limit()

	var next rate.Limit
	switch {
	case ratio > downThreshold:
		next = rate.Limit(float64(cur) * decreaseFactor)
		if next < c.min {
			next = c.min
		}
	case ratio < upThreshold:
		next = cur + c.step
		if next > c.max {
			next = c.max
		}
	default:
		return // inside the deadband: leave the limit alone
	}

	if next == cur {
		return
	}
	c.lim.SetLimit(next)
	c.logger.Debug("rate adjusted", "from", float64(cur), "to", float64(next), "error_ratio", ratio)
}
