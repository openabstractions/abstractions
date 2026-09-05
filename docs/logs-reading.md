# Reading logs

What a cross-platform log-reading layer can honestly offer, and what it must
refuse to pretend.

Status: **specified from a survey, unimplemented.** The survey is
`research/logging/system-log-access.txt`. Every limit below comes from vendor
documentation, not from guessing.

---

## The one rule this layer exists to obey

> **An empty result must never be returned as "nothing happened".**

A query returning no entries has at least nine causes: nothing matched; the
reader was silently denied files it could not open; it is sandboxed; it is not
in the admin group; the content exists but every interesting field is redacted;
the entries were info-level and were never persisted; rate limiting dropped
them; they aged out of a circular buffer; they were evicted hours ago.

Windows can distinguish most of these. Linux can distinguish some. macOS can
distinguish almost none — and macOS is where the causes are most numerous.

A facade that answers `[]` converts *"I cannot see this"* into *"this did not
happen"*, which is the precise error a log viewer exists to prevent. **That
would make it worse than no facade at all.**

## The interface

```
capabilities() -> Capabilities
query(since, until, min_severity, source, text) -> Result<Iterator<Entry>, Denied>
tail(from: Position)                            -> Result<Stream<Entry>, Denied>

Entry {
    when:     Timestamp
    severity: Error | Warn | Info | Debug
    source:   String        // an opaque label, not a structured identity
    message:  String
    position: OpaqueToken   // resumable where the platform allows
    raw:      PlatformNative
}
```

`raw` is what keeps this honest. A caller needing the `.evtx` XML, the ndjson
entry, or the full journal field dictionary reaches through, and is not trapped
by the floor of the abstraction.

## Absence is a value, not an empty list

```
Denied | NothingMatched | Redacted | NotRetained | NeverPersisted | Unknown
```

Where the platform cannot say which, the layer **infers and says it is
inferring**: whether the caller is in the admin group, whether it is sandboxed,
whether private data is enabled, whether the oldest retained entry is already
newer than the window asked for. A probable reason with stated confidence beats
a confident empty list.

## capabilities(), and why it is not optional

The privilege ladders are not the same ladder with different rungs. They are
different shapes. On Windows, OS logs are readable by any interactive user and
the *security* log is gated. On macOS, **every** other-process log is gated by
admin-group membership, the App Sandbox forbids them outright, and there is no
separate security channel to gate. No single ladder can be presented, so the
layer reports what it can actually do, at runtime, under the privilege it
actually has:

- can I see other processes' entries
- can I see the security or audit log
- can I see before this boot, and how far back is retained
- is content redaction active
- is live tail real, or polled
- can dropped entries be detected

Without this a caller has to guess, and it will guess wrong on macOS.

## What is deliberately absent, and why

**Structured fields.** Linux carries arbitrary binary-safe key-values, Windows
has named `EventData` resolved from publisher metadata, and macOS has
`subsystem` and `category` and nothing else. A `fields` map would be a lie on
one platform in three. Reach through `raw`.

**A lossless severity scale.** macOS has no Warning level. That is missing
information, not a mapping problem, so the common scale is four buckets and the
loss is documented rather than hidden.

**Guaranteed message text.** On Windows the template lives in the provider's
resource DLL and on macOS in the shared string cache; both resolve at read time
and both can fail. Only Linux stores the formatted text. `message` is
authoritative on one platform and best-effort on two.

**A security-log abstraction.** It is a capability flag the caller asks for
explicitly, never something the layer quietly includes or quietly omits.

**Exactly-once subscription.** Windows has bookmarks, Linux has cursors, macOS
has no public subscription API at all — every shipping tool polls or spawns
`log stream`, which drops under load.

## macOS, stated plainly

Three limits no privilege we hold can remove:

1. **Runtime-interpolated values are logged as `<private>`.** Only
   compile-time-constant format strings survive, unless the author of the
   logging call opted in. Lifting it needs a configuration profile installed by
   MDM or root — **and it is not retroactive.** Data already redacted is gone.
2. **`debug` is not captured and `info` is memory-only** by default. `log show`
   legitimately cannot show what was "logged".
3. **Typical retention is 8 to 20 hours.** "What happened last week" is
   answerable on Windows and Linux and not here.

A privileged service can grant *access*. It cannot un-redact, cannot resurrect
what was never persisted, and cannot extend retention backwards.

## Why nobody has done this

OpenTelemetry has receivers for all three: all alpha, and two of the three
shell out to `journalctl` and `log`. Vector and Fluent Bit and Elastic each
cover two platforms and skip macOS. osquery — the closest thing to a real
facade, a single SQL surface over all three — has **no journald table at all**
and forces platform-specific `WHERE` clauses through the abstraction.

Every one of those has money, motivation and years. That is not an accident of
effort; the platforms disagree about what a log entry is. It is also why the
small honest version is worth building: the alternative everyone reaches for
today is a platform switch plus three subprocesses.
