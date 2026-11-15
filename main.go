// Package main implements a bounded concurrent crawler restricted to one exact hostname.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/monzo-crawler/internal/crawl"
	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/filter"
	"example.com/monzo-crawler/internal/parser"
	"example.com/monzo-crawler/internal/urlx"
)

func main() {
	workers := flag.Int("workers", 8, "number of concurrent HTTP workers")
	timeout := flag.Duration("timeout", 10*time.Second, "HTTP request timeout")
	maxPages := flag.Int("max-pages", 10000, "maximum number of unique URLs to schedule")
	maxBytes := flag.Int64("max-bytes", 2_000_000, "maximum HTML response size in bytes")
	retries := flag.Int("retries", 3, "total attempts for retryable failures")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options] <starting-url>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	retryOpts := download.RetryOptions{
		MaxAttempts: *retries,
		Gate:        download.NewGate(),
		Logger:      logger,
	}
	if err := run(flag.Arg(0), *workers, *maxPages, *timeout, *maxBytes, retryOpts, logger); err != nil {
		logger.Error("crawl failed", "error", err)
		os.Exit(1)
	}
}

func run(raw string, workers, maxPages int, timeout time.Duration, maxBytes int64, retryOpts download.RetryOptions, logger *slog.Logger) error {
	if retryOpts.MaxAttempts < 1 {
		return fmt.Errorf("configuration error: retries must be at least 1")
	}
	start, err := urlx.Normalize(raw)
	if err != nil {
		return fmt.Errorf("invalid start URL: %w", err)
	}
	host := urlx.Hostname(start)

	httpDownloader, err := download.NewHTTP(download.HTTPOptions{
		AllowedHost: host,
		Timeout:     timeout,
		MaxBytes:    maxBytes,
		MaxConns:    workers,
		Checks:      download.DefaultChecks(host, maxBytes),
	})
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	retrying, err := download.NewRetry(httpDownloader, retryOpts)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	crawler, err := crawl.New(start, crawl.Options{Workers: workers, MaxPages: maxPages}, crawl.Deps{
		Downloaders: download.NewRegistry(retrying),
		Parser:      parser.HTML{},
		Rules:       filter.Chain{filter.Scheme("http", "https"), filter.ExactHost(host)},
		Sink:        crawl.NewStdoutSink(os.Stdout),
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = crawler.Run(ctx)
	if errors.Is(err, context.Canceled) {
		logger.Info("crawl interrupted")
		return nil
	}
	return err
}
