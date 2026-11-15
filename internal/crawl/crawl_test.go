package crawl_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"example.com/monzo-crawler/internal/crawl"
	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/filter"
	"example.com/monzo-crawler/internal/parser"
	"example.com/monzo-crawler/internal/urlx"
	crawlmocks "example.com/monzo-crawler/mocks/crawl"
	downloadmocks "example.com/monzo-crawler/mocks/download"
	parsermocks "example.com/monzo-crawler/mocks/parser"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newPageDownloader serves a successful HTML response for every page key; any
// other URL is an unexpected call, which gomock fails.
func newPageDownloader(t *testing.T, pages map[string][]string) *downloadmocks.MockDownloader {
	t.Helper()
	mock := downloadmocks.NewMockDownloader(gomock.NewController(t))
	mock.EXPECT().Schemes().Return([]string{"http", "https"}).AnyTimes()
	for key := range pages {
		u, err := urlx.Normalize(key)
		if err != nil {
			t.Fatalf("normalize page %q: %v", key, err)
		}
		want := urlx.Key(u)
		mock.EXPECT().
			Download(gomock.Any(), gomock.Cond(func(got *url.URL) bool { return urlx.Key(got) == want })).
			Return(&download.Result{
				Requested:   u,
				Final:       u,
				Status:      http.StatusOK,
				ContentType: "text/html",
				Body:        []byte("body of " + key),
			}, nil).
			AnyTimes()
	}
	return mock
}

// newLinkParser returns the configured links for each page key. The body is
// irrelevant because extraction is what the parser package owns.
func newLinkParser(t *testing.T, pages map[string][]string) *parsermocks.MockParser {
	t.Helper()
	mock := parsermocks.NewMockParser(gomock.NewController(t))
	for key, links := range pages {
		u, err := urlx.Normalize(key)
		if err != nil {
			t.Fatalf("normalize page %q: %v", key, err)
		}
		want := urlx.Key(u)
		normalized := make([]string, 0, len(links))
		for _, link := range links {
			ref, err := urlx.Normalize(link)
			if err != nil {
				t.Fatalf("normalize link %q: %v", link, err)
			}
			normalized = append(normalized, ref.String())
		}
		mock.EXPECT().
			Parse(gomock.Any(), gomock.Cond(func(base *url.URL) bool { return urlx.Key(base) == want }), gomock.Any()).
			Return(normalized, nil).
			AnyTimes()
	}
	return mock
}

// expectPages declares exactly one emit per URL. A duplicate emit consumes no
// expectation and fails, as does a missing one at test cleanup.
func expectPages(sink *crawlmocks.MockSink, urls ...string) {
	for _, u := range urls {
		want := u
		sink.EXPECT().Emit(gomock.Cond(func(p crawl.Page) bool { return p.URL == want }))
	}
}

func newTestCrawler(t *testing.T, start string, downloader download.Downloader, p parser.Parser, sink crawl.Sink, workers, maxPages int) *crawl.Crawler {
	t.Helper()
	u, err := urlx.Normalize(start)
	if err != nil {
		t.Fatalf("normalize start URL: %v", err)
	}
	c, err := crawl.New(u, crawl.Options{Workers: workers, MaxPages: maxPages}, crawl.Deps{
		Downloaders: download.NewRegistry(downloader),
		Parser:      p,
		Rules:       filter.Chain{filter.Scheme("http", "https"), filter.ExactHost(urlx.Hostname(u))},
		Sink:        sink,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// recorder collects emitted URLs for tests that drive the real HTTP stack.
type recorder struct {
	mu   sync.Mutex
	urls map[string]bool
}

func newRecordingSink(t *testing.T) (*crawlmocks.MockSink, *recorder) {
	t.Helper()
	sink := crawlmocks.NewMockSink(gomock.NewController(t))
	rec := &recorder{urls: make(map[string]bool)}
	sink.EXPECT().Emit(gomock.Any()).Do(rec.record).AnyTimes()
	return sink, rec
}

func (r *recorder) record(p crawl.Page) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.urls[p.URL] = true
}

func (r *recorder) has(url string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.urls[url]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.urls)
}

func (r *recorder) all() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.urls))
	for k := range r.urls {
		out[k] = true
	}
	return out
}

