# Setup: OA-labelled package installation and updates

Setup installs the fixed beginner choices (ComfyUI, the local picture
engines and, once its route exists, OpenCode from its official installer) from
the release-owned catalog, and owns updates and
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
apps and ComfyUI data under `local`, ComfyUI outputs under `pictures`. It
refuses the Lemonade provider's verbs and the core MSI's feature
maintenance, which belong to this account's own installation. A Panel given a
runtime without its state folder refuses Setup.

## Platforms

The catalogue pins every package for Windows x64 (`catalogForTarget`,
`install_pins.json`, `adopter_assets.json`); on every other platform each
component stays Pending and Setup installs nothing. The journal, archive and
update code is the same everywhere, and its tests run on Windows, Linux and
macOS, each with a stand-in home (`standInHome`). Names compare as the file
system compares them (`identity.SamePathName`): members or versions that
differ only in case are one name on Windows and two on a case-sensitive
volume.

## One Install

The Setup page reviews a plan on one screen: the goal, each app package and
the starter picture model with publisher, version, size and licence, the total
download and free space, and the permissions ComfyUI gets, as sentences. One
Install click approves all of it (`monitor/setup_install.go`, `POST
/setup/install`). The approval is `install.json` beside the plan journal: the
plan, the picture-model plan and the approved permissions. The page then runs
each step in order and resumes it after a restart from that record and each
step's own journal:

1. local picture support, the core MSI feature, where Windows may ask once;
2. the app packages, through the update transition below;
3. the picture model: start, downloads, then the engine and ComfyUI's image
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
   sidebar loads the picture recipe with no OA question.

Every pinned hash and licence check, the other-copies rule and the isolated
runtime's state folder apply as before. The step-by-step sections stay for a
plan approved before One Install.

## The OpenCode goal

The "Use an assistant" goal plans one component, `opencode-official`
(`OpenCodeOfficialComponent` in `catalog.go`). OA builds no OpenCode package:
OpenCode installs through its official installer and the OA plugin, the
route `adopters/opencode/official-install.js` finds, and the download starts
only on the person's Install click. The component stays Pending until Setup
has a closed action handler for that route, so the goal is reviewable and not
preparable.

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

The speech plan (`ApprovedSpeechPlan`, `speech_plan.go`) pins the
whisper.cpp v1.8.4 Vulkan and Kokoros b17 backends and the
Whisper-Large-v3-Turbo and kokoro-v1 model files by size and SHA-256, equal
to their rows in `installer/third-party.tsv`. Its consent text
(`SpeechConsent`) names each licence; `koko.exe` is GPL-3.0-or-later, and
OpenAbstractions never ships it. No Setup step downloads the plan yet: the
Lemonade provider stages the files with
`lemonaded configure --activate-reviewed-package --speech-only`.

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
