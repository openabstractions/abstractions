# OpenAbstractions 0.2.0

Released September 21, 2026. [Downloads and release notes](https://github.com/openabstractions/redist/releases/tag/v0.2.0).

## What you can use

- **Keep work running:** submit durable jobs and verified downloads, then observe,
  cancel or retrieve their results after reconnecting.
- **Connect AI providers:** use chat, embeddings, transcription, speech, images and
  live voice. Video generation and image batches use durable jobs. The service
  selects an eligible configured provider and applies authorized credentials.
- **Keep secrets with the service:** manage named credentials and let authorized
  services apply them without giving the stored secret to the calling application.
- **Connect applications:** discover registered applications, request activation,
  and bind interaction to a specific live instance and document context. The
  separately built MCP gateway connects compatible assistants to authorized OA tools.
- **Manage the shared runtime:** use the CLI and Windows Panel to inspect work,
  permissions, questions, configuration and activity.
- **Build in five languages:** Go, C++17, Python, Rust and JavaScript SDK sources
  expose capability contracts and typed options. SDK versions are independent of
  the runtime package; this release makes no npm, PyPI or crates.io publication claim.

## Downloads

| Platform | Package | Qualification |
| --- | --- | --- |
| Windows x64 | [MSI](https://github.com/openabstractions/redist/releases/download/v0.2.0/abstraction-x64.msi) | Per-user and machine installation, upgrade, rollback, startup, crash recovery and removal passed |
| Windows ARM64 | [MSI](https://github.com/openabstractions/redist/releases/download/v0.2.0/abstraction-arm64.msi) | Cross-built; installation on ARM64 hardware untested |
| Linux amd64 | [Tarball](https://github.com/openabstractions/redist/releases/download/v0.2.0/abstraction-0.2.0-linux-amd64.tar.gz) | Hosted Ubuntu installed runtime, real download, result observation and removal passed; no-user-manager install/removal also passed |
| Linux ARM64 | [Tarball](https://github.com/openabstractions/redist/releases/download/v0.2.0/abstraction-0.2.0-linux-arm64.tar.gz) | Cross-built; installation on ARM64 hardware untested |
| macOS universal | [Package](https://github.com/openabstractions/redist/releases/download/v0.2.0/abstraction-0.2.0-macos-universal.pkg) | Signed, notarised and stapled; protected service calls retain the caller-identity limitation |

Windows and Linux packages are unsigned. The final signed macOS package has no
end-user installation qualification; separate local lifecycle checks do not
establish protected capability readiness.

## Exact release evidence

| Check | Public evidence |
| --- | --- |
| Runtime build and tests | [Run 35568875030](https://github.com/openabstractions/abstractions/actions/runs/35568875030) |
| Installer candidate and lifecycle checks | [Run 35572834195](https://github.com/openabstractions/redist/actions/runs/35572834195) |
| Final package builds, verification and release tag | [Run 35573550787](https://github.com/openabstractions/redist/actions/runs/35573550787) |
| Published artifact hashes | [SHA256SUMS](https://github.com/openabstractions/redist/releases/download/v0.2.0/SHA256SUMS) |

The runtime commit is `a1763f0cbc6e287050c71fdd11d12b6315ffedf6`.
The redist tag targets `bec9612e439303487a24f7b27aeb9ac98c2b2bee`.
All five downloaded packages matched the published checksums after the workflows
passed. Installation evidence has the scope shown above; capability and language
checks retain their own revisions in [coverage](https://openabstractions.org/coverage.html).

## Upgrading from 0.1.7

The shared `openabstractions` runtime replaces `jobd`, `dl` and `jobctl`.
Use `openabstractions download` and `openabstractions jobs` for new work.
Old file-store downloads still in flight are abandoned; their records and partial
files remain. [Migration details](https://github.com/openabstractions/redist/releases/tag/v0.2.0).

The regenerated SDKs use named choices for closed options. Existing JSON wire
words remain unchanged. In Go use enum constants such as `facade.ScopeLocal`
and `.String()` to obtain a wire word. Rebuild integrations against matching
published module versions.
