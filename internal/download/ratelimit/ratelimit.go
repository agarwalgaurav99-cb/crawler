// Package ratelimit paces downloads with an adaptive rate limiter.
package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"golang.org/x/time/rate"

	"example.com/monzo-crawler/internal/download"
)

// DefaultMaxRate is the requests/sec ceiling used when Options.MaxRate is unset.
const DefaultMaxRate = 10

// Options configures the adaptive limiter.
type Options struct {
	// MaxRate is the requests/sec ceiling. Values <= 0 use DefaultMaxRate.
	MaxRate float64
	// Burst is the token bucket burst. Values < 1 use 1.
	Burst  int
	Logger *slog.Logger
}

// RateLimit paces downloads and adapts the rate to server back-pressure.
type RateLimit struct {
	inner   download.Downloader
	limiter *rate.Limiter
	ctrl    *Controller
}

// New wraps inner in an adaptive limiter bounded by opts.MaxRate.
func New(inner download.Downloader, opts Options) *RateLimit {
	max := opts.MaxRate
	if max <= 0 {
		max = DefaultMaxRate
	}
	burst := opts.Burst
	if burst < 1 {
		burst = 1
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	lim := rate.NewLimiter(rate.Limit(max), burst)
	return &RateLimit{
		inner:   inner,
		limiter: lim,
		ctrl:    newController(lim, rate.Limit(max), logger),
	}
}

// Schemes implements download.Downloader.
func (r *RateLimit) Schemes() []string { return r.inner.Schemes() }

// Download paces the call, delegates, then reports the outcome to the controller.
func (r *RateLimit) Download(ctx context.Context, u *url.URL) (*download.Result, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, download.Classify(err)
	}
	res, err := r.inner.Download(ctx, u)
	r.ctrl.observe(classifyOutcome(err))
	return res, err
}

// classifyOutcome treats 429 and 5xx as back-pressure.
func classifyOutcome(err error) outcome {
	if err == nil {
		return outcomeSuccess
	}
	var de *download.Error
	if errors.As(err, &de) && (de.Status == http.StatusTooManyRequests || (de.Status >= 500 && de.Status < 600)) {
		return outcomeFailure
	}
	return outcomeNeutral
}
