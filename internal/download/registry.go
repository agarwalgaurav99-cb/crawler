package download

import (
	"fmt"
	"net/url"
	"strings"
)

// Registry maps URL schemes to downloaders.
type Registry struct {
	byScheme map[string]Downloader
}

// NewRegistry indexes downloaders by every scheme they advertise.
func NewRegistry(downloaders ...Downloader) *Registry {
	r := &Registry{byScheme: make(map[string]Downloader, len(downloaders))}
	for _, d := range downloaders {
		for _, scheme := range d.Schemes() {
			r.byScheme[strings.ToLower(scheme)] = d
		}
	}
	return r
}

// For returns the downloader registered for u's scheme.
func (r *Registry) For(u *url.URL) (Downloader, error) {
	if d, ok := r.byScheme[strings.ToLower(u.Scheme)]; ok {
		return d, nil
	}
	return nil, &Error{Kind: KindPolicy, Err: fmt.Errorf("no downloader for scheme %q", u.Scheme)}
}
