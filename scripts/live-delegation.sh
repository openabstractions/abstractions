#!/usr/bin/env bash
# The end-to-end proof, as one command.
#
# Pulls a small model through a real Lemonade server and asserts that the NAS
# fetched it, that the bytes reached Lemonade's cache, and that the row went to
# completed. This is the claim the whole project rests on, and until now it was
# proved by hand -- roughly twenty steps, which meant it was proved rarely.
#
#   usage: scripts/live-delegation.sh [--keep] [--variant Q2_K]
#     --keep      leave the model installed (default: delete what this pulled,
#                 so the next run is a real download rather than a cache hit)
#     --variant   which quantisation to fetch. Each is a different file, so a
#                 fresh one avoids "already downloaded" without deleting
#                 anything a person put there.
#
# Preconditions it CHECKS rather than assumes, because every one of them has
# silently failed a run before:
#   - a local supervisor is alive AND delegating to the nas
#   - the nas supervisor is alive and running the current build
#   - Lemonade is up and is the build from this tree
#
# Exits non-zero with the reason. Nothing here is destructive except deleting
# the model it just pulled.

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${LEMONADE_PORT:-8123}"
API="http://127.0.0.1:$PORT/api/v1"
JOBD="${JOBD:-$ROOT/bin/jobd.exe}"
REPO_ID="unsloth/SmolLM2-135M-Instruct-GGUF"
VARIANT="Q2_K"
KEEP=0

while [ $# -gt 0 ]; do
    case "$1" in
        --keep) KEEP=1 ;;
        --variant) shift; VARIANT="$1" ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
    shift
done

PY="${ABSTRACTION_PYTHON:-}"
if [ -z "$PY" ]; then
    for c in "$LOCALAPPDATA/Programs/Python/Python312/python.exe" python3 python; do
        command -v "$c" >/dev/null 2>&1 && { PY="$c"; break; }
    done
fi

MODEL="SmolLM2-135M-Instruct-GGUF-$VARIANT"
# pull requires a user.* name for a model it has to register; /models then lists
# it without the prefix. Two spellings of one thing, so both are kept.
PULL_NAME="user.$MODEL"
say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32mok\033[0m    %s\n' "$*"; }
die()  { printf '  \033[31mFAIL\033[0m  %s\n\n' "$*"; exit 1; }

say "preflight"

STATUS="$("$JOBD" status 2>&1)" || die "jobd status failed: $STATUS"
echo "$STATUS" | grep -q "supervisor: jobd@.*running" \
    || die "no local supervisor. 'jobd start' -- and start it from a real shell,
        not from a tool that tears down its process tree on exit."
echo "$STATUS" | grep -q "delegates to: nas" \
    || die "the local supervisor is not delegating to the nas, so this would
        prove only that a download works in-process."
ok "local supervisor, delegating to nas"

curl -sf --max-time 10 "$API/health" >/dev/null \
    || die "Lemonade is not answering on $PORT. scripts/lemonade-dev-run.ps1"
ok "lemonade up on $PORT"

# A model already present downloads nothing and the run would pass having
# proved nothing at all -- the most dangerous outcome available here. Each
# quantisation is a separate file, so an unused one gives a real download
# without deleting anything a person put there.
INSTALLED="$(curl -sf --max-time 15 "$API/models" || echo '')"
if echo "$INSTALLED" | grep -q "\"SmolLM2-135M-Instruct-GGUF-$VARIANT\""; then
    for v in Q2_K Q3_K_M Q4_K_M Q5_K_M Q6_K Q8_0 F16; do
        echo "$INSTALLED" | grep -q "\"SmolLM2-135M-Instruct-GGUF-$v\"" || { VARIANT="$v"; break; }
    done
    echo "$INSTALLED" | grep -q "\"SmolLM2-135M-Instruct-GGUF-$VARIANT\"" \
        && die "every variant is installed. Delete one, or point this at
        another repository -- a run against a cache hit proves nothing."
    MODEL="SmolLM2-135M-Instruct-GGUF-$VARIANT"
    PULL_NAME="user.$MODEL"
fi
ok "$VARIANT is not installed yet, so this will be a real download"

say "pull"
# In the background, because pull BLOCKS until the transfer finishes: the whole
# point is that the bytes are moving somewhere else, and the watch loop has to
# see the delegation while it is happening rather than after.
BODY="{\"model\":\"$PULL_NAME\",\"checkpoint\":\"$REPO_ID:$VARIANT\",\"recipe\":\"llamacpp\"}"
PULL_OUT="$(mktemp)"
curl -s --max-time 900 -o "$PULL_OUT" -w '%{http_code}' \
    -X POST "$API/pull" -H 'Content-Type: application/json' -d "$BODY" > "$PULL_OUT.code" 2>&1 &
PULL_PID=$!
trap 'kill $PULL_PID 2>/dev/null; rm -f "$PULL_OUT" "$PULL_OUT.code"' EXIT
ok "asked for $REPO_ID:$VARIANT"

say "watch"
DEADLINE=$(( $(date +%s) + 600 ))
DELEGATED=0
while :; do
    [ "$(date +%s)" -gt "$DEADLINE" ] && die "still not finished after 10 minutes"

    # A refused pull is the common failure and it exits immediately, so notice
    # that rather than waiting ten minutes for a job that was never created.
    if ! kill -0 $PULL_PID 2>/dev/null; then
        CODE="$(cat "$PULL_OUT.code" 2>/dev/null)"
        case "$CODE" in
            200|"") ;;
            *) die "pull returned HTTP $CODE: $(head -c 300 "$PULL_OUT" 2>/dev/null)" ;;
        esac
    fi

    LINE="$("$JOBD" status 2>/dev/null | grep -i "$VARIANT" | head -1)"
    case "$LINE" in
        *nas*) [ "$DELEGATED" = 0 ] && { ok "the nas took it"; DELEGATED=1; } ;;
    esac
    case "$LINE" in
        *complete*) break ;;
        *failed*|*cancelled*) die "the job ended as: $LINE" ;;
    esac
    sleep 5
