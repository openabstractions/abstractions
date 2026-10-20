# Setup: OA-labelled package installation and updates

Setup installs the fixed beginner archive choices (ComfyUI and the local
picture engines) from the release-owned catalog, and owns updates and
repair of those OA-labelled packages. Downloads run as public OA jobs; Setup
owns its plan journal (`%APPDATA%\OpenAbstractions\setup\plan.json`), its
staged downloads and the installed package trees under
`%LOCALAPPDATA%\OpenAbstractions\apps\<install identity>\<version>`. Every
package tree carries a receipt (`.oa-install.json`) listing each file's size
and SHA-256. A personal upstream installation of the same app is a separate
installation and Setup never touches it. Setup has no update feed or schedule;
an update arrives with a new OpenAbstractions release.

These locations are the installed runtime's. A Panel started for another
runtime with `-runtime-state-dir <dir>` keeps every one of them under
`<dir>\setup` (`home.go` `UseRuntimeState`): the journals in it, installed
apps and picture workspace data under `local`, picture outputs under `pictures`. It
refuses the Lemonade provider's verbs and the core MSI's feature
maintenance, which belong to this account's own installation. A Panel given a
runtime without its state folder refuses Setup.

## Platforms

The catalogue pins every package for Windows x64 (`catalogForTarget`,
`install_pins.json`, `adopter_assets.json`); on every other platform each
component stays Pending and Setup installs nothing. The journal, archive and
update code is the same everywhere, and its tests run on Windows, Linux and
macOS. Every test resolves the per-user folders inside a temporary folder
(`TestMain`), and a test that installs takes an empty one of its own
(`standInHome`); `monitor/internal/personfolders` names each platform's
variables. Names compare as the file
system compares them (`identity.SamePathName`): members or versions that
differ only in case are one name on Windows and two on a case-sensitive
volume.

## One Install

The Setup page reviews a plan on one screen: the goal, each app package and
the starter picture model with publisher, version, size and licence, the total
download and free space, and the picture application's permissions, as sentences. One
Install click approves all of it (`monitor/setup_install.go`, `POST
/setup/install`). The approval is `install.json` beside the plan journal: the
plan, the picture-model plan, the approved permissions and the Panel build
that drives it. The page then runs each step in order. After a restart or an
OpenAbstractions update, opening the page shows the unfinished install as
stopped (`"interrupted": "update"` once another build reads it) and starts
nothing; the person's Install (`POST /setup/install/continue`) continues it
from that record and each step's own journal:

1. local picture support: the engine's adapter, which every x64 core installs, is read beside the Panel;
2. the app packages, through the update transition below;
3. the picture model: start, downloads, then the engine and the application's image
   policy;
4. ComfyUI's permissions, the grants package's picture feature set
   (`grants.FeatureRules(grants.Picture, …)`: complete on
   `host:lemonade-image`, and write and read on `output:image`), written once
   for the receipt-verified ComfyUI program. The record keeps the exact
   approved rules and their digest, which the screen showed as the
   permissions' ID. The step rebuilds the picture set as its ceiling,
   refuses a recorded rule outside it, and writes the recorded rules, so a
   newer Panel can only narrow an approval;
5. the picture workspace entry and ComfyUI's start;
6. ComfyUI opened at `http://127.0.0.1:8188/?oa_recipe=picture`, where the OA
   sidebar loads the picture recipe with no OA question. The Panel appends a
   one-time `oa_token` from the app's backend; without it nothing loads.

Every pinned hash and licence check, the other-copies rule and the isolated
runtime's state folder apply as before. The step-by-step sections stay for a
plan approved before One Install.

## The setup step

`Abstraction Panel.exe --setup` runs the setup step
(research/first-run/DECISION.md §4; `step.go`, `monitor/setup_step.go`). The
bundle's Finish starts it through `--finish` while "Open OpenAbstractions"
is ticked, Settings starts it with
Change, and the background presenter starts it again at sign-in while
approved work is unfinished. One step runs per session.

