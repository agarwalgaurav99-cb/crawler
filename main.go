// Package main implements a bounded concurrent crawler restricted to one exact hostname.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/monzo-crawler/internal/app"
	"example.com/monzo-crawler/internal/download"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := app.Run(ctx, app.Options{
		StartURL: flag.Arg(0),
		Workers:  *workers,
		MaxPages: *maxPages,
		Timeout:  *timeout,
		MaxBytes: *maxBytes,
		Retry: download.RetryOptions{
			MaxAttempts: *retries,
			Gate:        download.NewGate(),
			Logger:      logger,
		},
		Logger: logger,
		Output: os.Stdout,
	})
	if err != nil {
		logger.Error("crawl failed", "error", err)
		os.Exit(1)
	}
}
