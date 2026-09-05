#!/usr/bin/env bash
# The demo. One model file, 512 bytes changed, three tools asked the same thing.
#
# Everything here has its own transcript under docs/results/ — G1b (llama.cpp),
# G3 (Ollama), H2 (the vault). This script is those three results in one screen,
# on one file, so the whole argument can be seen at once instead of read.
#
#   usage: demo.sh [workdir]
#   needs: ollama, llama-server, go, python3, curl, sha256sum
#
# It never touches a real store. Everything runs in workdir against copies.

set -u

WORK="${1:-$HOME/vault-demo}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
OLLAMA_MODEL="${DEMO_OLLAMA_MODEL:-qwen2.5:0.5b}"
OLLAMA_SRC="${OLLAMA_SRC_MODELS:-$HOME/.ollama/models}"
GGUF="${VAULT_CONFORMANCE_GGUF:-}"
LLAMA="${VAULT_LLAMA_SERVER:-llama-server}"
PORT="${DEMO_PORT:-11435}"
LPORT="${DEMO_LLAMA_PORT:-18080}"
PROMPT="The capital of France is"
CORRUPT_BYTES=512

PY=""
for cand in python3 python "py -3"; do
    if $cand -c "pass" >/dev/null 2>&1; then PY="$cand"; break; fi
done
[ -n "$PY" ] || { echo "no working python"; exit 2; }

act() { printf '\n\n=========== %s ===========\n\n' "$*"; }
step() { printf '\n--- %s ---\n' "$*"; }

rm -rf "$WORK"; mkdir -p "$WORK"

act "ACT 0 — a model file, and the name it claims"

step "the file"
[ -f "$GGUF" ] || { echo "set VAULT_CONFORMANCE_GGUF to a real .gguf"; exit 2; }
cp "$GGUF" "$WORK/model.gguf"
ls -l "$WORK/model.gguf" | awk '{print $5, "bytes"}'
echo "sha256 of the bytes, which is the only name that cannot lie:"
sha256sum < "$WORK/model.gguf" | cut -d' ' -f1

step "corrupt it: $CORRUPT_BYTES bytes inverted, 90% of the way in, length untouched"
$PY "$REPO/scripts/corrupt.py" "$WORK/model.gguf" "$CORRUPT_BYTES" invert
echo "the file now hashes to:"
sha256sum < "$WORK/model.gguf" | cut -d' ' -f1
echo
echo "Nothing about the file's size, name, mtime or extension changed."
echo "This is what bit rot, a bad cable, a failing DIMM and an interrupted"
echo "write all look like on disk."

act "ACT 1 — llama.cpp is asked"

