// Package httpdown implements the http/https Downloader.
package httpdown

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"example.com/monzo-crawler/internal/download"
	"example.com/monzo-crawler/internal/urlx"
)

// DefaultUserAgent identifies the crawler to servers.
const DefaultUserAgent = "SimpleDomainCrawler/1.0"

// drainLimit caps how much of a rejected body is read to reuse the connection.
const drainLimit = 64 << 10

// HTTPOptions configures the http/https downloader.
type HTTPOptions struct {
	AllowedHost string
	UserAgent   string
	Timeout     time.Duration
	MaxBytes    int64
	MaxConns    int
	Checks      download.Chain
}

// HTTP downloads http and https URLs.
type HTTP struct {
	client      *http.Client
	allowedHost string
	userAgent   string
	maxBytes    int64
	checks      download.Chain
}

// NewHTTP builds a downloader that refuses redirects to other hostnames.
func NewHTTP(opts HTTPOptions) (*HTTP, error) {
	if opts.AllowedHost == "" || opts.Timeout <= 0 || opts.MaxBytes < 1 {
		return nil, fmt.Errorf("allowed host, timeout and max bytes must be set")
	}
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	maxConns := opts.MaxConns
	if maxConns < 1 {
		maxConns = 8
	}
	allowedHost := strings.ToLower(opts.AllowedHost)

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: opts.Timeout,
		MaxIdleConnsPerHost:   maxConns,
		ForceAttemptHTTP2:     true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !strings.EqualFold(req.URL.Hostname(), allowedHost) {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}
	checks := opts.Checks
	if checks == nil {
		checks = download.DefaultChecks(allowedHost, opts.MaxBytes)
	}
	return &HTTP{
		client:      client,
		allowedHost: allowedHost,
		userAgent:   userAgent,
		maxBytes:    opts.MaxBytes,
		checks:      checks,
	}, nil
}

// Schemes implements download.Downloader.
func (d *HTTP) Schemes() []string { return []string{"http", "https"} }

// Download fetches u and runs the verification chain before returning.
func (d *HTTP) Download(ctx context.Context, u *url.URL) (*download.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &download.Error{Kind: download.KindPolicy, Err: err}
	}
	req.Header.Set("User-Agent", d.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, download.Classify(err)
	}
	defer resp.Body.Close()

	if err := download.StatusError(resp.StatusCode); err != nil {
		// Drain so the connection can be reused.
		io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
		var de *download.Error
		if errors.As(err, &de) && de.Retryable() {
			de.RetryAfter = parseRetryAfter(resp.Header)
		}
		return nil, err
	}

	finalURL, err := urlx.Normalize(resp.Request.URL.String())
	if err != nil {
		return nil, &download.Error{Kind: download.KindVerify, Err: fmt.Errorf("final URL: %w", err)}
	}

	result := &download.Result{
		Requested:   u,
		Final:       finalURL,
		Status:      resp.StatusCode,
		Header:      resp.Header.Clone(),
		ContentType: mediaType(resp.Header.Get("Content-Type")),
	}

	if isHTML(result.ContentType) {
		if resp.ContentLength > d.maxBytes {
			return nil, &download.Error{
				Kind:   download.KindVerify,
				Status: resp.StatusCode,
				Err:    fmt.Errorf("content length %d exceeds limit %d", resp.ContentLength, d.maxBytes),
			}
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, d.maxBytes+1))
		if err != nil {
			return nil, download.Classify(err)
		}
		if int64(len(body)) > d.maxBytes {
			return nil, &download.Error{
				Kind:   download.KindVerify,
				Status: resp.StatusCode,
				Err:    fmt.Errorf("body exceeds limit of %d bytes", d.maxBytes),
			}
		}
		result.Body = body
	}

	if err := d.checks.Verify(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

func mediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		return strings.ToLower(mt)
	}
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

func isHTML(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

// parseRetryAfter reads Retry-After (seconds or HTTP-date); invalid values yield zero.
func parseRetryAfter(h http.Header) time.Duration {
	return parseRetryAfterAt(h.Get("Retry-After"), time.Now())
}

func parseRetryAfterAt(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
