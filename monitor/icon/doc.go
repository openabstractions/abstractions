// Package icon holds the Panel's application icon: the brand mark on a pale
// rounded plate, the native-size PNGs and packed panel.ico and panel.icns,
// and the generator that builds them from
// openabstractions-flat/openabstractions.github.io/brand.
//
// Nothing here is imported by the Panel program. The icon reaches the built
// binary through monitor/panel.rc and the two monitor/rsrc_windows_*.syso
// files go build links automatically (see monitor/README.md); this package
// exists only to hold those inputs and the tool that (re)builds them.
// macOS bundles copy panel.icns into Contents/Resources/AppIcon.icns.
//
// Run `go generate ./monitor/icon/...` after the brand mark changes. That
// rewrites the PNGs below, panel.ico, panel.icns and monitor/panel.rc. The
// .syso files need rc.exe and cvtres.exe and are rebuilt
// separately with `go run ./monitor/icon/gen -syso` on a machine with the
// Visual Studio or Windows SDK build tools (monitor/README.md).
package icon

//go:generate go run ./gen
