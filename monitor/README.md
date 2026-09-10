# monitor — the control panel

Three screens today, and room for the ones coming.

**Delegation** — every tier this machine could hand work to, which one is
serving, and why the others are not. Switch one off with a reason and the next
job goes elsewhere; a supervisor that has been running for days obeys it without
restarting. Find a NAS on the network instead of typing a UNC path, prove the
share is writable, and point this machine at it. Then a quick test: fetch
something small down the real path and see which tier answered, how long it took
and where it landed.

**Downloads** — every download on the machine, what it is doing, who is doing
it, which tier served it, and buttons that pause, resume, collect and cancel.

**May reach** — the hosts this machine's downloads have named, and the switch
that refuses one with a reason an application shows.

It is the first application built on
[`abstraction`](https://github.com/openabstractions/abstraction-facade) rather
than on a store and a directory. Everything it shows about a job comes from
`Discover()`; everything it changes about the machine goes through
[`config`](https://github.com/openabstractions/abstraction-config), which is the
one place a machine keeps its answers.

```bash
cd monitor && go run .
```

It listens on `127.0.0.1:8734`, prints a URL carrying this run's key, and opens
it. Nothing it shows leaves the machine.

## A window, not a page

Delegation also draws itself on the desktop, in the platform's own window class
and controls — no browser, no socket, no key:

```bash
cd monitor
go build -ldflags="-H windowsgui" -o "Abstraction Panel.exe" .
cp panel.manifest "Abstraction Panel.exe.manifest"
```

Double-click it and a window opens in the taskbar with the tiers, the network
store, find-check-adopt and the quick test in it. `monitor -native` opens the
same window from a terminal; `monitor` on its own still serves the page, which
is how a headless machine and the NAS are reached — neither has a desktop.

Both front ends call `delegation()` and the same methods `/act` does. The window
holds no state of its own, and adding the download and may-reach screens is more
of [`win`](win), not a second copy of anything.

## Who is allowed to drive it

A loopback socket carries no caller identity — any process here can open one —
so the powers are bounded rather than assumed:

- **A key, minted per run and never written to disk**, required in a header on
  every action. A custom header cannot be set cross-origin without a preflight
  this server does not answer, so a web page you happen to be visiting cannot
  reach in and cancel your downloads. It is **not** proof of who is calling.
- **The store can only be pointed at an address this machine found**, with the
  share and folder checked segment by segment and the path proven writable
  first. The address comes from the source of the datagram; a device's name and
  its `SERVER:` string are displayed and never joined to a path.
- **Switching a tier off** only reduces where bytes go. It cannot redirect them.

The unfixed part: any process on this machine that can read the URL can drive
the window.

## What it draws, and what each thing means

Buttons are drawn from what the handle admits to. `Pause` and `Resume` appear
only where `job.Pausable` does; `Collect` only where a job is `transferred`,
which is the second half of the two-phase completion nothing else in this
project has a button for.

The states worth having a window for are the awkward ones:

| it says | it means |
|---|---|
| `stopped` · *nobody — X stopped without saying so* | the lease lapsed with the record still running. Something was killed |
| `waiting to be collected` | the bytes are here and proven and nobody has taken delivery. `Collect` is that call |
| `paused` | somebody asked it to stop, and the owner honoured it. `Resume` sets the intent back and offers to do the work |
| `failed` | the last attempt's error, off the record, without finding the log of a process that no longer exists |

`Resume` is two calls — clear the intent, then resubmit — because a record
nobody is watching stays still no matter what it says it wants.

## What may break

- **Windows only for opening the browser.** Everything else is portable; the
  page is a URL and you can paste it anywhere.
- **The window is Windows only, and it is one screen.** Downloads and may-reach
  are still the page. Elsewhere `-native` refuses and says so.
- **The window's styling comes from `panel.manifest` beside the binary**, as
  `<name>.exe.manifest`. Windows loads a manifest of that name for a binary that
  carries none of its own; embedding one needs a resource compiler that is not
  in the Go toolchain. Without the file the controls draw in the pre-2001 style
  and everything still works.
- **System DPI, read once from the window at startup.** Dragging it to a monitor
  at a different scale leaves it the size it was. Measured at 200% on a laptop
  panel: 780x505 units became 1586x1081 pixels, laid out crisply.
- **No dark mode.** Win32 has no supported way to ask for one.
- **It is a downloader while it is open.** With no supervisor running, this
  process is the one moving the bytes, so closing the window stops the transfer
  — and the window is then the thing that shows you it stopped.
- **Progress is advisory.** The bar is `progress`, which the contract says
  decides nothing; the number that survives a crash is the checkpoint.
- **It cannot tell you a source it was not told.** A record carries the sink it
  was given; where the bytes came from is the submitter's to record.
- **Delegation needs a supervisor.** A tier is only offered work by `jobd`; with
  nothing watching the store, this process downloads in itself whatever the panel
  says. The panel shows whether one is alive.
- **Finding a NAS is Windows and IPv4 today.** The two multicast questions are
  portable; listing what a host serves uses `NetShareEnum`, and elsewhere you
  name the share yourself. A hidden share is never listed anywhere, so the list
  is a suggestion and the field takes anything.
- **A delegated job can look stuck for a minute or two.** The far side writes its
  record over SMB and this machine keeps reading the copy it has: measured at
  100 s between a NAS finishing and this window noticing.
