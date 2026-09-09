#!/usr/bin/env bash
# Does the SAME application work on three different downloaders, unchanged?
#
# This is criterion three of what an abstraction is for: switch the provider
# without touching the program. The program here is `dl`, built once, run three
# times, and never rebuilt or reconfigured between them:
#
#   here     nothing else on this machine, so dl downloads in its own process
#   system   jobd is running, so dl submits and EXITS — it is killed on purpose,
#            and the bytes still arrive
#   nas      jobd delegates onward to a machine that is not this one
#
# Nothing below passes a tier to dl. The only difference between the three runs
# is what happens to exist on the machine at the time, which is the SLF4J
# property: presence is the configuration.
#
# The NAS tier is SKIPPED unless a share is named on the command line. It writes
# a job record to that share, so it is opt-in by an argument rather than by an
# environment variable that might already be set for some other reason.
#
#   usage: tiers.sh [//nas/share/store]

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"

# THE TEST BUILDS ITS OWN MACHINE.
#
# The first version of this script did not, and the first run of it delegated a
# job onto a real NAS, which then retried a URL only this PC could reach until
# its lease expired three times over. Discovery is supposed to read the machine —
# that is the feature — so a test of discovery MUST replace the machine, or it is
# running against whatever the author happens to have configured and will do
# something different, and possibly something remote, on the next person's.
#
# os.UserConfigDir reads APPDATA on Windows and XDG_CONFIG_HOME elsewhere, so
# pointing both at a scratch directory is what isolates it.
export APPDATA="$WORK/cfg"
export XDG_CONFIG_HOME="$WORK/cfg"
unset ABSTRACTION_NAS_STORE 2>/dev/null || true

export ABSTRACTION_STORE="$WORK/store"
export JOB_STORE="$ABSTRACTION_STORE"   # jobctl reads this one
OUT="$WORK/out"
mkdir -p "$ABSTRACTION_STORE" "$OUT" "$WORK/cfg"

# The NAS tier is opt-in, by an argument rather than by an environment variable
# that might already be set for other reasons. It writes to a real share.
NAS_STORE="${1:-}"

pass=0
fail=0
skip=0
ok()   { echo "  PASS  $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL  $1"; fail=$((fail+1)); }
skipped() { echo "  SKIP  $1"; skip=$((skip+1)); }
step() { printf '\n%s\n' "$1"; }

TIMEOUT=""
command -v timeout >/dev/null 2>&1 && TIMEOUT="timeout 180"

PY=py
command -v py >/dev/null 2>&1 || PY=python3

echo "one application, three downloaders"
echo "  store: $ABSTRACTION_STORE"

# ---------------------------------------------------------------------------
# Build once. Nothing is rebuilt between tiers — that is the point.
# ---------------------------------------------------------------------------
DL="$WORK/dl.exe"
JOBD="$WORK/jobd.exe"
JOBCTL="$WORK/jobctl.exe"
(cd "$ROOT/download/go" && go build -o "$DL" ./cmd/dl) || { echo "build dl failed"; exit 1; }
(cd "$ROOT/download/go" && go build -o "$JOBD" ./cmd/jobd) || { echo "build jobd failed"; exit 1; }
(cd "$ROOT/job/go" && go build -o "$JOBCTL" ./cmd/jobctl) || { echo "build jobctl failed"; exit 1; }

# ---------------------------------------------------------------------------
# A payload that trickles, so a kill lands mid-transfer rather than after it.
# ---------------------------------------------------------------------------
PAYLOAD="$WORK/payload.bin"
"$PY" -3 - "$PAYLOAD" <<'PYEOF'
import sys
n = 4 * 1024 * 1024
open(sys.argv[1], "wb").write(bytes((i * 7 + 11) % 251 for i in range(n)))
PYEOF
WANT="$(sha256sum "$PAYLOAD" | cut -d' ' -f1)"
echo "  payload: $(stat -c %s "$PAYLOAD" 2>/dev/null || stat -f %z "$PAYLOAD") bytes  sha256:$WANT"

# The server listens on loopback only, unless the NAS tier is being run — a NAS
# is a different machine and cannot reach this PC's 127.0.0.1, which is exactly
# what the first run of this script proved the hard way.
#
# When it must be reachable, it binds to ONE address: the interface that routes
# to the NAS, worked out from the share path itself. Not 0.0.0.0 — that would
# listen on every interface this machine happens to have up, which is a wider
# door than the job needs, for a test payload, on somebody's home network.
BIND=127.0.0.1
if [ -n "$NAS_STORE" ]; then
  NASHOST="$(printf '%s' "$NAS_STORE" | sed 's#^[\\/][\\/]##; s#[\\/].*##')"
  # A UDP connect sends nothing. It only asks the routing table which local
  # address would be used to reach that host.
  HOSTIP="$("$PY" -3 -c 'import socket,sys
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.connect((sys.argv[1], 445))
print(s.getsockname()[0])
s.close()' "$NASHOST")" || { echo "cannot work out which interface reaches $NASHOST"; exit 1; }
  BIND="$HOSTIP"
  echo "  nas tier: serving on $BIND only — the interface that reaches $NASHOST"
