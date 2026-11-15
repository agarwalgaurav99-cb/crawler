package download_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	download "example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/urlx"
	"example.com/monzo-crawler/mocks/download"
)

func newTestDownloader(t *testing.T, srv *httptest.Server, maxBytes int64, timeout time.Duration) *download.HTTP {
	t.Helper()
	start, err := urlx.Normalize(srv.URL)
	if err != nil {
		t.Fatalf("normalize server URL: %v", err)
	}
	host := urlx.Hostname(start)
	d, err := download.NewHTTP(download.HTTPOptions{
		AllowedHost: host,
		Timeout:     timeout,
		MaxBytes:    maxBytes,
		MaxConns:    2,
		Checks:      download.DefaultChecks(host, maxBytes),
	})
	if err != nil {
		t.Fatalf("download.NewHTTP: %v", err)
	}
	return d
}

func TestHTTPDownloadHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<a href="/next">next</a>`)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	res, err := d.Download(context.Background(), u)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Status)
	}
	if res.ContentType != "text/html" {
		t.Errorf("content type = %q, want text/html", res.ContentType)
	}
	if got, want := string(res.Body), `<a href="/next">next</a>`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestHTTPDownloadNonHTMLHasNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(make([]byte, 128))
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	res, err := d.Download(context.Background(), u)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Body != nil {
		t.Errorf("body = %q, want nil for non-HTML", res.Body)
	}
}

func TestHTTPDownloadStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want *download.Error", err)
	}
	if de.Kind != download.KindStatus || de.Status != http.StatusNotFound {
		t.Errorf("error = %+v, want status 404", de)
	}
	if de.Retryable {
		t.Error("404 should not be retryable")
	}
}

func TestHTTPDownloadRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(strings.Repeat("a", 1024)))
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 16, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindVerify {
		t.Fatalf("error = %v, want verification error", err)
	}
}

func TestHTTPSameHostRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL + "/old")
	res, err := d.Download(context.Background(), u)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got, want := res.Final.String(), srv.URL+"/new"; got != want {
		t.Errorf("final URL = %q, want %q", got, want)
	}
}

func TestHTTPRejectsCrossHostRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/", http.StatusFound)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindStatus {
		t.Fatalf("error = %v, want status error for rejected redirect", err)
	}
}

func TestHTTPDownloadTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 25*time.Millisecond)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindTimeout {
		t.Fatalf("error = %v, want timeout error", err)
	}
	if !de.Retryable {
		t.Error("timeout should be retryable")
	}
}

func TestRegistrySelectsByScheme(t *testing.T) {
	mock := mocks.NewMockDownloader(gomock.NewController(t))
	mock.EXPECT().Schemes().Return([]string{"http", "https"})
	registry := download.NewRegistry(mock)
	for _, scheme := range []string{"http", "https"} {
		u, _ := url.Parse(scheme + "://example.com/")
		if _, err := registry.For(u); err != nil {
			t.Errorf("For(%s) error: %v", scheme, err)
		}
	}
	u, _ := url.Parse("ftp://example.com/")
	_, err := registry.For(u)
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindPolicy {
		t.Fatalf("error = %v, want policy error for unknown scheme", err)
	}
}

func TestDefaultChecksRejectOutOfScopeFinalURL(t *testing.T) {
	chain := download.DefaultChecks("example.com", 10)
	other, _ := url.Parse("https://other.example.com/")
	err := chain.Verify(context.Background(), &download.Result{Final: other, Status: http.StatusOK})
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindVerify {
		t.Fatalf("error = %v, want verification error", err)
	}
	if !strings.Contains(err.Error(), "final-host") {
		t.Errorf("error = %q, want it to name the failing check", err)
	}
}

func TestDefaultChecksRejectOversizedBody(t *testing.T) {
	chain := download.DefaultChecks("example.com", 10)
	inHost, _ := url.Parse("https://example.com/")
	err := chain.Verify(context.Background(), &download.Result{Final: inHost, Status: http.StatusOK, Body: []byte("01234567890")})
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindVerify {
		t.Fatalf("error = %v, want verification error", err)
	}
}

func TestHTTPDownloadServiceUnavailableIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "later", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want *download.Error", err)
	}
	if de.Kind != download.KindStatus || de.Status != http.StatusServiceUnavailable {
		t.Errorf("error = %+v, want status 503", de)
	}
	if !de.Retryable {
		t.Error("503 should be retryable")
	}
	if de.RetryAfter != 0 {
		t.Errorf("retry after = %v, want 0 without header", de.RetryAfter)
	}
}

func TestHTTPDownloadTooManyRequestsRetryAfterSeconds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want *download.Error", err)
	}
	if de.Status != http.StatusTooManyRequests || !de.Retryable {
		t.Errorf("error = %+v, want retryable 429", de)
	}
	if de.RetryAfter != 2*time.Second {
		t.Errorf("retry after = %v, want 2s", de.RetryAfter)
	}
}

func TestHTTPDownloadTooManyRequestsRetryAfterHTTPDate(t *testing.T) {
	target := time.Now().Add(3 * time.Second).UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", target.Format(http.TimeFormat))
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want *download.Error", err)
	}
	if de.RetryAfter <= 0 || de.RetryAfter > 3*time.Second {
		t.Errorf("retry after = %v, want (0, 3s]", de.RetryAfter)
	}
}

func TestHTTPDownloadInvalidRetryAfterIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "soon")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 2*time.Second)
	u, _ := urlx.Normalize(srv.URL)
	_, err := d.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want *download.Error", err)
	}
	if de.RetryAfter != 0 {
		t.Errorf("retry after = %v, want 0 for unparseable value", de.RetryAfter)
	}
}

func TestHTTPDrainsRejectedBodyForConnectionReuse(t *testing.T) {
	var conns atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/err", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(make([]byte, 1024))
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<p>ok</p>")
	})

	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	d := newTestDownloader(t, srv, 1<<20, 5*time.Second)
	errURL, _ := urlx.Normalize(srv.URL + "/err")
	okURL, _ := urlx.Normalize(srv.URL + "/ok")

	if _, err := d.Download(context.Background(), errURL); err == nil {
		t.Fatal("expected error for 503 response")
	}
	if _, err := d.Download(context.Background(), okURL); err != nil {
		t.Fatalf("Download ok: %v", err)
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("connections = %d, want 1 (rejected body should be drained)", got)
	}
}
