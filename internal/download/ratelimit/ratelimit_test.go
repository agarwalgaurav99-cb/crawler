package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	download "example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/download/ratelimit"
	mocks "example.com/monzo-crawler/mocks/download"
)

func TestRateLimitDelegatesWhenTokenAvailable(t *testing.T) {
	mock := mocks.NewMockDownloader(gomock.NewController(t))
	mock.EXPECT().Download(gomock.Any(), gomock.Any()).
		Return(&download.Result{Status: 200}, nil)

	r := ratelimit.New(mock, ratelimit.Options{MaxRate: 1})
	if _, err := r.Download(context.Background(), nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
}

func TestRateLimitWaitsForToken(t *testing.T) {
	mock := mocks.NewMockDownloader(gomock.NewController(t))
	mock.EXPECT().Download(gomock.Any(), gomock.Any()).
		Return(&download.Result{Status: 200}, nil).Times(2)

	// 2 req/s, burst 1: the second call must wait ~500ms for a token.
	r := ratelimit.New(mock, ratelimit.Options{MaxRate: 2, Burst: 1})

	start := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := r.Download(context.Background(), nil); err != nil {
			t.Fatalf("Download #%d: %v", i+1, err)
		}
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Errorf("second download returned after %v, want ~500ms wait", elapsed)
	}
}

func TestRateLimitReturnsOnCancellation(t *testing.T) {
	mock := mocks.NewMockDownloader(gomock.NewController(t))
	mock.EXPECT().Download(gomock.Any(), gomock.Any()).
		Return(&download.Result{Status: 200}, nil).Times(1)

	// A tiny rate with burst 1 lets one call through, then blocks.
	r := ratelimit.New(mock, ratelimit.Options{MaxRate: 0.001, Burst: 1})
	if _, err := r.Download(context.Background(), nil); err != nil {
		t.Fatalf("first Download: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Download(ctx, nil); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
