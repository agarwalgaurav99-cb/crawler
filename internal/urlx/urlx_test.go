package urlx

import "testing"

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"HTTPS://Example.COM:443/a#section", "https://example.com/a"},
		{"http://Example.com:8080/search?q=a#top", "http://example.com:8080/search?q=a"},
		{"https://example.com", "https://example.com/"},
		{"http://[::1]:80/x", "http://[::1]/x"},
		{"http://[::1]:8080/x", "http://[::1]:8080/x"},
	}
	for _, tt := range tests {
		got, err := Normalize(tt.input)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", tt.input, err)
		}
		if got.String() != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got.String(), tt.want)
		}
	}
}

func TestNormalizeRejectsUnsupportedSchemesAndCredentials(t *testing.T) {
	for _, input := range []string{"mailto:a@example.com", "https://user:pass@example.com/", "relative/path"} {
		if _, err := Normalize(input); err == nil {
			t.Errorf("Normalize(%q) unexpectedly succeeded", input)
		}
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	for _, input := range []string{"https://Example.com/a?b=c#d", "http://example.com:8080/"} {
		once, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", input, err)
		}
		twice, err := Normalize(once.String())
		if err != nil {
			t.Fatalf("Normalize(%q): %v", once.String(), err)
		}
		if once.String() != twice.String() {
			t.Errorf("Normalize is not idempotent for %q: %q != %q", input, once, twice)
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{"https://example.com/", "http://example.com:8080/a?b#c", "mailto:x@y"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := Normalize(raw)
		if err != nil {
			return
		}
		again, err := Normalize(u.String())
		if err != nil {
			t.Fatalf("Normalize(%q) result %q is not re-normalizable: %v", raw, u, err)
		}
		if again.String() != u.String() {
			t.Fatalf("Normalize not idempotent: %q -> %q -> %q", raw, u, again)
		}
	})
}
