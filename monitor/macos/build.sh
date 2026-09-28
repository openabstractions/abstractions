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
for tool in go swiftc iconutil; do
    command -v "$tool" >/dev/null 2>&1 || { echo "build.sh: $tool not on PATH" >&2; exit 1; }
done

icon="$root/monitor/icon"
brand="$root/openabstractions-flat/openabstractions.github.io/brand"
for f in "$icon/mark-16.png" "$icon/mark-32.png" "$icon/mark-64.png" "$icon/mark-128.png" "$icon/mark-256.png"; do
    [ -f "$f" ] || { echo "build.sh: $f missing; run \`go generate ./monitor/icon/...\` first" >&2; exit 1; }
done
[ -f "$brand/mark-512.png" ] || { echo "build.sh: $brand/mark-512.png missing" >&2; exit 1; }

contents="$out/Contents"
macos="$contents/MacOS"
resources="$contents/Resources"
mkdir -p -- "$macos" "$resources"

echo "building the Go program"
( cd "$root/monitor" && CGO_ENABLED=0 go build -ldflags '-s -w -buildid=' -trimpath -o "$macos/panel" . )

echo "building the Swift launcher"
swiftc -O "$here/PanelLauncher.swift" -o "$macos/Abstraction Panel"

echo "building the app icon"
iconset="$here/build/AppIcon.iconset"
rm -rf -- "$iconset"
mkdir -p -- "$iconset"
cp "$icon/mark-16.png"    "$iconset/icon_16x16.png"
cp "$icon/mark-32.png"    "$iconset/icon_16x16@2x.png"
cp "$icon/mark-32.png"    "$iconset/icon_32x32.png"
cp "$icon/mark-64.png"    "$iconset/icon_32x32@2x.png"
cp "$icon/mark-128.png"   "$iconset/icon_128x128.png"
cp "$icon/mark-256.png"   "$iconset/icon_128x128@2x.png"
cp "$icon/mark-256.png"   "$iconset/icon_256x256.png"
cp "$brand/mark-512.png"  "$iconset/icon_256x256@2x.png"
cp "$brand/mark-512.png"  "$iconset/icon_512x512.png"
# No icon_512x512@2x.png (1024px): the brand mark tops out at 512. iconutil
# accepts a partial iconset and just omits the sizes it was not given.
iconutil -c icns -o "$resources/AppIcon.icns" "$iconset"
rm -rf -- "$iconset"

# The menu-bar item's own image (PanelLauncher.swift), at 1x and 2x —
# Cocoa picks the @2x variant on a Retina display by the file name alone.
cp "$icon/mark-16.png" "$resources/MenuBarIcon.png"
cp "$icon/mark-32.png" "$resources/MenuBarIcon@2x.png"

cp "$root/installer/posix/macos/Info.plist" "$contents/Info.plist"
chmod 755 "$macos/panel" "$macos/Abstraction Panel"

echo "ok    $out"
