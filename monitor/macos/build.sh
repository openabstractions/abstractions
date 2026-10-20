#!/bin/sh
# Builds "Abstraction Panel.app" straight from this checkout: the Go "panel"
# binary (monitor/, the same program installer/posix/payload.tsv's macos rows
# build for the package) and the Swift launcher (PanelLauncher.swift) beside
# it, for the native arch only. It is the fast dev loop and the screenshot
# tool's target (research/panel-native/DECISION.md item 4), not the release
# packager: installer/posix/build.py stages the same two programs, universal
# and signable, into the actual .pkg. Requires macOS, go and swiftc.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$here/../.." && pwd)
out=${1:-$here/build/Abstraction Panel.app}

[ "$(uname -s)" = Darwin ] || { echo "build.sh: macOS only" >&2; exit 1; }
for tool in go swiftc; do
    command -v "$tool" >/dev/null 2>&1 || { echo "build.sh: $tool not on PATH" >&2; exit 1; }
done

icon="$root/monitor/icon"
[ -f "$icon/panel.icns" ] || { echo "build.sh: $icon/panel.icns missing; run \`go generate ./monitor/icon/...\` first" >&2; exit 1; }

contents="$out/Contents"
macos="$contents/MacOS"
resources="$contents/Resources"
mkdir -p -- "$macos" "$resources"

echo "building the Go program"
( cd "$root/monitor" && CGO_ENABLED=1 go build -ldflags '-s -w -buildid=' -trimpath -o "$macos/panel" . )

echo "building the Swift launcher"
# minimum-macos.txt is the launcher's one minimum macOS version: this build,
# installer/posix/build.py and the launcher's tests read it, and the
# installer checks its Info.plist LSMinimumSystemVersion against it.
minimum=$(tr -d '[:space:]' < "$here/minimum-macos.txt")
swiftc -O -target "$(uname -m)-apple-macos$minimum" "$here/PanelLauncher.swift" -o "$macos/Abstraction Panel"

echo "building the app icon"
cp "$icon/panel.icns" "$resources/AppIcon.icns"

cp "$root/installer/posix/macos/Info.plist" "$contents/Info.plist"
chmod 755 "$macos/panel" "$macos/Abstraction Panel"

echo "ok    $out"
