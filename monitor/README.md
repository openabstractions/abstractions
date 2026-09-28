# OpenAbstractions control panel

See what your OA runtime is doing and decide what each program may do. The
panel lets you inspect accepted work, answer service questions, manage exact
permissions, follow logs and edit revisioned OA settings. Every action calls
the owning service through the same resolved clients an application uses.

The browser UI runs on this computer's loopback address. Its per-run URL is a
bearer credential; keep it private. Native Windows and macOS packaging
provides a desktop window that opens the full browser controls.

## Running the Panel

```bash
cd monitor && go run .
```

This listens on `127.0.0.1:8734` and prints a URL carrying this run's key.
Windows opens the local browser at that URL automatically; on other
platforms, open the printed URL yourself. Pass `-open=false` to suppress the
automatic open.

To use a runtime other than the installed one, such as an isolated
`openabstractions serve runtime --isolated <name> --state-dir <dir>`, name its
resolver endpoint and the executable it must run as under your account:

```bash
go run . -open=false -runtime-endpoint '\\.\pipe\openabstractions-user-<SID>-<name>-runtime' -runtime-program 'C:\path\openabstractions.exe'
```

With no runtime, the panel reports the absence and opens no provider files.

## Pages

Start with **Check the service** to see availability, **Show all apps' work** (under Work started by apps) to
inspect account-wide operations, or **Questions** to review a pending first-use
request. Credentials, inference hosts and provider registrations have their own
pages. Each service enforces the panel executable's permissions.

**Check the service** — inspects supervision and each runtime capability.
The check shares the SDK's authorized resolution results and a three-second
budget. Partial failures leave unanswered services marked Not checked. It runs on
request, performs no activation and shows when the snapshot was taken.

**Work started from this Panel** — the inventory of work accepted for this panel program, page
by page, with observe, cancel and a download of completed bytes. Submitting an
HTTP download saves a recovery record first; an uncertain reply is reconciled
with the same key and owner.

**Work started by apps** — the inventory of every program in this account
through `abstraction.job/operator@1`, with Cancel by operation id. The runtime
lists it under the rule `abstraction.job/inventory.read` and cancels under
`abstraction.job/acceptance.cancel`; the panel holds both by installation.

