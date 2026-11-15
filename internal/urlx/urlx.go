// Package urlx defines the crawler's canonical URL representation.
package urlx

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Normalize canonicalizes an absolute http(s) URL: lowercase scheme/host, strip
// default port and fragment, empty path becomes "/". Rejects credentials.
func Normalize(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are supported")
	}
	if u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("URL must have a hostname and no credentials")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port == "" || (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		u.Host = host
	} else {
		u.Host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

// Hostname returns the lowercased hostname of u.
func Hostname(u *url.URL) string {
	return strings.ToLower(u.Hostname())
}

// Key returns the canonical identity used for deduplication.
func Key(u *url.URL) string {
	return u.String()
}