Which registered app gets what comes from Setup's catalog: each integrated
app's choice carries its use, `code` (a coding model as the chat default) or
`image` (pictures), and for pictures the engine choices they run in,
strongest first (`Choice.Use`, `Choice.Engines`, `Catalog.StepChoices`). The
step takes the first engine whose graphics family (`Choice.Graphics`, read
from the display adapters' hardware ids) this computer has, whose package
plan is preparable and whose core feature is present or can be added
(`Runner.StepPictureEngine`), and the last engine otherwise: the PyTorch
engine on Strix Halo, the fallback engine elsewhere. When the card fits a
faster engine the step cannot use yet, Show details says why in one sentence.
The PyTorch runtime inputs are a catalog package the runner downloads through
the job service after the approval. The picture rules name that engine's host
(`Choice.Host`). The step matches the
registered applications against those choices and names every app in its
question, notices and the Settings row by its registered title. Slice 1 is
the use `code` on windows-x64. The step asks
`abstraction.model/catalog@1 Fit` for that use. It then shows one native
question with the pick, the download and the free space it needs; licences
sit behind Show details. Not now writes only the step journal. Set up
records the approval in the step journal (`step.json` beside Setup's other
journals), has the runtime ask its own Keep question about a named app that
added itself (`AskConfirmation`), and runs the work. The journal records the
answer and is never its authority: each run recomputes Fit and the reviewed
plan and resumes only an answer to exactly that pick, asking again
otherwise. Slice 1 picks only what this computer runs or serves. The work:

1. the coding-model controller's public download job, journaled before
   submission, so a sign-out and sign-in continues the same job;
2. the existing ModelHost Setup operations: the bundled engine, the model
   folder's inventory store, the hosted model, declared with the plan's
   engine arguments (`CodingModelPlan.EngineArgs`: MTP,
   `--spec-type draft-mtp --spec-draft-n-max 3`, the measured 72.7 tok/s
   configuration; ModelHost adds `--no-mmap` and `--fit` itself);
3. the account default selection policy `chat` with this model first
   (`selection-operator@2 PutAccountDefault`).

An app that its OpenAbstractions Setup package installed but that is not
registered yet, because the person answered Not now to the registration
question the package started, still counts: the package records its manifest
(`HKCU\Software\OpenAbstractions\Applications\<name>`, value `manifest`),
the app waits as a row in Panel → Apps with Allow and Not now, the step
names the app by that manifest's title, and Set up asks the same
registration question again. Its picture part waits for the registration
its rules name.

A run that stops says where in its notice: reading the step journal, reading
the installed apps, Fit, a pick with no reviewed download, showing the
question, saving the answer, the Keep question, the download, preparing the
model to run, the chat default or pictures. The journal keeps that stage,
the sentence and the error behind it under `stopped` and leaves the phase
as it was; the Panel writes the same record to OA logging (`setup step
stopped`), which keeps nothing while logging is not ready. The next run
that asks or finishes clears it.

A coding model the person already has is adopted in place. The model
inventory owns finding it: inventoryd lists the model files of every known
store (the Hugging Face hub cache and every other app store its adapters
name, and operator-named roots) with the SHA-256 the store names, and
hashes a file its store names by no SHA-256 only when asked about its
manifest (local-stores LS-13). Before planning the download, the step asks
the storage inventory to `Find` the reviewed plan's pinned digest and
takes a model whose one weights entry has that digest and the pinned size
(`PinnedObject`, `model_local.go`). With none, it lists the models of the
pinned size that have no digest yet (`SameSizeCandidates`), asks the
inventory to hash exactly those (`Holders` on each id), posts "Checking a
copy already on this computer." and waits for the result (`findLocal`, at
most `localCheckBound`: about three minutes for 25 GB) before it offers a
download. A found copy makes the question's body read "Nothing to
download." and Show details "…which is already on this computer."; the
journal records the inventory object
under `adopted`, and Set up has ModelHost host that object where it lies
(`stepHostInPlace`). Nothing is copied. A copy is adopted only when the
file and every folder above it pass identity's `trustedpath` check (owned
by this account or the machine, changeable by no other account); a copy
other accounts can change gets the download, and Show details says "Other
accounts can change the copy here. OpenAbstractions downloads its own."

