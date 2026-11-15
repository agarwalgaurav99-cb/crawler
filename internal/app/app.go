// Package app wires the crawler's collaborators together into a single run.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"example.com/monzo-crawler/internal/crawl"
	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/filter"
	"example.com/monzo-crawler/internal/parser"
	"example.com/monzo-crawler/internal/urlx"
)

// Options configures a single crawl run.
type Options struct {
	StartURL string
	Workers  int
	MaxPages int
	Timeout  time.Duration
	MaxBytes int64
	Retry    download.RetryOptions
	Logger   *slog.Logger
	// Output receives discovered pages through the crawl sink. Defaults to
	// os.Stdout when nil.
	Output io.Writer
}

// Run crawls from opts.StartURL, emitting pages to opts.Output. Cancellation is
// not an error.
func Run(ctx context.Context, opts Options) error {
	if opts.Retry.MaxAttempts < 1 {
		return fmt.Errorf("configuration error: retries must be at least 1")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Output == nil {
		opts.Output = os.Stdout
	}

	start, err := urlx.Normalize(opts.StartURL)
	if err != nil {
		return fmt.Errorf("invalid start URL: %w", err)
	}
	host := urlx.Hostname(start)

	httpDownloader, err := download.NewHTTP(download.HTTPOptions{
		AllowedHost: host,
		Timeout:     opts.Timeout,
		MaxBytes:    opts.MaxBytes,
		MaxConns:    opts.Workers,
		Checks:      download.DefaultChecks(host, opts.MaxBytes),
	})
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	retrying, err := download.NewRetry(httpDownloader, opts.Retry)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	crawler, err := crawl.New(start, crawl.Options{Workers: opts.Workers, MaxPages: opts.MaxPages}, crawl.Deps{
		Downloaders: download.NewRegistry(retrying),
		Parser:      parser.HTML{},
		Rules:       filter.Chain{filter.Scheme("http", "https"), filter.ExactHost(host)},
		Sink:        crawl.NewStdoutSink(opts.Output),
		Logger:      opts.Logger,
	})
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	err = crawler.Run(ctx)
	if errors.Is(err, context.Canceled) {
		opts.Logger.Info("crawl interrupted")
		return nil
	}
	return err
}
