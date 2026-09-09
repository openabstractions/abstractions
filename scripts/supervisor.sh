#!/usr/bin/env bash
# Does the supervisor actually close the gap it exists for?
#
# The gap: a transfer is interrupted and NOBODY EVER RUNS A COMMAND AGAIN. Today
# the bytes sit half-written until a human happens to type something. That is the
# whole reason for a thing that runs on a timer.
#
# So this test never invokes the downloader a second time. It starts a real
# download, kills it, and then runs only `jobd once` — the sweep a scheduled task
# performs — and checks that the file arrived and verifies against the digest
# HuggingFace published.
#
#   usage: supervisor.sh [workdir] [ref]

set -u

WORK="${1:-$HOME/supervisor-test}"
REF="${2:-hf://bartowski/Qwen2.5-0.5B-Instruct-GGUF#IQ2_M}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"

export MODELGET_STORE="$WORK/jobs"
OUT="$WORK/out"

step() { printf '\n--- %s ---\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*"; exit 1; }

rm -rf "$WORK"; mkdir -p "$OUT" "$MODELGET_STORE"

step "build"
(cd "$REPO/model/go" && go build -o "$WORK/modelget.exe" ./cmd/modelget) || fail "build modelget"
(cd "$REPO/download/go" && go build -o "$WORK/jobd.exe" ./cmd/jobd) || fail "build jobd"
MG="$WORK/modelget.exe"
JOBD="$WORK/jobd.exe"

step "start a real download and kill it after 12 seconds"
"$MG" get "$REF" -o "$OUT" > "$WORK/get.log" 2>&1 &
GETPID=$!
sleep 12
taskkill //F //PID $GETPID >/dev/null 2>&1 || kill -9 $GETPID 2>/dev/null
sleep 1
echo "killed pid $GETPID"

JOBFILE=$(ls "$MODELGET_STORE"/jobs/*.json 2>/dev/null | head -1)
[ -n "$JOBFILE" ] || fail "no job record survived"
VERIFIED=$(grep -o '"verified_prefix": *[0-9]*' "$JOBFILE" | head -1 | grep -o '[0-9]*')
echo "checkpointed at ${VERIFIED:-0} bytes"
[ "${VERIFIED:-0}" -gt 0 ] || fail "nothing was checkpointed"

step "what the supervisor sees"
"$JOBD" status

step "the lease has to lapse first — nothing may touch a job someone may still hold"
sleep 31

step "one supervisor sweep, and NOTHING ELSE"
# This is the point. No human runs the downloader again. A scheduled task runs
# exactly this.
time "$JOBD" once || fail "jobd once"

step "did the file arrive?"
FILE=$(ls "$OUT"/*.gguf 2>/dev/null | head -1)
[ -n "$FILE" ] || fail "the supervisor did not deliver the file"
ls -l "$FILE" | awk '{print "  size:", $5, "bytes"}'

WANT=$(grep -o '"digest": *"sha256:[0-9a-f]*"' "$JOBFILE" | head -1 | sed 's/.*sha256://;s/"$//')
GOT=$(sha256sum < "$FILE" | cut -d' ' -f1)
echo "  want: $WANT"
echo "  got:  $GOT"
[ "$WANT" = "$GOT" ] || fail "the delivered file does not match the published digest"

step "and the supervisor now reports it as finished"
"$JOBD" status

step "a second sweep must do nothing"
# Re-finishing a finished job would mean re-downloading a correct file. The
# delegated equivalent of this bug was real and is why Delegation.Delivered is
# checked.
OUT2=$("$JOBD" once)
echo "$OUT2"
echo "$OUT2" | grep -q "adopted=0" || fail "the second sweep picked the job up again"

step "RESULT"
cat <<'EOF'
PASS

A real download was killed. No human ran the downloader again. A single
supervisor sweep — the same one a scheduled task performs every few minutes —
found the abandoned job, finished it, and delivered a file matching the digest
HuggingFace published. A second sweep correctly did nothing.

That is the gap closed: the bytes no longer wait for someone to notice.
EOF