`--finish` runs the same step with `SetupStep.Finish`: a run with nothing
to ask (already set up, or no app to set up) posts no notice, and the Panel
window opens instead. A Finish runs work only for a question answered in
that run: earlier answered work left to do stays stopped with "Setup
stopped when OpenAbstractions updated. Open Settings to continue.", and its
journal loses its build. Sign-in resumes approved work only for the Panel
build that ran it (`step.json`'s `build`).

`--setup` and `--finish` refuse an elevated token and starts the background presenter once
before its first notice. The download start is one `notification@1` notice
bound to the job's operation; the end ("Ready. Open" and the app's title) or
a failure is one more. A model this computer already serves downloads
nothing. A rerun with everything in place says "Already set up"; a removed
chat default is put back under the same answer. The step writes no sign-in
entry: the runtime's reconciler turns the presenter on once an app
registration is confirmed.

Slice 2 adds pictures to the same question (`step_pictures.go`,
`monitor/setup_step_pictures.go`). When Applications lists an app whose use
is `image`, the step offers the picture part if this Setup can prepare it
here: the app's engine is the one the reviewed picture plan runs in
(`ModelPlan.PictureEngine`), the engine package plan is preparable, the
provider feature can be added and the app's registered program is no shared
interpreter. The question then reads, for example, "Set up Coder and
Painter?" with the body "About 40 GB to download." The body adds "Windows
asks for approval once" when the picture part needs administrator approval.
Show details starts "Coder gets Qwen3.6 35B on this computer. Painter gets
pictures on this computer. 45 GB free needed." and adds the picture model,
its and the engine package's licences, and the sentence of each permission
the picture app gets. An app's own request heads the question "Set up a
coding model for OpenCode?" or "Set up pictures for ComfyUI?" (architect
ruling 2026-11-10, row 13). A picture app without a chat app is the picture part alone,
with no Fit read. The journal records the picture answer for exactly the
reviewed picture plan, Setup's engine package plan, the rules' digest and
the program; a change to any asks again. After the chat part, the step runs
One Install's owners in order:

1. Setup's runner: the engine package plan (the journal's choices plus the
   app's engine), the engine's provider feature and each package's automatic
   transitions (`StepEngine`, `AdvanceAutomatic`). `Status.NextAutomatic` is
   the one package driver: the Setup page asks `/setup/advance-next` for the
   same choice and no longer chooses in script;
2. the starter picture-model controller's public download jobs for the
   reviewed plan, with one start notice bound to their operation;
3. the controller's Prepare, recorded as the plan's configuration for that
   engine (`ModelRequirement`);
4. the `grants.Picture` rules on the local engine's host, written once for
   the picture app's confirmed registration when its program is the
   answered one.

A picture failure is one notice that names the picture app. When the chat
part is set up, it stays Ready: the journal records `pictures_stopped`, the
notice says the chat app is ready and why pictures stopped, and the Settings
row offers Try pictures again, whose run resubmits a failed picture download
with no new question. Only Settings and a run the person starts try pictures
again; an app's setup request leaves a stopped picture part as it is and
posts no notice about it, so a repeated failure never sends one per request.

## The OpenCode goal

The archive catalogue's `opencode-official` component remains Pending because
the ordinary archive journal does not run the application installer. The AI
apps section offers **Review app setup**, confirmation, then **Open Setup**.
This opens the verified current installed Burn bundle's interactive Modify
page. Its existing app choices own the official OpenCode Desktop and OA
integration MSI chain, application registration and package outcomes.

