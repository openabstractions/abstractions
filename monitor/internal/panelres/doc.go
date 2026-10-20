// Package panelres carries the Panel's Windows resources: its icon, native
// manifest, application manifest and VERSIONINFO, compiled from
// monitor/panel.rc by `go run ./monitor/icon/gen -syso`. Only the Panel's
// build imports it (monitor/resources_panel_windows.go); the presenter links
// its own (monitor/internal/presenterres).
package panelres
