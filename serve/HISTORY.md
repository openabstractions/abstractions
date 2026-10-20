# OpenAbstractions runtime contract history

This page keeps what the runtime's [CONTRACT.md](CONTRACT.md) replaced, the
rule ids it retired, and the downgrade notes `RUN-U2` requires.

## 0.8.0 remote leaf-key admission

Remote declarations require `remote.server_key_sha256`, the 64 lowercase hex
digits of SHA-256 over the server leaf certificate's DER SubjectPublicKeyInfo.
`provider add --remote` accepts it as `--server-key-sha256`. Existing CA-only
declarations are refused until the operator supplies the explicit paired key.
Removing, disabling or replacing a declaration invalidates retained clients
at the next handshake. The roots, server name and client credential remain
required. The pin survives registry@2 reads and provider-file persistence;
registry@1 retains its published five-field trust projection.

## Retired rule ids

| retired | release it left | replaced by |
|---|---|---|

No rule id has been retired.

## Where the rules came from

The contract was written in October 2026 (plan package W0-05b). Before it, the
runtime's rules lived in four places:

- `serve/SUPERVISION.md`: the supervised READY/EOF protocol, the Windows host's
  restart policy and the 0.3.0 downgrade note. Its rules are `RUN-S1` to
  `RUN-S5` and `RUN-U1`; its downgrade note is below.
- The help pages of `serve runtime`, `start` and `status`: flags and exit codes,
  now indexed under the contract's command line index and `RUN-E1`.
- Source comments in `serve/endpoint.go`, `serve/profile.go` and
  `serve/upgrade.go`: the endpoint rule (`RUN-N3`), the packaged-profile
  refusal (`RUN-R6`) and the upgrade exclusion (`RUN-U1`).
- The runtime audit of the 0.8.0 plan
  (`research/release-0.8.0/audit/runtime.md`, moves P0-1 and P1-6), which
  listed the topics a contract had to state.

## Downgrade notes

### Questions written after commit `51b4343b7`

A runtime built before commit `51b4343b7`, 0.3.0 included, does not read
`asks/application.json` in the runtime's state directory once a later runtime
has written it: question records carry the requester's publisher, and the
earlier reader refuses unknown fields (asks `ASK-P1`, `ASK-A6`). To downgrade,
stop the runtime and delete that file. Pending questions and the answered
question history are lost. The rights rules that answers wrote stay in the
rights policy and are unaffected.
