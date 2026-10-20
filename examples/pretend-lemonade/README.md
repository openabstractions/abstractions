# The five-abstraction journey

This Windows development folder combines `pretend-lemonade`,
`logging-quickstart`, the runtime and the existing Control Panel. The download
app exercises identity, durable jobs, Asks, rights and attributed logging on
the 0.4.0 defaults. It transfers a deterministic 1 MiB file from a loopback
source. It is a small integration example; it does not start the Lemonade
engine. A Python companion, run from the checkout from its own interpreter
copy, adds a second client language.

The runtime starts with five account-wide defaults, each a switch under
**Permissions → Defaults for all apps**: Add itself to Apps, Ask you
questions, Say when it is running, Send you notifications and Download in the
background. A rule the person saves for one app, a Block included, wins over
the default. A shared interpreter such as `python.exe` receives no default.
Anything else, such as reading a saved secret's value or the log history, needs
the person's yes for each app.

## Try the prepared folder

1. **Start.** Double-click **Start.cmd**. It opens the Control Panel and starts
   its native question presenter. All four background processes use this
   folder's runtime; their PIDs and executable paths are in `run/pids.txt`.
   Policy, questions, credentials, logs and work stay under `run/state`; the
   installed runtime's state is separate. The runtime trusts this folder's
   Panel as its operator. In **Settings → Identity**, check the runtime
   executable and endpoint, the current account and the program observation.
2. **The app adds itself.** Double-click **Register.cmd**. The app registers
   itself and prints `registered itself as Pretend Lemonade`. The native
   question reads "…pretend-lemonade.exe added itself to OpenAbstractions as
   Pretend Lemonade. Keep it?". Choose **Keep**. Apps lists Pretend Lemonade;
   before Keep it reads "Pretend Lemonade (added itself)". **Remove** deletes
   the entry and blocks the program from adding itself again.
3. **Download under the default.** Double-click **Download.cmd**. The job is
   accepted with no question. The app prints an operation ID and delivers
   `run/files/fixture.bin`. In **Activity**, find that ID, its completed state,
   result size and request key. **Source-stats.cmd** reports the bytes the
   source sent. Running Download.cmd again reuses the same request and
   operation and transfers nothing.
4. **Block the app while it downloads.** Double-click **Slow-download.cmd**;
   the slow source takes about thirty seconds. While it runs, open
   **Permissions → Saved permissions**, choose App **Pretend Lemonade**,
   Capability **jobs submit** (`abstraction.job/acceptance.submit`), Exact
   resource `job:download`, Choice **Block**, then **Save rule**. The running
   download ends within a second: the app prints `download failed:
   rights:denied`, and Activity shows the operation failed with that reason.
   **New-download.cmd** is now refused with `forbidden`, and no question
   appears: the Block wins over the default.
5. **Unblock.** Remove that rule with **Remove rule**. Run Slow-download.cmd
   again. The app prints `attempt 0 failed: rights:denied; trying again as
   attempt 1`, and the download completes under the default. A request that
   a rights refusal ended runs as its next attempt when the app runs again,
   since the person can lift that refusal; a request that failed for another
   reason, completed or was cancelled is reused.
6. **A permission no default covers.** In **Credentials**, add a secret named
   `mirror-token` with any harmless value, target `127.0.0.1`, for downloads.
   Double-click **Read-secret.cmd**. The first attempt prints `read
   mirror-token: forbidden`, and the native presenter shows the OA permission
   request: Pretend Lemonade, the raw Read of `mirror-token`, its disclosure
   effect and duration. Choose **Allow** and run Read-secret.cmd again. It
   prints `read mirror-token: 19 bytes; this app cleared its copy`; the app
   never prints the value. **Permissions → Questions waiting** lists
   first-use questions and app choices; this request appears only in the
   native presenter. The same Allow can be saved in Saved permissions: Capability
   **read saved secret value**, Target scope **One saved secret name**, Saved
   secret name `mirror-token`, Choice Allow.
7. **The app's own choice.** Double-click **Output-choice.cmd**. The file
   from step 3 exists, so the app asks through Asks and presents its full
   destination with **Replace**, **Keep** or **Cancel** in its terminal. Type
   the word or **r**, **k** or **c**, then press Enter. The Ask you questions
   default admits the question. The choice grants no permission, and the
   download stays completed for every option. **Permissions → Questions
   waiting** lists the same choice; an answer there is recorded too, and the
   app applies whichever answer the service recorded first.
