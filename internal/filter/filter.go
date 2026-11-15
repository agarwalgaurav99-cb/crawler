// Package filter decides which discovered URLs are in crawl scope.
package filter

import (
	"net/url"
	"strings"

	"example.com/monzo-crawler/internal/urlx"
)

// Rule is a named predicate over an absolute URL.
type Rule struct {
	Name  string
	Allow func(*url.URL) bool
}

// Chain is an ordered set of rules. A URL must pass every rule.
type Chain []Rule

// Allow reports whether u passes every rule in the chain.
func (c Chain) Allow(u *url.URL) bool {
	for _, rule := range c {
		if !rule.Allow(u) {
			return false
		}
	}
	return true
}

// Filter parses raw URLs and returns the in-scope ones, preserving order.
func (c Chain) Filter(raw []string) []*url.URL {
	out := make([]*url.URL, 0, len(raw))
	for _, s := range raw {
		u, err := url.Parse(s)
		if err != nil {
			continue
		}
		if c.Allow(u) {
			out = append(out, u)
		}
	}
	return out
}

// Scheme allows only the given URL schemes.
func Scheme(schemes ...string) Rule {
	allowed := make(map[string]struct{}, len(schemes))
	for _, s := range schemes {
		allowed[strings.ToLower(s)] = struct{}{}
	}
	return Rule{Name: "scheme", Allow: func(u *url.URL) bool {
		_, ok := allowed[strings.ToLower(u.Scheme)]
		return ok
	}}
}

// ExactHost allows only URLs whose hostname exactly matches host.
func ExactHost(host string) Rule {
	host = strings.ToLower(host)
	return Rule{Name: "exact-host", Allow: func(u *url.URL) bool {
		return urlx.Hostname(u) == host
	}}
}