serve_llama() {
    "$LLAMA" -m "$1" --host 127.0.0.1 --port "$LPORT" -c 512 -ngl 0 > "$WORK/llama.log" 2>&1 &
    for _ in $(seq 1 120); do
        curl.exe -s "http://127.0.0.1:$LPORT/health" >/dev/null 2>&1 && return 0
        sleep 1
    done
    return 1
}
ask_llama() {
    # /health answers 200 while the model is still loading, and the request then
    # comes back 503 "Loading model". Ask until it answers or the wait runs out.
    for _ in $(seq 1 120); do
        raw=$(curl.exe -s "http://127.0.0.1:$LPORT/completion" \
            -H "Content-Type: application/json" \
            -d "{\"prompt\":\"$PROMPT\",\"n_predict\":24,\"temperature\":0,\"seed\":1}")
        case "$raw" in
            *'"content"'*) echo "$raw" | $PY -c "import sys,json; print('   answer:', repr(json.load(sys.stdin).get('content')))"; return 0 ;;
        esac
        sleep 1
    done
    echo "   answer: (none) — last response: $raw"
}
kill_llama() { taskkill //F //IM llama-server.exe >/dev/null 2>&1 || pkill -f llama-server 2>/dev/null; sleep 1; }

kill_llama
step "pristine model"
cp "$GGUF" "$WORK/pristine.gguf"
if serve_llama "$WORK/pristine.gguf"; then echo "   loaded: yes"; ask_llama; else echo "   loaded: NO"; fi
kill_llama

step "corrupted model — same size, same name, 512 bytes different"
if serve_llama "$WORK/model.gguf"; then
    echo "   loaded: yes    <-- no error, no warning, no exit code"
    ask_llama
else
    echo "   loaded: NO (llama.cpp refused it — that is a result, record it)"
fi
kill_llama

act "ACT 2 — Ollama is asked"

echo "Ollama DOES store digests: every blob is named sha256-<hex> and its"
echo "manifests list each layer by digest. The question is whether it checks them."

repo="${OLLAMA_MODEL%%:*}"; tag="${OLLAMA_MODEL##*:}"
manifest="$OLLAMA_SRC/manifests/registry.ollama.ai/library/$repo/$tag"
if [ -f "$manifest" ]; then
    mkdir -p "$WORK/ollama/blobs" "$WORK/ollama/manifests/registry.ollama.ai/library/$repo"
    cp "$manifest" "$WORK/ollama/manifests/registry.ollama.ai/library/$repo/$tag"
    for d in $(grep -o "sha256:[0-9a-f]\{64\}" "$manifest" | sed "s/sha256://" | sort -u); do
        cp "$OLLAMA_SRC/blobs/sha256-$d" "$WORK/ollama/blobs/sha256-$d"
    done
    BLOB=$(ls -S "$WORK"/ollama/blobs/sha256-* | head -1)

    start_ollama() {
        OLLAMA_MODELS="$WORK/ollama" OLLAMA_HOST="127.0.0.1:$PORT" ollama serve > "$WORK/ollama.log" 2>&1 &
        for _ in $(seq 1 60); do
            curl.exe -s "http://127.0.0.1:$PORT/api/tags" >/dev/null 2>&1 && return 0
            sleep 1
        done
        return 1
    }
    ask_ollama() {
        curl.exe -s "http://127.0.0.1:$PORT/api/generate" \
            -H "Content-Type: application/json" \
            -d "{\"model\":\"$OLLAMA_MODEL\",\"prompt\":\"$PROMPT\",\"stream\":false,\"options\":{\"temperature\":0,\"seed\":1,\"num_predict\":24}}" \
        | $PY -c "import sys,json; d=json.load(sys.stdin); print('   answer:', repr(d.get('response'))); print('   error :', d.get('error'))"
    }
    stop_ollama() { taskkill //F //IM ollama.exe >/dev/null 2>&1 || pkill -f "ollama serve" 2>/dev/null; sleep 2; }

    stop_ollama
    step "pristine blob"
    start_ollama && ask_ollama
    stop_ollama

    step "corrupt the blob: $CORRUPT_BYTES bytes inverted, length untouched"
    $PY "$REPO/scripts/corrupt.py" "$BLOB" "$CORRUPT_BYTES" invert
    echo "   the blob no longer hashes to its own filename:"
    echo "   filename says  $(basename "$BLOB" | sed 's/sha256-//')"
    echo "   bytes hash to  $(sha256sum < "$BLOB" | cut -d' ' -f1)"

    step "ask again"
    start_ollama
    OLLAMA_HOST="127.0.0.1:$PORT" ollama list
    ask_ollama
    stop_ollama
else
    echo "(no local $OLLAMA_MODEL — skipping Act 2)"
fi

act "ACT 3 — the vault is asked"

(cd "$REPO/vault" && go build -o "$WORK/vault.exe" ./cmd/vault) || { echo "build failed"; exit 2; }
mkdir -p "$WORK/store"
# Read from stdin: coreutils escapes the whole line with a leading backslash
# when the filename contains one, and every path here is a Windows path.
PRISTINE_DIGEST=$(sha256sum < "$GGUF" | cut -d' ' -f1)

step "the honest file, stored under its own name"
cp "$GGUF" "$WORK/store/$PRISTINE_DIGEST"
VAULT_STORE="$WORK/store" "$WORK/vault.exe" verify "$PRISTINE_DIGEST"

step "the corrupted file, stored under the name it claims to be"
cp "$WORK/model.gguf" "$WORK/store/$PRISTINE_DIGEST"
VAULT_STORE="$WORK/store" "$WORK/vault.exe" verify "$PRISTINE_DIGEST"
echo "   exit code: $?"

act "THE POINT"
cat <<'EOF'
One file. 512 bytes out of half a gigabyte. Length identical.

  llama.cpp   accepted the file and started serving. The model then produced
              unusable output, reported as a generic 500 with no suggestion
              that the file on disk is damaged.
  Ollama      listed it, loaded it, and answered with confident nonsense —
              while storing, in the filename, the digest that would have
              caught it.
  vault       refused, named the file, and exited non-zero.

Note what none of the failures said: "this file is corrupt." A user sees a
server error, or a bad answer, and reaches for the prompt, the seed, the
quantisation or the context length. The file is the last thing anyone suspects
and the only thing that is actually wrong.

Every claim here has its own transcript: docs/results/G1b.txt, G2c.txt, G3.txt,
H2.txt. This script is those results on one file, in one screen.
EOF
