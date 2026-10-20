# Native Lemonade OA source

## Startup refusal without the runtime — 2026-10-31

[A-LEMONADE-OA](../../backlog/A-LEMONADE-OA.md) adds `admit_runtime()` to the
OA variant. After `--host`/`--port` validation, `CLIParser::parse` resolves
each enabled OA service once; when the selected runtime cannot serve one,
`lemond` and `LemonadeServer` print `OA Lemonade refused: <status>: <detail>`
and exit 69. `<status>` is the SDK resolution word (`runtime_unavailable`),
the IPC status name, `invalid_configuration`, or `integration_disabled` when
`LEMONADE_OA_DOWNLOADS` or `LEMONADE_OA_LOGGING` is `0`: the OA build has no
off switch (C-77). Unbranded `LEMONADE_OPENABSTRACTIONS` builds keep both
switches. The OA build's MSI has no Start menu or desktop shortcut: OA's
resource service starts `lemond` and the router reaches it (owner rule
2026-10-01). The patch now yields tree `892f4cad4ff82e4d3d2cd3f0d5b4efbd16014fdb`;
the 2026-10-27 tree below is its parent.

## Current correction — 2026-10-27

LemonadeLogging1724 starts from reviewed `bf47b580273cc5b97245a6d4c315fa5ad0eca12c`
on private branch `codex/oa-080-lemonade-logging1724`. The adapter's second log
queue discarded a failed record and had no shared retry/gap reporting. The
approved correction maps AixLog metadata into the existing C++ logging
`AsyncHandler` under LOG-S12/S13, retaining verified resolver/rebind and
cancellation. This handler is the existing interim adapter owner; this work
does not consolidate all language implementations.

The actual old patch fails the inert failed-record counterexample. The corrected
adapter passes the existing shared outage/count/gap corpus, severity/metadata,
AixLog nonblocking admission, cancelled shutdown and resolver-count controls.
The initial binding is shared across the handler's per-attempt writer copies;
failure/rebind changes that binding and healthy writes reuse it. The same adapter
also compiles against the actual public SDK headers with MSVC. These are fixed
in-memory sink fixtures, with no native IPC or vendor engine execution.

Evidence uses primary scratch `LemonadeLogging1724-*`. The retained source
archive reproduces the prior patched tree after restoring its source-archive
version stamp; reversing the original patch reproduces the pinned upstream tree.
Applying this patch produces the current `native-inputs.json` tree. Historical
QA/SDK/license facts below keep their dated scopes. No engine, model, installed
runtime, UI, download, signing or release execution is involved.

`oa-native.patch` is the complete reviewed OA delta from the exact upstream
commit in `native-inputs.json`. It preserves native IPC downloads/logging,
isolated OA settings and product identity, joined native tray/server lifetime,
dependency/font notices and focused regression sources. It contains no binary
payload or vendored upstream checkout.

In a clean upstream checkout, verify the base commit/tree, then run
`git apply --check --index oa-native.patch` and `git apply --index oa-native.patch`.
`git write-tree` must equal `oa_delta.expected_tree`. Verify the patch SHA256
before applying. The reviewed parent identifies the retained QA source; the expected tree binds
the full current delta. Publication is pending; no candidate SourceURL is claimed.

Materialize each pinned SDK `cpp` subtree at its manifest `SDK_DIR` destination,
and preserve its license. The manifest also retains the exact SDK license text.
Build/install facade and download through their existing CMake owners into
`SDK_PREFIX`. Materialize native pins under `DEPS_DIR`, including the single
mbedTLS framework gitlink; verify annotated tag objects and peeled commits.
Pass explicit `FETCHCONTENT_SOURCE_DIR_*` and `CMAKE_PREFIX_PATH=SDK_PREFIX`.
Use disconnected dependency mode and the exact web lock with scripts/audit/fund
disabled. The upstream CMake owner deliberately transforms libwebsockets during
configure; capture that diff and refuse unexplained input changes.

