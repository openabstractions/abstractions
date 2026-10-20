# The permission journey

Application A asks to read the saved dummy secret **alpha**. Raw Read gives the
value to A; Use lets OA apply it to a download from a local test receiver.
Application B tries the same name under its own identity. The Panel lets the
person inspect the rule, block access and remove it. This playground connects
the permission explanation to the actual effect and makes the boundary between
two applications visible. All secret values are harmless fixtures.

**Development checkpoint, October 6, 2026.** The prepared Windows pack has
native A/B windows with Read/Use buttons and in-window results. A real Read
opened the permission dialog; after the owner's Allow, Read succeeded. Public
API checks also prove app/name/operation isolation, One/All Block and removal,
and the six-operation full-control bundle. The live pack uses runtime
`db6f8d696`, apps/lifecycle `1cca22b01` and Panel `6446aa4f1`; its
`current-inputs.json` records full source and binary hashes. No compiler or
installation is needed to run that pack.

Permissions has compact rows and full-width details, including the recorded
account and writing app. Apps observes permission changes and now includes
native provider and permitted application registrations through Facade IPC,
including programs without saved grants. Selected checks and review passed.
Compact/expanded Permissions views were captured at `301bce9c9`. A temporary
public provider registration was added and withdrawn after the Apps update;
computer use stopped before its capture. App A's saved grant is unchanged.

