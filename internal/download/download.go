// Package download fetches URLs by scheme and verifies every response.
package download

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	Kind      Kind
	Status    int
	Retryable bool
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

// Result is one downloaded response.
type Result struct {
	Requested   *url.URL
	Final       *url.URL
	Status      int
	Header      http.Header
	ContentType string
	Body        []byte // nil when the response is not HTML
}

// Downloader fetches URLs for one or more schemes.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -source=download.go -destination=../../mocks/download/downloader.go -package=mocks
type Downloader interface {
	Schemes() []string
	Download(ctx context.Context, u *url.URL) (*Result, error)
}

// Registry maps URL schemes to downloaders.
type Registry struct {
	byScheme map[string]Downloader
}

// NewRegistry indexes downloaders by every scheme they advertise.
func NewRegistry(downloaders ...Downloader) *Registry {
	r := &Registry{byScheme: make(map[string]Downloader, len(downloaders))}
	for _, d := range downloaders {
		for _, scheme := range d.Schemes() {
			r.byScheme[strings.ToLower(scheme)] = d
		}
	}
	return r
}

// For returns the downloader registered for u's scheme.
func (r *Registry) For(u *url.URL) (Downloader, error) {
	if d, ok := r.byScheme[strings.ToLower(u.Scheme)]; ok {
		return d, nil
	}
	return nil, &Error{Kind: KindPolicy, Err: fmt.Errorf("no downloader for scheme %q", u.Scheme)}
}
