# OpenAbstractions control panel

See what your OA runtime is doing and decide what each program may do. The
panel lets you inspect accepted work, answer service questions, manage exact
permissions, follow logs and edit revisioned OA settings. Every action calls
the owning service through the same resolved clients an application uses.

The installed Panel is a desktop window on Windows and macOS. The window
answers its pages, data and actions inside the Panel process and opens no
network listener (VISION 2026-10-07). The browser page on this computer's
loopback address is the developer mode, `-native=false`; its per-run URL is a
bearer credential, so keep it private.

## Running the Panel

```bash
cd monitor && go run .
```

This listens on `127.0.0.1:8734` and prints a URL carrying this run's key.
Windows opens the local browser at that URL automatically; on other
platforms, open the printed URL yourself. Pass `-open=false` to suppress the
automatic open.

`-page` selects the initial page from `overview` (the default), `questions`,
or `credentials`. The Panel maps those names to its own existing pages; it
does not accept a path or URL. For example, `-page credentials` opens the
Credentials page in this Panel run. `-quiet` suppresses printing the keyed URL
for a launcher that only needs the Panel to open its own window or browser.
The Panel still creates and uses its per-run key internally.

An application's own installer registers the application through the Panel
(VISION 2026-10-06). `-register-application <file>` reads the application
manifest in the file: the Facade's `ApplicationDescriptor` in its wire form
(`name`, `program`, `title`, optional `start_guidance` and `activation`). The
program and each activation argument may begin with `{app}` (the folder the
manifest file is in), `{local_app_data}` or `{pictures}` (this account's
folders); the Panel resolves them, and refuses an unknown placeholder, one
anywhere but the start, and any path that leaves its folder, by file identity
(`identity.Within`, so a junction counts where it points). An absolute
argument must lie inside one of the three folders and the program inside
`{app}`. It then checks the manifest with the Facade's own rule
(`client.ParseApplicationDescriptorResolving`), and refuses an unknown or duplicate
field or an out-of-bounds value before any window opens. The window shows the
manifest's title, name, program and activation recipe as untrusted text.
When an entry with the same name is registered, the window shows it beside the
replacement. The Panel registers the manifest with its own
`application.manage` only after the person clicks Register; Cancel writes
nothing. The Register click also writes `application.read` on `app:<name>`
for the Panel's own program, so Apps lists the entry; the person can revoke
it on the Rights page. `-remove-application <name>` shows the registered entry and removes
it after the person clicks Remove, which also clears that rule. Each run settles one request. The window
takes a free loopback port unless `-addr` is given. Such a run never prints
its keyed URL, since the installer owns its standard output. A manifest whose
title, program, recipe or guidance holds a control or format character (a
bidirectional override, a zero-width character; the guidance may hold line
breaks) is refused. When the replacement of an entry fails after the earlier
entry was removed, the Panel registers the earlier entry again and says so.
The confirmation guards against accidental and installer-driven
registration; a hostile process already running on the same account is out
of scope until presence-verified Asks.

To use a runtime other than the installed one, such as an isolated
`openabstractions serve runtime --isolated <name> --state-dir <dir>`, name its
resolver endpoint and the executable it must run as under your account:

```bash
go run . -open=false -runtime-endpoint '\\.\pipe\openabstractions-user-<SID>-<name>-runtime' -runtime-program 'C:\path\openabstractions.exe' -runtime-state-dir 'C:\path\state'
```

`-runtime-state-dir` is that runtime's `--state-dir`. Setup keeps its plan,
model journals, installed apps and ComfyUI data under `<state-dir>\setup`
for it, and refuses the picture-model provider's verbs, whose folders are
this account's own. Without `-runtime-state-dir` Setup refuses instead of
writing your own Setup state.

The runtime answers questions and edits rules only for its operator
programs. A Panel built from source is one when the isolated runtime is
started with `--operator-program <absolute path of the built Panel>`; build
the Panel first, since `go run` places it at a new path each time. The
installed runtime refuses `--operator-program`, and its operators stay the
programs installed beside it.

With no runtime, the panel reports the absence and opens no provider files.

## Pages

Start with **Check the service** to see availability, **Activity** to
inspect account-wide operations, or **Questions** to review a pending first-use
request. Credentials, inference hosts and provider registrations have their own
pages. Each service enforces the panel executable's permissions.

**Check the service** — inspects supervision and each runtime capability.
The check shares the SDK's authorized resolution results and a three-second
budget. Partial failures leave unanswered services marked Not checked. It runs on
request, performs no activation and shows when the snapshot was taken.

