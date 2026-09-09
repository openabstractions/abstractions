# One service or many

How services are named and addressed, so that whether one process serves
everything or a dozen serve one thing each is a deployment decision no client
depends on.

Status: **specified for clients, unspecified for operators.** An adversarial
review of the first draft found fifteen failures, four of them structural. The
corrections are below; the migration procedure is new and untested.

---

## The requirement that decides everything

Switching between one process and many must be cheap. That is only possible if
the number of processes never appears in the contract, so a caller asks for a
*service* and receives a *reference*, never a process.

That is a claim about **clients**. It is not a claim about operators, and the
first draft conflated them. A merge or a split is a procedure with an order, and
performing it wrongly costs real work — see *Changing topology*.

## Caller identity is the keystone

Every grant this project will ever make rests on a service knowing which program
is talking to it. Get that wrong and permissions are decoration: any process
claims to be the log viewer and receives the log viewer's authority. This has
already been observed in shipped code — a lease API that accepts an `owner`
string it never verifies, over a pipe any interactive user may write.

> **A server determines its peer's identity from the transport, at accept, using
> the operating system. It never accepts an identity asserted in a message.**

`SO_PEERCRED` on Linux (kernel-stamped at connect, unforgeable), an access token
via `ImpersonateNamedPipeClient` on Windows, an audit token checked with
`SecCodeCreateWithXPCMessage` on macOS. What each can prove differs — user and
process everywhere, *verified application* only on macOS and for packaged
Windows apps — and a service must be able to learn which answer it got rather
than assume the strongest. See `research/integration/caller-identity.txt`.

Identity is established **once, when the channel is accepted**, and carried for
that connection. It is never re-asked per request and never read from a field.

## The registry

A directory, not a document: `<store>/services/<machine>/<name>.json`.

Three reasons, each from a failure the first draft had:

**One file per name.** A single JSON object holding every service is claimed by
read-modify-write, which is a lost update rather than an exclusion. Two
supervisors starting together each write a map that never saw the other's entry,
and one of them is silently unpublished while running perfectly.

**Not in `supervisor.json`.** The heartbeat writer rebuilds that struct from
scratch and renames over the file every sweep, dropping any field it does not
know about; a clean stop removes the file outright. A registry there is erased
within one interval, permanently, while every service is still listening.

**Keyed by machine.** Endpoints are machine-local names. On a store shared by
two machines, each reads the other's entry, connects locally, finds nothing, and
concludes the holder is gone — a self-sustaining flip in which both machines
report absent forever and each does every transfer itself. A machine subtree
makes "no entry names another machine" structurally true instead of a rule
someone has to obey. The machine id is 128 bits generated at installation and
stored **outside** the store, so a restored backup or a cloned image does not
inherit one.

An entry is `{"name", "endpoint", "epoch", "renewed_at", "content", "critical"}`.
`content` and `critical` follow the job record's rule, because two carried copies
at different versions will meet in one store and the second one must be able to
refuse what it cannot read.

## Claiming a name

**Not by probing whether the holder answers.** The first draft said a name could
be taken when its endpoint did not respond, and cited the job store's lease as
precedent. That is backwards: the store never breaks a claim. It advances past
one, because a claim broken on a timeout is how two owners appear — which is
written in `store.go` in the store's own words.

So a claim carries a generation, exactly as a job's does. A claimant reads the
entry, creates `<name>.epoch.<n+1>` with `O_EXCL`, and only then writes the entry
naming that epoch. A holder refreshes `renewed_at` on the sweep it already
performs. A lapsed entry may be taken by advancing the epoch; a fresh one may
not be taken at all.

**Liveness of the endpoint answers "should a client connect". Liveness of the
entry answers "may I claim". Confusing the two is how two servers serve one
name** — and a 200 ms probe cannot distinguish a dead holder from one that was
descheduled, garbage-collecting, or busy inside its own accept loop.

## Endpoints

128 bits from the platform CSPRNG — `crypto/rand`, `BCryptGenRandom`,
`getrandom(2)` — and a process that cannot obtain them fails to start rather
than falling back. Never a time-seeded PRNG, never a hash of anything a caller
could compute.

