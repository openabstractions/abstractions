# Discovery over local IPC

The contract. Implementations are written against this file and must not need to
agree with each other about anything not in it.

Status: **specified, unimplemented.** An architectural review of the first draft
found seven places where two competent implementers would diverge, three of them
silently — both sides correct, different endpoint, and the caller simply reports
"absent". Those are fixed below.

---

## What this replaces, and what it does not

A program asks "is a supervisor alive on this machine?" by reading
`supervisor.json` and deciding whether its heartbeat is recent enough. That is a
liveness heuristic over a file, and it fails the way heuristics fail: a
supervisor died and its record still said `running` for the next thirty seconds.
Everything that read the store in that window was lied to.

Connecting to a socket does not have that failure. The connection succeeds, and
something is listening now, or it does not.

**Unchanged.** The store is still files, because records must outlive every
process and be readable by a machine that was switched off. This replaces
*discovery*, not the store.

**Out of reach.** A local socket does not cross to another machine. When the
supervisor is a NAS the only thing both ends share is the store, so
cross-machine discovery keeps the file. Local IPC is the truthful case; the file
is the one that crosses a network. Removing the file path breaks the NAS.

## Which layer owns it

`download`, alongside the supervisor concept. Go has discovery in `download`,
the C++ port put it in `job`; they must not stay split. Nothing in `job` may
depend on it.

## The endpoint

