package app_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.com/monzo-crawler/internal/app"
	"example.com/monzo-crawler/internal/download"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runCrawl starts an httptest server serving mux, runs the full app.Run wiring
// against it, and parses the emitted stdout into a map of page URL -> links.
// mutate can override options before the run.
func runCrawl(t *testing.T, mux *http.ServeMux, mutate func(*app.Options)) (string, map[string][]string) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	opts := app.Options{
		StartURL: srv.URL,
		Workers:  4,
		MaxPages: 100,
		Timeout:  2 * time.Second,
		MaxBytes: 1 << 20,
		Retry: download.RetryOptions{
			MaxAttempts: 3,
			Logger:      discardLogger(),
		},
		Logger: discardLogger(),
	}
	if mutate != nil {
		mutate(&opts)
	}

	var buf bytes.Buffer
	opts.Output = &buf

	if err := app.Run(context.Background(), opts); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	return srv.URL, parseOutput(&buf)
}

// parseOutput decodes the StdoutSink format into page URL -> links.
func parseOutput(out *bytes.Buffer) map[string][]string {
	pages := make(map[string][]string)
	var cur string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "URL: "):
			cur = strings.TrimPrefix(line, "URL: ")
			pages[cur] = []string{}
		case strings.HasPrefix(line, "  - ") && cur != "":
			pages[cur] = append(pages[cur], strings.TrimPrefix(line, "  - "))
		}
	}
	return pages
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestRunCrawlsSiteGraphAndFilters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/a">a</a><a href="/b">b</a><a href="/b">b2</a><a href="/#frag">frag</a><a href="http://outside.invalid/">out</a><a href="/file.pdf">pdf</a>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/b">b</a><a href="/">home</a>`)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p>leaf</p>`)
	})
	mux.HandleFunc("/file.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.4 fake")
	})

	base, pages := runCrawl(t, mux, nil)

	want := map[string]bool{
		base + "/":         true,
		base + "/a":        true,
		base + "/b":        true,
		base + "/file.pdf": true,
	}
	if len(pages) != len(want) {
		t.Errorf("crawled %d pages, want %d; got %v", len(pages), len(want), pages)
	}
	for url := range want {
		if _, ok := pages[url]; !ok {
			t.Errorf("page %q was not crawled; got %v", url, pages)
		}
	}
	if _, ok := pages["http://outside.invalid/"]; ok {
		t.Error("out-of-scope host was crawled")
	}
	if !contains(pages[base+"/a"], base+"/b") || !contains(pages[base+"/a"], base+"/") {
		t.Errorf("cycle links missing from %q: %v", base+"/a", pages[base+"/a"])
	}
	if got := pages[base+"/file.pdf"]; len(got) != 0 {
		t.Errorf("non-HTML page has links = %v, want none", got)
	}
}

func TestRunRetriesTransientErrors(t *testing.T) {
	var flaky atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/flaky">flaky</a>`)
	})
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		if flaky.Add(1) < 3 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p>finally</p>`)
	})

	base, pages := runCrawl(t, mux, nil)

	if _, ok := pages[base+"/flaky"]; !ok {
		t.Errorf("flaky page not crawled after retries; got %v", pages)
	}
	if got := flaky.Load(); got != 3 {
		t.Errorf("flaky attempts = %d, want 3", got)
	}
}

func TestRunRespectsMaxPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/a">a</a><a href="/b">b</a>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/b">b</a>`)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p>leaf</p>`)
	})

	base, pages := runCrawl(t, mux, func(o *app.Options) { o.MaxPages = 2 })

	if _, ok := pages[base+"/"]; !ok {
		t.Errorf("start page not crawled; got %v", pages)
	}
	if _, ok := pages[base+"/a"]; !ok {
		t.Errorf("page /a not crawled; got %v", pages)
	}
	if _, ok := pages[base+"/b"]; ok {
		t.Errorf("page /b crawled beyond budget; got %v", pages)
	}
	if len(pages) != 2 {
		t.Errorf("crawled %d pages, want exactly 2; got %v", len(pages), pages)
	}
}

func TestRunRedirectSafety(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/same">same</a><a href="/cross">cross</a>`)
	})
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusFound)
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p>target</p>`)
	})
	mux.HandleFunc("/cross", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://outside.invalid/", http.StatusFound)
	})

	base, pages := runCrawl(t, mux, nil)

	// Same-host redirect is followed, so /same resolves to /target.
	if _, ok := pages[base+"/target"]; !ok {
		t.Errorf("same-host redirect not followed; got %v", pages)
	}
	if _, ok := pages[base+"/same"]; ok {
		t.Errorf("redirecting URL emitted as its own page; got %v", pages)
	}
	if _, ok := pages[base+"/cross"]; ok {
		t.Errorf("cross-host redirect emitted; got %v", pages)
	}
	if _, ok := pages["http://outside.invalid/"]; ok {
		t.Errorf("cross-host redirect target crawled; got %v", pages)
	}
}

func TestRunCancellationReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p>hi</p>`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := app.Run(ctx, app.Options{
		StartURL: srv.URL,
		Workers:  1,
		MaxPages: 10,
		Timeout:  time.Second,
		MaxBytes: 1 << 20,
		Retry:    download.RetryOptions{MaxAttempts: 1, Logger: discardLogger()},
		Logger:   discardLogger(),
		Output:   io.Discard,
	})
	if err != nil {
		t.Fatalf("app.Run = %v, want nil for cancelled context", err)
	}
}

func TestRunRejectsBadConfiguration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	opts := func(mutate func(*app.Options)) app.Options {
		o := app.Options{
			StartURL: srv.URL,
			Workers:  1,
			MaxPages: 10,
			Timeout:  time.Second,
			MaxBytes: 1 << 20,
			Retry:    download.RetryOptions{MaxAttempts: 1, Logger: discardLogger()},
			Logger:   discardLogger(),
			Output:   io.Discard,
		}
		if mutate != nil {
			mutate(&o)
		}
		return o
	}

	if err := app.Run(context.Background(), opts(func(o *app.Options) { o.Retry.MaxAttempts = 0 })); err == nil {
		t.Error("expected error for zero retry attempts")
	}
	if err := app.Run(context.Background(), opts(func(o *app.Options) { o.StartURL = "://bad" })); err == nil {
		t.Error("expected error for invalid start URL")
	}
}
