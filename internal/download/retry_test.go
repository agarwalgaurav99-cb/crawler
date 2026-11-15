package download_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"go.uber.org/mock/gomock"

	download "example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/urlx"
	mocks "example.com/monzo-crawler/mocks/download"
)

func statusFailure(code int) *download.Error {
	return &download.Error{
		Kind:      download.KindStatus,
		Status:    code,
		Retryable: code >= http.StatusInternalServerError || code == http.StatusTooManyRequests,
		Err:       fmt.Errorf("unexpected HTTP status %d", code),
	}
}

// recordingBackOff serves queued delays and records usage instead of sleeping.
type recordingBackOff struct {
	mu     sync.Mutex
	delays []time.Duration
	nexts  int
	resets int
}

func (b *recordingBackOff) NextBackOff() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nexts++
	if len(b.delays) == 0 {
		return 0
	}
	d := b.delays[0]
	b.delays = b.delays[1:]
	return d
}

func (b *recordingBackOff) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resets++
}

func (b *recordingBackOff) calls() (nexts, resets int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nexts, b.resets
}

type retryHarness struct {
	retry *download.Retry
	mock  *mocks.MockDownloader
	back  *recordingBackOff
}

func newRetryHarness(t *testing.T, opts download.RetryOptions) *retryHarness {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	mock := mocks.NewMockDownloader(gomock.NewController(t))
	retry, err := download.NewRetry(mock, opts)
	if err != nil {
		t.Fatalf("download.NewRetry: %v", err)
	}
	h := &retryHarness{retry: retry, mock: mock}
	h.back = &recordingBackOff{delays: []time.Duration{time.Millisecond, 2 * time.Millisecond}}
	download.SetRetryBackOff(retry, func() backoff.BackOff { return h.back })
	return h
}

func testURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := urlx.Normalize("https://example.com/")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return u
}

func TestRetryReturnsFirstSuccess(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 3})
	h.mock.EXPECT().Download(gomock.Any(), u).Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil)

	res, err := h.retry.Download(context.Background(), u)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res == nil || res.Status != http.StatusOK {
		t.Errorf("result = %+v, want status 200", res)
	}
	if nexts, _ := h.back.calls(); nexts != 0 {
		t.Errorf("backoff calls = %d, want 0", nexts)
	}
}

func TestRetryRetriesRetryableFailures(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 3})
	gomock.InOrder(
		h.mock.EXPECT().Download(gomock.Any(), u).Return(nil, statusFailure(http.StatusServiceUnavailable)),
		h.mock.EXPECT().Download(gomock.Any(), u).Return(nil, statusFailure(http.StatusServiceUnavailable)),
		h.mock.EXPECT().Download(gomock.Any(), u).Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil),
	)

	if _, err := h.retry.Download(context.Background(), u); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if nexts, _ := h.back.calls(); nexts != 2 {
		t.Errorf("backoff calls = %d, want 2", nexts)
	}
}

func TestRetryDoesNotRetryNonRetryableFailures(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 5})
	h.mock.EXPECT().Download(gomock.Any(), u).Return(nil, statusFailure(http.StatusNotFound))

	_, err := h.retry.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) || de.Status != http.StatusNotFound {
		t.Fatalf("error = %v, want 404 status error", err)
	}
	if nexts, _ := h.back.calls(); nexts != 0 {
		t.Errorf("backoff calls = %d, want 0", nexts)
	}
}

func TestRetryDoesNotRetryUnclassifiedErrors(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 3})
	h.mock.EXPECT().Download(gomock.Any(), u).Return(nil, errors.New("boom"))

	if _, err := h.retry.Download(context.Background(), u); err == nil {
		t.Fatal("expected error")
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 2})
	h.mock.EXPECT().Download(gomock.Any(), u).
		Return(nil, statusFailure(http.StatusInternalServerError)).Times(2)

	_, err := h.retry.Download(context.Background(), u)
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

func TestRetryGivesUpAfterSingleAttempt(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 1})
	h.mock.EXPECT().Download(gomock.Any(), u).
		Return(nil, statusFailure(http.StatusServiceUnavailable))

	_, err := h.retry.Download(context.Background(), u)
	if got, want := err.Error(), "gave up after 1 attempt(s):"; !strings.HasPrefix(got, want) {
		t.Errorf("error = %q, want prefix %q", got, want)
	}
}

