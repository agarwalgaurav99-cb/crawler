// Package crawl coordinates the frontier, downloads, parsing and filtering.
package crawl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"

	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/filter"
	"example.com/monzo-crawler/internal/parser"
	"example.com/monzo-crawler/internal/sink"
	"example.com/monzo-crawler/internal/state"
)

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
	Frontier    state.Frontier
	Sink        sink.Sink
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
	if deps.Frontier == nil {
		return nil, fmt.Errorf("frontier is required")
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
	page      sink.Page
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

	if _, err := c.deps.Frontier.Add(c.start); err != nil {
		return fmt.Errorf("seed frontier: %w", err)
	}
	scheduled := 1
	active := 0

	for {
		for active < c.opts.Workers {
			next, ok, err := c.deps.Frontier.Next(ctx)
			if err != nil {
				return err
			}
			if !ok {
				break
			}
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
			c.handle(res, &scheduled)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

func (c *Crawler) handle(res result, scheduled *int) {
	if res.err != nil {
		if !errors.Is(res.err, context.Canceled) {
			c.deps.Logger.Warn("could not crawl", "url", res.requested.String(), "error", res.err)
		}
		c.markDone(res.requested)
		return
	}
	c.deps.Sink.Emit(res.page)
	c.markDone(res.requested)

	for _, link := range c.deps.Rules.Filter(res.page.Links) {
		if *scheduled >= c.opts.MaxPages {
			break
		}
		added, err := c.deps.Frontier.Add(link)
		if err != nil {
			c.deps.Logger.Warn("could not schedule", "url", link.String(), "error", err)
			continue
		}
		if added {
			(*scheduled)++
		}
	}
}

func (c *Crawler) markDone(u *url.URL) {
	if err := c.deps.Frontier.Done(u); err != nil {
		c.deps.Logger.Warn("could not mark done", "url", u.String(), "error", err)
	}
}

func (c *Crawler) fetch(ctx context.Context, u *url.URL) (sink.Page, error) {
	downloader, err := c.deps.Downloaders.For(u)
	if err != nil {
		return sink.Page{}, err
	}
	res, err := downloader.Download(ctx, u)
	if err != nil {
		return sink.Page{}, err
	}
	page := sink.Page{URL: res.Final.String()}
	if res.Body == nil {
		return page, nil
	}
	links, err := c.deps.Parser.Parse(ctx, res.Final, res.Body)
	if err != nil {
		return sink.Page{}, err
	}
	page.Links = links
	return page, nil
}
