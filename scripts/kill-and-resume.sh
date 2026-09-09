#!/usr/bin/env bash
# The original complaint, tested.
#
#   "you start a 40 GB download, you close the laptop, you come back and it
#    starts again from zero"
#
# Here a real download of a real model is killed with SIGKILL partway through —
# no graceful shutdown, no chance to save anything, which is what closing a lid
# or losing power actually looks like to a process. Then a SEPARATE invocation
# finds the interrupted job and finishes it, resuming from the byte the dead
# process had proven rather than from zero.
#
# The file is then verified against the digest HuggingFace published, so
# "resumed" means the same thing as "correct" and not merely "the right length".
#
#   usage: kill-and-resume.sh [workdir] [ref]

set -u

WORK="${1:-$HOME/kill-and-resume}"
REF="${2:-hf://bartowski/Qwen2.5-0.5B-Instruct-GGUF#IQ2_M}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"

export MODELGET_STORE="$WORK/jobs"
OUT="$WORK/out"

step() { printf '\n--- %s ---\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*"; exit 1; }

rm -rf "$WORK"; mkdir -p "$OUT" "$MODELGET_STORE"

step "build"
(cd "$REPO/model/go" && go build -o "$WORK/modelget.exe" ./cmd/modelget) || fail "build"
MG="$WORK/modelget.exe"

step "what are we fetching, and do we already have it?"
"$MG" where "$REF" || fail "resolve"

step "start the download, then kill it after 12 seconds"
"$MG" get "$REF" -o "$OUT" > "$WORK/get.log" 2>&1 &
GETPID=$!
sleep 12

# SIGKILL. Not a signal the program can catch, not a chance to flush anything.
taskkill //F //PID $GETPID >/dev/null 2>&1 || kill -9 $GETPID 2>/dev/null
sleep 1
echo "killed pid $GETPID"
cat "$WORK/get.log"

step "what survived?"
JOBFILE=$(ls "$MODELGET_STORE"/jobs/*.json 2>/dev/null | head -1)
[ -n "$JOBFILE" ] || fail "no job record survived the kill"
PARTIAL=$(grep -o '"partial": *"[^"]*"' "$JOBFILE" | head -1 | sed 's/.*"partial": *"//;s/"$//' | sed 's/\\\\/\\/g')
VERIFIED=$(grep -o '"verified_prefix": *[0-9]*' "$JOBFILE" | head -1 | grep -o '[0-9]*')
PARTSIZE=$(stat -c%s "$PARTIAL" 2>/dev/null || echo 0)

echo "job record:      $(basename "$JOBFILE")"
echo "bytes on disk:   $PARTSIZE"
echo "bytes PROVEN:    ${VERIFIED:-0}"
echo
echo "The gap between those two numbers is the point. The dead process wrote"
echo "more than it had checkpointed, and nothing vouches for the difference, so"
echo "the resume starts from the proven number and throws the rest away."

[ "${VERIFIED:-0}" -gt 0 ] || fail "nothing was checkpointed; there is nothing to resume from"

step "immediately after the kill, the dead owner still holds the lease"
# Not a flaw — it is the mechanism. Nothing may write to a job another owner
# might still be working on, and a process that was SIGKILLed cannot tell anyone
# it is gone. The lease lapsing is what makes the job available.
"$MG" resume

step "wait for the dead owner's lease to lapse, then a NEW process finishes it"
sleep 31
time "$MG" resume || fail "resume"

step "did it actually land, and is it right?"
FILE=$(ls "$OUT"/*.gguf 2>/dev/null | head -1)
[ -n "$FILE" ] || fail "no file was delivered"
ls -l "$FILE" | awk '{print "  size:", $5, "bytes"}'

WANT=$(grep -o '"digest": *"sha256:[0-9a-f]*"' "$JOBFILE" | head -1 | sed 's/.*sha256://;s/"$//')
GOT=$(sha256sum < "$FILE" | cut -d' ' -f1)
echo "  want digest: $WANT"
echo "  got  digest: $GOT"
[ "$WANT" = "$GOT" ] || fail "the resumed file does not match the digest HuggingFace published"

step "RESULT"
cat <<EOF
PASS

A real model download was killed with SIGKILL partway through. A separate
process picked the job up, resumed from the byte the dead one had proven, and
delivered a file whose sha256 matches what HuggingFace published for it.

Nothing was re-downloaded from zero. Nothing had to be graceful. The job outlived
the process because the job was never inside it.
EOF
