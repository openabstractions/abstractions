#!/usr/bin/env bash
# Does any of this actually transfer to Lemonade?
#
# The claim is that a C++ application can use the abstraction to get durable
# downloads it does not perform itself, and can stop them with a button, without
# either side importing the other. This runs that.
#
#   the application    Lemonade's own job library, compiled from the fork
#   the supervisor     jobd, Go, a separate process
#   the contract       a job record and a lease. Nothing else passes between them.
#
# Four things have to be true, and each is checked rather than narrated:
#
#   1. C++ submits work Go can find.
#   2. Go does the work while the C++ program is not running.
#   3. C++ stops it MID-TRANSFER, holding no lease — the pause button.
#   4. C++ resumes it, and the bytes are right.
#
# Point 3 is the one that needed schema 4. Before it, cancelling or pausing a job
# somebody else was working on could not be expressed at all.
#
#   usage: lemonade-drives-it.sh [path-to-cpp-jobctl]
#     default: /c/jobbuild/jc/jobctl.exe, built by the fork's job library

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CPP="${1:-/c/jobbuild/jc/jobctl.exe}"
WORK="$(mktemp -d)"

# A machine of its own. This must not touch a real store or a real NAS.
export APPDATA="$WORK/cfg"
export XDG_CONFIG_HOME="$WORK/cfg"
export ABSTRACTION_STORE="$WORK/store"
export JOB_STORE="$ABSTRACTION_STORE"
unset ABSTRACTION_NAS_STORE 2>/dev/null || true
mkdir -p "$APPDATA" "$ABSTRACTION_STORE" "$WORK/out"

pass=0; fail=0
ok()   { echo "  PASS  $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL  $1"; fail=$((fail+1)); }
step() { printf '\n--- %s ---\n' "$*"; }

PY=py
command -v py >/dev/null 2>&1 || PY=python3

cleanup() {
  "$WORK/jobd.exe" stop >/dev/null 2>&1
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

[ -x "$CPP" ] || { echo "no C++ jobctl at $CPP — build it from the fork first"; exit 1; }

step "the two sides"
echo "  application:  $CPP  (C++, from forks/lemonade)"
(cd "$ROOT/download/go" && go build -o "$WORK/jobd.exe" ./cmd/jobd) || { echo "build jobd"; exit 1; }
echo "  supervisor:   jobd  (Go)"
echo "  neither imports the other. What passes between them is a record."

# A payload that trickles, so a button press lands mid-transfer.
PAYLOAD="$WORK/weights.gguf"
"$PY" -3 - "$PAYLOAD" <<'PYEOF'
import sys
n = 12 * 1024 * 1024
open(sys.argv[1], "wb").write(bytes((i * 13 + 5) % 251 for i in range(n)))
PYEOF
WANT=$(sha256sum "$PAYLOAD" | cut -d' ' -f1)

"$PY" -3 - "$PAYLOAD" "$WORK/port.txt" <<'PYEOF' &
import http.server, sys, time
path, portfile = sys.argv[1], sys.argv[2]
body = open(path, "rb").read()

class H(http.server.BaseHTTPRequestHandler):
    def do_HEAD(self):
        self.send_response(200)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
    def do_GET(self):
        start = 0
        rng = self.headers.get("Range")
        if rng:
            start = int(rng.split("=")[1].split("-")[0])
            self.send_response(206)
            self.send_header("Content-Range", "bytes %d-%d/%d" % (start, len(body)-1, len(body)))
        else:
            self.send_response(200)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(len(body) - start))
        self.end_headers()
        buf = body[start:]
        try:
            for i in range(0, len(buf), 32768):
                self.wfile.write(buf[i:i+32768])
                self.wfile.flush()
                time.sleep(0.04)
        except Exception:
            pass
    def log_message(self, *a):
        pass

