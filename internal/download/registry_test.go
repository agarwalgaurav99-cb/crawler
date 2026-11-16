package download_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	download "example.com/monzo-crawler/internal/download"
	mocks "example.com/monzo-crawler/mocks/download"
)

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
