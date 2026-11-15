package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"example.com/monzo-crawler/internal/urlx"
)

// DefaultUserAgent identifies the crawler to servers.
const DefaultUserAgent = "SimpleDomainCrawler/1.0"

// drainLimit bounds how much of a rejected response body is read so the
// underlying connection can be reused.
const drainLimit = 64 << 10

// HTTPOptions configures the http/https downloader.
type HTTPOptions struct {
	AllowedHost string
	UserAgent   string
	Timeout     time.Duration
	MaxBytes    int64
	MaxConns    int
	Checks      Chain
}

// HTTP downloads http and https URLs.
type HTTP struct {
	client      *http.Client
	allowedHost string
	userAgent   string
	maxBytes    int64
	checks      Chain
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
		checks = DefaultChecks(allowedHost, opts.MaxBytes)
	}
	return &HTTP{
		client:      client,
		allowedHost: allowedHost,
		userAgent:   userAgent,
		maxBytes:    opts.MaxBytes,
		checks:      checks,
	}, nil
}

// Schemes implements Downloader.
func (d *HTTP) Schemes() []string { return []string{"http", "https"} }

// Download fetches u and runs the verification chain before returning.
func (d *HTTP) Download(ctx context.Context, u *url.URL) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Error{Kind: KindPolicy, Err: err}
	}
	req.Header.Set("User-Agent", d.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, classify(err)
	}
	defer resp.Body.Close()

	if err := statusError(resp.StatusCode); err != nil {
		// Drain a bounded amount so the transport can reuse the connection
		// during error storms instead of re-dialling for every request.
		io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
		var de *Error
		if errors.As(err, &de) && de.Retryable {
			de.RetryAfter = parseRetryAfter(resp.Header)
		}
		return nil, err
	}

	finalURL, err := urlx.Normalize(resp.Request.URL.String())
	if err != nil {
		return nil, &Error{Kind: KindVerify, Err: fmt.Errorf("final URL: %w", err)}
	}

	result := &Result{
		Requested:   u,
		Final:       finalURL,
		Status:      resp.StatusCode,
		Header:      resp.Header.Clone(),
		ContentType: mediaType(resp.Header.Get("Content-Type")),
	}

	if isHTML(result.ContentType) {
		if resp.ContentLength > d.maxBytes {
			return nil, &Error{
				Kind:   KindVerify,
				Status: resp.StatusCode,
				Err:    fmt.Errorf("content length %d exceeds limit %d", resp.ContentLength, d.maxBytes),
			}
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, d.maxBytes+1))
		if err != nil {
			return nil, classify(err)
		}
		if int64(len(body)) > d.maxBytes {
			return nil, &Error{
				Kind:   KindVerify,
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

func classify(err error) error {
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: KindNetwork, Retryable: false, Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindTimeout, Retryable: true, Err: err}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Kind: KindTimeout, Retryable: true, Err: err}
	}
	return &Error{Kind: KindNetwork, Retryable: true, Err: err}
}