srv = http.server.HTTPServer(("127.0.0.1", 0), H)
open(portfile, "w").write(str(srv.server_port))
srv.serve_forever()
PYEOF
SERVER_PID=$!
for _ in $(seq 1 50); do [ -s "$WORK/port.txt" ] && break; sleep 0.1; done
URL="http://127.0.0.1:$(cat "$WORK/port.txt")/weights.gguf"
DEST="$WORK/out/weights.gguf"
# A path inside the JSON spec is never touched by the shell's path translation,
# so it has to be written the way the programs reading it will see it. Getting
# this wrong sent a finished download to C:\tmp while the test looked in /tmp
# and reported that nothing had happened.
DEST_JSON=$(cygpath -w "$DEST" 2>/dev/null || echo "$DEST")
DEST_JSON=${DEST_JSON//\\/\\\\}

# state <id> — read a field of the record, using whichever side is convenient.
field() { "$CPP" show "$1" | tr -d ' ' | grep -o "\"$2\":[^,]*" | head -1 | cut -d: -f2- | tr -d '"'; }
done_bytes() { "$CPP" show "$1" | tr -d ' \n' | grep -o '"done":[0-9]*' | head -1 | cut -d: -f2; }

# ---------------------------------------------------------------------------
step "1. the C++ application submits work"
# ---------------------------------------------------------------------------
SPEC="{\"artifact\":{\"digest\":\"sha256:$WANT\",\"size\":12582912},\"sources\":[{\"scheme\":\"http\",\"locator\":\"$URL\"}],\"sink\":{\"final\":\"$DEST_JSON\"}}"
ID=$("$CPP" submit --kind download --spec "$SPEC") || { bad "C++ could not submit"; exit 1; }
echo "  job $ID, written by C++"

if "$WORK/jobd.exe" status | grep -q "$(basename "$DEST")"; then
  ok "Go sees the job C++ wrote"
else
  bad "Go cannot see the job C++ wrote"
fi

# ---------------------------------------------------------------------------
step "2. Go does the work, with no C++ process running"
# ---------------------------------------------------------------------------
# A short sweep, because a delegated job's progress is a CACHE of what the
# delegate last reported, refreshed when the supervisor reconciles. At the
# default 30s this test would see nothing and conclude nothing had happened,
# which is exactly what it did.
"$WORK/jobd.exe" start --without nas --interval 2s >/dev/null 2>&1
for _ in $(seq 1 40); do
  [ "$(done_bytes "$ID")" -gt 0 ] 2>/dev/null && break
  sleep 0.5
done
MOVED=$(done_bytes "$ID")
if [ "${MOVED:-0}" -gt 0 ]; then
  ok "Go moved $MOVED bytes; the submitting program exited long ago"
else
  bad "nothing moved"
fi

# ---------------------------------------------------------------------------
step "3. the C++ application presses pause, holding no lease"
# ---------------------------------------------------------------------------
# This is the case schema 4 exists for. The C++ side never claimed this job and
# cannot — a supervisor holds it — so before intent there was no way to say stop.
"$CPP" intent "$ID" pause --by "lemonade-ui" >/dev/null || bad "C++ could not set intent"

STOPPED=""
for _ in $(seq 1 40); do
  A=$(done_bytes "$ID"); sleep 1.5; B=$(done_bytes "$ID")
  if [ "$A" = "$B" ]; then STOPPED="$B"; break; fi
done

STATE=$(field "$ID" state)
if [ -n "$STOPPED" ] && [ "$STOPPED" -lt 12582912 ]; then
  ok "it stopped at $STOPPED of 12582912 bytes — mid-transfer, on request"
else
  bad "it did not stop before finishing (saw ${STOPPED:-nothing})"
fi
case "$STATE" in
  complete|failed|cancelled) bad "pausing made it $STATE; pause is not an ending" ;;
  *) ok "still $STATE — paused, not finished" ;;
esac
if "$CPP" orphans | grep -q "$ID"; then
  bad "a paused job is offered as an orphan; a sweep would resume what a person stopped"
else
  ok "no supervisor will pick it up while it is paused"
fi

# ---------------------------------------------------------------------------
step "4. the C++ application resumes it"
# ---------------------------------------------------------------------------
"$CPP" intent "$ID" run --by "lemonade-ui" >/dev/null || bad "C++ could not resume"
for _ in $(seq 1 120); do
  [ -f "$DEST" ] && [ "$(sha256sum "$DEST" 2>/dev/null | cut -d' ' -f1)" = "$WANT" ] && break
  sleep 1
done
if [ -f "$DEST" ] && [ "$(sha256sum "$DEST" | cut -d' ' -f1)" = "$WANT" ]; then
  ok "finished from where it stopped, and the digest matches what was asked for"
else
  bad "it did not finish after being resumed"
  "$CPP" show "$ID" 2>&1 | head -20 | sed 's/^/        /'
fi

echo
echo "----------------------------------------"
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