That makes an independently generated collision impossible and leaves the
collisions that actually occur, all of which come from **copying** a value rather
than drawing one: a VM restored from a snapshot, a container cloned from an
image, a store restored from backup. So an endpoint is generated **at bind, held
only in memory, and republished on every renewal**. A process that has just
started has never seen its predecessor's endpoint and cannot inherit it.

Duplication is then caught rather than assumed away. On Windows a server binds
with `FILE_FLAG_FIRST_PIPE_INSTANCE` and treats `ERROR_ACCESS_DENIED` as "somebody
already holds this name" — **without that flag a second `CreateNamedPipe` on the
same name succeeds**, adds an instance, and requests are split at random between
two servers, which neither end can detect. On Unix a server that finds the path
bound and answering refuses, and may unlink only a path whose connect fails *and*
whose entry it holds at the current epoch.

## Reading the registry is not authority

The first draft claimed that because an endpoint can only be learned from the
store, being able to read the store confers the reference — a capability for
free. That is wrong. The registry is a **writable** file in a directory the
caller can write. An attacker does not predict an endpoint; it **assigns** one,
by rewriting the entry and binding what it published.

Until entries are signed by their holder — a signature over
`(name, endpoint, machine, epoch)` under a key generated at first run and held
with OS protection — the registry is an ambient-authority table, which is the
condition the capability model exists to remove.

> **No service that issues authority may be reached through this registry until
> entries are signed.** `abstraction.rights` in particular does not appear in it.

A supervisor refuses to start where `<store>/services/` is group- or
world-writable.

## Names

`abstraction.jobs@1` — namespace, service, major version.

Two unrelated projects that both carry this abstraction will resolve to the same
store by default and both publish `abstraction.jobs`. The result is not a visible
clash but a silent one: one project's client resolves the other's endpoint and
submits work into a foreign supervisor. Under a capability model there is nothing
to reject it, because the reference *is* the permission.

The `abstraction.` namespace belongs to this project. Everyone else uses a
reverse-DNS namespace they control, which needs no registry of names and no
coordination with anyone. The major version is in the name because two carried
copies at different versions must coexist in one store.

## The reference a client holds is the name

Not the endpoint. A client resolves before each request and caches only within
one request; that is what makes a split invisible, and it is why the registry
must be a small file per name rather than one document. A client meeting a
refused connection re-resolves exactly once before answering absent.

## Changing topology

Reversible does not mean instantaneous. There is an order and it is not
negotiable:

1. The new process starts and binds its endpoints, claiming nothing.
2. Each outgoing holder marks its entry `draining`; clients treat it as valid.
3. **Each outgoing holder releases every lease it holds.** Skip this and every
   in-flight job stalls for one lease TTL, which every caller can measure.
4. Each outgoing holder writes the successor's endpoint into its own entry at
   epoch n+1 and stops answering.
5. Outgoing processes exit.

A name is released by its holder, never taken from it. "Refuse to start if the
name is held" applies to a *contending* start; a successor presenting the
predecessor's epoch is a handoff and is allowed.

Absent does not mean "retry later" — for downloads it means the caller fetches
for itself. A stop-first migration therefore starts every client fetching
independently for the length of the gap.

## Where to split

A process holds the **union** of the privileges of every service in it, and
presents the union of their attack surfaces and failure modes. A deployment is
legal only if every service in it could hold that whole union — applied to the
transitive closure, not to pairs, because a merge of two legal merges is one
process.

- **Nothing merges with a service that issues authority.** Not logging, not
  config, not "almost nothing".
- **A merge is an availability decision.** N services in one process is one crash
  for N outages, and absent means callers do the work themselves.
- **A merge collapses caller identity.** Two services in one process share a pid,
  a token and a code signature, so nothing can tell their callers apart. That is
  a topology constraint arriving from below, and the reason "merging is free" is
  a claim about clients only.

## Tokens

An application says what it needs, is granted a reference to exactly that, and
presents it to the service it wants. It cannot ask for what it was not handed,
which removes the policy table and "may this app do that" with it.

A token is a capability, not a claim about identity. Nothing decides anything
from a name in a message. Platform identity enters exactly once, at the moment a
reference is first handed out — which is the accept-time identity above, and the
only place it applies.