8. **A notification that acts after the app exits.** Double-click
   **Notify-when-done.cmd**. The app starts a slow download, posts a
   notification under the Send you notifications default, prints `scheduled`
   and exits. When the download ends, Windows shows "Download finished" from
   Pretend Lemonade with **Copy now** and **Show in Activity**. Click Copy now
   while no app runs. Then double-click **Notification.cmd**: it reads the
   recorded choice, copies `slow.bin` into `run/files` and acknowledges the
   choice. Copy now is a file copy; the clipboard keeps its contents. Show in
   Activity opens the Panel at the download's row. **Activity →
   Notifications** counts each app's posts beside a switch that turns them
   off; right-clicking the notification offers **Turn off notifications from
   Pretend Lemonade** too, and the next Notify-when-done.cmd reports
   `suppressed`.
9. **Close and reconnect.** Double-click **Reconnect-download.cmd**. After it
   prints an operation ID, close its console. The runtime keeps the download
   in Activity. Run Reconnect-download.cmd again: it prints the same operation
   ID and follows it.
10. **Cancel.** While Reconnect-download.cmd follows the download, choose
    **Cancel** on its row in Activity. The app prints `download cancelled`.
11. **Restart the runtime.** Double-click **Restart-download.cmd** and close
    its console once it prints an operation ID. Double-click
    **Restart-runtime.cmd**: it stops this folder's runtime and starts it again
    on the same state and endpoint. An app started while the runtime is down
    reports `no download service on this machine`. Run Restart-download.cmd
    again: the same operation resumes and completes. Download.cmd still reuses
    its completed operation, and Logging reads the retained records again.
12. **Logs and who may read them.** Open **Settings → Logging** and filter by
    `pretend-lemonade.exe`. The app's events show each request, refusal,
    acceptance, delivery, the Block's `rights:denied` and the cancellation,
    with request key and operation ID. Compare the application's own claim
    with the receiver's verified executable and account stamp.
    **Logging.cmd** runs `logging-quickstart`: it writes its event, and its
    history read reports `history reading not permitted`. In Saved
    permissions choose App **Other…**, paste the path Logging.cmd printed,
    Capability **logging history**, Exact resource
    `abstraction.logging/history`, Choice Allow, and **Save rule**. Logging.cmd
    then reads its event back. This permission has no first-use question.
13. **Turn a default off.** In **Permissions → Defaults for all apps**, turn
    off **Download in the background**. New-download.cmd is refused, and the
    native presenter asks "Pretend Lemonade wants to download in the
    background." **Allow** saves a rule for this app alone; run
    New-download.cmd again and it downloads. Turn the switch on again.
14. **Stop.** Double-click **Stop.cmd**. It stops only the presenter, Panel,
    runtime and source PIDs recorded for this folder after checking their
    executable paths. Close any example app consoles. Logs, results and
    policy remain available for inspection. To repeat the journey from the
    start, use a new prepared folder or remove `run` after every owned
    process has stopped.

## Run the journey automatically

[journey/journey.py](journey/journey.py) takes the same steps through the
Panel routes the Panel page calls and records every outcome in
`run/journey.json`: commands, operation IDs, question and rule records,
source byte counts, digests and log messages. It needs a folder started
without the presenter, and it refuses one whose `pids.txt` records a
presenter, so nothing appears on screen. Where only the native presenter
can answer, the controller takes the Panel's equivalent and names the human
step under `human` in the evidence:

- step 2's Keep, step 13's Allow and the Python companion's Allow are
  answered through Questions waiting;
- step 6's Allow is saved as the Saved permissions rule above;
- step 8's Copy now click has no Panel route, so the controller checks that
  the notification is held until the download ends and that the app reads
  `posted` after it exited.

From the repository root in PowerShell:

```powershell
scripts/panel_review.ps1 -Dir .forks/journey-pack -Examples -PrepareOnly
scripts/panel_review.ps1 -Name journey-check -Dir .forks/journey-run -PrebuiltDir .forks/journey-pack -Examples
py -3.12 examples/pretend-lemonade/journey/journey.py --run .forks/journey-run
scripts/panel_review.ps1 -Dir .forks/journey-run -Stop
```

