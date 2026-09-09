#!/usr/bin/env bash
# The test that decides whether the job abstraction is real.
#
# A Go process takes a job and starts work. It is killed — not asked to stop,
# killed — leaving its lease behind with no chance to hand anything over. A
# PYTHON process then finds the orphan, adopts it, resumes from exactly the byte
# the Go process had proven, and finishes it. Go reads the result back.
#
# Neither implementation imports the other. Neither is generated from a shared
# schema. The only thing they share is the record on disk and the rules for
# taking it over. If this passes, the abstraction crosses languages; if it only
# works when both halves are written by one generator, it is a file format.
#
#   usage: xlang-job.sh [workdir]   default: a fresh temp dir, because a fixed
#                                   one under $HOME made two concurrent runs
#                                   look exactly like a real disagreement

set -u

WORK="${1:-}"
[ -n "$WORK" ] || { WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT; }
REPO="$(cd "$(dirname "$0")/.." && pwd)"
export JOB_STORE="$WORK/store"

PY=""
for cand in python3 python "py -3"; do
    if $cand -c "pass" >/dev/null 2>&1; then PY="$cand"; break; fi
done
[ -n "$PY" ] || { echo "no working python"; exit 2; }

step() { printf '\n--- %s ---\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*"; exit 1; }

rm -rf "$WORK"; mkdir -p "$JOB_STORE"

step "build the Go jobctl"
(cd "$REPO/job/go" && go build -o "$WORK/jobctl.exe" ./cmd/jobctl) || fail "go build"
GO="$WORK/jobctl.exe"
# A function, not a string: $REPO contains spaces, and a string would word-split
# into "AI" and "abstractions" the moment it was expanded.
pyctl() { $PY "$REPO/job/python/jobctl.py" "$@"; }
echo "go:     $GO"
echo "python: $PY $REPO/job/python/jobctl.py"

step "Go submits a job whose spec neither implementation understands"
# The spec is opaque to BOTH tools. Neither jobctl knows what a "download" is,
# and `anything_else` is a shape neither has a type for — which is exactly the
# property that lets downloading grow mirrors and chunk manifests without either
# of these tools, or the C++ one still to come, needing to change.
#
# Note the sources: an SMB share and a local path, no URL anywhere. If the
# descriptor were URL-shaped, neither could be expressed and delegating to a NAS
# would be impossible later.
ID=$("$GO" submit \
        --kind conformance-kind \
        --spec '{"artifact":{"digest":"sha256:abcd","size":1000},"sources":[{"scheme":"smb","locator":"//nas/models/qwen.gguf"},{"scheme":"file","locator":"D:/models/qwen.gguf"}],"anything_else":{"nested":[1,2,3]}}' \
        --total 1000) || fail "submit"
echo "job id: $ID"

step "Go claims it and proves the first 400 bytes"
CLAIM=$("$GO" claim "$ID" --owner "go-worker" --ttl 2) || fail "go claim"
echo "$CLAIM"
EPOCH=$(echo "$CLAIM" | sed 's/.*epoch=\([0-9]*\).*/\1/')
"$GO" progress "$ID" --epoch "$EPOCH" --done 460 --checkpoint '{"verified_prefix":400}' || fail "go progress"
echo "the Go worker stops here and releases nothing."
echo "what it leaves behind is byte-identical to a SIGKILL: a lease with no"
echo "process behind it. This test covers the record and the lease only — the"
echo "download runner has its own resume tests over real bytes."

step "Python looks for orphans while the Go lease is still live"
ORPH=$(pyctl orphans) || fail "python orphans failed while the lease was live"
if [ -n "$ORPH" ]; then fail "python saw a still-owned job as an orphan: $ORPH"; fi
echo "(none, correctly — the lease has not expired)"

step "wait out the lease, the way a crashed owner's lease expires"
sleep 3

step "Python finds the orphan"
ORPH=$(pyctl orphans) || fail "python orphans"
echo "$ORPH"
[ -n "$ORPH" ] || fail "python found no orphan after the lease expired"

step "Python adopts it"
PCLAIM=$(pyctl claim "$ID" --owner "python-worker" --ttl 60) || fail "python claim"
echo "$PCLAIM"
PEPOCH=$(echo "$PCLAIM" | sed 's/.*epoch=\([0-9]*\).*/\1/')
PRESUME=$(echo "$PCLAIM" | sed 's/.*"verified_prefix":\([0-9]*\).*/\1/')

[ "$PEPOCH" = "2" ] || fail "expected epoch 2 after adoption, got $PEPOCH"
# 400, not 460: the 60 bytes past the verified prefix were written by an owner
# that is gone and nothing vouches for them. Resuming from 460 is the bug this
# design exists to make unexpressible.
[ "$PRESUME" = "400" ] || fail "expected the checkpoint to carry verified_prefix 400, got $PRESUME"
echo "the successor inherited the predecessor's checkpoint: 400, not the 460 written"

step "the zombie Go worker wakes up and tries to write with its old epoch"
if "$GO" progress "$ID" --epoch "$EPOCH" --done 999 --checkpoint '{"verified_prefix":999}' 2>/dev/null; then
    fail "the stale Go owner was allowed to write after losing the lease"
fi
echo "refused, as it must be"

step "Python finishes the job"
pyctl progress "$ID" --epoch "$PEPOCH" --done 1000 --checkpoint '{"verified_prefix":1000}' || fail "python progress"
pyctl finish "$ID" --epoch "$PEPOCH" --state transferred || fail "python finish"

step "Go reads back what Python wrote"
"$GO" show "$ID" || fail "go show"

STATE=$("$GO" show "$ID" | sed -n 's/.*"state": "\([a-z]*\)".*/\1/p' | head -1)
[ "$STATE" = "transferred" ] || fail "Go read state=$STATE, expected transferred"

step "RESULT"
cat <<'EOF'
PASS

A job was created in Go, worked on in Go, abandoned without release, adopted in
Python, resumed from the byte Go had PROVEN rather than the byte it had written,
finished in Python, and read back in Go. The stale Go owner was refused when it
came back with its old epoch.

Neither implementation imports the other. Neither is generated from a shared
schema. What crossed intact is the SEMANTICS — exclusive claim, increasing
epoch, resume only from a proven prefix — which is the contract. Sharing a
directory is how these two happen to be bound today, not what they agree about.

SCOPE: no bytes moved here, deliberately. This exercises the record, the lease,
the epoch and the adoption path across two languages. Resume over real bytes —
discarding an unproven tail, rebuilding the hash over an inherited prefix — is
tested in download/go. What is still missing is a kill during a real
multi-gigabyte transfer, which needs the service tier.
EOF