The existing CMake targets are `lemond`, `lemonade`, `LemonadeServer`, and
`wix_installer_minimal`. OA builds enable `LEMONADE_OPENABSTRACTIONS` and
`LEMONADE_OA_BRANDED_BUILD`; MSI production requires explicit
`LEMONADE_OA_MSI_VERSION` and `LEMONADE_OA_SDK_LICENSE`. Default package
mode `qa` also requires `LEMONADE_OA_QA_LABEL`. Explicit package mode `release`
requires an empty QA label and exact source version `UPSTREAM_VERSION+oa.OA_VERSION`,
exported through existing `tools/source_archive.py --version` before build.
Build native3PE/web resources first, admit/sign the exact three own PEs through
the existing shared signer, then invoke `wix_installer_minimal`. In release mode
that target has no native/web build dependencies and packages the prepared bytes.
Read back every File/Binary PE against the signing receipt before/after MSI signing;
the native recipe supplies no signature authority. QA/stock dependencies remain. Build single-threaded with static MSVC CRT. Existing
source fixtures under `tests/oa_*` include real pinned-header listener tests;
the configured split header/static-library variant must also compile/link.

This preserved QA source was built against exact released0.4 SDK/protocol
inputs. It does not qualify a future0.8 rebuild. Shared producer wiring,
release identity, PE signing before MSI construction, publication and current
installed/runtime qualification remain separately admitted work. The manifest
records the existing-owner release identity implementation and pending shared
signed-payload/SDK0.8 companion. Current source changes do not qualify new binaries.

`build_native.py materialize` performs offline input preparation before
`prepare-native`. The caller obtains current source pins through the existing
`app_producer_inputs` owner and checks out its clean public repositories into
`SOURCES_ROOT`. `local_source_inputs()` returns the finite local cache names,
public URLs and exact commits from this recipe's manifest. The caller acquires
those objects through the existing checkout owner; materialization accepts
local Git repositories only and shallow-fetches exact commits over `file:`.
It performs no network acquisition, dependency build or npm install.
Every caller tool is validated and pinned before dispatch. The selected public
`redist/scripts/git_env.py` owner removes repository/index redirection; this
offline phase additionally disables ambient Git configuration, exec/template
overrides, hooks and remote protocols in its child environment. Global Git
configuration remains unchanged.

Pass `--pins SOURCE_PINS.json --sources-root SOURCES_ROOT
--git-cache-root GIT_CACHE_ROOT --inputs-root NEW_INPUTS_ROOT
--npm-cache SELECTED_OFFLINE_CACHE --wix-ui-package SELECTED_UI_NUPKG
--wix-ui-extension SELECTED_UI_DLL`, plus explicit absolute `--git`, `--python`,
`--node`, `--npm` and `--wix` paths selected by the job. The existing public
`redist/sdk_smokes/prepare.py` Windows selector supplies its actual VS instance,
version, bundled CMake, generator and MSBuild; absence refuses preparation.
The source job retains setup-action/tool provenance and authenticates these
measurements. Historical QA tool versions are separate evidence.

The fixed outputs are `upstream/lemonade` (exact reconstructed native tree),
`upstream/deps` (the eight manifest dependencies, including the mbedTLS gitlink),
`npm-cache`, `wix-ui.nupkg`, `WixToolset.UI.wixext.dll`, `host-tools.json`,
`selected-toolchain.json`, and `toolchain.cmake`. A local checkpoint commit
allows the existing source archive owner to consume the reconstructed tree;
its expected tree, full patch and upstream commit bind source identity.
Materialization extracts the single selected native CMake LWS transform,
including its Windows socket correction, and evaluates it with metadata-only
`cmake -P` on a disposable local dependency checkout. It records the exact
script and canonical expected diff without modifying the actual dependency.
The caller supplies a closed lock-integrity-verified offline npm cache; no
unrelated global cache or configuration is selected automatically.
An initially empty cache is permitted before input-receipt finalization. The
caller can then copy the reconstructed `src/web-app/package.json` and exact
manifest-pinned lockfile to a disposable workspace and run selected
`npm ci --ignore-scripts --no-audit --no-fund --cache NEW_INPUTS_ROOT/npm-cache`
in that workspace during its separately admitted acquisition stage. Finalize
the receipt after warming that cache; `prepare-native` remains offline.

