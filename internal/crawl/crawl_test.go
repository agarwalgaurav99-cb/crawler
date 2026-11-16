package crawl_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"

	"go.uber.org/mock/gomock"

	"example.com/monzo-crawler/internal/crawl"
	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/filter"
	"example.com/monzo-crawler/internal/parser"
	sinkpkg "example.com/monzo-crawler/internal/sink"
	"example.com/monzo-crawler/internal/state"
	"example.com/monzo-crawler/internal/urlx"
	downloadmocks "example.com/monzo-crawler/mocks/download"
	parsermocks "example.com/monzo-crawler/mocks/parser"
	sinkmocks "example.com/monzo-crawler/mocks/sink"
	statemocks "example.com/monzo-crawler/mocks/state"
)

func TestCrawlDedupesAndFilters(t *testing.T) {
	pages := map[string][]string{
		"https://example.com/":  {"https://example.com/a", "https://example.com/b", "https://evil.invalid/x", "https://example.com/a#frag"},
		"https://example.com/a": {"https://example.com/b", "https://example.com/"},
		"https://example.com/b": {},
	}
	sink := sinkmocks.NewMockSink(gomock.NewController(t))
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
	sink := sinkmocks.NewMockSink(gomock.NewController(t))
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

	sink := sinkmocks.NewMockSink(gomock.NewController(t))
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
	sink := sinkmocks.NewMockSink(gomock.NewController(t))
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

func TestCrawlSurfacesSeedError(t *testing.T) {
	frontier := statemocks.NewMockFrontier(gomock.NewController(t))
	frontier.EXPECT().Add(gomock.Any()).Return(false, errors.New("frontier down"))

	if err := newErrorCrawler(t, frontier).Run(context.Background()); err == nil {
		t.Fatal("expected frontier seed error to surface")
	}
}

func TestCrawlSurfacesNextError(t *testing.T) {
	frontier := statemocks.NewMockFrontier(gomock.NewController(t))
	frontier.EXPECT().Add(gomock.Any()).Return(true, nil)
	frontier.EXPECT().Next(gomock.Any()).Return(nil, false, errors.New("frontier down"))

	if err := newErrorCrawler(t, frontier).Run(context.Background()); err == nil {
		t.Fatal("expected frontier next error to surface")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newPageDownloader serves each page key; any other URL fails the test.
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

// newLinkParser returns the links configured for each page key.
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

// expectPages declares exactly one emit per URL.
func expectPages(sink *sinkmocks.MockSink, urls ...string) {
	for _, u := range urls {
		want := u
		sink.EXPECT().Emit(gomock.Cond(func(p sinkpkg.Page) bool { return p.URL == want }))
	}
}

func newTestCrawler(t *testing.T, start string, downloader download.Downloader, p parser.Parser, sink sinkpkg.Sink, workers, maxPages int) *crawl.Crawler {
	t.Helper()
	u, err := urlx.Normalize(start)
	if err != nil {
		t.Fatalf("normalize start URL: %v", err)
	}
	c, err := crawl.New(u, crawl.Options{Workers: workers, MaxPages: maxPages}, crawl.Deps{
		Downloaders: download.NewRegistry(downloader),
		Parser:      p,
		Rules:       filter.Chain{filter.Scheme("http", "https"), filter.ExactHost(urlx.Hostname(u))},
		Frontier:    state.NewMemory(),
		Sink:        sink,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// newErrorCrawler builds a crawler around the given frontier mock.
func newErrorCrawler(t *testing.T, f state.Frontier) *crawl.Crawler {
	t.Helper()
	start, err := urlx.Normalize("https://example.com/")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	downloader := downloadmocks.NewMockDownloader(gomock.NewController(t))
	downloader.EXPECT().Schemes().Return([]string{"http", "https"}).AnyTimes()

	c, err := crawl.New(start, crawl.Options{Workers: 1, MaxPages: 10}, crawl.Deps{
		Downloaders: download.NewRegistry(downloader),
		Parser:      parsermocks.NewMockParser(gomock.NewController(t)),
		Rules:       filter.Chain{},
		Frontier:    f,
		Sink:        sinkmocks.NewMockSink(gomock.NewController(t)),
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
