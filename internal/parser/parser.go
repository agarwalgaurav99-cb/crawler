// Package parser extracts links from downloaded documents.
package parser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"example.com/monzo-crawler/internal/urlx"
)

// Parser extracts absolute, normalized links from a document body.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -source=parser.go -destination=../../mocks/parser/parser.go -package=mocks
type Parser interface {
	Parse(ctx context.Context, base *url.URL, body []byte) ([]string, error)
}

// HTML extracts anchor href values from an HTML document.
type HTML struct{}

// Parse returns unique, normalized links resolved against base.
func (HTML) Parse(ctx context.Context, base *url.URL, body []byte) ([]string, error) {
	z := html.NewTokenizer(bytes.NewReader(body))
	seen := make(map[string]struct{})
	var links []string

	for {
		if err := ctx.Err(); err != nil {
			return links, err
		}
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); err != nil && !errors.Is(err, io.EOF) {
				return links, err
			}
			return links, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if !hasAttr || !strings.EqualFold(string(name), "a") {
				continue
			}
			for {
				key, val, more := z.TagAttr()
				if strings.EqualFold(string(key), "href") {
					if link, ok := resolve(base, string(val)); ok {
						if _, dup := seen[link]; !dup {
							seen[link] = struct{}{}
							links = append(links, link)
						}
					}
					break
				}
				if !more {
					break
				}
			}
		}
	}
}

func resolve(base *url.URL, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	normalized, err := urlx.Normalize(base.ResolveReference(parsed).String())
	if err != nil {
		return "", false
	}
	return normalized.String(), true
}
