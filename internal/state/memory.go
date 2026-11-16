package state

import (
	"context"
	"net/url"

	"example.com/monzo-crawler/internal/urlx"
)

// Memory is an in-process Frontier backed by a slice and a set.
type Memory struct {
	queue []*url.URL
	seen  map[string]struct{}
}

// NewMemory returns an empty in-memory frontier.
func NewMemory() *Memory {
	return &Memory{seen: make(map[string]struct{})}
}

// Add implements Frontier.
func (m *Memory) Add(u *url.URL) (bool, error) {
	key := urlx.Key(u)
	if _, exists := m.seen[key]; exists {
		return false, nil
	}
	m.seen[key] = struct{}{}
	m.queue = append(m.queue, u)
	return true, nil
}

// Next implements Frontier.
func (m *Memory) Next(ctx context.Context) (*url.URL, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if len(m.queue) == 0 {
		return nil, false, nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	return next, true, nil
}

// Done implements Frontier; nothing to record in memory.
func (m *Memory) Done(*url.URL) error { return nil }
