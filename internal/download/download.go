// Package download fetches URLs by scheme and verifies every response.
package download

import (
	"context"
	"net/http"
	"net/url"
)

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
