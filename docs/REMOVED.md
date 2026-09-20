# Removed entry points

This migration log records retired public entry points, their replacements and
the reasons for each change. Current applications resolve capabilities through
the facade and shared runtime. See the
[capability catalogue](https://openabstractions.org/catalogue.html) for current
interfaces and repositories.

Entries describe completed removals. Historical development commit identifiers
are retained for maintainers tracing the migration.

## Entries

### 1. 2026-09-15, Go facade `legacy` package

- **Was:** `abstraction-facade/go/legacy`, exporting `Discover`, `Machine` and
  `SetDeprecationReporter`. It selected file-backed providers by reading the
  machine's store rather than by resolving a service.
- **Went because:** a facade whose answer comes from a store file names its
  binding. The resolved clients answer the same questions from the runtime.
- **Replaced by:** the parent facade `Discover` and its `Resolve*` clients.
  Callers moved: monitor lost `--legacy-local` and its delegation, downloads and
  may-reach screens; `examples/pretend-lemonade` submits through `ResolveJobs`;
  the Synology `jobui` opens the download provider with `download.Discover`; the
  `research/facade-linking` Go packages were deleted and their `RESULTS.txt`
  kept.
- **In history:** `d41e9184`.

### 2. 2026-09-15, Go `logging.Auto` and its deprecation reporter

- **Was:** `logging.Auto`, `logging.DeprecatedUse` and
  `logging.SetDeprecationReporter`. `Auto` picked a logging sink by inspecting
  the machine.
- **Went because:** every in-tree caller had already moved to the explicit
  `LegacyAuto`, which says that it opens the file provider.
- **Replaced by:** `logging.Default` or the resolved logging client for
  applications; `logging.LegacyAuto` for a named local file provider.
- **In history:** `f84dc4b4`.

### 3. 2026-09-15, unprefixed Go config entry points

- **Was:** `config.Load`, `config.JobStore`, `config.Watch`, `config.WatchQuiet`,
  `config.DeprecatedUse` and `config.SetDeprecationReporter`. Each returned a
  file location or a file-backed store to any caller that asked.
- **Went because:** an application that calls them holds the provider's binding.
  The names carried a typed deprecation warning for one release and then went.
- **Replaced by:** `facade.Discover().ResolveConfig`, the resolved job service
  and a resolved config reader for applications. Providers that own the legacy
  file store call `config.LegacyLoad`, `LegacyJobStore` and `LegacyWatchQuiet`,
  which report nothing.
- **In history:** `84c99775`. The deprecation warnings they carried first landed
  in `78d58329`.

### 4. 2026-09-15, unprefixed Python config entry points

- **Was:** `abstraction_config.load`, `job_store`, `watch` and
  `LegacyConfigDeprecationWarning`.
- **Went because:** the same reason as entry 3, in the second language.
- **Replaced by:** `Machine.resolve_config()` and the resolved job service for
  applications; `legacy_load`, `legacy_job_store` and `legacy_watch` for
  providers. `jobctl`, the Python download provider and
  `research/conf180/watchprobe.py` call the `legacy_*` names; the ComfyUI
  `_vendor` copies were refreshed.
- **In history:** `28160c57`.

### 5. 2026-09-15, the `services.json` discovery registry

- **Was:** a registry file at the root of a job store naming the services on the
  machine, with a C++ `abstraction::discovery` client, its test, its installed
  target and an identity consumer probe, plus a reserved root name in the Go,
  Python and C++ job layouts.
- **Went because:** jobd never read it. Store-file discovery is what the
  resolver replaces.
- **Replaced by:** resolver bootstrap and registered services.
- **In history:** `ef638d7f`. A store written by an older release may still hold
  a `services.json`; nothing reads it, and a sink may now overwrite it. The name
  is listed as a free sink name in the containment and layout tests of all three
  languages.

### 6. 2026-09-17, `dl`

- **Was:** the Go file-store downloader, `abstraction-download/go/cmd/dl`:
  `dl <url> -o <dir> [--digest sha256:<hex>]`, `dl list`, `dl watch` and
  `dl tiers`. It submitted a record into the legacy job store, ran the transfer
  in its own process or handed it to a supervisor it found through the
  `supervisor.json` heartbeat, resumed from the proven prefix and verified the
  digest. It shipped as `tools\dl.exe` in the Windows MSI and `~/.local/bin/dl`
  in the Linux and macOS packages through 0.1.7, signed on Windows.
- **Went because:** owner decision 2026-09-15 (VISION.md, "Legacy file-store
  tools, NAS/BITS demonstrations and file-store libraries are removed while the
  abstractions are made correct"). A command that names a store holds the
  provider's binding; the runtime's job service is the abstraction.
- **Replaced by:** `openabstractions download <url> [--sha256 HEX] [--out PATH]`,
  which submits to the installed runtime's job service and copies the result
  out, and `openabstractions jobs list|show|wait|cancel|result`
  (the 0.1.8 command design, `feedback/cli-service-commands-0.1.8.md` §2, maps
  each `dl` use). Downloads a `dl` left unfinished in the legacy store at upgrade are
  abandoned: their records and partial files stay on disk and nothing finishes
  them.
- **In history:** `6709771d` (source), `6827f846` and `a5acedf7` (payloads),
  `eeb255ac` (release workflows). The last published source is
  `abstraction-download/go@v0.4.4` `cmd/dl`.

### 7. 2026-09-17, `jobctl` (Go)

- **Was:** `abstraction-job/go/cmd/jobctl`, a shell driver over the legacy job
  store: `submit`, `claim`, `progress`, `finish`, `show`, `list`, `cancel`,
  `intent`, `recall` and `orphans`, resolving a store root through `JOB_STORE`,
  `ABSTRACTION_STORE`, the per-user configuration's `store` and then
  `<home>/.abstraction`. It shipped beside `dl` in the same packages, and it was
  the Go half of the cross-language job harness `scripts/xlang-job.sh`.
- **Went because:** the same owner decision as entry 6. Its worker-side verbs
  are the file store's lease protocol, which applications no longer hold.
- **Replaced by:** `openabstractions jobs list|show|wait|cancel|result` for the
  work `openabstractions` submitted; the store interface (`Store.SetIntent`,
  `Store.Recall`) for a program that embeds the Go store.
- **In history:** `6709771d`. The last published source is
  `abstraction-job/go@v0.4.4` `cmd/jobctl`.

### 8. 2026-09-17, `jobd`, its heartbeat, its bus and the `service-jobd` repository

- **Was:** `abstraction-download/go/cmd/jobd`, published as the
  `service-jobd` repository, with `jobdw.exe` its windowless link until the
  Windows host moved into `openabstractions` earlier in 0.1.8.
  - The file-store worker: `jobd run|once|status|stop|start|discover|setup|install`
    and `serve.JobsContext`, `StoreRoot`, `OpenRunner`, `DropFolder` and `Sweep`
    in `abstraction-download/go/serve`, which swept the legacy store,
    reconciled delegates, adopted orphans, took in `wanted/` requests and
    printed `schtasks` lines.
  - The `supervisor.json` heartbeat: `download.Heartbeat`, `StopHeartbeat`,
    `SupervisorOf` and `Supervisor`, by which a client decided whether to hand
    work to a supervisor.
  - The supervisor bus: `download.ListenBus`, `Nudge`, `Who`, `Scope`,
    `DefaultEndpoint`, and `StartSupervisor` with its detached launch.
  - `download.Discover` and `download.Open`, which found the legacy store
    through the configuration's job-store locator.
  - The Linux `abstraction-jobd.service` and `abstraction-jobd.timer` user units,
    a sweep every five minutes, and the macOS LaunchAgent label
    `com.openabstractions.jobd`, which by 0.1.7 already ran
    `openabstractions serve runtime`.
- **Went because:** the owner decision of entry 6, and the 2026-09-16 decision
  that `openabstractions` is the Windows host
  (`research/windows-service-host/DECISION.md`): jobd's
  remaining purpose was the provider the owner removed.
- **Replaced by:** the installed runtime, which runs the same sweep
  (`serve.Pass`) inside its download execution profile, is hosted by
  `openabstractions serve host` on Windows, `abstraction-runtime.service` on
  Linux and the LaunchAgent `com.openabstractions.runtime` on macOS, and is
  found by resolution. The legacy client in `abstraction-download/go` remains
  for a program that embeds a store (`NewClient(DiscoverIn(store))`); it works
  every job in its own process, and `ExecuteDelegated` always refuses. An
  upgrade from 0.1.7 stops and disables the Linux timer and boots out the macOS
  `com.openabstractions.jobd` label. `supervisor.json`, `supervisor.json.tmp` and
  `supervisor.sock` stay reserved sink names. `service-jobd` publishes an
  archive README pointing here; archiving the repository is the owner's action.
  `docker-jobd` is a separate product and unchanged.
- **In history:** `6709771d` (source), `a5acedf7` (Linux units and macOS label),
  `6827f846` and `eeb255ac` (Windows payload and workflows). Earlier moves of
  the Windows host out of jobd are `9e87d7e2`, `957dec72`, `195c8985` and
  `25d91954`. The last published `service-jobd` is `v0.3.6`.

### 9. 2026-09-17, the config job-store locator and legacy loaders

- **Was:** Go `config.LegacyLoad`, `config.LegacyJobStore`,
  `config.LegacyWatchQuiet` and the `config.Subscription` type they returned.
  `LegacyLoad` read the
  configuration files and this process's `ABSTRACTION_*` environment;
  `LegacyJobStore` answered where the legacy job store lived (the configured
  `store`, an existing `~/.modelget`, or `~/.abstraction`).
- **Went because:** owner decision 2026-09-15, recorded in TODO.md ("0.1.8
  removes them"). Their callers were `jobd setup`, jobd's store root, the legacy
  download client's `Discover`, all removed.
- **Replaced by:** `config.LoadWithOverrides(values)` for a provider that passes
  its run overrides, and `config.WatchInvalidations` followed by a reread for
  change observation, which the config service already used. Applications
  resolve the config service. Python keeps `legacy_load`, `legacy_watch` and
  `legacy_job_store`, which the ComfyUI adopter's vendored download provider
  still calls.
- **In history:** `6709771d`.

### 10. 2026-09-17, the Python and C++ file-store libraries

- **Was:**
  - Python `abstraction_job` (`abstraction-job/python/abstraction_job.py`): the
    file job store, records, leases, ranges, recall and watch, with its
    `jobctl.py` driver and tests, packaged for PyPI and carried in the Windows,
    Linux and macOS developer files through 0.1.7.
  - Python `abstraction_download` (`abstraction-download/python/`): a download
    runner and client over that store, with `replay.py`, `specread.py`, its
    tests and a generated copy of the request codec, packaged the same way.
  - C++ `abstraction::job`, `abstraction::json` and `abstraction::job_layout`
    (`abstraction-job/cpp/`): store, record, ranges, the `supervisor.json`
    reader `discovery`, the awake lease, `jobctl.cpp` and tests.
  - C++ `abstraction::download` and `abstraction::download_runner`
    (`abstraction-download/cpp/`): runner, sink containment, fetchers, the
    `wanted/` reader, `specread.cpp`, `replay.cpp` and tests.
  - The harnesses that compared them with Go: `scripts/xlang-job.sh`,
    `xlang-download.sh`, `conformance.sh`, `verdict-conformance.sh`,
    `tiers.sh`, `lemonade-drives-it.sh`, the Python and C++ `obtain` projects
    and peer verdict drivers.
  - The resident broker `abstraction-model/python/resident.py`, a file-store
    program, with its tests and install scripts.
- **Went because:** owner decision 2026-09-15 (VISION.md): "yes. we have git
  history if what. note somehwere that we used to have it." Three
  implementations of a store that applications no longer reach made the file
  store look like the interface.
- **Replaced by:** the generated service clients in each language (`py/`,
  `cpp/abstraction/...`) resolved through the facade, and the runtime's job
  service. The generated acceptance and request targets remain. The Go store
  and runner remain as the runtime provider's persistence and execution, and
  `spec-conformance.sh`, `behaviour-conformance.sh` and the published suite
  judge the Go implementation. The ComfyUI adopter still vendors its own copies
  of the Python libraries.
- **In history:** `4533290e`. The last published sources are
  `abstraction-job` and `abstraction-download` at their `go/v0.4.4` commits.
