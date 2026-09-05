# Discovery over local IPC

The contract. Implementations are written against this file and must not need to
agree with each other about anything not in it.

Status: **amended from three implementations and one adversarial review.**

The first draft had seven places two implementers would diverge. Building it
found more, and one finding matters more than any individual fix: the author of
the Python and C++ clients reported that their agreeing on every cross-language
case was *nearly worthless as evidence*, because one person made a dozen
unstated choices the same way twice, within an hour, from one head.

That applies to this whole project. Agreement between implementations written by
the same author tests the author's consistency, not the specification. What
follows is written so a stranger cannot get it wrong, and the conformance
section names what would actually prove it.

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

**Out of reach.** A local endpoint should not cross to another machine, so when
the supervisor is a NAS the only thing both ends share is the store and
cross-machine discovery keeps the file. Removing the file path breaks the NAS.

**That is not true by default on Windows.** A named pipe is reachable remotely
as `\\host\pipe\name` over SMB unless it is created with
`FILE_PIPE_REJECT_REMOTE_CLIENTS`. The first draft asserted locality as a
property of the transport; it is a property of one flag, and an implementation
written from that draft would have published a remotely reachable endpoint while
the document explained that local IPC is the truthful case. Set the flag.

## Which layer owns it

`download`, alongside the supervisor concept. Go has discovery in `download`,
the C++ port put it in `job`; they must not stay split. Nothing in `job` may
depend on it.

## The endpoint

