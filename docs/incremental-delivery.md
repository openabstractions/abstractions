# Incremental delivery

Pulling from a delegate that has not finished yet.

Status: **not built.** This is the design path and its prerequisite, written
down so the prerequisite is not discovered halfway through.

---

## The observation

Today a delegated download is serial. The NAS fetches the whole artifact, marks
it `TRANSFERRED`, and only then does the local machine copy it over. Two
transfers end to end, and the wall clock is the sum:

    nas fetches (WAN)  ──────────────►
                                       local copies (LAN/WiFi) ──────►

Nothing forces that. The NAS has a **proven prefix** the whole time — it is in
the record already, as `checkpoint.verified_prefix`. A local reader could be
consuming that prefix while the NAS is still appending to it:

    nas fetches (WAN)  ──────────────►
      local follows        ──────────────►

This is what BitTorrent does, and [`research/transfer/libtorrent.txt`](https://github.com/openabstractions/research/blob/main/transfer/libtorrent.txt) already
says why it is the right shape: *"a content-addressed manifest of fixed-size
pieces, each independently verifiable, fetchable from any number of sources in
any order."* That note calls it the data model "this abstraction needs". It was
right.

## What it is worth — honestly

Less than it feels like. Overlapping two transfers saves the **shorter** of the
two, not the sum:

    saved = min(wan_time, lan_time)

If WiFi is the bottleneck, the WAN leg is the one being hidden, and the floor is
still the WiFi leg. The win is real on a slow uplink and a fast LAN, and much
smaller in the reverse case. Worth building, not worth over-promising.

The second benefit is less obvious and probably larger: **the delegate stops
needing to be a place bytes rest.** A NAS with 4 GB free could relay a 40 GB
model it can never store.

## The prerequisite: piecewise digests

This is the part that makes it a design change rather than a feature.

Today a spec carries one digest for the whole artifact:

```json
"artifact": { "digest": "sha256:4ffe3cb5…" }
```

That digest can only be checked when the last byte arrives. It is sufficient
while one participant fetches and then verifies. It is **not** sufficient for a
follower, because:

1. A follower consuming a prefix is trusting bytes it has not verified, on the
   word of a participant whose failure modes are exactly what the lease exists
   to contain.
2. If the artifact turns out to be corrupt, the follower learns at the end and
   has to discard everything — the pathology the whole checkpoint mechanism
   exists to prevent.

So incremental delivery requires the spec to carry per-piece digests, and the
record to say how far the *verified* prefix extends, not just the transferred
one. Those are different numbers and conflating them is the bug.

This is additive and fits the existing shape: a new content model, advisory to
readers that do not do incremental delivery.

    abstraction.download/pieces@1

A reader that does not understand it fetches whole, as now. A reader that does
may follow. Advisory, never critical — refusing an ordinary download because it
lacks piece hashes would be absurd.

## What it changes in the contract

**A readable prefix must be monotonic.** A follower relies on the prefix never
going backwards. Today nothing promises that: an owner that discards an unproven
tail legitimately moves its checkpoint *down*. Separating "transferred" from
"verified" is what makes the guarantee expressible — the verified prefix can be
promised monotonic precisely because it never includes an unproven tail.

**Following is not owning.** The follower takes no lease and writes no progress
to the delegate's record. One writer, unchanged. This stays outside the lease
for the same reason `intent` does.

**It is a `storage` question as much as a `download` one.** A partially complete
artifact with a verified prefix and known piece boundaries is a *source* — it is
just a source that grows. That is the same object `storage` already addresses by
content. The layering to avoid: `download` growing a private notion of pieces
that `storage` cannot read. If pieces are real, they are storage's unit and
download borrows them.

## Where it would be built

1. `job`: a `verified_prefix` distinct from `progress.done`, promised monotonic.
2. `storage`: piece boundaries and per-piece digests as a first-class manifest.
3. `download`: `abstraction.download/pieces@1`, and a source that can read a
   growing artifact from a delegate.
4. `nas`: serve a prefix — the smallest part, and the only one that is obviously
   just plumbing.

Steps 1–3 are contract changes in three languages. Step 4 is an afternoon. That
ratio is why this is written down rather than started.

## Prior art to design against

- [`research/transfer/libtorrent.txt`](https://github.com/openabstractions/research/blob/main/transfer/libtorrent.txt) — pieces, independent verification,
  recovery by re-hashing a partial with no control file at all.
- [`research/transfer/aria2.txt`](https://github.com/openabstractions/research/blob/main/transfer/aria2.txt) — multi-source fetch of one artifact.
- [`research/transfer/bits.txt`](https://github.com/openabstractions/research/blob/main/transfer/bits.txt) — what a system service does and does not expose
  about a partial transfer.
