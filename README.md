# Go single-host web crawler

A small concurrent crawler written directly in Go. It uses the standard
`net/http`, `net/url`, goroutines and channels, plus `golang.org/x/net/html`
for HTML parsing and `github.com/cenkalti/backoff/v5` for retry scheduling.
It does not use a crawling framework.

## Project layout

- `main.go` — flag parsing, dependency wiring, signal handling.
- `internal/urlx` — URL normalization and canonical identity.
- `internal/download` — scheme registry, HTTP downloader, typed errors, and the
  per-download verification chain.
- `internal/parser` — HTML link extraction.
- `internal/filter` — composable scope rules.
- `internal/crawl` — frontier loop, worker pool, and output sink.

## Requirements

- Go 1.23+
- Network access to download dependencies on first build

## Run

```bash
go mod tidy
go run . https://crawlme.monzo.com/
```

Optional limits:

```bash
go run . --workers 8 --timeout 10s --max-pages 10000 \
  --max-bytes 2000000 --retries 3 https://crawlme.monzo.com/
```

For each successful HTML page, stdout prints the page URL and discovered
normalized anchor links. Fetch failures are logged to stderr once per URL,
after any retries are exhausted. Non-HTML responses are visited but have no
discovered links.

## Tests

```bash
go generate ./...   # regenerate mocks after changing download.Downloader
go test ./...
go test -race ./...
```

## Design notes

- **Package seams:** Downloaders are selected by URL scheme through
  `download.Registry`; HTML extraction lives behind `parser.Parser`; scope
  rules compose as `filter.Rule`s; the crawl loop consumes all three. Each
  package is unit-testable in isolation.
- **Per-download verification:** Every downloader runs a named
  `download.Chain` (`status`, `final-host`, `body-size`) before returning a
  result. Failures are typed `download.Error` values carrying a `Kind` and a
  `Retryable` flag, so callers never parse error strings.
- **Retries and backoff:** `download.Retry` decorates any downloader and
  advertises its schemes unchanged, driving `github.com/cenkalti/backoff/v5`
  with a fresh exponential policy per attempt using the library defaults
  (500ms initial interval, 1.5x growth, 60s interval cap, +/-50% jitter). Only
  errors whose `Retryable` flag is set (5xx, 429, timeouts, transient network
  failures) are retried; everything else is returned as a permanent failure, up
  to `--retries` total attempts. A `Retry-After` header is passed to the
  library as a `RetryAfterError` (resetting the backoff) but is capped at 5s,
  so a single server cannot stall the crawl. Each attempt is logged at debug
  level; the crawl logs one warning per URL once retries are exhausted.
- **Rate-limit cool-down:** A 429 also pauses a shared `download.Gate`, so all
  workers back off together instead of continuing to hit a host that has asked
  the crawler to slow down.
- **Error resilience:** Rejected responses are drained up to a bounded size
  before closing, keeping HTTP keep-alive effective during error storms.
- **Exact hostname boundary:** Only URLs whose parsed hostname exactly equals
  the starting hostname pass `filter.ExactHost`. This excludes the parent
  domain, sibling subdomains, and lookalike suffixes.
- **Redirect safety:** The HTTP client's redirect hook rejects redirects to a
  different hostname. The final response URL is checked again by the
  verification chain.
- **Concurrency:** A coordinator owns the pending queue and `scheduled` set;
  a fixed worker pool fetches pages. This keeps deduplication race-free without
  a mutex around crawl state. Output order is completion order, not strict BFS.
- **URL identity:** Scheme and hostname are lowercased, default ports removed,
  empty paths become `/`, and fragments removed. Query strings are retained.
  Trailing slashes are not collapsed because servers can distinguish them.
- **Resource limits:** Worker count, request timeout, maximum scheduled pages,
  and maximum HTML response size are configurable. HTML bodies are read through
  a hard size limit; non-HTML responses are not buffered.
- **Parsing:** Relative references are resolved against the final response URL.
  Only anchor `href` values are considered. The parser does not execute JS.
- **Shutdown:** `SIGINT`/`SIGTERM` cancel the crawl context so in-flight
  requests stop and the process exits cleanly.
- **Testing:** `mocks/download`, `mocks/parser` and `mocks/crawl` hold
  gomock-generated mocks for `download.Downloader`, `parser.Parser` and
  `crawl.Sink` (regenerate with `go generate ./...`). Download and crawl unit
  tests assert through expectations, so an unexpected URL fetch, parser call or
  page emit fails the test. Scope rules stay real (`filter.Chain`) so crawl
  tests still exercise in-scope filtering, and the HTTP integration tests use
  the real downloader, parser and filter end to end. `export_test.go` exposes
  the few unexported knobs (backoff factory, Retry-After cap, gate clock) to the
  external download test package.
- ** Other Production considerations:** For a public production crawler, add
  `robots.txt` compliance, per-host crawl-delay tuning, persistent frontier
  state for resume, metrics.

