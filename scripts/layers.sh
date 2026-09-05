#!/usr/bin/env bash
# Fetch every layer into .layers/, so the cross-language harnesses have
# something to run against.
#
# This is what splitting the layers into their own repositories costs. Inside
# one repository the proof that three implementations agree was `check.sh` and a
# working tree. Now it is N checkouts, and this script is the thing that makes
# that cheap enough to keep doing -- because a proof nobody runs is not a proof.
#
#   usage: scripts/layers.sh [--ssh]
#     --ssh   clone over ssh (for pushing). Default is https, which is what a
#             stranger can do without an account.
#
# Re-running updates existing checkouts rather than re-cloning.

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/.layers"
ORG="openabstractions"
BASE="https://github.com/$ORG"
[ "${1:-}" = "--ssh" ] && BASE="git@github.com:$ORG"

LAYERS="abstraction-job abstraction-download abstraction-storage
        abstraction-config abstraction-logging abstraction-model
        abstraction-facade adopter-comfyui"

mkdir -p "$DEST"
for repo in $LAYERS; do
    printf '%-24s ' "$repo"
    if [ -d "$DEST/$repo/.git" ]; then
        git -C "$DEST/$repo" fetch --quiet origin
        git -C "$DEST/$repo" reset --quiet --hard origin/main
        printf 'updated to %s\n' "$(git -C "$DEST/$repo" rev-parse --short HEAD)"
    else
        git clone --quiet "$BASE/$repo.git" "$DEST/$repo"
        printf 'cloned at %s\n' "$(git -C "$DEST/$repo" rev-parse --short HEAD)"
    fi
done

# The harnesses expect the old single-tree shape: job/go, download/python and so
# on. Rather than rewrite every one of them for a layout that may change again,
# give them that shape as a directory of links into the checkouts.
#
# Windows makes real symlinks a privilege, so a junction is used where it can
# be, and a copy where it cannot. A copy is correct but goes stale, which is why
# this prints which one it did.
TREE="$ROOT/.tree"
rm -rf "$TREE"
mkdir -p "$TREE"
link() {  # link <name in tree> <path under .layers>
    local name="$1" src="$DEST/$2"
    [ -d "$src" ] || return 0
    if ln -s "$src" "$TREE/$name" 2>/dev/null; then return 0; fi
    if command -v cmd.exe >/dev/null 2>&1 &&
       cmd.exe //c mklink //J "$(cygpath -w "$TREE/$name")" "$(cygpath -w "$src")" >/dev/null 2>&1; then
        return 0
    fi
    cp -r "$src" "$TREE/$name"
    printf '  %s copied rather than linked -- it will go stale\n' "$name"
}
link job          abstraction-job
link download     abstraction-download
link storage      abstraction-storage
link config       abstraction-config
link logging      abstraction-logging
link model        abstraction-model
link abstraction  abstraction-facade

echo
echo "checkouts in .layers, single-tree view in .tree"
