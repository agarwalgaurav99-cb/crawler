package download

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Kind classifies a download failure without callers parsing error strings.
type Kind string

const (
	KindNetwork Kind = "network"
	KindTimeout Kind = "timeout"
	KindStatus  Kind = "status"
	KindVerify  Kind = "verify"
	KindPolicy  Kind = "policy"
)

// Error is a classified download failure.
type Error struct {
	Kind   Kind
	Status int
	// RetryAfter is the server-requested delay before the next attempt. Zero means none.
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		if e.RetryAfter > 0 {
			return fmt.Sprintf("%s: %v (status %d, retry after %s)", e.Kind, e.Err, e.Status, e.RetryAfter)
		}
		return fmt.Sprintf("%s: %v (status %d)", e.Kind, e.Err, e.Status)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Retryable reports whether the failure is worth retrying.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case KindTimeout:
		return true
	case KindStatus:
		return retryableStatus(e.Status)
	case KindNetwork:
		return !errors.Is(e.Err, context.Canceled)
	default:
		return false
	}
}

// Classify maps a transport error to a classified Error.
func Classify(err error) error {
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: KindNetwork, Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindTimeout, Err: err}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Kind: KindTimeout, Err: err}
	}
	return &Error{Kind: KindNetwork, Err: err}
}

// StatusError returns nil for a 2xx code, else a KindStatus Error.
func StatusError(code int) error {
	if code >= http.StatusOK && code < http.StatusMultipleChoices {
		return nil
	}
	return &Error{
		Kind:   KindStatus,
		Status: code,
		Err:    fmt.Errorf("unexpected HTTP status %d", code),
	}
}

// retryableStatus reports whether a status is worth retrying.
func retryableStatus(code int) bool {
	return code >= http.StatusInternalServerError || code == http.StatusTooManyRequests
}
