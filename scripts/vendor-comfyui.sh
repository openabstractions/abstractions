#!/usr/bin/env bash
# Refresh the facade copies the ComfyUI node ships.
#
# The node is installed by dropping one directory into custom_nodes/, so it has
# to carry the facade rather than point at it. Both modules are pure standard
# library, which is the only reason that is cheap.
#
# test_node.py compares the copies with the originals byte for byte, so
# forgetting to run this fails the tests rather than shipping a stale
# abstraction to whoever installed the node.

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/adopters/comfyui/abstraction_downloads/_vendor"

mkdir -p "$DEST"
for src in "$ROOT/job/python/abstraction_job.py" \
           "$ROOT/watch/python/abstraction_watch.py" \
           "$ROOT/download/python/abstraction_download.py"; do
    cp "$src" "$DEST/$(basename "$src")"
    printf '  %s\n' "$(basename "$src")"
done
echo "vendored into adopters/comfyui/abstraction_downloads/_vendor"
