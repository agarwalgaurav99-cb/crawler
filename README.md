# Monzo Crawler — Single-Host Web Crawler

Crawls one exact hostname breadth-first with a bounded worker pool, printing each
page and its discovered links. Written directly on `net/http`, goroutines and
channels — no crawling framework.

- **Language / runtime:** Go 1.23
- **Dependencies:** [`golang.org/x/net/html`](https://pkg.go.dev/golang.org/x/net/html) (parsing), [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) (throttling), [`github.com/cenkalti/backoff/v5`](https://pkg.go.dev/github.com/cenkalti/backoff/v5) (retries)
- **Module:** `example.com/monzo-crawler`

There is a single process (`main.go`). Its one job is to drive a **download
decorator chain**, outermost first:

| Layer              | Package             | Responsibility                                     |
|--------------------|---------------------|----------------------------------------------------|
| `retry.Retry`      | `download/retry`    | retries retryable failures with exponential backoff |
| `ratelimit.RateLimit` | `download/ratelimit` | shared token bucket + AIMD adjustment           |
| `httpdown.HTTP`    | `download/httpdown` | fetches, follows safe redirects, verifies response |

## Repository layout

```
.
├── main.go                 # flags, signal handling, wiring
├── internal/
│   ├── app/                # Run(): assembles the whole chain
│   ├── crawl/              # frontier loop + worker pool
│   ├── state/              # frontier (queue, dedupe, completion) behind an interface
│   ├── download/           # Downloader contract, registry, typed errors, verify chain
│   │   ├── httpdown/       # http/https downloader
│   │   ├── ratelimit/      # AIMD throttling decorator
│   │   └── retry/          # retry/backoff decorator
│   ├── parser/             # HTML anchor extraction
│   ├── filter/             # composable scope rules
│   ├── sink/               # page output
│   └── urlx/               # URL normalization and canonical identity
├── mocks/                  # gomock-generated mocks (go generate ./...)
├── scripts/e2e.sh          # end-to-end test over the built binary
├── go.mod / go.sum
└── README.md
```

### Layering

```
crawl (orchestration)  →  download chain (transport)  →  parser / filter  →  sink
                      ↘  state.Frontier (queue + dedupe)
```

- `crawl` knows only interfaces: `download.Downloader`, `parser.Parser`,
  `filter.Chain`, `state.Frontier`, `sink.Sink`.
- `state` hides the frontier; `state.Memory` is the only backend today, and a
  durable one can be added without touching the loop.
- `download` holds the shared contract (`Result`, `Downloader`, typed `Error`);
  each decorator lives in its own package and only depends on that contract.

## URL & crawl model

The crawl is bounded, breadth-first over one exact hostname.

**URL identity** — scheme and hostname are lowercased, default ports and
fragments removed, an empty path becomes `/`. Query strings are kept; trailing
slashes are *not* collapsed because servers can distinguish them. Normalization
is what makes `/a`, `/a#top` and `HTTP://HOST/a` one URL, so each is fetched once.

**Scope** — a discovered link is scheduled only if it passes every rule in
`filter.Chain`. Today that is `filter.Scheme("http", "https")` plus
`filter.ExactHost(start)`, an exact hostname match: the parent domain, sibling
subdomains and lookalike suffixes are all excluded.

**Frontier** — the coordinator owns a queue, a dedupe set and a completion log.
A URL is added only if unseen; `--max-pages` caps the number scheduled. Because
one goroutine drives it, no locking is needed.

## How to run

### 1. Build

```bash
go mod tidy     # first build only
go build ./...
```

### 2. Crawl

```bash
go run . https://crawlme.monzo.com/
```

### 3. Tune the limits

```bash
go run . --workers 8 --timeout 10s --max-pages 10000 \
  --max-bytes 2000000 --retries 3 https://crawlme.monzo.com/
```

### 4. Throttle to be polite

`--rate` is the requests/sec ceiling shared by all workers and `--burst` the
token bucket burst; the AIMD controller lowers the rate on back-pressure.

```bash
go run . --rate 5 --burst 2 https://crawlme.monzo.com/
```

### 5. Read the output

For each successful HTML page, stdout prints the page URL and its normalized
anchor links. Non-HTML responses are visited but list no links. Fetch failures go
to stderr once per URL, after retries are exhausted.

## Configuration

All configuration is command-line flags.

| Flag          | Default   | Meaning                                              |
|---------------|-----------|------------------------------------------------------|
| `--workers`   | `8`       | concurrent HTTP workers                              |
| `--timeout`   | `10s`     | per-request timeout                                  |
| `--max-pages` | `10000`   | maximum unique URLs scheduled                        |
| `--max-bytes` | `2000000` | maximum buffered HTML body size                      |
| `--retries`   | `3`       | total attempts for retryable failures                |
| `--rate`      | `10`      | requests/sec ceiling (AIMD start point)              |
| `--burst`     | `1`       | token bucket burst                                   |

## Output & exit codes

| Stream | Content                                                        |
|--------|----------------------------------------------------------------|
| stdout | `URL: <url>` followed by `  - <link>` lines per discovered link |
| stderr | one `WARN could not crawl` per URL that permanently failed     |

Page order is completion order, not strict BFS.

| Exit | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | crawl finished, or interrupted by `SIGINT`/`SIGTERM` |
| `1`  | configuration or run error (invalid URL, bad flags) |
| `2`  | wrong number of arguments (usage printed)          |

## Testing

```bash
go generate ./...   # regenerate mocks after changing an interface
go test ./...       # unit tests
go test -race ./... # race detector (worker pool and throttle controller)
./scripts/e2e.sh    # end-to-end over the built binary (needs python3, ~10s)
```

Unit tests are mock-driven: `mocks/download`, `mocks/parser`, `mocks/sink`,
`mocks/state` and `mocks/backoff` fail the test on any unexpected fetch, parse,
emit or frontier call. Scope rules stay real (`filter.Chain`), so crawl tests
still exercise in-scope filtering.

`scripts/e2e.sh` builds the binary, serves a local site, and asserts scope,
dedupe, retries, redirects, non-HTML handling, SIGINT shutdown,
timeout/max-bytes enforcement, AIMD rate limiting and CLI exit codes.

## Design notes & trade-offs

- **Package seams.** Downloaders are chosen by scheme through
  `download.Registry`; parsing, scope and output are interfaces. Every package is
  unit-testable in isolation, and the crawl loop has no knowledge of HTTP.
- **Per-download verification.** Every downloader runs a named `download.Chain`
  (`status`, `final-host`, `body-size`) before returning a result. Failures are
  typed `download.Error` values carrying a `Kind` and a `Retryable()` method, so
  callers never parse error strings.
- **Retries and backoff.** `retry.Retry` drives `backoff/v5` with a fresh
  exponential policy per attempt (500ms initial, 1.5x growth, 60s cap, ±50%
  jitter). Only errors for which `Retryable()` is true (5xx, 429, timeouts,
  transient network failures) are retried, up to `--retries` total attempts. A
  `Retry-After` header resets the backoff but is capped at 5s so one server
  cannot stall the crawl.
- **Adaptive throttling.** `ratelimit.RateLimit` owns a shared `rate.Limiter`
  controlled by an AIMD loop. Once per second it looks at the window's error
  ratio: above 20% it halves the limit (429 and 5xx both count as back-pressure),
  below 5% it adds a small step, between 5–20% it holds steady to avoid
  oscillation. The limit is clamped to `[rate/10, rate]`, so it never reaches
  zero. `Retry-After` only triggers the decrease path; per-URL waiting stays in
  `retry.Retry`, which sits outside this decorator.
- **Exact hostname boundary.** Only URLs whose parsed hostname exactly equals the
  start hostname pass `filter.ExactHost`. The HTTP client's redirect hook also
  rejects cross-host redirects, and the final URL is re-checked by the
  verification chain.
- **Concurrency.** A coordinator owns the frontier; a fixed worker pool fetches
  pages. Single-goroutine ownership keeps dedupe race-free without a mutex, at the
  cost of a serialization point — fine for one host.
- **Resource limits.** `--max-bytes` is enforced while reading HTML; non-HTML
  bodies are never buffered. `--max-pages` bounds the frontier.
- **Error resilience.** Rejected responses are drained up to a bounded size
  before closing, keeping HTTP keep-alive effective during error storms.
- **Parsing.** Relative references resolve against the *final* response URL.
  Only `<a href>` is considered; no JavaScript is executed.
- **Shutdown.** `SIGINT`/`SIGTERM` cancel the crawl context; in-flight requests
  stop and the process exits `0`.
- **In-memory frontier.** `state.Memory` is lost on exit, so a resumed crawl
  restarts from the beginning. Possible extension: a durable backend behind the
  existing `state.Frontier` interface (pending / in-progress / done states, with
  in-progress rows re-queued on startup).
- **One host at a time.** The exact-host rule and a single global limiter target
  politeness toward one server. Possible extension: per-host shards with their own
  worker pool, limiter and scope rules.
- **Politeness gaps.** No `robots.txt` handling and no per-host crawl-delay.
  Possible extensions: `robots.txt` compliance, per-host rate limits, metrics.
