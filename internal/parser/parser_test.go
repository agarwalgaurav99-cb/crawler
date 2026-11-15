package parser

import (
	"context"
	"net/url"
	"reflect"
	"testing"
)

func TestHTMLParse(t *testing.T) {
	base, err := url.Parse("https://example.com/dir/page.html")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`<html><body>
		<a href="/a">a</a>
		<a href="b">b</a>
		<a href="https://example.com/a#frag">duplicate of a</a>
		<a href="mailto:x@example.com">mail</a>
		<a href="">empty</a>
		<a href="//other.example.com/x">protocol relative</a>
		<a name="anchor">no href</a>
	</body></html>`)

	got, err := HTML{}.Parse(context.Background(), base, body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{
		"https://example.com/a",
		"https://example.com/dir/b",
		"https://other.example.com/x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
}

func TestHTMLParseMalformedDocument(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	body := []byte(`<a href="/one"><div><a href="/two">`)
	got, err := HTML{}.Parse(context.Background(), base, body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"https://example.com/one", "https://example.com/two"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
}

func TestHTMLParseRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base, _ := url.Parse("https://example.com/")
	if _, err := (HTML{}).Parse(ctx, base, []byte(`<a href="/a">`)); err == nil {
		t.Fatal("expected cancellation error")
	}
}
