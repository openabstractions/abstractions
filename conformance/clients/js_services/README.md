# Installed JavaScript service proof

Run `python conformance/clients/js_services/run.py --help`, then `--run --race`
with `--cmake` pointing to the installed Visual Studio CMake where needed.
The Go race build requires a configured C compiler. The current fixture targets
Windows x64/MSVC and an existing Node runtime. It downloads matching official
headers and node.lib into a temporary directory, verifies SHA256 against the
official release manifest, and removes them afterward. `--node-sdk` reuses a
caller-supplied previously verified directory; the fixture leaves that directory
under caller ownership.

The fixture builds the existing IPC library and Node-API addon, uses real
`npm pack` artifacts, and installs them with offline npm, scripts disabled, into
an isolated consumer tree. The installed packages are IPC, pure facade, logging, job acceptance, storage
content, config, rights, asks and download request vocabulary. Capability API
packages contain generated code and package metadata, with no native dependency.
All npm configuration/cache and native build artifacts stay in temporary paths.
The default addon import is package-relative, with the separate IPC library
linked statically. No global package install or service registration occurs.

The pure connector test covers reusable default waiting, fixed deadlines,
cumulative composite budgets and reference refusals with no native load. The
installed consumer writes an exact Unicode log record and reads it from the
production Go runtime. It checks typed invalid limits, unmet guarantees,
cancelled binding reuse, absent runtime and forged reference refusal. Private
peers exercise malformed, oversized and truncated replies plus timeout and
cancellation. With one libuv worker, queued calls cancel/expire while a prior
call remains blocked. Runtime data stays in the Go fixture's private directory.

The job consumer constructs a typed generated download Request, submits once,
reconciles the same receipt at the fixed selected endpoint, observes completion,
and reads 150000 result bytes in three bounded binary chunks. It validates owner,
request identity and requested guarantees across replies. Job inventory reports
the same receipt. The storage consumer reads the same-sized fixture through an
explicit receiving-program/digest policy, checks invalid range and forbidden
content, then releases the resource and observes its gap. Provider-private paths
are confined to fixture setup. Application calls receive only endpoints, source
URL and digest.

Config, rights and asks packages receive installed pure-import checks here;
their service behaviors are not exercised by this fixture. This evidence supplies
no browser, other-OS or public-release verdict. One-way logging remains
transport submission; it is not a durable receipt.
