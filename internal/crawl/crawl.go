// Package crawl coordinates the frontier, downloads, parsing and filtering.
package crawl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sync"

	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/filter"
	"example.com/monzo-crawler/internal/parser"
	"example.com/monzo-crawler/internal/urlx"
)

// Page is the crawl output for one successfully downloaded URL.
type Page struct {
	URL   string
	Links []string
}

// Sink receives pages as they complete. Emit is called from one goroutine.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -source=crawl.go -destination=../../mocks/crawl/crawl.go -package=mocks
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

// Options bounds the crawl.
type Options struct {
	Workers  int
	MaxPages int
}

// Deps are the pluggable collaborators of a Crawler.
type Deps struct {
	Downloaders *download.Registry
	Parser      parser.Parser
	Rules       filter.Chain
	Sink        Sink
	Logger      *slog.Logger
}

// Crawler runs a bounded breadth-first crawl from a single start URL.
type Crawler struct {
	start *url.URL
	opts  Options
	deps  Deps
}

// New validates and constructs a Crawler.
func New(start *url.URL, opts Options, deps Deps) (*Crawler, error) {
	if start == nil {
		return nil, fmt.Errorf("start URL is required")
	}
	if opts.Workers < 1 || opts.MaxPages < 1 {
		return nil, fmt.Errorf("workers and max-pages must be positive")
	}
	if deps.Downloaders == nil {
		return nil, fmt.Errorf("downloader registry is required")
	}
	if deps.Parser == nil {
		return nil, fmt.Errorf("parser is required")
	}
	if deps.Sink == nil {
		return nil, fmt.Errorf("sink is required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Crawler{start: start, opts: opts, deps: deps}, nil
}

type result struct {
	requested *url.URL
	page      Page
	err       error
}

// Run crawls until the frontier empties, the budget is hit, or ctx is cancelled.
func (c *Crawler) Run(ctx context.Context) error {
	jobs := make(chan *url.URL)
	results := make(chan result, c.opts.Workers)

	var workers sync.WaitGroup
	for i := 0; i < c.opts.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for u := range jobs {
				page, err := c.fetch(ctx, u)
				select {
				case results <- result{requested: u, page: page, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	defer func() {
		close(jobs)
		workers.Wait()
	}()

	queue := []*url.URL{c.start}
	scheduled := map[string]struct{}{urlx.Key(c.start): {}}
	active := 0

	for len(queue) > 0 || active > 0 {
		for len(queue) > 0 && active < c.opts.Workers {
			next := queue[0]
			queue = queue[1:]
			select {
			case jobs <- next:
				active++
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if active == 0 {
			break
		}
		select {
		case res := <-results:
			active--
			c.handle(res, &queue, scheduled)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

func (c *Crawler) handle(res result, queue *[]*url.URL, scheduled map[string]struct{}) {
	if res.err != nil {
		if errors.Is(res.err, context.Canceled) {
			return
		}
		c.deps.Logger.Warn("could not crawl", "url", res.requested.String(), "error", res.err)
		return
	}
	c.deps.Sink.Emit(res.page)
	for _, link := range c.deps.Rules.Filter(res.page.Links) {
		key := urlx.Key(link)
		if _, exists := scheduled[key]; exists {
			continue
		}
		if len(scheduled) >= c.opts.MaxPages {
			break
		}
		scheduled[key] = struct{}{}
		*queue = append(*queue, link)
	}
}

func (c *Crawler) fetch(ctx context.Context, u *url.URL) (Page, error) {
	downloader, err := c.deps.Downloaders.For(u)
	if err != nil {
		return Page{}, err
	}
	res, err := downloader.Download(ctx, u)
	if err != nil {
		return Page{}, err
	}
	page := Page{URL: res.Final.String()}
	if res.Body == nil {
		return page, nil
	}
	links, err := c.deps.Parser.Parse(ctx, res.Final, res.Body)
	if err != nil {
		return Page{}, err
	}
	page.Links = links
	return page, nil
}