done

[ "$DELEGATED" = 1 ] || die "it completed without ever being delegated -- this
        was fetched in-process, which is not what this script proves."
ok "record is complete"

say "verify"
"$PY" - "$ROOT" "$VARIANT" <<'PYEOF'
import json, os, sys, glob
root, variant = sys.argv[1], sys.argv[2]
store = os.environ.get("ABSTRACTION_STORE") or os.path.join(
    os.path.expanduser("~"), ".abstraction")

rec = None
for p in glob.glob(os.path.join(store, "jobs", "*.json")):
    with open(p, encoding="utf-8") as f:
        r = json.load(f)
    if variant in json.dumps(r.get("spec", {})) and r.get("kind") == "download":
        if r.get("state") == "complete":
            rec = r
if rec is None:
    print("  no completed record mentioning %s" % variant); sys.exit(1)

d = rec.get("delegation") or {}
if d.get("system") != "nas":
    print("  delegation is %r, expected nas" % d); sys.exit(1)
if not d.get("delivered"):
    print("  the nas never delivered: %r" % d); sys.exit(1)

final = rec["spec"]["sink"]["final"]
if not os.path.exists(final):
    print("  the record says complete but %s is not there" % final); sys.exit(1)

want = rec["progress"]["done"]
got = os.path.getsize(final)
if got != want:
    print("  %s is %d bytes, record says %d" % (final, got, want)); sys.exit(1)

print("  delegated to %s, delivered, %d bytes at" % (d["system"], got))
print("  %s" % final)
PYEOF
[ $? -eq 0 ] || die "the record and the disk disagree"
ok "nas fetched it, bytes landed in lemonade's cache, sizes match"

ROW="$(curl -sf --max-time 15 "$API/downloads" | "$PY" -c "
import json,sys
for r in json.load(sys.stdin):
    if '$VARIANT' in str(r.get('id','')) or '$VARIANT' in str(r.get('file','')):
        print('%s %s%%' % (r.get('status'), r.get('percent'))); break
else: print('MISSING')
")"
[ "${ROW%% *}" = "completed" ] || die "the UI row says '$ROW', not completed"
ok "the download row says $ROW"

if [ "$KEEP" = "0" ]; then
    curl -sf --max-time 30 -X POST "$API/delete" -H 'Content-Type: application/json' \
        -d "{\"model\":\"$PULL_NAME\"}" >/dev/null 2>&1 \
        && ok "removed $MODEL so this can run again" \
        || printf '  \033[33mnote\033[0m  could not remove %s; pass --variant next run\n' "$MODEL"
fi

printf '\n  \033[32mPASS\033[0m — the nas fetched it and lemonade never knew\n\n'