func TestRetryGivesUpOnRateLimitKeepsStatusError(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 2})
	h.mock.EXPECT().Download(gomock.Any(), u).DoAndReturn(
		func(context.Context, *url.URL) (*download.Result, error) {
			err := statusFailure(http.StatusTooManyRequests)
			err.RetryAfter = time.Millisecond
			return nil, err
		}).Times(2)

	_, err := h.retry.Download(context.Background(), u)
	var de *download.Error
	if !errors.As(err, &de) || de.Status != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want wrapped 429 status error", err)
	}
	if !strings.HasPrefix(err.Error(), "gave up after 2 attempt(s)") {
		t.Errorf("error = %q, want give-up prefix", err)
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 2})
	gomock.InOrder(
		h.mock.EXPECT().Download(gomock.Any(), u).DoAndReturn(
			func(context.Context, *url.URL) (*download.Result, error) {
				err := statusFailure(http.StatusTooManyRequests)
				err.RetryAfter = 25 * time.Millisecond
				return nil, err
			}),
		h.mock.EXPECT().Download(gomock.Any(), u).
			Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil),
	)

	start := time.Now()
	if _, err := h.retry.Download(context.Background(), u); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Errorf("waited %v, want at least ~25ms from Retry-After", elapsed)
	}
	if _, resets := h.back.calls(); resets == 0 {
		t.Error("backoff was not reset after Retry-After")
	}
}

func TestRetryClampsRetryAfterToCap(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 2})
	download.SetRetryAfterCap(h.retry, 25*time.Millisecond)
	gomock.InOrder(
		h.mock.EXPECT().Download(gomock.Any(), u).DoAndReturn(
			func(context.Context, *url.URL) (*download.Result, error) {
				err := statusFailure(http.StatusTooManyRequests)
				err.RetryAfter = time.Hour
				return nil, err
			}),
		h.mock.EXPECT().Download(gomock.Any(), u).
			Return(&download.Result{Requested: u, Final: u, Status: http.StatusOK}, nil),
	)

	start := time.Now()
	if _, err := h.retry.Download(context.Background(), u); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v, want Retry-After clamped to the cap", elapsed)
	}
}

func TestRetryStopsWhenContextCancelled(t *testing.T) {
	u := testURL(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newRetryHarness(t, download.RetryOptions{MaxAttempts: 3})
	h.mock.EXPECT().Download(gomock.Any(), u).DoAndReturn(
		func(context.Context, *url.URL) (*download.Result, error) {
			cancel()
			return nil, statusFailure(http.StatusServiceUnavailable)
		})

	_, err := h.retry.Download(ctx, u)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if nexts, _ := h.back.calls(); nexts != 0 {
		t.Errorf("backoff calls = %d, want 0", nexts)
	}
}

func TestRetryForwardsSchemes(t *testing.T) {
	mock := mocks.NewMockDownloader(gomock.NewController(t))
	mock.EXPECT().Schemes().Return([]string{"http", "https"})
	r, err := download.NewRetry(mock, download.RetryOptions{})
	if err != nil {
		t.Fatalf("download.NewRetry: %v", err)
	}
	got := r.Schemes()
	if len(got) != 2 || got[0] != "http" || got[1] != "https" {
		t.Errorf("schemes = %v, want [http https]", got)
	}
}

func TestRetryDefaultsToThreeAttempts(t *testing.T) {
	u := testURL(t)
	h := newRetryHarness(t, download.RetryOptions{})
	h.mock.EXPECT().Download(gomock.Any(), u).
		Return(nil, statusFailure(http.StatusServiceUnavailable)).Times(3)

	_, err := h.retry.Download(context.Background(), u)
	if got, want := err.Error(), "gave up after 3 attempt(s)"; !strings.HasPrefix(got, want) {
		t.Errorf("error = %q, want prefix %q", got, want)
	}
}

func TestNewRetryRejectsMissingDownloader(t *testing.T) {
	if _, err := download.NewRetry(nil, download.RetryOptions{}); err == nil {
		t.Error("expected error for nil downloader")
	}
}