**The name is not derived from the store path.** Deriving it looked tidy and is
a trap: case folding, `\\?\` prefixes, short names, mapped drives versus UNC,
symlinks under `/private`, Unicode normalisation on HFS+, and a legacy store
alias each yield two names for one store. Every one fails silently.

Instead the supervisor invents a name once and publishes it in the file the
client is already reading:

```json
{ "endpoint": "abstraction-3f9a1c04e7b25d8613ca9f2076be4481", … }
```

128 random bits, hex. The client reads the store, takes the endpoint, connects.
No normalisation, no collisions, and the name is unguessable, so being able to
read the store is what confers the reference — a capability rather than an
ambient name any process could compute and squat.

| platform | endpoint |
|---|---|
| Windows | `\\.\pipe\<name>` |
| macOS, Linux | `$XDG_RUNTIME_DIR/<name>.sock`, else `/tmp/abstraction-<uid>/<name>.sock` created `0700` |

**Not Linux abstract sockets.** They carry no permissions at all — any uid in
the namespace may connect, which contradicts the permission rule — and they are
per-netns, so a container or Flatpak adopter cannot reach a host supervisor.

Where the fallback directory already exists with different ownership or mode,
the supervisor refuses to listen rather than using it. Unix socket paths are
capped near 104 bytes; a supervisor that cannot fit must fail loudly at startup,
never silently decline to listen.

## The protocol

One request, one response, connection closed. No streaming, no sessions, no
multiplexing.

UTF-8 JSON, one object, terminated by a single `0x0A`. No `\r`, no BOM, no
embedded newlines — a pretty-printed object is unframeable, and the existing
heartbeat writer indents, so anyone copying it produces a response the reader
cannot parse.

**Request** `{"ask": "who"}` — also `{"ask": "look"}`, meaning look at the store
now. That replaces the separate nudge socket; one daemon should not maintain two
sockets with two stale-file policies.

**Response**

```json
{
  "content": ["abstraction.discovery/base@1"],
  "critical": ["abstraction.discovery/base@1"],
  "owner": "jobd@host:9242",
  "host": "host",
  "store": "/volume1/docker/abstraction/store",
  "delegates_to": "here",
  "pid": 9242,
  "started_at": "2026-09-05T06:34:49.123456Z"
}
```

`started_at` is exactly the job record's timestamp format, six fractional
digits. `owner` is `program@host:pid`. `host` is required — the cross-machine
rule needs it. `delegates_to` is advisory; a client must work without
understanding it. An unknown `ask` gets `{"error": "unknown ask"}`; a malformed
request gets `{"error": "invalid request"}`. The server never closes without
answering, because silence and a hang are indistinguishable.

## Three answers, not two

A client returns `absent`, `present`, or `incompatible`.

- **absent** — nothing listening; also a partial line, EOF before the newline,
  malformed JSON, a line over 64 KiB (cap it; a hostile local process can
  otherwise feed unbounded bytes), a `store` that does not match the one the
  client opened, or any timeout.
- **incompatible** — a `critical` name the client does not know. The caller must
  not hand work over. This is not an error to report; it is a newer supervisor.
- **present** — everything else.

A false `absent` is safe only because the lease prevents two owners, and costs a
transfer that could have been handed off. A client must never cache `present`.

## Rules

1. **Connect failure is the answer.** No supervisor is the ordinary case. Not a
   warning, not a log line above debug, never an exception the caller catches.
2. **One deadline of 200 ms** computed at entry and applied across connect,
   write and read together. On Windows a pipe read cannot be interrupted
   synchronously, so a client must use overlapped I/O — a synchronous read
   against a server that accepts and never writes hangs forever, which is the
   failure this rule exists to prevent.
3. **Answering touches no disk, no lease, no store.** The answer is what the
   process already knows about itself, so a supervisor whose store has become
   unreadable can still say it is alive.
4. **Only a supervisor listens.** Asking never creates an endpoint.
5. **The server applies its own per-connection deadline** — one second is
   ample — and must not let one connection block accept. A single local process
   that connects and never sends would otherwise push every other caller into
   its timeout, and every application on the machine reports absent.
6. **Only the server unlinks a stale socket**, before bind, and only after its
   own connect to that path fails. A client that unlinks can remove a socket a
   supervisor bound a millisecond earlier, leaving it listening on an unlinked
   inode and undiscoverable — worse than the bug being fixed. Clients treat a
   refused connection as absent and touch nothing.
7. **Permissions.** Socket mode `0600`, owned by the supervisor's uid; on
   Windows a pipe DACL granting the supervisor's own SID. Version one requires
   the supervisor to run as the same user as its callers, and says so; a
   service-account supervisor is a later problem and must not be implied.
8. **Windows pipes.** Create with `PIPE_UNLIMITED_INSTANCES`, always keep a
   pending `ConnectNamedPipe`, byte mode not message mode, and
   `FlushFileBuffers` before disconnecting. A client meeting `ERROR_PIPE_BUSY`
   waits within its remaining budget and retries once rather than reporting
   absent, and opens with `SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION`.
9. **Nothing in the response authorises anything.** It is self-description. No
   field may become an identity a decision is made on.

## Cross-machine, as an algorithm

If `supervisor.json` names this host, IPC is authoritative and the heartbeat is
ignored entirely — including when IPC says absent. If it names another host, the
heartbeat freshness heuristic applies exactly as today. Never consult both and
combine them.

## Scope of the first implementation

**One server, in Go, inside the supervisor. Clients in Go, Python and C++.**

Only the supervisor listens, and the supervisor is Go. A serve-and-query matrix
across three languages is nine cross-pairs on three platforms for a capability
nobody has: there is no Python or C++ supervisor. Other servers arrive when a
non-Go supervisor does.

Platform order: **Windows, then macOS, then Linux.** Android and iOS are last
and may be impossible — a mobile application cannot host a background listener
on either platform under normal sandbox rules — so nothing here should be shaped
around them.

## Conformance

- each client, against the Go server, on each platform in turn
- connecting where nothing listens returns absent within the timeout
- a server that accepts and never writes is abandoned within the timeout —
  **run this on Windows against a real pipe**, which is where a synchronous
  implementation hangs
- a stale socket file left by a killed process reports absent, not an error
- an unknown `ask` returns the error object rather than a closed connection
- an unknown `critical` name returns incompatible, and the caller does not hand
  work over
- a response whose `store` differs from the client's returns absent
- two stores on one machine have different endpoints and do not cross-talk
- a line longer than the cap returns absent rather than consuming memory
