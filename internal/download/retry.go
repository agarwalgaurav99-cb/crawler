package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/cenkalti/backoff/v5"
)

const (
	defaultMaxAttempts = 3
	// maxRetryAfter caps Retry-After so one endpoint can't stall the crawl.
	maxRetryAfter = 5 * time.Second
	// defaultRateLimitPause is used when a 429 has no usable Retry-After.
	defaultRateLimitPause = time.Second
)

// RetryOptions configures Retry. Zero values select the documented defaults.
type RetryOptions struct {
	// MaxAttempts is the total number of attempts, including the first.
	MaxAttempts int
	Logger      *slog.Logger
	// Gate, when set, pauses all workers after a 429.
	Gate *Gate
}

// Retry wraps a Downloader and retries retryable failures with backoff.
type Retry struct {
	inner         Downloader
	maxAttempts   int
	logger        *slog.Logger
	gate          *Gate
	retryAfterCap time.Duration
	newBackOff    func() backoff.BackOff
}

// NewRetry wraps inner, whose schemes it advertises unchanged.
func NewRetry(inner Downloader, opts RetryOptions) (*Retry, error) {
	if inner == nil {
		return nil, fmt.Errorf("inner downloader is required")
	}
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = defaultMaxAttempts
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Retry{
		inner:         inner,
		maxAttempts:   opts.MaxAttempts,
		logger:        opts.Logger,
		gate:          opts.Gate,
		retryAfterCap: maxRetryAfter,
		newBackOff:    func() backoff.BackOff { return backoff.NewExponentialBackOff() },
	}, nil
}

// Schemes implements Downloader.
func (r *Retry) Schemes() []string { return r.inner.Schemes() }

// Download implements Downloader, retrying only retryable failures.
func (r *Retry) Download(ctx context.Context, u *url.URL) (*Result, error) {
	var (
		attempts int
		lastErr  error
	)

	operation := func() (*Result, error) {
		attempts++
		if r.gate != nil {
			if err := r.gate.Wait(ctx); err != nil {
				return nil, backoff.Permanent(err)
			}
		}

		res, err := r.inner.Download(ctx, u)
		if err == nil {
			return res, nil
		}
		lastErr = err

		var de *Error
		if !errors.As(err, &de) || !de.Retryable {
			return nil, backoff.Permanent(err)
		}

		retryAfter := de.RetryAfter
		if retryAfter > r.retryAfterCap {
			retryAfter = r.retryAfterCap
		}
		if r.gate != nil && de.Status == http.StatusTooManyRequests {
			r.gate.Pause(max(de.RetryAfter, defaultRateLimitPause))
		}
		if retryAfter > 0 {
			return nil, &backoff.RetryAfterError{Duration: retryAfter}
		}
		return nil, err
	}

	res, err := backoff.Retry(ctx, operation,
		backoff.WithBackOff(r.newBackOff()),
		backoff.WithMaxTries(uint(r.maxAttempts)),
		// Retry bounds are attempts and max delay, not wall-clock time.
		backoff.WithMaxElapsedTime(0),
		backoff.WithNotify(func(_ error, next time.Duration) {
			r.logger.DebugContext(ctx, "retrying download",
				"url", u.String(), "attempt", attempts, "delay", next, "error", lastErr)
		}),
	)
	if err != nil {
		var de *Error
		if attempts >= r.maxAttempts && errors.As(lastErr, &de) && de.Retryable {
			return nil, fmt.Errorf("gave up after %d attempt(s): %w", attempts, lastErr)
		}
		return nil, err
	}
	return res, nil
}
