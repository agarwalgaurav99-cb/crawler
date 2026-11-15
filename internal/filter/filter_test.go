package filter

import (
	"net/url"
	"reflect"
	"testing"
)

func TestExactHostnameBoundary(t *testing.T) {
	chain := Chain{Scheme("http", "https"), ExactHost("crawlme.monzo.com")}
	tests := []struct {
		candidate string
		want      bool
	}{
		{"https://crawlme.monzo.com/inside", true},
		{"https://monzo.com/", false},
		{"https://community.monzo.com/", false},
		{"https://crawlme.monzo.com.evil.test/", false},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.candidate)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.candidate, err)
		}
		if got := chain.Allow(u); got != tt.want {
			t.Errorf("Allow(%q) = %v, want %v", tt.candidate, got, tt.want)
		}
	}
}

func TestSchemeRule(t *testing.T) {
	chain := Chain{Scheme("http", "https")}
	for _, raw := range []string{"http://example.com/", "https://example.com/"} {
		u, _ := url.Parse(raw)
		if !chain.Allow(u) {
			t.Errorf("Allow(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"mailto:a@example.com", "ftp://example.com/"} {
		u, _ := url.Parse(raw)
		if chain.Allow(u) {
			t.Errorf("Allow(%q) = true, want false", raw)
		}
	}
}

func TestFilterPreservesOrderAndDropsInvalid(t *testing.T) {
	chain := Chain{ExactHost("example.com")}
	got := chain.Filter([]string{
		"https://example.com/one",
		"https://other.example.com/two",
		"https://example.com/three",
	})
	var urls []string
	for _, u := range got {
		urls = append(urls, u.String())
	}
	want := []string{"https://example.com/one", "https://example.com/three"}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("Filter = %v, want %v", urls, want)
	}
}