**Work started by apps** — the Activity page's first section, read on
arrival: the inventory of every program in this account, downloads included,
through `abstraction.job/operator@1`, with Cancel by operation id. The runtime
lists it under the rule `abstraction.job/inventory.read` and cancels under
`abstraction.job/acceptance.cancel`; the panel holds both by installation.

**Work started from this Panel** — the inventory of work accepted for this
panel program, read on arrival, page by page, with Check state, Cancel and a
download of completed bytes.

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
installation the way it holds the others. Below the table: who holds the
machine awake.

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

**Logging**, a section of Settings, reads retained records through `abstraction.logging/reader@1` page
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

**Identity**, a section of Settings, shows which runtime installed selection chose (`TRUSTED`,
`UNTRUSTED` or `PROOF_UNAVAILABLE`) and the platform declaration for this
platform. It shows how the runtime bound the panel as a caller, through
`abstraction.facade/caller@1`: account, program, pid, and the proof and
transport ceiling of each attribute. Beside it are each contract's resolution
for the panel and the logging service's stamp on a record the panel wrote, read
back from the history end. The rights decision and administration contracts
read "Not ready" while the runtime cannot read its policy file. It
also states the transport's identity limits; the installed macOS runtime uses
XPC for protected calls, while standalone Unix sockets cannot prove the caller's
program and refuse those calls.

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
listing Get started, Apps, Requests and permissions, Models, Activity, Device,
Providers, Credentials and Settings, with the current page marked,
and a content pane where each page's own sections become grouped rows.
Explore (`/explore`) keeps its page outside the
navigation. Every page keeps its
route (`/`, `/rights`, `/inference`, `/work`, `/card` and the rest), and
`/apps`, `/questions`, `/models`, `/activity`, `/device` and `/get-started`
redirect to their pages, and `/logging` and `/identity` to Settings.

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

`scripts/panel_screenshots.py` renders Panel pages in one headless Edge or
Chrome at 1100x760, in light and dark, for each skin: Get started, Apps,
one app's page (`--app`), Models, Permissions, Activity, Device, Providers,
Credentials and Settings. It waits for each page's `data-ready` mark, runs
`static/panel_rules.js` on the rendered page, and fails the set on a
violation. The recorded set in `docs/results/panel/<page>-<platform>-<scheme>.png`
comes from `--panel-url` against `scripts/panel_review.ps1 -Examples`; without
`--panel-url` the tool builds and starts a Panel alone. Run `--help` first.

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
DPI-aware. The window opens `https://panel.invalid/<page>?k=<key>`; WebView2
raises `WebResourceRequested` for every request under that origin, and the
Panel answers it in process through the same routes the browser page uses
([`panel_resource.go`](panel_resource.go)). Nothing listens, and the name
cannot resolve, so a request that escaped interception would fail. Each
answer is buffered whole; one over 64 MiB is refused with a sentence naming
`openabstractions jobs result <id>`. A route that panics answers 500 and
logs one line; one that aborts (`http.ErrAbortHandler`) gets a 502 reading
"This request stopped before it finished." on Windows and a failed WebKit
task on macOS. A route that aborts after its answer passed the cap gets the
cap's sentence. The run
prints `service control panel: window`. `monitor -native` opens the same
window from a terminal; `monitor -native=false` serves the browser page on
loopback, the developer mode. Closing the window ends the process. Windows 11
ships the WebView2 runtime; where it is missing, `-native` shows a message
naming the Microsoft Edge WebView2 Runtime and opens neither a listener nor a
browser. The window is hosted with an in-house syscall and COM binding in
[`win`](win) — no cgo, no vendored library — that finds the Evergreen
installation through Windows registration and loads its architecture-matched
`EmbeddedBrowserWebView.dll`. The package ships no separate SDK loader. The
message loop and DPI awareness are supplied by the same native wrapper.

