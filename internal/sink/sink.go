// Package sink receives completed pages from the crawler.
package sink

import (
	"fmt"
	"io"
)

// Page is the crawl output for one downloaded URL.
type Page struct {
	URL   string
	Links []string
}

// Sink receives pages as they complete. Emit is called from one goroutine.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -source=sink.go -destination=../../mocks/sink/sink.go -package=mocks
type Sink interface {
	Emit(Page)
}

// StdoutSink prints pages in a human-readable format.
type StdoutSink struct {
	w io.Writer
}

// NewStdoutSink writes pages to w.
func NewStdoutSink(w io.Writer) *StdoutSink {
	return &StdoutSink{w: w}
}

// Emit implements Sink.
func (s *StdoutSink) Emit(p Page) {
	fmt.Fprintf(s.w, "URL: %s\nLinks:\n", p.URL)
	for _, link := range p.Links {
		fmt.Fprintf(s.w, "  - %s\n", link)
	}
	fmt.Fprintln(s.w)
}
