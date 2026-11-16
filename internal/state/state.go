// Package state holds the crawl frontier (queue, dedupe, completion) behind an
// interface a durable backend can implement.
package state

import (
	"context"
	"net/url"
)

// Frontier is the crawl frontier, driven by the single coordinator goroutine
// (implementations need not be concurrency-safe).
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -source=state.go -destination=../../mocks/state/frontier.go -package=mocks
type Frontier interface {
	// Add schedules u if unseen and reports whether it was newly added.
	Add(u *url.URL) (bool, error)
	// Next claims the oldest pending URL; ok is false when the frontier is empty.
	Next(ctx context.Context) (*url.URL, bool, error)
	// Done marks a claimed URL as finished.
	Done(u *url.URL) error
}