Add `--python-path`, `--ipc-prefix` and `--python` to run the Python
companion as its own step. [python/companion.py](python/companion.py) writes
two events through Python's logging adapter, submits the same kind of
download and tries to read the log history.

A Python app's identity is the interpreter file its process runs, so the app
runs from its own interpreter copy inside its own folder
([python-identity findings](../../research/python-identity/FINDINGS.md)):
on Windows, a venv whose `Scripts` holds a copy of `python.exe` with
`python3X.dll`, `python3.dll` and the `vcruntime140` DLLs beside it. Its
records, questions and rules then name that copy, and a grant covers that
folder's copy alone. The copy keeps the Python Software Foundation's signature
and its signed original file name `python.exe`, so OA still treats it as a
script host: it receives no default, its first download is asked at first
use with the script-host sentence, and Allow writes a rule for the copy. The
controller then runs the companion once from the shared `python.exe`: its
download is refused, and the runtime's question about the shared interpreter
carries the same sentence; a rule there would cover every script that
interpreter runs. Both runs' history reads are refused.

The companion needs the OA Python packages on `PYTHONPATH` and the native
`abstraction_ipc` library, built as the
[facade's Python README](https://github.com/openabstractions/abstraction-facade/blob/main/py/README.md)
describes:

```powershell
py -3.12 -m venv .forks/companion/venv
$base = py -3.12 -c "import sys, os; print(os.path.dirname(sys.executable))"
foreach ($f in 'python.exe', 'python312.dll', 'python3.dll', 'vcruntime140.dll', 'vcruntime140_1.dll') { Copy-Item "$base\$f" .forks/companion/venv/Scripts -Force }
$f = "$PWD\openabstractions-flat"
$pp = (@('identity','facade','logging','job','download') | ForEach-Object { "$f\abstraction-$_\py" }) -join ';'
py -3.12 examples/pretend-lemonade/journey/journey.py --run .forks/journey-run --python-path $pp --ipc-prefix <ipc prefix> --python "$PWD\.forks\companion\venv\Scripts\python.exe"
```

## Prepare or run from the checkout

```powershell
scripts/panel_review.ps1 -Dir .forks/five-example-pack -Examples -PrepareOnly
```

This builds the current development sources and writes the compiler-free
folder. Copy the entire folder before launching it; its executable paths form
the test policy's program identities. An existing current build folder can be
copied with `-PrebuiltDir PATH`. The prepared Start.cmd uses those binaries and
requires PowerShell; it does not invoke a compiler or install anything.
`panel_review.ps1 -Dir DIR -Runtime stop|start|restart` stops or starts only
that folder's runtime with its recorded arguments.

The app can also download a real HTTP URL with known size and SHA-256 digest:
`pretend-lemonade URL SIZE sha256:DIGEST`. For an isolated runtime, set
`ABSTRACTION_RUNTIME_ENDPOINT` and pass `--runtime-program` with its absolute
executable path. `--out` selects the application's copied-result directory.
The same URL, size and digest retain the existing request identity.
`--register` adds the app to Apps and `--read-secret NAME` reads one saved
secret; both take `--runtime-program` the same way.

## Optional existing-output choice

Add `--ask-before-replace` to ask through `abstraction.asks/application@2` when
the final copied file exists. The app prints the full destination and offers
Replace, Keep and Cancel in its terminal. Type the word or r/k/c, then press
Enter. Empty or unrecognized words prompt again. EOF, closed input or
cancellation selects nothing and leaves the file intact; an unanswered request
can remain pending until its deadline. The app uses its own scoped answer
route. It needs no operator grant to answer its own request.
The Ask you questions default permits this program to ask
(`abstraction.asks/question.ask` on `account`, **show app requests** under
Saved permissions). A Block on that permission for this app, or the default
turned off, refuses the question, and the app then leaves the file intact.
Asking grants the app no authority to answer another application's questions
or administer policy.
Its title names the full filename and adds its parent path or parent folder
when it fits the 80-character limit. The console prints the full destination
beside the pending question ID. Filename control characters are displayed as
escapes. A filename that cannot fit in full reports an error and retains the
file. Parent context may be omitted for long names; the private destination
used for the copy always remains the exact full path.
Dismissal also keeps the file. An unanswered choice expires after 90 seconds,
capped by the invocation's ten-minute deadline, and leaves the file intact.
An answer recorded in time remains usable within a five-minute replay window.
If the choice service is absent or returns gone, expired or unknown, the app
reports it and leaves the existing output intact.

For an external answer, add `--wait-output-choice` together with
`--ask-before-replace`. This explicit mode reads no terminal input and observes
the existing Panel operator's answer. It can be used by noninteractive fixtures.
OA does not automatically draw ordinary application choices. The app's normal
terminal mode also observes concurrent operator answers; the recorded winner
controls the copy even when it differs from the word entered locally. An
enrolled assistant answers only the choices an app shares with it. This app
shares its output choice when the runtime supports `ShareChoice`, in both
terminal and external-answer modes. An older runtime that reports
`unknown_method` receives the same request as an unshared choice; the app reports
that fallback and waits for the person.

The app creates one complete request reference, option mapping and deadlines
per invocation and retains them unchanged during connection retries. Uncertain
admission retries that same request. Known admission retries observation.
The first local answer is conditional on the request still being pending. A
lost answer reply observes the retained decision; if it remains pending, the
app explicitly reconciles the same selection and reference. It observes the
final service answer before changing the destination.
Replace streams the already verified job result to a sibling file, checks its
size and digest, flushes and closes it, then performs one safe replacement. A
failed copy or unsupported replacement leaves the old file intact. The app
checks its process and replay deadline again before replacement.

The app acknowledges the exact recorded answer when its local copy or no-op
finishes and this invocation no longer needs the service's full decision replay.
It retains the exact answer in process for acknowledgement retries. A lost
acknowledgement reply retries only that acknowledgement;
expiry reports the unconfirmed acknowledgement without repeating the copy or
opening another prompt. Delivery logs describe the finished local effect even
when its acknowledgement fails. The service's acknowledgement confirms receipt
of the answer and surrenders its full replay; the destination digest confirms
the application's file effect. Replacement flushes the file and, on POSIX,
the containing directory where supported. This demo makes no power-loss
recovery promise beyond the platform's file synchronization behavior.

Closing and launching the app again creates a new output-choice intent and may
ask again. The CLI keeps no choice sidecar and makes no claim to resume that
local intent after process exit. Its URL, size and digest still reconcile the
same durable download request and completed operation.

### Native release acceptance scene

The Windows release acceptance owner prepares the official OpenCode console,
the current OA plugin and SDKs, a managed Lemonade provider, and a real ComfyUI
host. The person approves the owned project folder and enrolls OpenCode through
the native Panel. The same live assistant session then answers this app's Keep
choice and ComfyUI's model choice. The scene checks an unchanged existing file,
the exact recorded answers and acknowledgements, and one received PNG from one
image operation. These are acceptance requirements; a source check alone does
not establish that the scene ran successfully.

The acceptance owner opens a native permission review before Python posts its
notification. The review names the actual Python executable, shows a one-hour
lifetime, and offers Allow or Refuse. Its disclosure explains that this is a
shared interpreter permission: it covers every script run by that executable
in this account. The person does not enter paths, action identifiers or resources.

Before the ComfyUI workflow, the owner opens the registered helper's picture
permission review. It names the registered app and configured image host and
shows the five operations in plain language with a one-hour lifetime and Allow
or Refuse. The owner verifies the native review receipt and reads back every
exact rule through the existing Rights service before starting the workflow.
A missing, partial or interrupted review remains unconfirmed. The advanced
exact editor remains an administrative tool.

The guided reviews and their receiving checks are implemented and source/check
qualified. Fresh installed execution and the complete live journey remain
required; these source checks do not demonstrate a new native acceptance pass.
The separate saved-secret acceptance case exercises a native typed permission
request through its existing owner.

The person then clicks Stop for OpenCode in the Panel. A fresh output choice
must remain unanswered and expire, preserving the existing file. When asked to
close OpenCode, enter `/exit` or press Ctrl+C inside its TUI. Leave the owner
console open until it records closure and completes cleanup.