The maintenance reader accepts only protected machine-owned registration of
the existing bundle family, this release's version, its matching held signed
Burn GUID and Setup identity, and the trusted Panel publisher. Source and
ancestor holds cover process creation. Registry command strings are never
executed. Only fixed `/modify` is supplied, with the existing clean Windows
environment owner and a private temporary folder under Setup's own home.
Opening that window leaves archive-install journals and installed state alone.
An absent or unverifiable cache explains the refusal and points to the normal
installer download entry. `adopters/opencode/official-install.js` remains a
separate read-only development diagnostic for the protected CLI session.

## Adopter fix-ups and Repair

A catalogue component can name an adopter fix-up (`Fixup`, `fixup.go`): one
file in its app's official folder, the app's stock SHA-256 digests, the
adopter's fixed digests, the supported versions with each official program's
SHA-256, the fixed file the adopter package carries in its own folder, and
the Windows Installer upgrade code of the adopter's per-user integration
package. The data is outside the catalogue digest a plan binds. OpenCode's
watcher fix is the first (`backlog/OPENCODE-WATCHER-PIN.md`, ruling 1); the
adopter's installer owns its values.

`CheckFixups` reads each installed app's files read-only, for the overview's
`fixes`. An app whose program or integration package is absent, or whose
package carries no fixed file, has none. The fixed digest reads `current`. A
stock digest in a listed release, decided by the official program's digest,
reads `needs_repair`: the row says "Needs its OpenAbstractions fix again"
beside its Repair button, adds "· Close <app> to repair" while the app runs,
and never reads Installed. Any other release reads `unknown`: "Not a version this release
supports · Supports <versions>", with no Repair. A supported release whose
file is missing or unknown also reads `unknown`, with `supported`: "The
OpenAbstractions fix can't be checked · Reinstall <app> with
OpenAbstractions", with no Repair. `POST /setup/repair`
runs `RepairFixup` as a Setup step of kind `repair`. It refuses while
another step runs, while a program in the app's folder runs, and from a Panel
serving an isolated runtime, whose overview offers no Repair. The repair is
the integration package's own `msiexec /fomus` repair; the adopter's
installer stays the file's only writer. Setup reads the file again
afterwards. A failure's Windows Installer code is in the row's technical
details.

## Other copies of the same app

Before a plan is reviewed, Setup looks for ComfyUI installed without the OA
mark (`other_copies.go`, VISION 2026-10-02). The OA mark is the package's
`OA-PROVENANCE.json` together with Setup's `.oa-install.json` receipt. Setup
reads these places and writes nothing:

- ComfyUI portable folders and source checkouts named `ComfyUI_windows_portable`
  or `ComfyUI` in `%USERPROFILE%`, its Desktop, Downloads and Documents, and
  the system drive's root;
- ComfyUI Desktop under `%LOCALAPPDATA%\Programs\@comfyorgcomfyui-electron`
  or `%LOCALAPPDATA%\Programs\ComfyUI`;
- the per-user and per-machine uninstall registry entries, both views, for
  names containing ComfyUI.

The Setup page names each copy and its location. A goal whose app has another
copy installs nothing until the person ticks "Also install the
OpenAbstractions build"; `/setup/plan` and `/setup/intent` refuse such a plan
without that choice. Setup never modifies or removes the other copy. Setup's
own marked build is replaced by the update transition below, and updates of
OA builds stay disabled until OA's release feed exists.

## Catalog pins

Each package entry is an exact `Artifact`: version, source commit, release
URL, SHA-256, size, license, publisher and install identity. Per package kind
the catalog also requires:

| Package | Additional pins |
|---|---|
| ComfyUI (`comfyui-app`) | paired source archive (another asset of the same release, SHA-256, size); RECORD-set digest of its private Python, passed to the launcher as `--records` |
| Lemonade (`lemonade-engine`) | the image backend archive SHA-256 and model revision it was tested with; both must equal the release's picture-model plan |

`install_pins.json` records every exact pin a Setup build has carried and only
grows. A release that changes a pin appends the new pin; all other recorded
pins for the platform become `PreviousInstallPins`, which keep installed
trees of earlier pins recognizable. A version folder belongs to one set of
bytes: a recorded version reused for other bytes is refused.

