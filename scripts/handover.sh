#!/usr/bin/env bash
# A download starts here, is interrupted, and a supervisor picks it up. Does the
# work already proven survive the handover?
#
# The decisive evidence is in the HTTP requests, because it cannot be argued
# with. A second fetch carrying a Range header means the proven prefix was
# honoured. One with no Range means the delegate started from byte zero and
# every proven byte was thrown away.
#
# This exists because it happened. BITS and the NAS both declared CapResume —
# "can start from a byte offset rather than from zero" — and neither could:
# Start-BitsTransfer owns its own temporary file, and the NAS submits a fresh
# record for a partial that is on the wrong machine. A 16 MiB transfer with
# 6,586,368 bytes checkpointed was handed over and re-fetched in full. The digest
# still matched, so no test looking at the result could have caught it.
#
# The fix has two halves and this guards both: delegators no longer claim a
# capability they lack, and Delegate refuses to hand proven work to one that
# cannot continue it — releasing the lease so the supervisor runs it here, where
# the checkpoint IS honoured.
#
#   usage: handover.sh
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"

export APPDATA="$WORK/cfg"
export XDG_CONFIG_HOME="$WORK/cfg"
export ABSTRACTION_STORE="$WORK/store"
export JOB_STORE="$ABSTRACTION_STORE"
unset ABSTRACTION_NAS_STORE 2>/dev/null || true
OUT="$WORK/out"
mkdir -p "$ABSTRACTION_STORE" "$OUT" "$WORK/cfg"

PY=py
command -v py >/dev/null 2>&1 || PY=python3
DL="$ROOT/bin/dl.exe"
JOBD="$ROOT/bin/jobd.exe"

PAYLOAD="$WORK/payload.bin"
"$PY" -3 - "$PAYLOAD" <<'PYEOF'
import sys
n = 16 * 1024 * 1024
open(sys.argv[1], "wb").write(bytes((i * 7 + 11) % 251 for i in range(n)))
PYEOF
WANT="$(sha256sum "$PAYLOAD" | cut -d' ' -f1)"

# A server that writes down every request it is asked for.
"$PY" -3 - "$PAYLOAD" "$WORK/port.txt" "$WORK/requests.log" <<'PYEOF' &
import http.server, sys, time
path, portfile, reqlog = sys.argv[1], sys.argv[2], sys.argv[3]
body = open(path, "rb").read()

def note(line):
    with open(reqlog, "a") as f:
        f.write(line + "\n")

class H(http.server.BaseHTTPRequestHandler):
    def do_HEAD(self):
        note("HEAD  Range=%s" % (self.headers.get("Range") or "(none)"))
        self.send_response(200)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()

    def do_GET(self):
        rng = self.headers.get("Range")
        note("GET   Range=%s" % (rng or "(none)"))
        start = 0
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
        except Exception:
            pass
    def log_message(self, *a):
        pass

srv = http.server.HTTPServer(("127.0.0.1", 0), H)
open(portfile, "w").write(str(srv.server_port))
srv.serve_forever()
PYEOF
SERVER_PID=$!
cleanup() { "$JOBD" stop >/dev/null 2>&1; kill "$SERVER_PID" 2>/dev/null; }
trap cleanup EXIT

for _ in $(seq 1 50); do [ -s "$WORK/port.txt" ] && break; sleep 0.1; done
URL="http://127.0.0.1:$(cat "$WORK/port.txt")/payload.bin"

echo "payload 16 MiB, sha256:${WANT:0:16}..."
echo

echo "--- 1. dl starts it here, no supervisor ---"
"$DL" "$URL" -o "$OUT/x.bin" >"$WORK/dl1.log" 2>&1 &
DL_PID=$!
sleep 8
kill -9 "$DL_PID" 2>/dev/null; wait "$DL_PID" 2>/dev/null
ID="$(grep -oE '[0-9]{13}-[0-9a-f]+' "$WORK/dl1.log" | head -1)"
PROVEN=$(grep -m1 '"verified_prefix"' "$ABSTRACTION_STORE/jobs/$ID.json" | grep -oE '[0-9]+')
PARTIAL=$(stat -c %s "$ABSTRACTION_STORE/work/$ID" 2>/dev/null || echo 0)
echo "    killed dl. proven=$PROVEN  partial on disk=$PARTIAL"

echo
echo "--- 2. jobd starts, delegating to bits ---"
"$JOBD" start --without nas | sed 's/^/    /'

echo
echo "    waiting for it to finish..."
for i in $(seq 1 120); do
  [ -f "$OUT/x.bin" ] && [ "$(sha256sum "$OUT/x.bin" | cut -d' ' -f1)" = "$WANT" ] && break
  sleep 1
done

echo
echo "--- 3. what the server was actually asked for ---"
cat -n "$WORK/requests.log" | sed 's/^/    /'

echo
if [ -f "$OUT/x.bin" ]; then
  echo "    file arrived, digest $([ "$(sha256sum "$OUT/x.bin"|cut -d' ' -f1)" = "$WANT" ] && echo matches || echo MISMATCH)"
else
  echo "    file never arrived"
fi
echo "    proven bytes before handover: $PROVEN"
echo
rc=0
if [ ! -f "$OUT/x.bin" ] || [ "$(sha256sum "$OUT/x.bin" | cut -d' ' -f1)" != "$WANT" ]; then
  echo "  FAIL  the file never arrived, or its digest is wrong"
  echo "        (a supervisor that refuses to delegate must still run the job itself;"
  echo "         holding the lease and doing nothing is a livelock, and was one)"
  tail -20 "$ABSTRACTION_STORE/jobd.log" 2>/dev/null | sed 's/^/        /'
  rc=1
elif [ "${PROVEN:-0}" -eq 0 ]; then
  echo "  SKIP  nothing was checkpointed before the kill, so there was nothing to lose"
  echo "        (make the payload larger or the kill later)"
elif grep -q "GET   Range=bytes=" "$WORK/requests.log"; then
  echo "  PASS  the handover resumed from the proven prefix — a Range request was made"
else
  echo "  FAIL  the handover RESTARTED FROM ZERO. $PROVEN proven bytes were re-fetched."
  rc=1
fi
exit "$rc"
