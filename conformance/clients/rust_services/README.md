# Installed Rust logging services fixture

Run `python conformance/clients/rust_services/run.py --help`, then `--run` with
`--cmake` when CMake is outside PATH. Existing Rust/MSVC and Go toolchains are
required. The fixture installs static IPC into a temporary prefix and copies the
actual facade/logging/identity Cargo source packages, including generated rs
files. It builds this committed consumer manifest offline, without synthetic
package metadata or registry dependencies.

The outside consumer uses native bootstrap with an explicit fixture override,
resolves a production Go logging service and writes/reads an exact Unicode
record with attributes and receiver identity. It checks absent/unmet resolution,
a forged reference from a generated Go dispatcher, outbound schema refusal, cancellation after binding and during quiet resolver
I/O, and preservation of the original deadline. Crate unit tests reject forged
provider/contract/scope/guarantee/transport references and malformed history.
No Rust server, provider files or owner runtime are used by the client; its
isolated home must remain empty. The Go fixture owns and removes its history.

The runner measures Windows/MSVC only and cleans temporary binaries, source
copies and provider data. It makes no full Rust language-matrix claim.

The source fixture selects `rust-native` and `rust-logging` explicitly. The pure `rust` resolver core has no logging or native dependency.
