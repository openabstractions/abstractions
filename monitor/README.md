# monitor — the control panel

The panel shows the work, questions, rights and user configuration that the
installed runtime owns, through resolved service clients.

**Runtime** — Check runtime inspects supervision and each runtime capability.
The check shares the SDK's authorized resolution results and a three-second
budget. Partial failures leave unanswered services marked Not checked. It runs on
request, performs no activation and shows when the snapshot was taken.

**Accepted work** — the inventory of work accepted for this panel program, page
by page, with observe, cancel and a download of completed bytes. Submitting an
HTTP download saves a recovery record first; an uncertain reply is reconciled
with the same key and owner.

**Questions** and **Rights** — answer or retire pending questions, and set or
revoke exact rules at the listed policy revision. The owning service decides
whether this panel may act.

**User configuration** — read the user settings and replace them at their
revision; a conflicting edit requires a reread.

With no runtime the panel reports the absence and opens no provider files.

```bash
cd monitor && go run .
```

It listens on `127.0.0.1:8734`, prints a URL carrying this run's key, and opens
it. Nothing it shows leaves the machine.

## A window on the desktop

```bash
cd monitor
go build -ldflags="-H windowsgui" -o "Abstraction Panel.exe" .
cp panel.manifest "Abstraction Panel.exe.manifest"
```

Double-click it and a window lists the panel's accepted work, with a button
that opens the full controls in the browser. `monitor -native` opens the same
window from a terminal; `monitor` on its own serves the page, which is how a
headless machine is reached. The window is drawn with [`win`](win).

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

## What may break

- **Windows only for opening the browser.** Everything else is portable; the
  page is a URL and you can paste it anywhere.
- **The window is Windows only.** Elsewhere `-native` refuses and says so.
- **The window's styling comes from `panel.manifest` beside the binary**, as
  `<name>.exe.manifest`. Windows loads a manifest of that name for a binary that
  carries none of its own; embedding one needs a resource compiler that is not
  in the Go toolchain. Without the file the controls draw in the pre-2001 style
  and everything still works.
- **No dark mode.** Win32 has no supported way to ask for one.
- **Pause, resume, destination collection, host refusals and network tiers are
  absent.** The service contracts do not offer them to the panel yet.
