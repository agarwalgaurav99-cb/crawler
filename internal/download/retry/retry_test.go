package retry_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"go.uber.org/mock/gomock"

	download "example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/download/retry"
	"example.com/monzo-crawler/internal/urlx"
	backoffmocks "example.com/monzo-crawler/mocks/backoff"
	downloadmocks "example.com/monzo-crawler/mocks/download"
)

func TestRetryReturnsFirstSuccess(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 3})
	u := testURL(t)
	downloader.EXPECT().Download(gomock.Any(), u).
		Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil)

	res, err := r.Download(context.Background(), u)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res == nil || res.Status != http.StatusOK {
		t.Errorf("result = %+v, want status 200", res)
	}
}

func TestRetryRetriesRetryableFailures(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 3})
	u := testURL(t)
	gomock.InOrder(
		downloader.EXPECT().Download(gomock.Any(), u).Return(nil, statusFailure(http.StatusServiceUnavailable)),
		downloader.EXPECT().Download(gomock.Any(), u).Return(nil, statusFailure(http.StatusServiceUnavailable)),
		downloader.EXPECT().Download(gomock.Any(), u).Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil),
	)

	if _, err := r.Download(context.Background(), u); err != nil {
		t.Fatalf("Download: %v", err)
	}
}

func TestRetryDoesNotRetryNonRetryableFailures(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 5})
	u := testURL(t)
	downloader.EXPECT().Download(gomock.Any(), u).Return(nil, statusFailure(http.StatusNotFound))

	_, err := r.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) || de.Status != http.StatusNotFound {
		t.Fatalf("error = %v, want 404 status error", err)
	}
}

func TestRetryDoesNotRetryUnclassifiedErrors(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 3})
	downloader.EXPECT().Download(gomock.Any(), testURL(t)).Return(nil, errors.New("boom"))

	if _, err := r.Download(context.Background(), testURL(t)); err == nil {
		t.Fatal("expected error")
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 2})
	u := testURL(t)
	downloader.EXPECT().Download(gomock.Any(), u).
		Return(nil, statusFailure(http.StatusInternalServerError)).Times(2)

	_, err := r.Download(context.Background(), u)
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "gave up after 2 attempt(s)"; !strings.HasPrefix(got, want) {
		t.Errorf("error = %q, want prefix %q", got, want)
	}
	var de *download.Error
	if !errors.As(err, &de) || de.Status != http.StatusInternalServerError {
		t.Errorf("error = %v, want wrapped 500 status error", err)
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 2})
	u := testURL(t)
	gomock.InOrder(
		downloader.EXPECT().Download(gomock.Any(), u).DoAndReturn(
			func(context.Context, *url.URL) (*download.Result, error) {
				err := statusFailure(http.StatusTooManyRequests)
				err.RetryAfter = 25 * time.Millisecond
				return nil, err
			}),
		downloader.EXPECT().Download(gomock.Any(), u).
			Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil),
	)

	start := time.Now()
	if _, err := r.Download(context.Background(), u); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Errorf("waited %v, want at least ~25ms from Retry-After", elapsed)
	}
}

func TestRetryClampsRetryAfterToCap(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 2})
	retry.SetRetryAfterCap(r, 25*time.Millisecond)
	u := testURL(t)
	gomock.InOrder(
		downloader.EXPECT().Download(gomock.Any(), u).DoAndReturn(
			func(context.Context, *url.URL) (*download.Result, error) {
				err := statusFailure(http.StatusTooManyRequests)
				err.RetryAfter = time.Hour
				return nil, err
			}),
		downloader.EXPECT().Download(gomock.Any(), u).
			Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil),
	)

	start := time.Now()
	if _, err := r.Download(context.Background(), u); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v, want Retry-After clamped to the cap", elapsed)
	}
}

func TestRetryStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, downloader := newRetryHarness(t, retry.Options{MaxAttempts: 3})
	downloader.EXPECT().Download(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, *url.URL) (*download.Result, error) {
			cancel()
			return nil, statusFailure(http.StatusServiceUnavailable)
		})

	if _, err := r.Download(ctx, testURL(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRetryDefaultsToThreeAttempts(t *testing.T) {
	r, downloader := newRetryHarness(t, retry.Options{})
	downloader.EXPECT().Download(gomock.Any(), gomock.Any()).
		Return(nil, statusFailure(http.StatusServiceUnavailable)).Times(3)

	_, err := r.Download(context.Background(), testURL(t))
	if got, want := err.Error(), "gave up after 3 attempt(s)"; !strings.HasPrefix(got, want) {
		t.Errorf("error = %q, want prefix %q", got, want)
	}
}

func TestNewRetryRejectsMissingDownloader(t *testing.T) {
	if _, err := retry.New(nil, retry.Options{}); err == nil {
		t.Error("expected error for nil downloader")
	}
}

// newRetryHarness builds a Retry over a mock downloader, with a mock backoff
// that returns no delay so tests never sleep.
func newRetryHarness(t *testing.T, opts retry.Options) (*retry.Retry, *downloadmocks.MockDownloader) {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctrl := gomock.NewController(t)
	downloader := downloadmocks.NewMockDownloader(ctrl)
	back := backoffmocks.NewMockBackOff(ctrl)
	back.EXPECT().Reset().AnyTimes()
	back.EXPECT().NextBackOff().Return(time.Duration(0)).AnyTimes()

	r, err := retry.New(downloader, opts)
	if err != nil {
		t.Fatalf("retry.New: %v", err)
	}
	retry.SetRetryBackOff(r, func() backoff.BackOff { return back })
	return r, downloader
}

func statusFailure(code int) *download.Error {
	return &download.Error{
		Kind:   download.KindStatus,
		Status: code,
		Err:    fmt.Errorf("unexpected HTTP status %d", code),
	}
}

func testURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := urlx.Normalize("https://example.com/")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return u
}
