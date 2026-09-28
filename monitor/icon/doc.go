// Package icon holds the Panel's application icon: the brand mark PNGs
// copied and resampled to the sizes a Windows ICO wants, the packed
// panel.ico itself, and the generator that builds both from
// openabstractions-flat/openabstractions.github.io/brand.
//
// Nothing here is imported by the Panel program. The icon reaches the built
// binary through monitor/panel.rc and the two monitor/rsrc_windows_*.syso
// files go build links automatically (see monitor/README.md); this package
// exists only to hold those inputs and the tool that (re)builds them.
//
// Run `go generate ./monitor/icon/...` after the brand mark changes. That
// rewrites the PNGs below, panel.ico and monitor/panel.rc; it does not touch
// the .syso files, which need rc.exe and cvtres.exe and so are rebuilt
// separately with `go run ./monitor/icon/gen -syso` on a machine with the
// Visual Studio or Windows SDK build tools (monitor/README.md).
package icon

//go:generate go run ./gen
