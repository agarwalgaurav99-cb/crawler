#!/usr/bin/env bash
# End-to-end tests: builds the real binary, serves a small site, and asserts the
# observable behaviour (scope, dedupe, retries, redirects, non-HTML, SIGINT,
# rate limiting, timeout/max-bytes, exit codes).
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
GO=${GO:-go}
workdir=$(mktemp -d)
server_pid=""
fail=0

cleanup() {
	if [ -n "$server_pid" ]; then
		kill "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
	fi
	rm -rf "$workdir"
}
trap cleanup EXIT

command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }

echo "==> building crawler"
(cd "$root" && "$GO" build -o "$workdir/crawler" ./cmd/crawler)

cat >"$workdir/server.py" <<'PY'
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

port_file, log_file = sys.argv[1], sys.argv[2]
attempts = {}
lock = threading.Lock()

THROTTLE_URLS = 20

PAGES = {
    "/": '<a href="/a">a</a><a href="/a">dup</a><a href="/b">b</a>'
         '<a href="/file.pdf">pdf</a><a href="/flaky">flaky</a>'
         '<a href="/old">old</a><a href="/cross">cross</a>'
         '<a href="/#frag">frag</a><a href="http://example.invalid/">out</a>',
    "/a": '<a href="/b">b</a><a href="/">home</a>',
    "/b": "<p>leaf</p>",
    "/new": "<p>redirect target</p>",
    "/ok": "<p>fine</p>",
    "/limits": '<a href="/ok">ok</a><a href="/slow-response">slow</a><a href="/huge">huge</a>',
    "/throttle": "".join(f'<a href="/t/{i}">{i}</a>' for i in range(1, THROTTLE_URLS + 1)),
}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def reply(self, status, body=b"", content_type=None, location=None):
        self.send_response(status)
        if content_type:
            self.send_header("Content-Type", content_type)
        if location:
            self.send_header("Location", location)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def handle(self):
        try:
            super().handle()
        except (ConnectionResetError, BrokenPipeError):
            pass

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        with lock:
            attempts[path] = attempts.get(path, 0) + 1
            count = attempts[path]
            with open(log_file, "a") as f:
                f.write(f"{path}\t{time.time():.6f}\n")

        if path == "/flaky" and count < 3:
            self.reply(503, b"try later", "text/plain")
            return
        if path == "/old":
            self.reply(302, location="/new")
            return
        if path == "/cross":
            self.reply(302, location="http://example.invalid/")
            return
        if path == "/file.pdf":
            self.reply(200, b"%PDF-1.4 fake", "application/pdf")
            return
        if path == "/slow-response":
            time.sleep(1.0)
            self.reply(200, b"<p>slow</p>", "text/html; charset=utf-8")
            return
        if path == "/huge":
            self.reply(200, b"x" * 5000, "text/html; charset=utf-8")
            return
        if path.startswith("/slow/"):
            nxt = min(int(path.rsplit("/", 1)[1]) + 1, 500)
            time.sleep(0.25)
            self.reply(200, f'<a href="/slow/{nxt}">next</a>'.encode(), "text/html; charset=utf-8")
            return
        if path.startswith("/t/"):
            self.reply(429, b"slow down", "text/plain")
            return
        self.reply(200, PAGES.get(path, "<p>ok</p>").encode(), "text/html; charset=utf-8")


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w") as f:
    f.write(str(server.server_address[1]))
server.serve_forever()
PY

echo "==> starting test server"
python3 "$workdir/server.py" "$workdir/port" "$workdir/requests.log" &
server_pid=$!

for _ in $(seq 1 100); do
	[ -s "$workdir/port" ] && break
	sleep 0.05
done
[ -s "$workdir/port" ] || { echo "server did not start" >&2; exit 1; }
base="http://127.0.0.1:$(cat "$workdir/port")"
log="$workdir/requests.log"

pass() { echo "  ok   $1"; }
fail_on() { echo "  FAIL $1" >&2; fail=1; }
contains() { grep -qF -- "$2" "$1"; }
count_of() { awk -F'\t' -v p="$1" '$1 == p' "$log" | wc -l | tr -d ' '; }
count_prefix() { awk -F'\t' -v p="$1" 'index($1, p) == 1' "$log" | wc -l | tr -d ' '; }

expect_contains() { if contains "$1" "$2"; then pass "output has $2"; else fail_on "output missing $2"; fi; }
expect_absent() { if contains "$1" "$2"; then fail_on "output should not contain $2"; else pass "output omits $2"; fi; }
expect_count() {
	local got; got=$(count_of "$1")
	if [ "$got" = "$2" ]; then pass "requested $1 x$2"; else fail_on "requested $1 x$got, want x$2"; fi
}

# ---------------------------------------------------------------- phase 1: crawl
echo "==> phase 1: crawl $base/"
"$workdir/crawler" \
	--workers 4 --timeout 5s --max-pages 50 --retries 3 \
	--rate 100 --burst 10 "$base/" \
	>"$workdir/p1.out" 2>"$workdir/p1.err"