**Graphics card** — who holds a scarce resource on this machine now, through
`abstraction.resource/table@1`: for each resource the table reports, its
capacity, held bytes, instrument and observed age, and the holder rows —
program (shortened to its file name, with the full path on hover), account,
amount, evidence and since. Verified rows (the instrument measured them) and
claimed rows (a host adapter's own claim, carrying no amount) are styled
apart and never summed into one total, and neither are two resources' held
figures, since `card:<n>` and `memory` can report the same bytes on an APU.
Refresh always rereads the instrument (`fresh`); a bounded auto-refresh
rereads the standing sample every five seconds while the page is visible, up
to 120 reads, and pauses while it is hidden. The runtime narrows a caller
without `abstraction.resource/table.read` on `account` to its own rows rather
than refusing the read (CONTRACT.md RES-T4); the panel holds that rule by
installation the way it holds the others. Below the table: what is being
fetched, the account work rows `abstraction.job/operator@1` already lists,
and who holds the machine awake.

The awake section reads the awake resource's lease rows through the same
table client the rows above use (`abstraction.resource/leases@1`,
CONTRACT.md RES-A1): holder program, account, amount if any, since and lease
id, one row per live hold. When the table reports no awake resource — no
program currently holds it, or this runtime does not compose the lease book
— the section falls back to the legacy connection-owned rights service
exactly as `openabstractions rights holds` reads it, labelled "rights
fallback"; that service is not started by `serve runtime`, so the fallback
itself commonly reads unavailable unless something else runs it. The status
line always names which source answered, `table` or `rights fallback`, or
says unavailable and why when neither could.

**Questions** and **Rights** — answer or retire pending questions, and set or
revoke exact rules at the listed policy revision. The owning service decides
whether this panel may act. Answering a runtime's first-use question
(`rights.first_use`) writes its rule: allow writes the permit and never an exact
deny, both with why "asked at first use", and refuse writes nothing. The answer
and its rule are two edits: when the rule write meets a conflict, the list shows
the question as "Answered, rule not written" with a Retry rule button, which
answers the same option again and writes the rule at the current revision. "Allow
downloads for…" and "Allow inference for…" write each exact rule of the bundle
for the program chosen from the listed rules or typed in, one edit at a time;
the first rule that does not apply stops the bundle, and the status names the
rules that landed.

**Settings** — read the user settings and replace them at their
revision. Watch settings shows live service snapshots and their provenance
without replacing an editing draft. A conflicting edit preserves the draft; save
it before reading and reconciling the current revision. Stop watching cancels the
wait. A lost cursor or unavailable service stops observation and reports why.

**Logging** reads retained records through `abstraction.logging/reader@1` page
by page, or follows them live through `abstraction.logging/observer@1`, filtered
by program, lowest level and time. Following continues from the last page read,
and with no page read it starts at the history end (the `end` cursor word), so
only new records arrive. Every record lists its attestation hops. The
writer's own claim is styled as an unverified claim, and the stamp the logging
service made itself as verified. A stamp another relay asserted stays untrusted,
because the panel authorises no relay. A handler's sink-loss record (LOG-S13)
and a history gap are shown as gaps, and a gap stops following until the person
reads from the start or follows from the end again. The panel logs its own actions and identity checks
through a resolved sink, and the section shows that sink's counts.

**Identity** shows which runtime installed selection chose (`TRUSTED`,
`UNTRUSTED` or `PROOF_UNAVAILABLE`) and the platform declaration for this
platform. It shows how the runtime bound the panel as a caller, through
`abstraction.facade/caller@1`: account, program, pid, and the proof and
transport ceiling of each attribute. Beside it are each contract's resolution
for the panel and the logging service's stamp on a record the panel wrote, read
back from the history end. The rights decision and administration contracts
read "Not ready" while the runtime cannot read its policy file. It
also states the platform's identity limits; on macOS, protected calls are
refused.

**Explore** calls each probe of `abstraction-facade/go/probe` as the panel,
the one list `openabstractions probe` also runs: config read, user and rewrite,
storage list, read and inventory, model resolve, router hosts, models and pick,
jobs inventory, logging history, credentials list, inference, asks and a rights
decision. Config rewrite writes: it is labelled, asks for confirmation, and the
panel refuses it without `confirm=write`. Each typed outcome, refusals and `runtime_unavailable`
included, sits beside the exact rights rule that decides the call, read through
the rights operator for the program and account in the subject picker. Grant
and Revoke edit that rule at the listed policy revision through the Rights
endpoints and call again. The panel acts only as itself;
`openabstractions probe` makes the same calls from the command line, and a copy
named `openabstractions-probe` acts as a second program.

## Skins

Every page shares one stylesheet, served at `/static/panel.css`
(`monitor/static/panel.css`, embedded), and one shell: a navigation pane
listing Status, Work, Graphics card, Inference, Registry, Credentials, Questions and
rights, Settings, Logging, Identity and Explore, with the current page
marked, and a content pane where each page's own sections become grouped
rows. Status, Work, Questions and rights, Settings, Logging, Identity and
Explore are sections of the one service page; Graphics card, Inference, Registry and
Credentials keep their own routes, reused by the shell as before.

The document carries `data-platform="windows"`, `"macos"` or `"linux"`, set
by the server from `runtime.GOOS`. The stylesheet reads that attribute to
choose a skin. `windows` matches the Windows Settings app: Segoe UI, a
320px navigation pane, 4px-cornered cards, an accent bar on the selected
item. `macos` matches System Settings: a 215px sidebar with icon tiles, a
selection pill, 10px-cornered grouped lists. `linux` takes the macOS layout
with neutral gray tones. Both skins follow `prefers-color-scheme` for dark
mode. A request names the platform explicitly with
`?platform=windows|macos|linux`, which changes only the skin that renders;
the screenshot tool below uses it to capture every skin from one machine.

The Windows and macOS skins have recorded screenshots in
`docs/results/panel/`, in light and dark. The Linux skin renders the same way
as macOS with neutral tones but has no recorded screenshots yet.

`scripts/panel_screenshots.py --run` builds the Panel, starts it alone
(without a runtime; none of the captured pages need one to render their
shell) on a scratch loopback port, and drives a headless Edge or Chrome
through Status, Graphics card, Inference, Registry and Credentials at 1280x800
(`windows`) and 1180x760 (`macos`), in light and dark, writing
`docs/results/panel/<page>-<platform>-<scheme>.png`. Run `--help` first.

## Window behavior

### Windows

```bash
cd monitor
go build -ldflags="-H windowsgui" -o "Abstraction Panel.exe" .
```

`go build` alone carries the icon and the manifest, from the committed
`.syso` files [`monitor/icon`](icon) builds; see that package's README for
the `rc.exe`/`cvtres.exe` rebuild.

Double-click the built executable and a
[WebView2](https://developer.microsoft.com/microsoft-edge/webview2/) window
titled "Abstraction Panel" opens the Panel itself, at 1100 by 760 and
DPI-aware, on this run's own loopback URL and key. `monitor -native` opens the
same window from a terminal; `monitor` on its own serves the page, which is
how a browser UI is served on the local machine. Closing the window ends the
process. Windows 11 ships the WebView2 runtime; where it is missing, `-native`
opens the browser instead and says so on stderr. The window is hosted with an
in-house syscall and COM binding in [`win`](win) — no cgo, no vendored
library — that finds the Evergreen installation through Windows registration
and loads its architecture-matched `EmbeddedBrowserWebView.dll`. The package
ships no separate SDK loader. Missing runtime exports retain the browser fallback. The
message loop, DPI awareness and `win.Tray` are what the tray icon still
draws for itself.

`monitor -tray` (built as `Abstraction Panel.exe --tray`, Windows only) shows
a notification-area icon instead of serving a page or drawing a window, with
a menu of Open Panel, Questions and Quit. It runs the same loopback panel
server underneath, so Open Panel and Questions launch this run's own URL. It
polls `QuestionOperator.ListQuestions` every three seconds with a two-second
budget, remembers which pending question ids it has already announced, and
shows one Windows notification per new pending question, naming the asking
program and the action it wants. Clicking the icon or a shown notification
opens the Panel's Questions page. A runtime that cannot be reached changes
the icon's tooltip once and keeps polling silently; it is not itself
announced. The per-user installer registers `--tray` to start at sign-in; a
machine-scope installation leaves that to the person (installer/README.md).

### macOS

`Abstraction Panel.app` is a menu-bar item, the macOS analogue of `-tray`,
built by [`monitor/macos/build.sh`](macos/build.sh):

```bash
monitor/macos/build.sh
```

`PanelLauncher.swift` is a small AppKit program, compiled with `swiftc`
alone, no Xcode project and no third-party code. It starts `panel` (this
package's own binary, unchanged) as a child process, reads the loopback URL
it prints on its first line, and hosts that URL in a WKWebView window behind
an `NSStatusItem` menu of Open Panel, Questions and Quit — the same three
actions and the same URL `-tray` uses, read off the child instead of bound by
the launcher itself. `LSUIElement` in `Info.plist` keeps it out of the Dock.
The per-user installer registers it to run once at login through a
LaunchAgent, `installer/posix/macos/com.openabstractions.panel.plist`, the
analogue of the Windows Startup shortcut `PanelTrayLogonStart`: `RunAtLoad`
with no `KeepAlive`, so quitting it stays quit until the next login.

`installer/posix/payload.tsv` builds and stages the app bundle for the
release package; `build.sh` here is the standalone dev loop and the
screenshot tool's target, not the packager. Compiling the launcher and
opening the window are unproven until they run on an actual Mac.

### Linux

`go run .` or a plain `go build` serves the browser page; there is no native
window or tray for Linux. Open the printed loopback URL locally.

## Who is allowed to drive it

A loopback socket carries no caller identity — any process here can open one —
so the powers are bounded rather than assumed:

- **A key, minted per run and never written to disk**, required in a header on
  every action. A custom header cannot be set cross-origin without a preflight
  this server does not answer, so a web page you happen to be visiting cannot
  reach in and cancel your downloads. It is **not** proof of who is calling.
- **Loopback Host and same Origin** are required on every request.
- **Saved operations stay with their binding.** An action naming another
  endpoint, owner or history epoch is refused rather than resubmitted.

The unfixed part: any process on this machine that can read the URL can drive
the window.

## Limits

- **Windows opens the browser automatically.** On other platforms, open the
  printed loopback URL locally.
- **The window is Windows only.** Elsewhere `-native` refuses and says so.
- **The window's styling comes from `panel.manifest`, embedded as a resource**
  rather than shipped beside the binary. A build missing
  `monitor/rsrc_windows_*.syso` — a plain checkout is not one, since both are
  committed — carries no manifest, and the controls fall back to the
  pre-2001 style; everything still works. The manifest's Common-Controls
  dependency names `processorArchitecture="amd64"`; on the arm64 build this
  is a known mismatch, and Windows resolves it the same way, by falling back
  to unthemed controls for that architecture only.
- **No dark mode on the pre-manifest fallback.** Win32's stock controls have
  no supported way to ask for one; the WebView2 window itself follows
  `prefers-color-scheme` like any other page.
- **Pause, resume, destination collection, host refusals and network tiers are
  absent.** The service contracts do not offer them to the panel yet.

## Design records

- `research/panel-native/DECISION.md` item 1 — the decision for one web UI, a
  shared shell and two platform skins.
- `research/panel-native/DECISION.md` item 4 — the decision for
  `monitor/macos/build.sh` as the standalone macOS dev loop and screenshot
  target, separate from the release packager.
- `research/panel-native/WEBVIEW2-2026-09-22.md` — the research behind
  hosting the native Windows window in WebView2.
- `research/panel-native/MACOS-2026-09-22.md` — the record of what remains
  unproven on macOS until the Mac leg runs.