Appearance acceptance, including title-bar chrome, the full human walkthrough,
macOS and release qualification remain open. Historical failed/partial checks
and their focused corrections are retained; no fresh full-gate or whole-state
secret-absence verdict is claimed. The [current status](../../PROGRESS.md#where-this-stands)
and [evidence index](../../research/evidence-location.md) locate the results,
captures, cleanup receipts and exact compositions.
[Released 0.3.0](https://github.com/openabstractions/redist/releases/tag/v0.3.0)
has its own package contents and platform evidence.

## Who owns each decision

| Owner | Responsibility |
|---|---|
| [Identity](https://github.com/openabstractions/abstraction-identity/blob/main/CONTRACT.md) | Bind the native connection to its account and program, and report the available identity evidence. |
| [Rights](https://github.com/openabstractions/abstraction-rights/blob/main/CONTRACT.md) | Decide the requested action and resource for that subject; retain rules, revisions, expiry and provenance. |
| [Asks](https://github.com/openabstractions/abstraction-asks/blob/main/CONTRACT.md) | Retain the proposed decision and answer, enforce who may answer, and coordinate the permission effect with Rights. |
| [Credentials](https://github.com/openabstractions/abstraction-credentials/blob/main/CONTRACT.md) | Validate the named credential, its consumer and target restrictions, recheck access, and perform the authorized operation. |
| OA native presenter and Panel | Explain the complete permission effect and show pending requests, saved rules and current results. |
| Operating system | Authorize protected platform operations through its genuine administrator or privacy-consent path. |

The [facade](https://github.com/openabstractions/abstraction-facade) resolves a
supported service and typed client. A successful resolution still leaves the
receiving service responsible for each operation's authorization. Publishing a
permission description supplies explanations; an authorized rule edit supplies
access. The resource service checks again when the application acts.

Ordinary choices such as Replace, Keep or selecting a model belong inside the
application. An app can use Asks for shared pending and answer recovery while
presenting those choices itself. An ordinary answer selects app behavior and
has no permission effect.

## Operation, resource and breadth

The accepted common schema describes a stable action, an immutable versioned
resource profile, its enforcement boundary and the complete human explanation.
The capability owning the resource defines its fields and lifecycle. Rights
owns matching and precedence. A display label or application-supplied purpose
cannot change the action's authority.

The Panel's administrative access is configured by the connected runtime.
After an authorized permission listing, the Panel explains that role. Its saved
application permissions appear in Apps with their existing rule details;
removing one of those rules leaves its administrative access in place.
The current API supplies no editable operator role or designation provenance.

**Use** lets a designated OA service apply a saved credential to a permitted
target. The application receives the operation's result. The separate
**Read** operation discloses the secret value to the authorized application;
its permission must say that explicitly. Credential metadata, raw value
disclosure and permission administration have separate effects.

The served interfaces implement these scope and bundle rules:

- **One resource** selects one canonical target for a defined operation.
- **All saved application secrets** covers present and future members of
  that collection in this account. Runtime-managed local keys are excluded.
  The prompt shows a broad-access warning and names the collection.
- **Full control, version 1** fixes six operations: Use, raw Read,
  create, rotate, revoke and named metadata read. The compiled catalogue supplies
  that fixed list. It excludes account-wide List/Audit,
  permission administration and future operations. The person sees the exact
  list before granting it; later operations require a new grant.
- **Any matching explicit Block wins.** An exact block also applies when an
  All permit exists. Removing one rule can leave another matching permit;
  the Panel must show the effective result after the edit.

Breadth and operation count are independent. All for Use covers more resources
for that operation. Full control fixes a set of operations. OS administrator
authorization follows the privilege required by the operation. OA answerer
tiers separately govern who may answer.

The same structure describes reading one application's logs or the account's
present and future OA application logs through Logging's scoped reader.
A protected OS log additionally needs its source owner's actual platform
authorization.

## The journey to demonstrate

The playground uses dummy secrets, a local receiver and two physical application
binaries, A and B. Its setup grants only the prerequisite download
submission permission. Each app starts without credential Use or Read access.
The receiver records effect counts and dummy labels while keeping credential
values out of output and evidence. Raw Read privately verifies the returned
dummy value and clears A's received copy.

The prepared Windows folder contains `Start.cmd`, `App-A.cmd`, `App-B.cmd`,
`Stop.cmd` and `Reset.cmd`, generated by [Build.ps1](Build.ps1) around the
tracked [Lifecycle.ps1](Lifecycle.ps1). The following uses that prepared folder;
preparing its binaries is a separate build step.

1. **Start the isolated playground.** Open `Start.cmd`. It opens the native
   Panel and separate A/B app windows for this runtime. It stores only the two dummy
   names and grants the narrow download-job prerequisites.
2. **Read from A.** Select **alpha** and click **Read secret** in A. Missing access creates an
   OA permission request identifying A, its account, the raw Read operation,
   name, disclosure effect and duration. Choose Allow in the native request,
   then click **Read secret** again. The in-window result says A verified and
   cleared its copy; it displays no secret bytes. **Use secret** exercises the separate
   credential-bearing download and verifies its receiver artifact.
3. **Try from B.** B has its own native identity. A's exact grant gives B no
   access. Select **alpha** and click **Read secret** in B; its attempt requires its own decision.
   Selecting **beta** in A also tests a separately named secret. Use permission does
   not grant raw Read.
4. **Change policy in the Panel.** Inspect the saved rule, Block it, and make a
   fresh attempt from the app. Remove a rule and inspect effective access;
   ask again when access is missing. The open view observes policy changes,
   while an unsaved draft retains its revision for explicit review.
5. **Compare scopes.** Compare One with All and inspect the present-and-future
   warning. An exact Block wins over an All permit for that name. Preview the
   fixed Full control operation list before granting that bundle; its visible
   management chapter still needs separate acceptance.
6. **Reopen or clean up.** `App-A.cmd` and `App-B.cmd` reopen their windows.
   `Stop.cmd` stops matching owned PIDs and retains state and dummy registrations.
   `Reset.cmd` requires this runtime alive, publicly revokes both dummy values,
   then stops owned processes and removes only this pack's run folder. After
   Stop, use Start to resume the run before Reset.

Fresh Use after completion or definite refusal gets a new operation identity.
An uncertain Use retains its identity: repeat the same Use to reconcile it.
Replaying completed work retains its original result. The setup grant is a
test prerequisite; routine-download product defaults remain open research.

Each click starts one operation from that app's own process. The action buttons
and secret selector are disabled while it runs. Permission decisions stay in
the native OA dialog; the app reports its result without a second popup.
Retry the same action explicitly after deciding. Closing an app cancels its
current wait and leaves the isolated runtime running. Redirected command-line
operations remain available for setup and automated verification.

Block stores an explicit denial. Removing a rule withdraws that rule. Undo of
an answer targets only its still-current permission effect. Revoking a stored
credential removes the secret itself. The walkthrough keeps these actions
visible as separate operations.

## Native appearance and evidence

OA identifies itself and uses the native treatment for the actual OS version:
Windows 10, Windows 11 and AppKit on macOS. The permission explains the program,
recorded publisher, exact action and resource, effect and duration. Expandable
details retain the exact request. Publisher verification reports identity
evidence; the requested operation and breadth describe the access being sought.
Microsoft's [current UAC guidance](https://learn.microsoft.com/en-us/windows/security/application-security/application-control/user-account-control/how-it-works)
describes gray for Windows/verified publishers and yellow for unsigned or
untrusted apps. Its [theme guidance](https://learn.microsoft.com/en-us/windows/apps/desktop/modernize/ui/apply-windows-themes)
supports system light/dark appearance. The owner's revised Windows direction
uses gray/blue reference headers and orange for unverified apps, compact
identity/action text and visible raw Read/breadth consequences. Windows 11's
offered Allow/Refuse pair uses equal rectangular Allow/Don't allow buttons;
Don't allow records Refuse and is the filled-blue safe default. X/Escape
dismisses. This revision is integrated and shown on Windows 11; final appearance
acceptance remains open. Block is a separate policy edit.
Text and icons explain broad access and actual OS privilege independently of
publisher color. Protected access still needs its genuine OS authorization;
that form remains planned.

The earlier `ccc0fefde` Windows 11 pack automatically displayed the typed native Read
request after missing access denied A's first attempt. Its capture shows App A,
One named secret,
the raw-value/export disclosure and Allow/Refuse/Cancel, before any answer.
A later `read alpha` succeeded after Allow, verifying and clearing the dummy
value in A. No automated answer or policy edit was used in that manual run;
the UI click itself was not captured. The owner requested appearance revisions;
visual acceptance remains pending.
On macOS the runtime admits the Ask after A's refused raw Read.
`questions pending` excludes permission requests by design;
`questions permissions` lists them. The dialog shows only with the screen
unlocked and the display awake. On 2026-10-06 the run went refused Read,
admitted Ask, dialog on unlock, Allow at 04:46:50Z and a successful Read; B
stayed refused. No screenshot exists, because `screencapture` needs Screen
Recording permission. The owner's visual acceptance remains open.

The final walkthrough needs real OS captures tied to its source and binary
hashes, the exact request and applied rule, A/B identity separation, actual
receiver effects, and fresh attempts after Block and rule removal. Keyboard,
accessibility, DPI/high-contrast and installed login/upgrade checks retain their
own acceptance. The 0.4.0 release qualification remains separate and open.

For protected system access, OA consent precedes the genuine OS authorization
required by the named operation. Windows owns
[UAC consent and its secure desktop](https://learn.microsoft.com/en-us/windows/security/application-security/application-control/user-account-control/how-it-works).
The protected helper and capability-specific privacy forms remain planned.

## Research behind the design

[Cedar schemas](https://docs.cedarpolicy.com/schema/human-readable-schema.html)
declare actions with applicable principal, resource and context types.
[OAuth Rich Authorization Requests, RFC 9396](https://www.rfc-editor.org/rfc/rfc9396.html)
uses typed authorization details whose interpretation belongs to the API owner.
OA's design inference is to keep capability-owned operation/resource semantics
in its existing IDL, with common structured definitions and a single Rights
matcher. The finite bundle and explicit breadth warning preserve the authority
the person actually saw. These sources inform OA's model; the source checkpoint
uses OA's own contracts and native transport.