out="$workdir/p1.out"
expect_contains "$out" "URL: $base/"
expect_contains "$out" "URL: $base/a"
expect_contains "$out" "URL: $base/b"
expect_contains "$out" "URL: $base/file.pdf"
expect_contains "$out" "URL: $base/flaky"
expect_contains "$out" "URL: $base/new"

# Scope: out-of-scope hosts are printed as discovered links but never crawled.
expect_absent "$out" "URL: http://example.invalid/"
expect_absent "$out" "URL: $base/cross"
expect_count "/cross" 1

# Dedupe: duplicate, fragment and cyclic links fetch each page once.
expect_count "/" 1
expect_count "/a" 1
expect_count "/b" 1
expect_count "/file.pdf" 1

# Retry: two 503s then success.
expect_count "/flaky" 3

# Non-HTML pages are visited but list no links.
if awk -v url="URL: $base/file.pdf" '
	$0 == url { found = 1; next }
	found && /^URL: / { exit 0 }
	found && /^  - / { exit 1 }
	END { exit !found }
' "$out"; then
	pass "non-HTML page has no links"
else
	fail_on "non-HTML page listed links"
fi

# ------------------------------------------------------- phase 2: SIGINT shutdown
echo "==> phase 2: SIGINT stops the crawl cleanly"
"$workdir/crawler" \
	--workers 1 --timeout 5s --max-pages 500 --retries 1 \
	--rate 100 --burst 5 "$base/slow/0" \
	>"$workdir/p2.out" 2>"$workdir/p2.err" &
crawl_pid=$!

sleep 0.8
kill -INT "$crawl_pid" 2>/dev/null || true

exited=0
for _ in $(seq 1 100); do
	kill -0 "$crawl_pid" 2>/dev/null || { exited=1; break; }
	sleep 0.1
done
if [ "$exited" -eq 0 ]; then
	kill -9 "$crawl_pid" 2>/dev/null || true
	fail_on "crawler did not exit after SIGINT"
fi

set +e
wait "$crawl_pid" 2>/dev/null
code=$?
set -e
if [ "$code" -eq 0 ]; then pass "exit code 0 after SIGINT"; else fail_on "exit code $code after SIGINT, want 0"; fi
if contains "$workdir/p2.err" "crawl interrupted"; then pass "logged crawl interrupted"; else fail_on "missing 'crawl interrupted' log"; fi

# ------------------------------------------- phase 3: timeout and max-bytes limits
echo "==> phase 3: timeout and max-bytes are enforced, crawl continues"
"$workdir/crawler" \
	--workers 2 --timeout 300ms --max-pages 20 --retries 1 \
	--rate 100 --burst 10 --max-bytes 1000 "$base/limits" \
	>"$workdir/p3.out" 2>"$workdir/p3.err"

out="$workdir/p3.out"
expect_contains "$out" "URL: $base/ok"
expect_absent "$out" "URL: $base/slow-response"
expect_absent "$out" "URL: $base/huge"
warnings=$(grep -c "could not crawl" "$workdir/p3.err" || true)
if [ "$warnings" -ge 2 ]; then pass "warned for $warnings failed URLs"; else fail_on "warnings = $warnings, want >= 2"; fi

# --------------------------------------------------- phase 4: rate limiting (AIMD)
echo "==> phase 4: sustained 429 lowers the request rate"
"$workdir/crawler" \
	--workers 1 --timeout 5s --max-pages 100 --retries 1 \
	--rate 10 --burst 1 "$base/throttle" \
	>"$workdir/p4.out" 2>"$workdir/p4.err"

throttled=$(count_prefix "/t/")
span=$(awk -F'\t' '$1 ~ /^\/t\// { if (first == "") first = $2; last = $2 } END { if (first != "") printf "%.2f", last - first }' "$log")
# 20 URLs at the 10/s ceiling would finish in ~2s; AIMD must spread them further.
if awk -v s="$span" 'BEGIN { exit !(s >= 2.5) }'; then
	pass "throttled requests spread over ${span}s (ceiling would be ~2s)"
else
	fail_on "throttled requests spread over only ${span}s, AIMD did not slow down"
fi
if [ "$throttled" -eq 20 ]; then pass "all 20 throttled URLs attempted"; else fail_on "throttled URLs = $throttled, want 20"; fi

# --------------------------------------------------------------- phase 5: exit codes
echo "==> phase 5: exit codes"
set +e
"$workdir/crawler" >/dev/null 2>&1
code=$?
set -e
if [ "$code" -eq 2 ]; then pass "no args exits 2"; else fail_on "no args exit $code, want 2"; fi

set +e
"$workdir/crawler" "ftp://example.com/" >/dev/null 2>&1
code=$?
set -e
if [ "$code" -eq 1 ]; then pass "unsupported scheme exits 1"; else fail_on "unsupported scheme exit $code, want 1"; fi

echo
if [ "$fail" -ne 0 ]; then
	echo "E2E FAILED" >&2
	for f in "$workdir"/p*.err; do echo "--- $f ---" >&2; cat "$f" >&2; done
	exit 1
fi
echo "E2E PASSED"
