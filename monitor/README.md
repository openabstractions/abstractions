# OpenAbstractions control panel

See what your OA runtime is doing and decide what each program may do. The
panel lets you inspect accepted work, answer service questions, manage exact
permissions, follow logs and edit revisioned OA settings. Every action calls
the owning service through the same resolved clients an application uses.

Start with **Check runtime** to see availability, **Every program's work** to
inspect account-wide operations, or **Questions** to review a pending first-use
request. Credentials, inference hosts and provider registrations have their own
pages. Each service enforces the panel executable's permissions.

The browser UI runs on this computer's loopback address. Its per-run URL is a
bearer credential; keep it private. Native Windows packaging provides a desktop
window that opens the full browser controls.

## Available controls

**Runtime** — Check runtime inspects supervision and each runtime capability.
The check shares the SDK's authorized resolution results and a three-second
budget. Partial failures leave unanswered services marked Not checked. It runs on
request, performs no activation and shows when the snapshot was taken.

**Accepted work** — the inventory of work accepted for this panel program, page
by page, with observe, cancel and a download of completed bytes. Submitting an
HTTP download saves a recovery record first; an uncertain reply is reconciled
with the same key and owner.

**Every program's work** — the inventory of every program in this account
through `abstraction.job/operator@1`, with Cancel by operation id. The runtime
lists it under the rule `abstraction.job/inventory.read` and cancels under
`abstraction.job/acceptance.cancel`; the panel holds both by installation.

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

**User configuration** — read the user settings and replace them at their
revision. Follow effective settings shows live service snapshots and their provenance
without replacing an editing draft. A conflicting edit preserves the draft; save
it before reading and reconciling the current revision. Stop following cancels the
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

With no runtime the panel reports the absence and opens no provider files.

```bash
cd monitor && go run .
```

To use a runtime other than the installed one, such as an isolated
`openabstractions serve runtime --isolated <name> --state-dir <dir>`, name its
resolver endpoint and the executable it must run as under your account:

```bash
go run . -open=false -runtime-endpoint '\\.\pipe\openabstractions-user-<SID>-<name>-runtime' -runtime-program 'C:\path\openabstractions.exe'
```

It listens on `127.0.0.1:8734` and prints a URL carrying this run's key.
Windows opens the local browser unless `-open=false` is supplied.

## A window on the desktop

```bash
cd monitor
go build -ldflags="-H windowsgui" -o "Abstraction Panel.exe" .
cp panel.manifest "Abstraction Panel.exe.manifest"
```

Double-click it and a window lists the panel's accepted work, with a button
that opens the full controls in the browser. `monitor -native` opens the same
window from a terminal; `monitor` on its own serves the page, which is how a
browser UI is served on the local machine. The window is drawn with [`win`](win).

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

- **Windows opens the browser automatically.** On other platforms, open the
  printed loopback URL locally. Protected service calls require native Program
  proof; the current macOS provider reports its documented proof limitation.
- **The window is Windows only.** Elsewhere `-native` refuses and says so.
- **The window's styling comes from `panel.manifest` beside the binary**, as
  `<name>.exe.manifest`. Windows loads a manifest of that name for a binary that
  carries none of its own; embedding one needs a resource compiler that is not
  in the Go toolchain. Without the file the controls draw in the pre-2001 style
  and everything still works.
- **No dark mode.** Win32 has no supported way to ask for one.
- **Pause, resume, destination collection, host refusals and network tiers are
  absent.** The service contracts do not offer them to the panel yet.
