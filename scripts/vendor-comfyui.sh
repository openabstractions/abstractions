#!/usr/bin/env bash
# Refresh the facade copies the ComfyUI node ships.
#
# The node is installed by cloning one repository into custom_nodes, so it has
# to carry the facade rather than point at it. Both modules are pure standard
# library, which is the only reason that is cheap.
#
#   usage: scripts/vendor-comfyui.sh <path to an adopter-comfyui checkout>
#
# The layers live in their own repositories now, so this takes them from
# .layers -- run scripts/layers.sh first. The node's own test compares the
# copies against the originals when ABSTRACTION_LAYERS names a checkout, so
# forgetting to run this fails that test rather than shipping a stale
# abstraction to whoever installed the node.

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NODE="${1:-}"
[ -n "$NODE" ] || { echo "usage: $0 <path to an adopter-comfyui checkout>" >&2; exit 2; }
[ -f "$NODE/__init__.py" ] || { echo "not a node checkout: $NODE" >&2; exit 2; }

LAYERS="$ROOT/.layers"
[ -d "$LAYERS" ] || { echo "no $LAYERS -- run scripts/layers.sh first" >&2; exit 2; }

for pair in "abstraction-job:abstraction_job.py" \
            "abstraction-download:abstraction_download.py"; do
    repo="${pair%%:*}"
    file="${pair##*:}"
    src="$LAYERS/$repo/python/$file"
    [ -f "$src" ] || { echo "missing $src" >&2; exit 1; }
    cp "$src" "$NODE/_vendor/$file"
    printf '  %-26s from %s\n' "$file" "$(git -C "$LAYERS/$repo" rev-parse --short HEAD)"
done

echo "vendored into $NODE/_vendor -- update _vendor/VERSION to match"
