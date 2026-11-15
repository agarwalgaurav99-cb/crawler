package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Check is one named verification step applied to a download.
type Check struct {
	Name string
	Fn   func(context.Context, *Result) error
}

// Chain is an ordered verification pipeline. The first failing check wins.
type Chain []Check

// Verify runs every check and returns the first failure.
func (c Chain) Verify(ctx context.Context, r *Result) error {
	for _, check := range c {
		if err := check.Fn(ctx, r); err != nil {
			var de *Error
			if errors.As(err, &de) {
				return de
			}
			return &Error{Kind: KindVerify, Status: r.Status, Err: fmt.Errorf("%s: %w", check.Name, err)}
		}
	}
	return nil
}

// DefaultChecks returns the checks every downloader should apply: a successful
// status, a final URL inside the allowed host, and a body inside the size limit.
func DefaultChecks(allowedHost string, maxBytes int64) Chain {
	allowedHost = strings.ToLower(allowedHost)
	return Chain{
		{Name: "status", Fn: func(_ context.Context, r *Result) error {
			return statusError(r.Status)
		}},
		{Name: "final-host", Fn: func(_ context.Context, r *Result) error {
			if r.Final == nil || !strings.EqualFold(r.Final.Hostname(), allowedHost) {
				return fmt.Errorf("final URL host %q is outside %q", hostOf(r.Final), allowedHost)
			}
			return nil
		}},
		{Name: "body-size", Fn: func(_ context.Context, r *Result) error {
			if int64(len(r.Body)) > maxBytes {
				return fmt.Errorf("body of %d bytes exceeds limit of %d", len(r.Body), maxBytes)
			}
			return nil
		}},
	}
}

func statusError(code int) error {
	if code >= http.StatusOK && code < http.StatusMultipleChoices {
		return nil
	}
	return &Error{
		Kind:      KindStatus,
		Status:    code,
		Retryable: retryableStatus(code),
		Err:       fmt.Errorf("unexpected HTTP status %d", code),
	}
}

// retryableStatus reports whether a status code is worth retrying: server
// errors and the explicit rate-limit response.
func retryableStatus(code int) bool {
	return code >= http.StatusInternalServerError || code == http.StatusTooManyRequests
}

// parseRetryAfter reads the Retry-After header, supporting both the
// delta-seconds and HTTP-date forms. Unparseable or past values yield zero.
func parseRetryAfter(h http.Header) time.Duration {
	return parseRetryAfterAt(h.Get("Retry-After"), time.Now())
}

func parseRetryAfterAt(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func hostOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Hostname()
}
