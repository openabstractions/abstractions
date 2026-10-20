// Package presenterres carries the Asks presenter's Windows resources: the
// OA icon, the native manifest and a VERSIONINFO whose FileDescription,
// "OpenAbstractions", is the name Task Manager shows. They are compiled from
// monitor/presenter.rc by `go run ./monitor/icon/gen -syso`. Only the
// presenter build imports it (monitor/presenter_main_windows.go).
package presenterres