## Update transition

Approving a plan whose catalog changes an installed package's pin starts an
update. The installed pin becomes the effect's `retained` version.

| Phase | Meaning | Next step (`Advance`) |
|---|---|---|
| `pending` | update approved; checks the new pin's folder and install rights | reserve and submit the public download |
| `requested` | public job identity recorded | stage the completed download and verify its hash |
| `verified` | verified download staged | extract beside the retained version into `<identity>\<new version>`, publishing the folder by rename |
| `installing` | extraction may have happened | check the new receipt |
| `staged` | new version on disk, receipt verified, not in use | run the package health check |
| `checked` | health check passed | activate |
| `installed` with `retired` | new version active; the replaced one awaits removal | remove the retired version |
| `installed` | update complete | — |

The retained version stays on disk and is the version in use (ComfyUI launches
it, the Lemonade provider's selection names it) until activation. Activation
rechecks the install rights and the staged receipt, records `installed`, and
moves the retained version to `retired`. A Lemonade update first moves the
provider's binding: `lemonaded package activate --version <new>` installs the
new version's engine files, stages its paired backend into its own cache
(passing the backend archive the model step verified while that copy is still
on disk) and selects it last; Setup records `installed` only on the
provider's success object. The provider runs the newly selected version from
its next launch; a run in progress finishes on the version it holds. A
definitive activation refusal (exit 2, such as a private model of another
revision) fails the update and retires the staged version, as a refused
health check does; the provider writes its selection last, so the earlier
version is still selected.

A first installation of any of the three passes the same `staged` and
`checked` steps without a retained version. A first Lemonade installation
moves no binding; the Lemonade model step's `lemonaded configure --package
<installed version>` creates the selection.

Setup's journal is the authority over which Lemonade version is installed
(`activeArtifact`). The provider bridge reads `setup-info` (reply version 2)
and accepts the provider's active package only when it is that version: its
program lies inside the version's folder and the provider's table entry for
it carries the pin's archive, backend archive and model revision. The bridge
and the provider come from one OA release, and the bridge reads no other reply
version. `lemonade_pins_test.go` refuses a release whose `install_pins.json` or
catalog names a Lemonade pin the provider's `server.Packages` does not know or
pairs differently.

**Rights.** Every package is a per-user archive (`archive_per_user`). Its
installation needs no elevation and writes only under the account's
`%LOCALAPPDATA%`. An update must use the same install handler as the version
it replaces, and activation runs as the same account. For ComfyUI the Panel
re-registers the updated program with `abstraction.facade/application.manage`
and recreates its caller-scoped picture policy with
`abstraction.inference/selection.manage_for`, the same rights the first
installation used. Registration replaces only an application entry equal to
one Setup built for a retained or retired version; any other entry is
preserved.

**Removing a version.** The version folder first moves to the sibling
`.<version>.removing`; Windows refuses the move while a program in it runs, and
removal waits. Setup then deletes only files whose size and SHA-256 still match
the version's receipt, removes empty folders and the receipt, and moves any
remaining files back to the version's folder. The step reports how many files
it kept. ComfyUI's retired version is removed only after the Panel confirms
that the registered `comfyui` entry starts another version. A retired Lemonade
version is first passed to `lemonaded package retire --version <old>`, which
removes the provider's cache, config folder and engine-file records of that
version; it answers `held` (exit 3) while an engine launch holds the version,
and refuses the version the provider's selection names. Either answer keeps
the folder and the `retired` entry, and the next `Advance` retries. A version
that never became active in the journal may still be the provider's
selection, when an activation was overtaken by the receipt check or by
`ReconcilePrior` of an earlier plan; Setup first runs `package activate` for
the version in use, then `package retire`. If the provider cannot select the
version in use again, Setup stops with that message and removes nothing.

**Health checks** read the receipt-verified tree and run none of its programs:

- ComfyUI: `OA-PROVENANCE.json` names the catalog's paired source archive
  (`corresponding_comfyui_source_zip_sha256`) and RECORD pin
  (`private_python_records_sha256`); `ComfyUI_windows_portable/update/`,
  `update_comfyui*` files and `custom_nodes/ComfyUI-Manager` are absent; the
  embedded `python.exe`, `oa_comfyui_launch.py` and `ComfyUI/main.py` are
  regular files. The OA build removes upstream update scripts.
- Lemonade: `lemonaded package check --version <version> --root <staged
  folder> --json` proves every file of the staged package against the
  provider's reviewed manifest for that version, without changing an ACL or
  the provider's selection, and reports the backend archive and model revision
  that version was tested with; both must equal the catalog's
  `tested_backend_sha256` and `tested_model_revision`. A provider refusal
  (exit 2) or another pairing retires the staged version; a provider that does
  not answer leaves it staged.

A version that fails its health check, or whose files change before
activation, is retired and removed; a retained version stays in use. `Retry`
installs the verified download again. Removal takes the first retired version
nothing holds, so a ComfyUI version its registration still starts does not
keep a failed copy on disk.

A version string names a folder: a trailing dot or space and reserved names
such as `CON` are refused, and two versions that differ only in case name the
same folder.

## Recovery

Each step is one compare-and-replace journal transition, and `Advance` on the
next Setup start continues from the recorded phase:

| Interrupted | Recorded phase | Next start |
|---|---|---|
| after `installing` was recorded, before extraction | `installing` | no receipt; `uncertain`, and `Retry` extracts again |
| after the new folder was published, before it was recorded | `installing` | the receipt verifies; `staged` |
| after staging | `staged` | health check |
| after the health check passed, before it was recorded | `staged` | health check again |
| after the health check | `checked` | activation |
| after Lemonade's `package activate`, before `installed` was recorded | `checked` | activation again; the provider finds the version selected, proves it and copies nothing |
| after activation, before removal | `installed` with `retired` | removal |
| during removal | `installed` with `retired`, `.<version>.removing` on disk | removal resumes from the sibling folder |

A plan approved under an earlier release resumes nothing. Its unfinished
steps block a new plan until the person settles them with `ReconcilePrior`
(the Panel's "Settle this unfinished step", `/setup/recover`): an open public
download is cancelled through the job service; an interrupted installation
whose exact earlier pin verifies is recorded as installed, one that left
nothing as failed, and a folder that differs from the package is preserved
with a refusal; a staged update that was never activated is removed while the
version it would have replaced stays in use.

## User data

Setup's removal touches only package files listed in a version's receipt.
User data lives outside the version folders:

| Package | User data, never removed by Setup |
|---|---|
| ComfyUI | `%LOCALAPPDATA%\OpenAbstractions\ComfyUI\user` (settings, workflows, the OA image recipe), `...\input`, `...\temp`, `...\launch` (the launcher's RECORD-check record), and outputs in `Pictures\OpenAbstractions\ComfyUI`. Models used by the OA picture path are the Lemonade provider's. Files a person adds inside a version folder, such as `ComfyUI/models` or `custom_nodes`, are kept. |
| Lemonade | the OA provider's private root (reported by `setup-info` as `provider_root`): the model files (`engine-models`), Setup's model downloads (`engine-downloads`, reported as `download_dir`, which the provider publishes into `engine-models` by rename; each is a second name the job service made by hard link for its own result file, so the bytes are on disk once and the service keeps its name until its retention ends; the read-only attribute is per file and shared by both names, so OA never sets it on a model file, and the provider folder's ACL protects the model), the selection and the content store, shared by every version, and per version `engine-cache\<version>` (the expanded paired backends), `engine-config\<version>` and `engine-files\<version>`, which `package retire` removes with the version |
| Setup | `%APPDATA%\OpenAbstractions\setup` (plan and model journals, staged app downloads) and `%LOCALAPPDATA%\OpenAbstractions\setup\downloads\models` (staged model files) |
