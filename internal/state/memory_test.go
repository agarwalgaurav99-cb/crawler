package state_test

import (
	"context"
	"net/url"
	"testing"

	"example.com/monzo-crawler/internal/state"
	"example.com/monzo-crawler/internal/urlx"
)

func TestMemoryAddDedupes(t *testing.T) {
	f := state.NewMemory()
	a := mustURL(t, "https://example.com/a")

	added, err := f.Add(a)
	if err != nil || !added {
		t.Fatalf("first Add = %v, %v; want true, nil", added, err)
	}
	added, err = f.Add(a)
	if err != nil || added {
		t.Fatalf("duplicate Add = %v, %v; want false, nil", added, err)
	}
}

func TestMemoryNextIsFIFO(t *testing.T) {
	f := state.NewMemory()
	a := mustURL(t, "https://example.com/a")
	b := mustURL(t, "https://example.com/b")
	if _, err := f.Add(a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Add(b); err != nil {
		t.Fatal(err)
	}

	for _, want := range []*url.URL{a, b} {
		got, ok, err := f.Next(context.Background())
		if err != nil || !ok {
			t.Fatalf("Next = %v, %v, %v; want a URL", got, ok, err)
		}
		if got.String() != want.String() {
			t.Errorf("Next = %q, want %q", got, want)
		}
	}
}

func TestMemoryNextEmpty(t *testing.T) {
	f := state.NewMemory()
	got, ok, err := f.Next(context.Background())
	if got != nil || ok || err != nil {
		t.Errorf("Next on empty = %v, %v, %v; want nil, false, nil", got, ok, err)
	}
}

func TestMemoryNextReturnsOnCancellation(t *testing.T) {
	f := state.NewMemory()
	if _, err := f.Add(mustURL(t, "https://example.com/a")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := f.Next(ctx); err == nil {
		t.Fatal("Next with cancelled context should fail")
	}
}

func TestMemoryDoneIsNoop(t *testing.T) {
	f := state.NewMemory()
	if err := f.Done(mustURL(t, "https://example.com/a")); err != nil {
		t.Errorf("Done = %v, want nil", err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := urlx.Normalize(raw)
	if err != nil {
		t.Fatalf("normalize %q: %v", raw, err)
	}
	return u
}