`monitor -background` (built as `Abstraction Panel.exe --background`, Windows
only) starts the account's native question presenter at sign-in. A hidden
window receives session-lock messages and runs the native dialog thread. A
session-local kernel object admits one presenter for the selected runtime;
repeated startup exits quietly. Its handle is released when the process ends.
`--tray` remains a compatibility alias for existing Startup shortcuts. It
polls `QuestionOperator.ListQuestions` every three seconds with a two-second
budget, remembers which pending question ids it has already announced, and
presents each new pending question as a centered Windows permission prompt
(a TaskDialog, [`tray_presenter.go`](tray_presenter.go)). The prompt names the
requesting application and its publisher as the runtime recorded them when it
admitted the question, read through `QuestionOperator.ReadAttributedQuestion` (asks
`ASK-A6`, `ASK-O7`): a program with a trusted signature by its signed
description (Windows PowerShell), shortened to 64 characters, any other by
its file name. The Questions page names the program and its publisher the
same way. The publisher
reads "not verified" when no trusted signature was recorded, which includes
every inference first-use question. A script host on the asks list adds
"<host> runs scripts; its publisher covers <host> and the script it runs is
unverified." (asks `ASK-A8`). It
states the action and resource in plain words and offers the answers the
question allows. Enter or Space selects the refusing answer (or Cancel when a
question has none), and the answer buttons accept a click only 500 ms after
the prompt appears. The exact program path, its full signed description and original file name,
the action, resource and question id sit behind Show details. The prompt appears only on the unlocked interactive
desktop; a session lock closes it, sends nothing, and presents the question
again after unlock. The presenter re-reads a shown question every poll and closes
the prompt when the question is answered, dismissed or expired (15 minutes
after it was asked, asks `ASK-A7`). Before sending a choice it re-reads the
question again and sends nothing when it changed (asks `ASK-O6`). The choice
goes to the runtime's asks operator over the shared IPC, which binds this
Panel's program identity, through `AnswerQuestionIfPending` (asks `ASK-O8`):
an answer that landed elsewhere first, a delegated one included, is
`conflict`, and no rule is written. A first-use answer writes its rights rule
through the rights operator, as the Questions page does. Successful completion
closes the prompt. Failed or uncertain completion shows a result dialog and
starts the existing Panel executable on its Questions page, carrying the
presenter's explicit runtime selection when one was supplied. The background
presenter uses native runtime IPC; the Panel window it opens binds no
listener. Runtime availability changes are logged once and polling
continues. A session-watcher startup failure ends the presenter with a visible
error in a desktop build. The per-user installer registers `--background` at
sign-in; a machine-scope installation leaves that to the person
(installer/README.md). Open Abstraction Panel or OpenAbstractions Setup from
Start whenever needed. Closing either window leaves the background presenter
running for Asks. Signing out ends its session lifetime.

### macOS

`Abstraction Panel.app` supplies native Asks dialogs and an on-demand
Panel window, built by [`monitor/macos/build.sh`](macos/build.sh):

```bash
monitor/macos/build.sh
```

`PanelLauncher.swift` is a small AppKit program, compiled with `swiftc`
alone, no Xcode project and no third-party code. It starts `panel` (this
package's own binary) as a child process in `-native-asks` mode. Its bounded
stdin/stdout channel carries presentations, dialog replies, session observations
and results. Go uses the same public operator-backed presenter as Windows:
it reads the runtime's attributed question, rejects stale or interrupted answers,
and records the selected answer and any permission rule. Swift renders the
requester, publisher, action, resource, effect and choices supplied by Go; it
makes no authorization decision. Answer buttons arm after 500 ms; Enter selects
the refusing option, or Cancel when none exists. Details disclose the exact
service-authored values.
Every question and its report appears on OA's own question panel
(`NativeQuestionPanel`, Asks ASK-O14). It is a
non-activating panel at the modal-panel window level on every Space and
beside full-screen windows, placed on the screen that holds the pointer in
its upper third. The app in front keeps keyboard focus until the person
clicks the panel; a click makes it key without activating the launcher, so
Esc and Return then work. Each step is logged to the unified log:
`log show --predicate 'subsystem == "org.openabstractions.panel" AND category == "questions"'`.
When a question appears the launcher logs where it landed from the window
server's on-screen list (found, layer, occluders: other apps' windows in
front at or below its level), the active Space, the screen and whether the
frontmost app kept focus. The list gives these without Screen Recording;
the launcher reads no window name. Application notifications (`notification@1`)
use the system notification center under the person's own settings.
`LSUIElement` in `Info.plist` keeps it out of the Dock. Opening the app from
Finder opens the Panel after its child announces readiness; reopening the
running app shows the same window. Native answering requires neither this
window nor an HTTP listener. The window loads `oa-panel://panel/<page>?k=<key>`:
its `WKURLSchemeHandler` sends each request to the Go child over the same
bounded channel as `panel-request` lines, and the child answers through the
shared routes in `panel-response` lines of at most 512 KiB of body each
([`native_panel_bridge.go`](native_panel_bridge.go)). A request the page
abandons is cancelled. Nothing listens, and the per-run key is created only
when the window is requested.
Closing the window keeps native Asks available. Explicit `-runtime-endpoint`,
`-runtime-program` and `-runtime-state-dir` arguments on the app launcher are
forwarded to Go without selecting a different runtime. An installer runs the
bundle's executable with `-register-application <file>` or
`-remove-application <name>`: the launcher forwards it, opens no window, and
the child asks that one confirmation on the question panel with the review the
Windows page shows ([`application_native.go`](application_native.go)). The
run exits 0 when the entry is as asked and non-zero after a cancel, a refusal
or a failure; a refusal or failure is also reported on the panel.
Child startup and exit failures are logged, with an alert when the person
has requested or opened the window. Reopening the app can restart a stopped
child.

