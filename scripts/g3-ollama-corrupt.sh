#!/usr/bin/env bash
# G3 — does Ollama check the digests it stores?
#
# C2 proved Ollama HAS content addressing: blobs are named sha256-<hex> and the
# bytes hash to their own filename. It did not prove Ollama CHECKS it. This does.
#
# It never touches the real store. It copies one model's manifest and blobs into
# an isolated OLLAMA_MODELS directory, runs its own server on its own port,
# answers a prompt, corrupts one blob in place without changing its length,
# restarts, and asks the same question again.
#
#   usage: g3-ollama-corrupt.sh [model] [workdir]
#   needs: ollama on PATH, python3 (or py -3), curl, sha256sum
#
# A failure is a result: if Ollama refuses the corrupted blob, say so plainly.

set -u

MODEL="${1:-qwen2.5:0.5b}"
WORK="${2:-$HOME/g3-ollama}"
PORT="${G3_PORT:-11435}"
HOSTPORT="127.0.0.1:$PORT"
PROMPT="The capital of France is"
SRC="${OLLAMA_SRC_MODELS:-$HOME/.ollama/models}"

# Windows ships a python3 stub on PATH that exists and does not run, so test
# that the interpreter actually executes rather than that the name resolves.
PY=""
for cand in python3 python "py -3"; do
    if $cand -c "pass" >/dev/null 2>&1; then PY="$cand"; break; fi
done
[ -n "$PY" ] || { echo "no working python found (tried python3, python, py -3)"; exit 2; }

say() { printf '\n=== %s ===\n' "$*"; }

repo="${MODEL%%:*}"
tag="${MODEL##*:}"
manifest="$SRC/manifests/registry.ollama.ai/library/$repo/$tag"

say "inputs"
echo "model     $MODEL"
echo "source    $manifest"
echo "workdir   $WORK"
ollama --version 2>&1 | grep -i version | head -2
[ -f "$manifest" ] || { echo "no manifest for $MODEL under $SRC — pull it first"; exit 2; }

say "isolate: copy manifest and blobs into a private store"
rm -rf "$WORK"
mkdir -p "$WORK/blobs" "$WORK/manifests/registry.ollama.ai/library/$repo"
cp "$manifest" "$WORK/manifests/registry.ollama.ai/library/$repo/$tag"
digests=$(grep -o "sha256:[0-9a-f]\{64\}" "$manifest" | sed "s/sha256://" | sort -u)
[ -n "$digests" ] || { echo "could not read layer digests out of the manifest"; exit 2; }
for d in $digests; do cp "$SRC/blobs/sha256-$d" "$WORK/blobs/sha256-$d" || exit 2; done
ls -l "$WORK/blobs"

# The largest layer is the weights. That is the one worth corrupting.
BLOB=$(ls -S "$WORK"/blobs/sha256-* | head -1)
[ -f "$BLOB" ] || { echo "no blobs copied"; exit 2; }
say "target blob"
ls -l "$BLOB"
echo "content hashes to its own name (C2, re-checked here):"
sha256sum "$BLOB"

start_server() {
    OLLAMA_MODELS="$WORK" OLLAMA_HOST="$HOSTPORT" ollama serve > "$WORK/serve.log" 2>&1 &
    for _ in $(seq 1 60); do
        curl.exe -s "http://$HOSTPORT/api/tags" >/dev/null 2>&1 && return 0
        sleep 1
    done
    echo "server did not come up; log:"; cat "$WORK/serve.log"; exit 2
}
stop_server() { taskkill //F //IM ollama.exe >/dev/null 2>&1 || pkill -f "ollama serve" 2>/dev/null; sleep 2; }

ask() {
    curl.exe -s "http://$HOSTPORT/api/generate" \
        -d "{\"model\":\"$MODEL\",\"prompt\":\"$PROMPT\",\"stream\":false,\"options\":{\"temperature\":0,\"seed\":1,\"num_predict\":24}}" \
    | $PY -c "
import sys,json
d=json.load(sys.stdin)
print('response   ', repr(d.get('response')))
print('done_reason', d.get('done_reason'))
print('error      ', d.get('error'))
"
}

stop_server
say "baseline: pristine blob"
start_server
OLLAMA_HOST="$HOSTPORT" ollama list
ask
stop_server

say "corrupt: invert 512 bytes at 90% of the blob, length unchanged"
$PY -c "
import os,sys
p=sys.argv[1]
n=os.path.getsize(p); off=n*9//10
with open(p,'r+b') as f:
    f.seek(off); b=bytearray(f.read(512)); before=bytes(b)
    for i in range(len(b)): b[i]^=0xFF
    f.seek(off); f.write(bytes(b))
print('offset      ', off, 'of', n)
print('before[:16] ', before[:16].hex())
print('after [:16] ', bytes(b)[:16].hex())
print('size        ', os.path.getsize(p), '(unchanged:', os.path.getsize(p)==n, ')')
" "$BLOB"
echo "the blob no longer hashes to its own name:"
sha256sum "$BLOB"
echo "its filename still claims it does:"
basename "$BLOB"

say "same question, corrupted blob"
start_server
OLLAMA_HOST="$HOSTPORT" ollama list
ask
stop_server

say "done"
echo "If the second answer differs from the first and nothing errored, Ollama"
echo "stores digests it does not check, and the refusal differentiator covers"
echo "both incumbents. If it refused, say so plainly instead."
