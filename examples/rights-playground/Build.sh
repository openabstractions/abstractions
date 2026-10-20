#!/bin/sh
# Prepare the cross-platform application from this source. Runtime and native
# Panel packaging must be supplied by the platform's existing reviewed builder.
set -eu
case "${1-}" in
  ''|-h|--help) printf '%s\n' 'Build.sh PACK PREBUILT_RUNTIME_AND_PANEL_DIR (Go on PATH; launches nothing)'; exit 0 ;;
esac
test "$#" -eq 2 || { printf '%s\n' 'Pass PACK and PREBUILT_RUNTIME_AND_PANEL_DIR.' >&2; exit 2; }
module=$(dirname -- "$0")
case "$module" in /*) ;; *) module="$PWD/$module" ;; esac
pack=$1
case "$pack" in /*) ;; *) pack="$PWD/$pack" ;; esac
prebuilt=$2
case "$prebuilt" in /*) ;; *) prebuilt="$PWD/$prebuilt" ;; esac
[ "$(uname -s)" = Darwin ] || { printf '%s\n' 'Build.sh prepares the native macOS pack; use Build.ps1 on Windows.' >&2; exit 1; }
test ! -e "$pack/run" || { printf '%s\n' 'Existing run is preserved; choose a fresh pack.' >&2; exit 1; }
mkdir -p -- "$pack"
test -x "$prebuilt/openabstractions" || { printf '%s\n' 'Missing reviewed native runtime.' >&2; exit 1; }
test -x "$prebuilt/Abstraction Panel.app/Contents/MacOS/panel" || { printf '%s\n' 'Missing reviewed native Panel bundle.' >&2; exit 1; }
cp -- "$prebuilt/openabstractions" "$pack/openabstractions"
/usr/bin/ditto "$prebuilt/Abstraction Panel.app" "$pack/Abstraction Panel.app"
go -C "$module" build -o "$pack/Playground A" .
cp -- "$pack/Playground A" "$pack/Playground B"
for helper in Start.command Stop.command Reset.command App-A.command App-B.command; do
    cp -- "$module/$helper" "$pack/$helper"
    chmod 700 "$pack/$helper"
done
chmod 700 "$pack/Playground A" "$pack/Playground B"
/usr/bin/shasum -a 256 "$pack/openabstractions" "$pack/Abstraction Panel.app/Contents/MacOS/panel" "$pack/Abstraction Panel.app/Contents/MacOS/Abstraction Panel" "$pack/Playground A" "$pack/Playground B" > "$pack/build-hashes.txt"
git -C "$module/../.." rev-parse HEAD > "$pack/build-source.txt"
printf '%s\n' 'Prepared. Start.command uses only the active console account and an owned isolated XPC agent.' 'Runtime signing and its native Keychain backend must be qualified before the real credential journey.'
