package download

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
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

// DefaultChecks applies status, final-host, and body-size checks.
func DefaultChecks(allowedHost string, maxBytes int64) Chain {
	allowedHost = strings.ToLower(allowedHost)
	return Chain{
		{Name: "status", Fn: func(_ context.Context, r *Result) error {
			return StatusError(r.Status)
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

func hostOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Hostname()
}