After materialization, run the existing `pinned_input_receipt` operation over
the selected source pins and input root. This binds the generated source-owned
toolchain/transform, selected cache/package bytes and actual host-tool pins in
the existing receipt shape. Feed the returned source/dependency paths, selected
generator and fixed output files to `prepare-native`. Current SDK components
remain the independently selected public source checkouts in `SOURCES_ROOT`.

`build_native.py prepare-native` is the app-owned native build entry point.
Its `--input-receipt` uses the existing `app_producer_inputs.pinned_input_receipt`
shape (`schema`, exact public `sources`, measured relative `build_inputs`).
`--sources-root` contains the selected clean public repositories, including the
five C++ SDK components, the charter and the independently selected `redist`
repository. The release
workflow independently authenticates this selection from its current source
rosters and Plan. Capability module versions are independent; the native app
version comes from `--version`. The preserved QA SDK pin set is historical and
is never an automatic fallback for missing selected sources.

Pass explicit measured host tools (`--cmake`, `--python`, `--node`, `--npm`, `--wix`,
`--msbuild`, `--git`), the selected toolchain file, disconnected dependency roots,
and measured offline npm cache. The producer exports the native version overlay
through the existing source archive owner, builds/installs the selected facade
and download CMake owners, then builds the three native targets and web resources
single-threaded. `libwebsockets-configure.patch` in `build_inputs` must match the
existing CMake owner's intentional transform exactly. No other dependency source
mutation is accepted. Installed SDK headers/libraries and all prepared payload
bytes are recorded in `native-receipt.json`.

`--host-tools` names one small file inside the immutable inputs, with shape
`{"schema":1,"tools":{"cmake":{"sha256":"...","bytes":0},...}}` for exactly
cmake/python/node/npm/wix/msbuild/git. The file is itself pinned by `build_inputs`.
The workflow selects installed tools and supplies their explicit absolute CLI
paths. The producer compares those bytes to the named pins and records actual
paths and pins before/after both phases. Installed runtime/compiler trees stay
in their selected host locations. Source files, cache, toolchain and UI package
inputs remain beneath `--inputs-root`. There is no ambient PATH lookup.
Both SDK configurations and the native configuration disable system,
environment and package-registry search. The selected sibling SDK sources and
explicit staged `CMAKE_PREFIX_PATH` remain the dependency inputs.

Both phases also require the measured `--wix-ui-package` and
`--wix-ui-extension` inputs. The shared WiX owner authenticates their package/DLL
pins; OA release CMake uses this explicit DLL path instead of extension-cache
discovery. The producer checks the actual source-derived UI resources and app
bitmap/Icon overrides before emitting container expectations.

Sign only the three `Release/<name>` rows from the prepared `build` root through
the shared signer. The `package` phase receives that native root, the actual
signing measurements and the same immutable input receipt. It calls only
`wix_installer_minimal`, checks signed members and prepared source/resource bytes
remain unchanged, and emits `bundle-app.json` plus `installer-receipt.json`.
Transfer the complete prepared native root, including its source, SDK, build
project/cache/resources and receipt, with the measured input root and selected
clean public sources. The three PE files alone cannot satisfy packaging checks.
The latter supplies full source-derived File/Binary/Icon expectations. The
selected pinned WiX UI package is read through `wix_packages.ui_source_resources`;
the authenticated sibling `redist` checkout exposes that owner at
`windows/wix_packages.py` (`SOURCES_ROOT/redist/windows/wix_packages.py`).
It is evaluated from its exact source bytes without adding bytecode to the
immutable checkout. The configured `installer/Product.wxs` is pinned at
preparation and checked before and after packaging, alongside native members.
Configured Product bitmap overrides and Icon bytes remain owned by the app's
WiX source. The release receiver adds verified own/supplier PE roles and performs
the existing unsigned-container readback, MSI signing and unchanged-member
readback. A producer JSON file supplies measurements, not signing or publication
authority. Missing supplier inputs refuse packaging. Run
`python test_build_native.py` for inert phase/receipt checks; these tests launch
no compiler, signer, installer or product.
