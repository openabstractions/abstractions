# Security review: @parcel/watcher 2.5.1 plus PR #277, OA Windows x64 build

Reviewed 2026-11-10 for the OpenCode with OA installer, which replaces the
official OpenCode Desktop's stock `watcher.node` with this build (owner
direction 2026-11-10: "just pick existing ones or ours to patch our oc build").

Independence: the implementer of the installer change, who picked the
artifact, drafted the first version. The OpenCode stream's quality reviewer
(Opus 5.5, high effort), who had no part in the choice, checked every claim
below against the sources and a rebuild, wrote this file and gives the verdict.

## Artifact

| Field | Value |
| --- | --- |
| File | `watcher.node`, shipped in the OpenCode with OA MSI as `desktop-fix/watcher.node` |
| SHA-256 | `0251ee3ba7027af84a0cb8e0c2ffa1c5989eacf21bc091f95a7c5c00cc486e12` |
| Size | 528,896 bytes |
| Debug record | CodeView `watcher.pdb` (relative; no builder path) |
| Licence | MIT, `desktop-fix/LICENSE`: upstream `LICENSE` at `v2.5.1`, SHA-256 `5cfba9d5711264d45a2431a53c79f597cef12720cc7c764db2602daa92d0bd2a`, 1,077 bytes; the stock npm package ships the same bytes |
| Replaces | `resources\app.asar.unpacked\node_modules\@parcel\watcher-win32-x64\watcher.node` of the official Desktop 1.18.34 and 1.19.5 |
| Stock file | npm `@parcel/watcher-win32-x64` 2.5.1, SHA-256 `a8199cf7b6c5102267a5889f80321a4cc92e631d7504c474d32a35b4d440f315`, 518,144 bytes, Authenticode `NotSigned`; the same bytes in both Desktop releases |
| Earlier OA build | `9ec8ac8abf41c8ff2a7cc2298961f2165e14939e8e3a6d9d2aa49757697e4fed`: the same code built without `/Brepro`; its debug record named the builder's absolute checkout path. The installer replaces it and restores the stock bytes over it on Remove. |
| Pin owners | `adopters/opencode/installer/build_installer.py` (`DESKTOP_FIX`, `DESKTOP_RELEASES`), `desktop_state.h`, `installer/windows_signing.py` `opencode_content_policy`, Setup's `openCodeWatcherFixup` in `monitor/internal/setup/catalog.go` |

## Provenance

Checked on GitHub on 2026-11-10:

- Base: tag `v2.5.1` of `github.com/parcel-bundler/watcher` resolves to commit
  `664695dbb2488d53f782c3e2b8791c4c114731fb`, the version both Desktop
  releases ship.
- Fix: parcel-bundler/watcher#277, "Windows: keep a subscription alive until
  its pending read completes", by `lokicik` (Lokman Baturay Efe), Fixes #262.
  The pull request is open and unmerged. Its head is
  `d09df163078cd54e422c8fbacacbf0359be3f52e` on
  `lokicik/watcher:fix/windows-unsubscribe-use-after-free`, one commit over
  parent `b46c53fe`.
- OA build commit: `052dc8d70871bc73d8ea1a30675f23347d144d98`, #277's commit
  cherry-picked onto `v2.5.1` (branch `oa/v2.5.1-pr277` in
  `.forks/opencode-patched/watcher-src`). Its diff against `v2.5.1` touches
  `src/windows/WindowsBackend.cc` (+29/-5) and `test/watcher.js` (+39).
- `src/windows/WindowsBackend.cc` is byte-identical at `v2.5.1` and at #277's
  parent `b46c53fe`. The patched file's blob `e5741049edb336552c58acf0b4ad1e6ea8fe9248`
  is identical at `d09df163` and at `052dc8d7`. The shipped Windows source is
  exactly upstream 2.5.1 plus #277.
- Unsigned, like the stock npm binary it replaces.

## Build inputs

- Source tree: `052dc8d7`, clean.
- node-gyp 12.4.0 (the copy bundled with npm), `node-gyp rebuild --release`.
  It compiles its own `src/win_delay_load_hook.cc` into the module.
- Node 24.19.0 headers and `x64/node.lib` from the node-gyp header cache.
- node-addon-api 7.0.0 headers, installed against `yarn.lock` integrity
  `sha512-vgbBJTS4m5/KkE16t5Ly0WW9hz46swAstv0hYYwMtbG7AznRhNyfLRe8HZAiWIpcHzoO7HxhLuBQj9rJ/Ho0ZA==`.
- MSVC cl 19.51.36256, link 14.51 (Visual Studio 18), node-gyp's Release
  flags: `/MT /O2 /GL /GS /EHsc`, `NAPI_DISABLE_CPP_EXCEPTIONS`.
- Environment for a reproducible image: `CL=/Brepro` and
  `_LINK_=/Brepro /PDBALTPATH:watcher.pdb`.
- The image imports `KERNEL32.dll` and delay-loads `node.exe` (static C
  runtime). DLL characteristics: dynamic base, high-entropy VA, NX; no
  `/guard:cf`, the same as the stock build. `installer/runtime_imports.py`
  passes.

