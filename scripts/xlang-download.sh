#!/usr/bin/env bash
# Go and Python finishing each other's downloads.
#
# This is the test that decides whether `download` is an abstraction or just a
# program with a file format. Everything else in the repository could pass with
# one implementation; none of this can.
#
# Two directions, and both matter:
#
#   Go submits and dies halfway   -> Python resumes and delivers
#   Python submits and dies halfway -> Go resumes and delivers
#
# "Dies halfway" is not graceful. The process is killed, so it never hands
# anything over, never releases its lease and never writes a final checkpoint —
# which is the case that loses a 40 GB download in every tool the survey looked
# at.
#
#   bash scripts/xlang-download.sh
set -uo pipefail
cd "$(dirname "$0")/.."
# See scripts/layers.sh: the layers are separate repositories now and .tree is
# the single-tree view these harnesses were written against.
ROOT="${ABSTRACTION_TREE:-$PWD}"

GO_DL="$ROOT/download/go"
PY_DL="$ROOT/download/python"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
ok()   { echo "  PASS  $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL  $1"; fail=$((fail+1)); }

PY=py
command -v py >/dev/null 2>&1 || PY=python3

echo "cross-language download conformance"
echo "  store: $WORK"
echo

# ---------------------------------------------------------------------------
# A payload big enough that a half-finished download is unambiguous, served by
# a plain HTTP server that honours Range.
# ---------------------------------------------------------------------------
PAYLOAD="$WORK/payload.bin"
"$PY" -3 - "$PAYLOAD" <<'PYEOF'
import sys
n = 4 * 1024 * 1024
open(sys.argv[1], "wb").write(bytes((i * 7 + 11) % 251 for i in range(n)))
PYEOF
DIGEST="sha256:$(sha256sum "$PAYLOAD" | cut -d' ' -f1)"
SIZE=$(stat -c %s "$PAYLOAD" 2>/dev/null || stat -f %z "$PAYLOAD")
echo "payload: $SIZE bytes  $DIGEST"

# A server that serves the payload and honours Range.
"$PY" -3 - "$PAYLOAD" "$WORK/port.txt" <<'PYEOF' &
import functools, http.server, sys, threading
path, portfile = sys.argv[1], sys.argv[2]
body = open(path, "rb").read()

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        start = 0
        rng = self.headers.get("Range")
        if rng:
            start = int(rng.split("=")[1].split("-")[0])
            self.send_response(206)
            self.send_header("Content-Range", "bytes %d-%d/%d" % (start, len(body)-1, len(body)))
        else:
            self.send_response(200)
        self.send_header("Content-Length", str(len(body) - start))
        self.end_headers()
        # Trickle, so a kill lands mid-transfer rather than after it.
        buf = body[start:]
        import time as _t
        try:
            for i in range(0, len(buf), 65536):
                self.wfile.write(buf[i:i+65536])
                self.wfile.flush()
                _t.sleep(0.05)   # trickle, so a kill lands mid-transfer
        except (ConnectionResetError, BrokenPipeError, ConnectionAbortedError, OSError):
            pass  # the client was killed. That is the point of this test.
    def log_message(self, *a):
        pass

srv = http.server.HTTPServer(("127.0.0.1", 0), H)
open(portfile, "w").write(str(srv.server_port))
srv.serve_forever()
PYEOF
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null; rm -rf "$WORK"' EXIT

for _ in $(seq 1 50); do [ -s "$WORK/port.txt" ] && break; sleep 0.1; done
URL="http://127.0.0.1:$(cat "$WORK/port.txt")/payload.bin"
echo "server:  $URL"
echo

# Helpers that submit + partially download, using each implementation.
build_go_helper() {
  mkdir -p "$WORK/gohelper"
  cat > "$WORK/gohelper/main.go" <<'GOEOF'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	download "github.com/ReinisLusis/abstraction/download/go"
	job "github.com/ReinisLusis/abstraction/job/go"
)