fi

"$PY" -3 - "$PAYLOAD" "$WORK/port.txt" "$BIND" <<'PYEOF' &
import http.server, sys, time
path, portfile, bind = sys.argv[1], sys.argv[2], sys.argv[3]
body = open(path, "rb").read()

class H(http.server.BaseHTTPRequestHandler):
    # BITS asks HEAD before it asks for bytes, and a handler without this
    # answers 501 and the transfer never starts. Go and Python clients only ever
    # issue GET, so no previous test in this repo needed it.
    def do_HEAD(self):
        self.send_response(200)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Content-Type", "application/octet-stream")
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
            for i in range(0, len(buf), 65536):
                self.wfile.write(buf[i:i+65536])
                self.wfile.flush()
                time.sleep(0.05)
        except (ConnectionResetError, BrokenPipeError, ConnectionAbortedError, OSError):
            pass  # a client was killed. That is the point of this test.
    def log_message(self, *a):
        pass

srv = http.server.HTTPServer((bind, 0), H)
open(portfile, "w").write(str(srv.server_port))
srv.serve_forever()
PYEOF
SERVER_PID=$!
JOBD_PID=""
cleanup() {
  [ -n "$JOBD_PID" ] && kill "$JOBD_PID" 2>/dev/null
  kill "$SERVER_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

for _ in $(seq 1 50); do [ -s "$WORK/port.txt" ] && break; sleep 0.1; done
PORT="$(cat "$WORK/port.txt")"
# Every tier uses the address the server is actually listening on. A run that
# binds to the LAN interface for the NAS has no 127.0.0.1 listener at all, and
# the first attempt at this left tiers 1 and 2 fetching from a closed port —
# where they hung rather than failed, because a download that nobody is working
# on is a valid state, not an error.
URL="http://$BIND:$PORT/payload.bin"
echo "  server:  $URL"

# The machine this test runs on must be the one it built, not the author's. If
# discovery finds a tier nobody asked for, stop: the next thing that happens is
# a job landing somewhere real.
if "$DL" tiers 2>/dev/null | grep -q "delegates to"; then
  echo
  echo "REFUSING TO RUN: discovery found a delegation tier that this test did not set up."
  "$DL" tiers | sed 's/^/    /'
  exit 1
fi

# digest_is <file> — true when the file is exactly the payload.
digest_is() {
  [ -f "$1" ] || return 1
  [ "$(sha256sum "$1" | cut -d' ' -f1)" = "$WANT" ]
}

# await <file> <seconds> — wait for the bytes to arrive, however they arrive.
await() {
  local f="$1" limit="$2" i=0
  while [ "$i" -lt "$((limit * 4))" ]; do
    digest_is "$f" && return 0
    sleep 0.25
    i=$((i + 1))
  done
  return 1
}

# stop_supervisor really stops it. Git Bash's kill does not reliably reach a
# native child, and the first run of this proved it: the bits-configured jobd
# survived into tier 3, so two supervisors watched one store at once. The lease
# protocol meant nothing was corrupted — which is a decent demonstration in
# itself — but the test was then measuring something it had not set up.
#
# The heartbeat carries the real Windows pid of the supervisor watching THIS
# store, so this kills exactly that one and never a jobd the user is running.
stop_supervisor() {
  local pid i=0
  pid="$(grep -oE '"owner": *"[^"]*"' "$ABSTRACTION_STORE/supervisor.json" 2>/dev/null | grep -oE '[0-9]+"$' | tr -d '"')"
  [ -n "${JOBD_PID:-}" ] && kill "$JOBD_PID" 2>/dev/null
  if [ -n "$pid" ] && command -v taskkill >/dev/null 2>&1; then
    taskkill //F //PID "$pid" >/dev/null 2>&1
  fi
  JOBD_PID=""
  rm -f "$ABSTRACTION_STORE/supervisor.json"
  while [ "$i" -lt 40 ]; do
    "$DL" tiers 2>/dev/null | grep -q "^supervisor   jobd" || return 0
    sleep 0.25
    i=$((i + 1))
  done
  return 1
}

wait_for_supervisor() {
  local i=0
  while [ "$i" -lt 40 ]; do
    "$DL" tiers 2>/dev/null | grep -q "^supervisor   jobd" && return 0
    sleep 0.25
    i=$((i + 1))
  done
  return 1
}

# ===========================================================================
step "TIER 1 — here. Nothing else on this machine."
# ===========================================================================
"$DL" tiers | sed 's/^/    /'
if $TIMEOUT "$DL" "$URL" -o "$OUT/here.bin" >"$WORK/here.log" 2>&1 && digest_is "$OUT/here.bin"; then
  ok "here: dl fetched it in its own process, digest matches"
else
  bad "here: no file, or the digest did not match"
  sed 's/^/        /' "$WORK/here.log"
fi

# ===========================================================================
step "TIER 2 — system. jobd is running, so dl is killed on purpose."
# ===========================================================================
"$JOBD" run >"$WORK/jobd.log" 2>&1 &
JOBD_PID=$!
if ! wait_for_supervisor; then
  bad "system: jobd never announced itself"
  sed 's/^/        /' "$WORK/jobd.log"
else
  "$DL" tiers | sed 's/^/    /'

  "$DL" "$URL" -o "$OUT/system.bin" >"$WORK/system.log" 2>&1 &
  DL_PID=$!
  # Long enough to submit, far too short to have transferred 4 MiB at 1.2 MiB/s.
  sleep 2
  kill -9 "$DL_PID" 2>/dev/null
  wait "$DL_PID" 2>/dev/null
  echo "    killed dl (pid $DL_PID) — from here on nothing this test started is downloading"

  if digest_is "$OUT/system.bin"; then
    bad "system: it had already finished before dl was killed — make the payload slower"
  elif await "$OUT/system.bin" 90; then
    ok "system: dl was dead and the bytes still arrived, digest matches"
    ID="$(grep -oE '[0-9]{13}-[0-9a-f]+' "$WORK/system.log" | head -1)"
    if [ -n "$ID" ] && "$JOBCTL" show "$ID" 2>/dev/null | grep -q "jobd"; then
      ok "system: the record names jobd as the owner, not dl"
    else
      bad "system: could not confirm from the record that jobd did the work"
    fi
  else
    bad "system: the bytes never arrived after dl was killed"
    ID="$(grep -oE '[0-9]{13}-[0-9a-f]+' "$WORK/system.log" | head -1)"
    [ -n "$ID" ] && "$JOBCTL" show "$ID" 2>&1 | sed 's/^/        /'
    tail -20 "$WORK/jobd.log" | sed 's/^/        /'
  fi
fi

# ===========================================================================
step "TIER 3 — nas. A machine that is not this one."
# ===========================================================================
if [ -z "$NAS_STORE" ]; then
  skipped "nas: not requested — this tier writes to a real share, so it is opt-in"
  echo "        To run it:  scripts/tiers.sh //your-nas/share/store"
elif [ ! -d "$NAS_STORE" ]; then
  skipped "nas: $NAS_STORE is not reachable from here"
else
  # Only now does a NAS enter this machine's configuration, and only because it
  # was named on the command line.
  mkdir -p "$WORK/cfg/abstraction"
  printf '{\n  "nas_store": "%s"\n}\n' "$NAS_STORE" > "$WORK/cfg/abstraction/config.json"
  echo "    nas store: $NAS_STORE"

  echo "    serving:   $URL  (reachable from the NAS, unlike 127.0.0.1)"

  if ! stop_supervisor; then bad "nas: could not stop the previous supervisor"; fi
  "$JOBD" run >"$WORK/jobd-nas.log" 2>&1 &
  JOBD_PID=$!
  if ! wait_for_supervisor; then
    bad "nas: jobd never came back up with a NAS configured"
  else
    "$DL" tiers | sed 's/^/    /'
    "$DL" "$URL" -o "$OUT/nas.bin" >"$WORK/nas.log" 2>&1 &
    DL_PID=$!
    sleep 2
    kill -9 "$DL_PID" 2>/dev/null
    wait "$DL_PID" 2>/dev/null

    ID="$(grep -oE '[0-9]{13}-[0-9a-f]+' "$WORK/nas.log" | head -1)"
    if [ -n "$ID" ] && "$JOBCTL" show "$ID" 2>/dev/null | grep -q '"system": *"nas"'; then
      ok "nas: the record says the work was delegated onward to the nas"
    else
      bad "nas: the job was never delegated — jobd kept it"
    fi
    if await "$OUT/nas.bin" 300; then
      ok "nas: a machine that is not this one fetched it, and the digest matches"
    else
      bad "nas: the bytes never arrived"
      tail -20 "$WORK/jobd-nas.log" | sed 's/^/        /'
    fi
  fi
fi

echo
echo "----------------------------------------"
echo "  $pass passed, $fail failed, $skip skipped"
[ "$fail" -eq 0 ]