Prompting requires the caller's public CoreGraphics session to have this UID,
be on the console and have completed login, plus an exact Boolean `false` for
the root IOKit `IOConsoleLocked` property. That property is a private key in
[Apple's published XNU source](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/iokit/IOKit/IOKitKeysPrivate.h#L36),
measured on macOS 26.6.2; it is not a documented public screen-lock guarantee.
Missing, malformed or failing evidence defers prompts. A present private
CoreGraphics screen-lock flag can veto prompting but its absence never proves
unlock. Session switches and screen sleep interrupt an open dialog; the gate
is refreshed while running, immediately before display and after selection.
Manual lock/unlock on the target release remains a qualification step.
The per-user installer registers it to run once at login through a
LaunchAgent, `installer/posix/macos/com.openabstractions.panel.plist`, the
analogue of the Windows Startup shortcut `PanelTrayLogonStart`: `RunAtLoad`
with `--background` and no `KeepAlive`. Login startup keeps the window closed.
LaunchAgent shutdown routes SIGTERM through AppKit and reaps the owned Go
child. A stopped launcher can be opened again from Finder or at the next login.

`installer/posix/payload.tsv` builds and stages the app bundle for the
release package; `build.sh` here is the standalone dev loop and the
screenshot tool's target, not the packager. Both require cgo for native XPC.
`python3 monitor/macos/test_launcher_asks.py` builds automatic native fixtures
in a temporary app and disposable XPC job, exercising a real public operator
answer, stale dismissal, close without answering and injected lock deferral.
The mixed-choice fixture admits ordinary requests through the runtime's
Rights-backed ask policy after proving denial before one exact fixture rule.
It checks that ordinary requests remain pending while a permission question
reaches the native presenter and records Refuse. Applications own ordinary
choice presentation; the on-demand Panel retains explicit operator actions.
The session fixture checks missing keys, exact types and account switches.
These fixtures never lock the desktop or wait for owner input; synthetic
session transitions do not qualify actual screen-lock detection.
Mac fixtures show nothing on the owner's screen and play no sound. Asks
fixtures (`PANEL_ASKS_FIXTURE`) make their panels transparent and
click-through and keep the Panel window ordered out
(`fixtureHidesPresentation`). Window and quit fixtures (`PANEL_WINDOW_FIXTURE`)
make the Panel window transparent and click-through, never activate the
launcher, and record the activation policy instead of applying it, since
`.regular` shows a Dock icon (`fixtureHidesWindow`); they check the policy the
launcher sets. The signal fixture's own alert is transparent. Both switches
are absent from the shipped launcher, which a symbol check proves.
`OA_OWNER_PRESENT=1`, for an owner-present session the owner was told about,
shows the window and applies the policy, and also runs the edit-keys fixture,
which needs the launcher active with a key window and uses the clipboard.
`python3 monitor/macos/test_launcher_question_panel.py` checks the panel's
placement, window traits, button order, Details re-layout and Esc, and that
the release configuration compiles no fixture switch. With
`OA_OWNER_PRESENT=1`, during an owner-present test the owner was told about,
it also shows one panel for about a second to read the window-list probe: on
screen at its level, no app window over it, on the active Space, focus kept
by the app in front. That check skips without a console GUI session.

### Linux

`go run .` or a plain `go build` serves the browser page on loopback, the
developer mode; Linux has no native window or tray. Open the printed loopback
URL locally.

## Who is allowed to drive it

The native windows bind no listener: only the window's own page reaches the
Panel's routes. The developer mode's loopback socket carries no caller
identity — any process here can open one — so its powers are bounded rather
than assumed:

- **A key, minted per run and never written to disk**, required in a header on
  every action. A custom header cannot be set cross-origin without a preflight
  this server does not answer, so a web page you happen to be visiting cannot
  reach in and cancel your downloads. It is **not** proof of who is calling.
- **Loopback Host and same Origin** are required on every request.
- **Saved operations stay with their binding.** An action naming another
  endpoint, owner or history epoch is refused rather than resubmitted.

The unfixed part of the developer mode: any process on this machine that can
read the URL can drive the Panel.

## Limits

- **The developer mode opens the browser automatically on Windows.** On other
  platforms, open the printed loopback URL locally.
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
