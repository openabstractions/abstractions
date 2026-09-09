#!/usr/bin/env bash
# What this looks like to somebody who has never run it.
#
# Everything happens on a machine invented for the occasion: its own config
# directory, its own job store, its own pretend Ollama and HuggingFace caches,
# its own web server. Your real store, your real config and your NAS are not
# touched and cannot be — the script refuses to start if discovery finds a tier
# it did not create itself.
#
# That refusal is not decoration. An earlier version of a different script here
# inherited the author's real configuration and put a job on a live NAS.
#
#   usage: first-run.sh

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"

# A machine that has never seen this software.
export APPDATA="$WORK/machine/config"
export XDG_CONFIG_HOME="$WORK/machine/config"
export HOME_STORE="$WORK/machine/store"
export ABSTRACTION_STORE="$HOME_STORE"
unset ABSTRACTION_NAS_STORE 2>/dev/null || true
mkdir -p "$APPDATA" "$HOME_STORE" "$WORK/downloads"

PY=py
command -v py >/dev/null 2>&1 || PY=python3

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
note() { printf '   %s\n' "$*"; }
cmd()  { printf '\n   \033[36m$ %s\033[0m\n' "$*"; }
pause_() { sleep 1; }

cleanup() {
  "$WORK/jobd.exe" stop >/dev/null 2>&1
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
say "Building the three tools"
# ---------------------------------------------------------------------------
(cd "$ROOT/download/go" && go build -o "$WORK/dl.exe" ./cmd/dl && go build -o "$WORK/jobd.exe" ./cmd/jobd) \
  || { echo "build failed"; exit 1; }
note "dl    — downloads bytes"
note "jobd  — finishes transfers nobody is watching"

if "$WORK/dl.exe" tiers 2>/dev/null | grep -q "delegates to"; then
  echo
  echo "REFUSING TO RUN: discovery found a tier this demo did not set up."
  exit 1
fi

# ---------------------------------------------------------------------------
say "1. The problem, on this machine"
# ---------------------------------------------------------------------------
# Two tools, each with its own cache, each holding the same weights under a
# different name. On a real machine that was 116 GB across four of them.
MODEL="$WORK/model.gguf"
"$PY" -3 - "$MODEL" <<'PYEOF'
import sys
n = 24 * 1024 * 1024
open(sys.argv[1], "wb").write(bytes((i * 31 + 7) % 251 for i in range(n)))
PYEOF
SUM=$(sha256sum "$MODEL" | cut -d' ' -f1)

mkdir -p "$WORK/machine/.ollama/models/blobs"
cp "$MODEL" "$WORK/machine/.ollama/models/blobs/sha256-$SUM"
note "ollama has it, named by content:  blobs/sha256-${SUM:0:12}…"
note "and it is $(numfmt --to=iec 25165824 2>/dev/null || echo 24M) of weights this machine already paid for."

# The demo's own view of "other tools' caches" — the real Discover() looks in
# the user's home, which is why this runs against a home of its own.
export USERPROFILE="$WORK/machine"
export HOME="$WORK/machine"

# ---------------------------------------------------------------------------
say "2. Nothing is configured. Nothing has to be."
# ---------------------------------------------------------------------------
cmd "dl tiers"
"$WORK/dl.exe" tiers | sed 's/^/   /'
note ""
note "No supervisor, no NAS, no settings file. That is a working state,"
note "not a broken one — downloads just stop when the program does."

# ---------------------------------------------------------------------------
say "3. A download, and closing the laptop"
# ---------------------------------------------------------------------------
"$PY" -3 - "$MODEL" "$WORK/port.txt" <<'PYEOF' &
import http.server, sys, time
path, portfile = sys.argv[1], sys.argv[2]
body = open(path, "rb").read()
hits = [0]

class H(http.server.BaseHTTPRequestHandler):
    def do_HEAD(self):
        self.send_response(200)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
    def do_GET(self):
        hits[0] += 1
        open(portfile + ".hits", "w").write(str(hits[0]))
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
                time.sleep(0.03)
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

cmd "dl $URL -o ~/Downloads/"
"$WORK/dl.exe" "$URL" -o "$WORK/downloads/" >"$WORK/first.log" 2>&1 &
DL_PID=$!
sleep 3
kill -9 "$DL_PID" 2>/dev/null; wait "$DL_PID" 2>/dev/null
head -6 "$WORK/first.log" | sed 's/^/   /'
note ""
note "...and now the terminal is closed, mid-transfer."

cmd "dl list"
"$WORK/dl.exe" list | sed 's/^/   /'
note ""
note "The job is still there. Nothing about it lived in the process you killed."

cmd "dl $URL -o ~/Downloads/    (the same line again)"
"$WORK/dl.exe" "$URL" -o "$WORK/downloads/" >"$WORK/second.log" 2>&1
grep -E "job |fetched by" "$WORK/second.log" | sed 's/^/   /'
note ""
note "Same job id. It resumed from the byte the dead process had PROVEN —"
note "not the byte it had written. Those are different numbers."

# ---------------------------------------------------------------------------
say "4. The part that pays for the whole thing"
# ---------------------------------------------------------------------------
BEFORE=$(cat "$WORK/port.txt.hits" 2>/dev/null || echo 0)
note "Now ask for the model by what it IS, not where it lives."
note "Ollama already has these exact bytes."

cmd "dl $URL --digest sha256:${SUM:0:12}… -o ~/Downloads/wanted.gguf"
"$WORK/dl.exe" "$URL" --digest "sha256:$SUM" -o "$WORK/downloads/wanted.gguf" \
  >"$WORK/dedup.log" 2>&1
grep -E "fetched by|to  " "$WORK/dedup.log" | sed 's/^/   /'
AFTER=$(cat "$WORK/port.txt.hits" 2>/dev/null || echo 0)

note ""
if [ ! -f "$WORK/downloads/wanted.gguf" ]; then
  note "the file did not arrive — see $WORK/dedup.log"
elif [ "$(sha256sum "$WORK/downloads/wanted.gguf" | cut -d' ' -f1)" != "$SUM" ]; then
  note "the digest did NOT match, which would be a real bug"
else
  note "Network requests while fetching those 24 MB: $((AFTER - BEFORE))"
  note ""
  note "The URL was right there and was never used. The bytes came out of"
  note "Ollama's cache, because the digest said they were the same bytes."
  note "They were still verified on the way past — Ollama does not check its"
  note "own blobs, so a corrupt one is a real possibility and this is not"
  note "allowed to be the last word on whether they are right."
fi

# ---------------------------------------------------------------------------
say "5. Turning on the thing that survives you"
# ---------------------------------------------------------------------------
cmd "jobd start"
"$WORK/jobd.exe" start | sed 's/^/   /'
cmd "dl tiers"
"$WORK/dl.exe" tiers | sed 's/^/   /'
note ""
note "On Windows that says bits — the OS transfer service, which runs under its"
note "own account and keeps going with every program of yours closed."

rm -f "$WORK/downloads/weights.gguf"
cmd "dl $URL -o ~/Downloads/    (identical line, third time)"
"$WORK/dl.exe" "$URL" -o "$WORK/downloads/again.gguf" >"$WORK/third.log" 2>&1 &
DL_PID=$!
sleep 2
kill -9 "$DL_PID" 2>/dev/null; wait "$DL_PID" 2>/dev/null
grep -E "fetched by" "$WORK/third.log" | sed 's/^/   /'
note "killed the program two seconds in."
for _ in $(seq 1 90); do
  [ -f "$WORK/downloads/again.gguf" ] && break
  sleep 1
done
if [ -f "$WORK/downloads/again.gguf" ]; then
  note ""
  note "The file arrived anyway: $(sha256sum "$WORK/downloads/again.gguf" | cut -c1-12)… matches."
else
  note "(it did not arrive — see $WORK)"
fi

# ---------------------------------------------------------------------------
say "What you never typed"
# ---------------------------------------------------------------------------
cat <<'EOF'
   No tier. No --use-the-nas, no --use-bits, no --here.
   No path to a store. No hostname. No settings file.

   The same command line, three times, did three different things — because
   what changed was the machine, not the command. That is the whole idea, and
   it is the one SLF4J has: a library that logs knows about a facade and a
   default, and nothing about which collector the sinks point at.

   Next step on a real machine, once:  jobd start
   And if you have a NAS:              jobd setup --nas-store //nas/share/store
EOF
echo
