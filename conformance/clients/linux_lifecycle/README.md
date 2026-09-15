# Linux complete local lifecycle

Run `python3 run.py --help` and `python3 drive.py --help`. The slow path from a
Windows checkout is `scripts/check.sh --linux-lifecycle`. It runs `drive.py` as
root in WSL on a `git archive` of HEAD.

    python3 conformance/clients/linux_lifecycle/drive.py --run --archive SOURCE.tar --work /var/tmp/oa-lifecycle-run

`drive.py` builds the candidate Linux package, the installed C++ prefix, a
shared IPC prefix and the Python client packages from the archive. It downloads
the published redist v0.1.6 Linux tarball and SHA256SUMS, then runs `run.py`
with every scenario group. `--scenarios candidate,python,go,upgrade` on either
script selects groups; `drive.py` downloads the predecessor only for `upgrade`.

`go_client/main.go` has no module of its own in the repository. `drive.py` copies
it into a module, adds that module to the extracted tree's `go.work`, and builds
it with `GOPROXY=off`. The root `go.work` already uses
`conformance/faults/lostreply/go`, so the client imports the helper by its module
path.

`client.cpp` is the application. It is built once against an installed prefix
containing `abstraction_facade` (jobs only), `abstraction_ipc` and
`abstraction_download_request`, and the same binary runs every scenario. It uses
the default runtime bootstrap and installation-selected trust. It receives a
source URL, the expected artifact size and digest, and its own request identity.
It names no provider, private path, daemon or endpoint; the runner checks its
source for such tokens. On an unknown Submit outcome it reconciles the identity it
retained before submitting. `python_client.py` is the installed Python job client
with the same contract and output.

Everything else belongs to the fixture:

- `installer/posix/qualify_linux.py` supplies temporary nonadmin accounts, their
  linger records and systemd user managers, and removes and verifies them.
- `source.py` is a loopback HTTP source with a strong ETag, byte ranges, a
  throughput limit and a one-shot hold gate. It logs every request's range, bytes
  and wall-clock interval. Overlapping transfer intervals for one artifact would
  show a second writer. The gated artifacts are 3 MiB, below the downloader's
  32 MiB parallel-range threshold, so each writer uses one connection.
- `conformance/faults/lostreply/lostreply.c`, the shared lost-reply helper, is
  preloaded into the C++ application and into the Python client process. The
  service's reply to the first Submit arrives completely, is discarded, and the
  client sees a connection reset.
- The runner alone inspects private state: unit properties, `/proc/<pid>/exe`
  hashes and the managed job record count.

Scenario groups:

- `candidate` (account A): absence; installation and activation; resolution;
  acceptance; caller exit while work runs; lost acceptance reply; reconnect after
  a runtime restart; typed refusals; host crash with SIGKILL.
- `python` (account A): the installed Python job client loses its Submit reply
  through the shared helper and reconciles. Acceptance scope is the authenticated
  account plus program path, so the C++ application, another program of the same
  account, must fail to reconcile that receipt and must not read its bytes.
- `go` (account A): the installed-runtime Go job client selects the runtime with
  `bootstrap.SelectInstalled`, sends Submit through the job endpoint and
  installation trust wrapped by `conformance/faults/lostreply/go`, and reconciles
  through the facade `JobsClient`. It then observes and copies the exact result in
  a new process. The C++ application must fail to reconcile that receipt.
- `upgrade` (account B): install the predecessor, accept work, and upgrade to the
  candidate while the transfer is held. With `--predecessor-sums` the predecessor
  must match the published checksum.

The evidence directory receives `evidence.json` with a verdict per scenario, the
installer logs and `source-log.json`. Exit status is zero only when every selected
scenario passed and every account was cleaned up.