## Reproducibility

- The reviewer rebuilt `052dc8d7` with the inputs above in two different
  folders. Both builds are byte-identical to the pinned file:
  `0251ee3b…`, 528,896 bytes.
- Anyone with the same toolchain can rebuild the module from the recorded
  source and inputs and compare it bit for bit. The pinned hash binds the
  installer to exactly those reviewed, rebuildable bytes.
- The earlier build `9ec8ac8a…`, rebuilt without `/Brepro` at a path of the
  same length, differed from its pin only in the PE and debug timestamps, the
  CodeView GUID and the PDB path text. All code and data were identical.
- `build_installer.py` `desktop_fix_payload` refuses a watcher whose CodeView
  record names an absolute builder path.

## The change

The stock backend frees a `Subscription` on the libuv worker thread in
`unsubscribe()` while its `ReadDirectoryChangesW` read is still pending on the
backend thread. `CancelIo` cancels only I/O issued by the calling thread, so
the read survives, and its completion routine later writes into freed memory.
OpenCode Desktop resubscribes on idle and reload, and this crashes its server
process.

#277:

- makes `Subscription` `enable_shared_from_this` and holds a self reference
  (`mSelf`) while a read is pending; the completion routine moves that
  reference into a local, so the object outlives `processEvents` and a
  following `poll()` takes a fresh one;
- clears `mSelf` when `ReadDirectoryChangesW` fails immediately;
- passes the start-polling asynchronous procedure call a heap `shared_ptr` it
  deletes after `run()`;
- queues `stop()` to the backend thread from `unsubscribe()`, so `CancelIo`
  runs on the thread that issued the read, and the cancelled read's completion
  drops the last reference there.

Findings:

- Every new heap `shared_ptr` is deleted on both the success and the
  `QueueUserAPC` failure path.
- The start and stop calls run in queue order on the backend thread, so `run`
  precedes `stop` for the same subscription.
- `unsubscribe` runs only for a watcher in `mSubscriptions`, which `subscribe`
  entered after setting its state; the stop call never receives an empty
  pointer.
- When the stop call cannot be queued, the pending read keeps the object alive
  until it completes; the result is at worst a leaked subscription.
- No new file, network, registry, process or environment access. No new
  dependency. No change outside the Windows backend except the test.

## Vulnerabilities and behaviour

- Known vulnerabilities: the GitHub advisory database lists no advisories for
  npm `@parcel/watcher` or `node-addon-api` (queried 2026-11-10).
- Intentional behaviour: none found. The module watches directories its
  caller names and reports changes; it opens no network connection, starts no
  process, carries no updater and runs no install script.
- Writable paths it runs from: the module loads from the Desktop's per-user
  program folder, as the stock file does. The MSI's copy under
  `Programs\OpenAbstractions\apps\opencode\desktop-fix` is copied with hash
  verification and never loaded.

## Licences

- `@parcel/watcher`: MIT, Copyright (c) 2017-present Devon Govett. #277 is
  contributed under the repository's licence. OA ships upstream's `LICENSE`
  bytes beside the module.
- Compiled in: node-addon-api headers (MIT) and node-gyp's delay-load hook
  (MIT). The stock npm package ships only watcher's `LICENSE`; OA follows it.
- The MSVC static runtime is linked under Visual Studio's redistribution terms.

## Validation

Electron 42.3.3, the Desktop's runtime (`ELECTRON_RUN_AS_NODE`), a
subscribe, hold and unsubscribe loop of 200 rounds over a tree a child process
churns (`.forks/opencode-patched/repro.mjs`, outputs in `runs/`):

- stock 2.5.1: crashed in 3 of 3 runs;
- the earlier OA build with identical code: passed 8 of 8 in 1.18.34's
  runtime and 2 of 2 in 1.19.5's;
- this build, `0251ee3b…`: passed 1 of 1.

## Known limits and not verified

- Stock 2.5.1 keeps a use-after-free that #277 leaves unchanged. When the last
  subscription fails on the backend thread (for example with
  `ERROR_NOTIFY_ENUM_DIR`), `handleWatcherError` calls `unwatch`, whose
  `removeShared` destroys the backend from its own thread. `unwatch`'s lock
  release and `start()`'s loop then touch the freed object.
- Subscriptions still pending when the backend thread exits stay allocated
  until process exit.
- Upstream publishes no build of #277 to compare against.
- Windows on Arm: only `win32-x64` is replaced.
- The bundled CLI `opencode.exe` embeds its own watcher; this replacement
  covers the Desktop only.

## Verdict

approved with conditions:

1. The installer replaces the Desktop's file only when it carries a listed
   stock hash or an earlier OA build's hash. It leaves this build's own file,
   any other file and a missing file unchanged.
2. Rollback and Remove restore the verified stock bytes kept beside it.
3. The shipped bytes are the reproducible build `0251ee3b…` recorded above.
4. The swap runs only at ordinary, non-elevated integrity (`user_token()`
   refuses an elevated token).
5. Retire this build when an upstream `@parcel/watcher` release containing
   #277 ships in an OpenCode Desktop OA admits.
