# Panel Windows icon

This package holds the Panel's Windows icon source — the brand mark
(`openabstractions-flat/openabstractions.github.io/brand`) copied and
resampled to the sizes an ICO wants, packed into `panel.ico` — and the
generator that builds it, `monitor/icon/gen`.

`go build` alone carries the icon and the manifest into the Panel executable:
`monitor/rsrc_windows_amd64.syso` and `rsrc_windows_arm64.syso` are committed,
and Go links whichever one matches `GOARCH` automatically because of its
name, no build tag or flag needed. They hold the compiled form of
`monitor/panel.rc`, `panel.ico` and `panel.manifest`. Rebuilding them needs
`rc.exe` and `cvtres.exe`, not part of a plain build:

```bash
go run ./monitor/icon/gen -syso
```

writes `monitor/rsrc_windows_amd64.syso` and `rsrc_windows_arm64.syso`. Both
are committed, so a plain `go build` from the published module — on any
platform Go cross-compiles from — links the matching one in without needing
`rc.exe` itself; only changing the icon, the manifest or the version block
needs this step rerun and its output re-committed.

The shared manifest uses `processorArchitecture="*"` for the application and
Common Controls dependency. Windows selects its native assembly on both x64
and ARM64; the COFF machine type still comes from the matching `.syso` file.
This follows Microsoft's [application manifest schema](https://learn.microsoft.com/en-us/windows/win32/sbscs/application-manifests#assemblyidentity).

`win_windows.go` loads the icon back out of the running executable at
resource id 1 (`LoadImageW`, `IMAGE_ICON`) for the window's title bar and the
taskbar, and `win/tray_windows.go` the same way for the notification-area
icon; a binary built without the `.syso` files carries no such resource, and
both fall back to the stock `IDI_APPLICATION` icon rather than failing to
draw.

Visual Studio 18's `cvtres.exe` stamps every object it writes with an
absolute `@comp.id` symbol recording its own tool version, which breaks the
Go linker; `go run ./monitor/icon/gen -syso` strips that symbol from the
`.syso` files it writes before they are committed. The generator also clears
the path-bearing CodeView debug section and normalizes the COFF timestamp,
so the committed resources do not expose the builder's source directory and
repeat runs produce identical bytes. The resource sections retain the icon,
manifest and version block. Reuse this generator
rather than hand-running `rc.exe`/`cvtres.exe` for any new `.syso` file.

Run `go generate ./monitor/icon/...` after the brand mark changes; that
rewrites the PNGs, `panel.ico` and `monitor/panel.rc`, and does not touch the
`.syso` files, which still need the separate `-syso` run above.