**The name is not derived from the store path.** Deriving it looked tidy and is
a trap: case folding, `\\?\` prefixes, short names, mapped drives versus UNC,
symlinks under `/private`, Unicode normalisation on HFS+, and a legacy store
alias each yield two names for one store. Every one fails silently.

Instead the supervisor publishes a name it invented once, in
`<store>/services/<machine>/<name>.json` — see `service-topology.md`, which is
normative for the registry's location and shape. **The earlier example showing
an endpoint inside `supervisor.json` is superseded**: that file is rewritten
wholesale by the heartbeat writer, so anything published there is erased within
one interval.

An entry contains:

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
capped at 104 bytes on macOS and 108 on Linux; a supervisor that cannot fit must
fail loudly at startup, never silently decline to listen. In practice the name
is 49 bytes, so with any ordinary runtime directory the cap is unreachable —
worth implementing, not worth designing around.

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

## The decision procedure

Two implementations written from the first draft agreed on every case, and the
author reported that as worthless evidence: one person made a dozen unstated
choices the same way twice. At least one plausible alternative ordering returns
**present** for an error object, which is the one answer that must never be
wrong. So the procedure is total and ordered, and an implementer transcribes it
rather than deriving it:

1. read the line; if it is not a single JSON object -> **absent**
2. `error` present -> **absent**
3. `store` missing, not a string, or not equal -> **absent**
4. `critical` present and not an array -> **absent**
5. any element of `critical` not a string, or not a name this client knows ->
   **incompatible**
6. otherwise -> **present**

`store` is compared **byte for byte, with no normalisation of any kind** — no
case folding, no `realpath`, no trailing-separator trimming. The supervisor
publishes the same string its callers hold, and that is the supervisor's
burden. Anything else re-enters the trap this document spends a section
refusing: a path is not an identity, and two spellings of one directory would
report absent forever on a correctly configured machine.

## Three answers, not two

A client returns `absent`, `present`, or `incompatible`.

- **absent** — nothing listening; also a partial line, EOF before the newline,
  malformed JSON, a line over 64 KiB (cap it; a hostile local process can
  otherwise feed unbounded bytes), a `store` that does not match the one the
  client opened, or any timeout.
- **incompatible** — **any well-formed answer this client cannot fully
  interpret**, not only an unknown `critical` name. A supervisor that changed
  the framing would otherwise report absent, and the operator would get no
  signal that something newer is running. Only the absence of an answer is
  absent. The caller must not hand work over; this is not an error to report.
- **present** — everything else.

A false `absent` is safe only because the lease prevents two owners, and costs a
transfer that could have been handed off. A client must never cache `present`.

## Rules

1. **Connect failure is the answer.** No supervisor is the ordinary case. Not a
   warning, not a log line above debug, never an exception the caller catches.
2. **The request is exactly `{"ask":"who"}` followed by `0x0A`** — no spaces,
   no other key order, because a server that string-matches will exist and the
   response framing is already pinned byte-exactly.
3. **One deadline of 200 ms** computed at entry and applied across connect,
   write and read together, measured on a monotonic clock. The registry read is
   outside it; a client cannot bound a file read anyway.

   **On POSIX this is not `SO_RCVTIMEO`.** Per-operation socket timeouts give
   connect, write and every read its own 200 ms, so a server stalling each
   phase in turn gets three budgets. One budget means non-blocking plus `poll`
   with a recomputed remaining.

   **On Windows it is overlapped I/O, and the part that bites is the cleanup.**
   After the wait times out, call `CancelIoEx` **naming the specific pending
   `OVERLAPPED`, never `NULL`** — a null pointer cancels only I/O issued by the
   calling thread, so a cancellation raised from any other thread silently does
   nothing and the buffer stays live. go-winio, mio and libuv all target the
   specific operation, and a patch passing `NULL` was rejected in tokio for
   exactly this reason.

   **Then wait for the cancellation to complete** before the `OVERLAPPED` and
   the read buffer go out of scope — the kernel may still be writing into both.
   Three mature implementations satisfy that invariant three different ways: a
   blocking wait on a channel, retiring the operation through the event loop's
   normal completion path, or holding the structure alive by reference count
   until the completion arrives. Any of them is conforming. Getting it wrong
   corrupts memory intermittently and no test catches it.

   **A worker thread with a timed join is not a substitute.** A thread blocked
   in a synchronous `ReadFile` on a pipe cannot be cancelled or killed, so that
   design leaks a thread and a handle per call, leaves the process unable to
   exit, and passes every test in the suite. `nDefaultTimeOut` on the pipe is
   also not a substitute: it bounds `WaitNamedPipe` and says nothing about how
   long a read may take.
3. **Answering touches no disk, no lease, no store.** The answer is what the
   process already knows about itself, so a supervisor whose store has become
   unreadable can still say it is alive.
4. **Only a supervisor listens.** Asking never creates an endpoint.
5. **The server applies its own per-connection deadline** — one second is
   ample — and must not let one connection block accept.

   **A deadline bounds duration, not count.** A local process opening thousands
   of connections and holding each just under the deadline is otherwise
   unbounded, so the server also caps concurrent connections in total and *per
   peer uid*. systemd's varlink, at this exact scale, uses 4096 global and 128
   per uid, the global figure self-tuning down from the process's descriptor
   limit. A per-uid cap is the part that matters: without it one hostile local
   user exhausts every slot for everybody else. A single local process
   that connects and never sends would otherwise push every other caller into
   its timeout, and every application on the machine reports absent.
6. **Only the server unlinks a stale socket** — a Unix rule, not a universal
   one. A Windows pipe has no stale state at all: the object dies with its last
   handle, so the conformance case below cannot be run on the primary platform
   and its Windows analogue is a different one, a registry entry naming a pipe
   that no longer exists. Unlink before bind, and only after its own connect to
   that path fails. A client that unlinks can remove a socket a
   supervisor bound a millisecond earlier, leaving it listening on an unlinked
   inode and undiscoverable — worse than the bug being fixed. Clients treat a
   refused connection as absent and touch nothing.
7. **Permissions.** Socket mode `0600`, owned by the supervisor's uid; on
   Windows a pipe DACL granting the supervisor's own SID. Version one requires
   the supervisor to run as the same user as its callers, and says so; a
   service-account supervisor is a later problem and must not be implied.
8. **Windows pipes.** Byte mode, not message mode. Always keep a pending
   `ConnectNamedPipe`.

   Create the first instance with **`FILE_FLAG_FIRST_PIPE_INSTANCE`**. Without
   it, `CreateNamedPipe` on a name another process already created *succeeds*
   and adds an instance to theirs — connections then land on whichever instance
   the kernel hands out, so anyone who has read the registry takes roughly half
   of every caller's traffic. The unguessable name protects the endpoint only
   until it is published; this flag is what makes the capability argument true
   on Windows rather than aspirational.

   Create with **`FILE_PIPE_REJECT_REMOTE_CLIENTS`**, or the endpoint is
   reachable over SMB from any machine that can route to this one.

   **Do not require `FlushFileBuffers`.** On a pipe it blocks until the peer
   drains, with no timeout, which contradicts the server's own per-connection
   deadline — and it is unreachable from a portable connection type that does
   not expose the handle. Instead: after writing, the server does not close
   until the peer closes or its deadline expires. Closing a pipe handle discards
   data the client has not read, and that is the failure being avoided.

   `ERROR_PIPE_BUSY` is **the healthy steady state**, not an edge case — a
   listener has an instance free only while it is inside accept. A client
   retries until its budget is spent, not once.
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
- **shutting the listener down while an accept is pending returns promptly** —
  run this on Windows against a real pipe. go-winio, a decade-mature library,
  still hangs on this today, and the fix proposed for it is the "wait briefly
  then give up" pattern rule 3 forbids
- **after sending an error object the server stops processing that request** —
  the error path looks correct in isolation and continuing anyway survives
  review, which is why Chromium tracks it as a recurring class rather than one
  bug
- a request that is exactly `{"ask":"who"}` with nothing after it before the
  `0x0A` parses — a token filling the whole buffer is the boundary a
  line-scanner gets wrong, and systemd shipped that bug for years
