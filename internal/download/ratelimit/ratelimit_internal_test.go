package ratelimit

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/urlx"
	downloadmocks "example.com/monzo-crawler/mocks/download"
)

func TestDownloadDecreasesRateOnBackPressure(t *testing.T) {
	r, clock := newThrottled(t, statusErr(http.StatusTooManyRequests))
	u := testRequestURL(t)

	for i := 0; i < 2; i++ {
		if _, err := r.Download(context.Background(), u); err == nil {
			t.Fatal("expected error")
		}
		clock.Advance(adjustInterval)
	}
	if got, want := float64(r.limiter.Limit()), 5.0; got != want {
		t.Errorf("limit = %v, want %v", got, want)
	}
}

func TestDownloadKeepsRateOnClientError(t *testing.T) {
	r, clock := newThrottled(t, statusErr(http.StatusNotFound))
	u := testRequestURL(t)

	for i := 0; i < 5; i++ {
		if _, err := r.Download(context.Background(), u); err == nil {
			t.Fatal("expected error")
		}
		clock.Advance(adjustInterval)
	}
	if got, want := float64(r.limiter.Limit()), 10.0; got != want {
		t.Errorf("limit = %v, want unchanged %v", got, want)
	}
}

// newThrottled builds a RateLimit whose downloader always fails with err, with
// a fake clock so adjustments are deterministic.
func newThrottled(t *testing.T, err error) (*RateLimit, *fakeClock) {
	t.Helper()
	mock := downloadmocks.NewMockDownloader(gomock.NewController(t))
	mock.EXPECT().Download(gomock.Any(), gomock.Any()).Return(nil, err).AnyTimes()

	r := New(mock, Options{MaxRate: 10, Burst: 10})
	clock := &fakeClock{now: time.Unix(1000, 0)}
	r.ctrl.now = clock.Now
	r.ctrl.last = clock.Now()
	return r, clock
}

func statusErr(status int) *download.Error {
	return &download.Error{Kind: download.KindStatus, Status: status}
}

func testRequestURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := urlx.Normalize("https://example.com/")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return u
}
