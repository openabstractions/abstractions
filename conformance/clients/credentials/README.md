# Credentials holder, resolved through the runtime

Run `python conformance/clients/credentials/run.py --help`.

`--unit` runs the credentials Go module tests on this host. On Windows they
write, read and delete real Credential Manager items of the current user under
a test-unique store namespace, and delete everything under it afterwards. On
Linux they cover the opt-in 0600 file backend, and they start a private
`dbus-daemon` when one is installed: authentication and marshalling against the
real bus, and the headless case, where a bus without an
`org.freedesktop.secrets` owner reads `unavailable`. A Secret Service round trip
runs only when `gnome-keyring-daemon` is installed. From Windows,
`python conformance/clients/wsl_run.py credentials --unit` runs the Linux leg.
The service package tests drive the holder and applier over real framed IPC and
search 400 random replies to an application peer for every registered secret.

`--run` hosts the credentials service in the facade runtime beside a real rights
decision service and the platform store (Credential Manager on Windows, the file
backend on Linux) in a test-unique namespace, resolves
`abstraction.credentials/holder@1` and `abstraction.credentials/applier@1`, and
checks through the typed Go clients:

- `Store` is `forbidden` before a `holder.manage` rule, then `stored`;
- `Apply` for an application subject is `not_permitted` before an `apply` rule
  and `applied` with the bearer header after it; `Check` agrees;
- a wrong consumer contract is `consumer_refused` and a wrong host
  `target_refused`, both without headers;
- rotation changes the next request's bytes, and a stale revision conflicts;
- `Revoke` leaves a tombstone that `Apply` reads as `revoked`;
- the audit trail holds the stored, refused, checked, applied, rotated and
  revoked events with their exact outcomes;
- a machine-scope holder answers `Store` with `no_secure_store`;
- with the rights service stopped, `Apply` and `List` are `unavailable`;
- neither secret appears in list or audit replies, reported errors or the
  runtime log.

The designated consuming service is the fixture process. Four application
programs then resolve both contracts through their language's facade: an
installed C++ consumer (`find_package(abstraction_facade)` and
`find_package(abstraction_credentials_api)`, `resolve_credentials`), a
JavaScript consumer (`Machine.resolveService` with the generated
`HolderClient` over the test-only pipe connector), a Python consumer
(`Machine.resolve_credentials` over the shared `abstraction_ipc` library) and a
Rust consumer (`CredentialsMachine` over `abstraction-facade-native`). Each reads
`List` as `forbidden` and `Apply` as `forbidden`; the C++ consumer reads a
metadata page after a `holder.read` rule for its own program. No reply carries a
secret. `--no-cpp`, `--node ""`, `--no-python` and `--no-rust` skip a consumer.