func TestCrawlDedupesAndFilters(t *testing.T) {
	pages := map[string][]string{
		"https://example.com/":  {"https://example.com/a", "https://example.com/b", "https://evil.invalid/x", "https://example.com/a#frag"},
		"https://example.com/a": {"https://example.com/b", "https://example.com/"},
		"https://example.com/b": {},
	}
	sink := crawlmocks.NewMockSink(gomock.NewController(t))
	expectPages(sink, "https://example.com/", "https://example.com/a", "https://example.com/b")
	c := newTestCrawler(t, "https://example.com/", newPageDownloader(t, pages), newLinkParser(t, pages), sink, 4, 100)

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestCrawlRespectsMaxPages(t *testing.T) {
	pages := map[string][]string{
		"https://example.com/":  {"https://example.com/a", "https://example.com/b"},
		"https://example.com/a": {"https://example.com/b"},
	}
	sink := crawlmocks.NewMockSink(gomock.NewController(t))
	expectPages(sink, "https://example.com/", "https://example.com/a")
	c := newTestCrawler(t, "https://example.com/", newPageDownloader(t, pages), newLinkParser(t, pages), sink, 4, 2)

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestCrawlKeepsGoingAfterFetchErrors(t *testing.T) {
	pages := map[string][]string{
		"https://example.com/":   {"https://example.com/missing", "https://example.com/ok"},
		"https://example.com/ok": {},
	}
	downloader := newPageDownloader(t, pages)
	missing, err := urlx.Normalize("https://example.com/missing")
	if err != nil {
		t.Fatalf("normalize missing URL: %v", err)
	}
	downloader.EXPECT().
		Download(gomock.Any(), gomock.Cond(func(u *url.URL) bool { return urlx.Key(u) == urlx.Key(missing) })).
		Return(nil, &download.Error{Kind: download.KindStatus, Status: http.StatusNotFound, Err: fmt.Errorf("no page")})

	sink := crawlmocks.NewMockSink(gomock.NewController(t))
	expectPages(sink, "https://example.com/", "https://example.com/ok")
	c := newTestCrawler(t, "https://example.com/", downloader, newLinkParser(t, pages), sink, 2, 10)

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestCrawlStopsOnCancellation(t *testing.T) {
	pages := map[string][]string{
		"https://example.com/":  {"https://example.com/a"},
		"https://example.com/a": {},
	}
	sink := crawlmocks.NewMockSink(gomock.NewController(t))
	c := newTestCrawler(t, "https://example.com/", newPageDownloader(t, pages), newLinkParser(t, pages), sink, 2, 10)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	start, _ := urlx.Normalize("https://example.com/")
	if _, err := crawl.New(nil, crawl.Options{Workers: 1, MaxPages: 1}, crawl.Deps{}); err == nil {
		t.Error("expected error for nil start URL")
	}
	if _, err := crawl.New(start, crawl.Options{Workers: 0, MaxPages: 1}, crawl.Deps{}); err == nil {
		t.Error("expected error for zero workers")
	}
	if _, err := crawl.New(start, crawl.Options{Workers: 1, MaxPages: 0}, crawl.Deps{}); err == nil {
		t.Error("expected error for zero max-pages")
	}
	if _, err := crawl.New(start, crawl.Options{Workers: 1, MaxPages: 1}, crawl.Deps{}); err == nil {
		t.Error("expected error for missing dependencies")
	}
}

func TestCrawlIntegrationWithHTTP(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/a">a</a><a href="https://outside.invalid/">outside</a>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/">home</a>`)
	})

	start, err := urlx.Normalize(srv.URL)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	host := urlx.Hostname(start)
	httpDownloader, err := download.NewHTTP(download.HTTPOptions{
		AllowedHost: host,
		Timeout:     2 * time.Second,
		MaxBytes:    1 << 20,
		MaxConns:    4,
		Checks:      download.DefaultChecks(host, 1<<20),
	})
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}

	sink, rec := newRecordingSink(t)
	c, err := crawl.New(start, crawl.Options{Workers: 4, MaxPages: 10}, crawl.Deps{
		Downloaders: download.NewRegistry(httpDownloader),
		Parser:      parser.HTML{},
		Rules:       filter.Chain{filter.Scheme("http", "https"), filter.ExactHost(host)},
		Sink:        sink,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, want := range []string{srv.URL + "/", srv.URL + "/a"} {
		if !rec.has(want) {
			t.Errorf("page %q was not crawled; got %v", want, rec.all())
		}
	}
	if got := rec.count(); got != 2 {
		t.Errorf("crawled %v, want exactly the in-scope pages", rec.all())
	}
}

func TestCrawlRetriesTransientErrors(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var flakyAttempts atomic.Int32
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/flaky">flaky</a>`)
	})
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		if flakyAttempts.Add(1) < 3 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p>finally</p>`)
	})

	start, err := urlx.Normalize(srv.URL)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	host := urlx.Hostname(start)
	httpDownloader, err := download.NewHTTP(download.HTTPOptions{
		AllowedHost: host,
		Timeout:     2 * time.Second,
		MaxBytes:    1 << 20,
		MaxConns:    2,
		Checks:      download.DefaultChecks(host, 1<<20),
	})
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	retrying, err := download.NewRetry(httpDownloader, download.RetryOptions{
		MaxAttempts: 3,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}

	sink, rec := newRecordingSink(t)
	c, err := crawl.New(start, crawl.Options{Workers: 2, MaxPages: 10}, crawl.Deps{
		Downloaders: download.NewRegistry(retrying),
		Parser:      parser.HTML{},
		Rules:       filter.Chain{filter.Scheme("http", "https"), filter.ExactHost(host)},
		Sink:        sink,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !rec.has(srv.URL + "/flaky") {
		t.Errorf("flaky page was not crawled after retries; got %v", rec.all())
	}
	if got := flakyAttempts.Load(); got != 3 {
		t.Errorf("flaky attempts = %d, want 3", got)
	}
}