func main() {
	store, err := job.NewFileStore(os.Args[2])
	if err != nil {
		panic(err)
	}
	switch os.Args[1] {
	case "submit":
		id, err := download.Submit(store, download.Spec{
			Artifact: download.Artifact{Digest: os.Args[3], Size: mustInt(os.Args[4])},
			Sources:  []download.Source{{Scheme: "http", Locator: os.Args[5]}},
			Sink:     download.Sink{Final: "out/payload.bin"},
		})
		if err != nil {
			panic(err)
		}
		fmt.Print(id)
	case "partial":
		// Download for a moment, then stop as if killed: no release, no final
		// checkpoint beyond what the periodic one wrote.
		r := download.NewRunner(store, "go-helper")
		r.PersistInterval = 150 * time.Millisecond
		r.LeaseTTL = 3 * time.Second
		r.PersistEvery = 1 << 16
		ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
		defer cancel()
		r.Run(ctx, os.Args[3])
	case "finish":
		r := download.NewRunner(store, "go-finisher")
		if err := r.Run(context.Background(), os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "state":
		rec, err := store.Load(os.Args[3])
		if err != nil {
			panic(err)
		}
		fmt.Print(rec.State)
	}
}

func mustInt(s string) int64 {
	var n int64
	fmt.Sscan(s, &n)
	return n
}
GOEOF
  cp "$GO_DL/go.mod" "$WORK/gohelper/go.mod" 2>/dev/null
  ( cd "$WORK/gohelper" && \
    go mod edit -module=xlanghelper \
      -replace=github.com/ReinisLusis/abstraction/download/go="$GO_DL" \
      -replace=github.com/ReinisLusis/abstraction/job/go="$ROOT/job/go" \
      -replace=github.com/ReinisLusis/abstraction/storage/go="$ROOT/storage/go" \
      -replace=github.com/ReinisLusis/abstraction/config/go="$ROOT/config/go" && \
    go mod tidy >/dev/null 2>&1 && \
    go build -o "$WORK/gohelper/helper" . ) || return 1
}

py_helper() {
  "$PY" -3 - "$@" <<'PYEOF'
import os, sys, time
here = os.environ["XLANG_ROOT"]
sys.path.insert(0, os.path.join(here, "job", "python"))
sys.path.insert(0, os.path.join(here, "download", "python"))
from abstraction_job import FileStore
import abstraction_download as dl

mode, root = sys.argv[1], sys.argv[2]
store = FileStore(root)

if mode == "submit":
    digest, size, url = sys.argv[3], int(sys.argv[4]), sys.argv[5]
    print(dl.submit(store, dl.Spec(
        artifact=dl.Artifact(digest=digest, size=size),
        sources=[dl.Source(scheme="http", locator=url)],
        sink=dl.Sink(final="out/payload.bin"),
    )), end="")
elif mode == "partial":
    # Download briefly in a thread, then exit the process outright — no release,
    # no clean shutdown, exactly like a kill.
    import threading
    r = dl.Runner(store, "py-helper", lease_ttl=3, persist_every=1 << 16, persist_interval=0.15)
    t = threading.Thread(target=lambda: r.run(sys.argv[3]), daemon=True)
    t.start()
    time.sleep(0.9)
    os._exit(0)
elif mode == "finish":
    dl.Runner(store, "py-finisher").run(sys.argv[3])
elif mode == "state":
    print(store.load(sys.argv[3]).state, end="")
PYEOF
}

export XLANG_ROOT="$ROOT"

echo "building the Go helper..."
if ! build_go_helper; then
  echo "  could not build the Go helper; cannot run this test"
  exit 1
fi
GO="$WORK/gohelper/helper"

# ---------------------------------------------------------------------------
run_direction() {
  local name="$1" starter="$2" finisher="$3" store="$store_dir"
  mkdir -p "$store"

  local id
  if [ "$starter" = go ]; then
    id=$("$GO" submit "$store" "$DIGEST" "$SIZE" "$URL")
  else
    id=$(py_helper submit "$store" "$DIGEST" "$SIZE" "$URL")
  fi
  [ -n "$id" ] || { bad "$name: submit produced no job id"; return; }

  # Start it, then die partway through.
  if [ "$starter" = go ]; then
    "$GO" partial "$store" "$id" >/dev/null 2>&1
  else
    py_helper partial "$store" "$id" >/dev/null 2>&1
  fi

  local proven
  proven=$("$PY" -3 -c "
import json,sys
d=json.load(open(sys.argv[1]))
print((d.get('checkpoint') or {}).get('verified_prefix',0))
" "$store/jobs/$id.json")

  if [ "${proven:-0}" -le 0 ]; then
    bad "$name: nothing was checkpointed, so the resume would prove nothing"
    return
  fi
  if [ "${proven:-0}" -ge "$SIZE" ]; then
    bad "$name: the starter finished the whole file; nothing was left to resume"
    return
  fi
  echo "        $starter stopped with $proven of $SIZE bytes proven"

  # The lease is still held by a process that no longer exists. Wait it out, the
  # way a real successor must.
  sleep 4

  if [ "$finisher" = go ]; then
    "$GO" finish "$store" "$id" || { bad "$name: $finisher could not finish it"; return; }
  else
    py_helper finish "$store" "$id" || { bad "$name: $finisher could not finish it"; return; }
  fi

  local got
  got=$(sha256sum "$store/out/payload.bin" 2>/dev/null | cut -d' ' -f1)
  if [ "sha256:$got" != "$DIGEST" ]; then
    bad "$name: delivered bytes do not match ($got)"
    return
  fi
  ok "$name: $starter started it, $finisher finished it, digest matches"
}

echo
echo "Go starts, Python finishes"
store_dir="$WORK/go2py"
run_direction "go->python" go python

echo
echo "Python starts, Go finishes"
store_dir="$WORK/py2go"
run_direction "python->go" python go

echo
echo "----------------------------------------"
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
