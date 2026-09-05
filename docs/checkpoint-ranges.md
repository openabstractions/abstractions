# A checkpoint of ranges

The design change that lets this library describe a download somebody actually
runs.

Status: **specified, unimplemented.** Additive — nothing already written breaks.

---

## What is wrong now

```go
type Checkpoint struct {
    VerifiedPrefix int64
}
```

One integer. It says "the first N bytes are proven" and it cannot say anything
else. Every download this library can describe is therefore a single stream
appending to the end of a file.

That is not how downloads are fetched. Ollama runs sixteen concurrent ranged
parts landing at scattered offsets in a sparse file; aria2, `hf_transfer` and
`s5cmd` all do the same thing. An investigation of Ollama as an adopter
concluded that taking this library would mean **deleting their parallelism** —
because "parts 0, 2 and 5 done, 1, 3 and 4 at forty percent" has no
representation here.

Three separate investigations arrived at the same fix. Following a delegate's
partial mid-transfer needs pieces. Our own libtorrent survey, written weeks
earlier, called a manifest of independently verifiable pieces *"the data model
this abstraction needs"*. And parallel ranges cannot be a prefix.

## The change

A checkpoint carries a set of proven ranges. The single prefix becomes the
degenerate case of one range starting at zero.

```json
"checkpoint": {
  "verified_prefix": 4194304,
  "verified": [[0, 4194304], [8388608, 12582912], [20971520, 23068672]]
}
```

`verified_prefix` stays, and stays correct: it is the end of the range starting
at zero, or 0 if there is none. **A reader that knows nothing about `verified`
still works** — it resumes from the prefix and re-fetches the rest, which is
exactly what it does today. That is what makes this additive.

Ranges are half-open, non-overlapping, sorted, and merged on write, so there is
one canonical form and two implementations cannot disagree about how to spell
the same state.

Declared as a content model, so a reader can refuse what it cannot handle:

    abstraction.download/ranges@1

**Advisory, never critical.** An old reader ignoring `verified` re-downloads
some bytes; it does not corrupt anything. Marking it critical would break every
existing reader for no safety gain.

## What a range means

**Proven, not merely written.** A range enters the set when its bytes are on
disk *and* checked — against a piece digest where the spec carries one, or
against the transport's own framing where it does not. Bytes in flight when a
process is killed are exactly the ones a successor must not trust, and that rule
does not change; it now applies per range instead of to one tail.

Without piece digests, a range is only as trustworthy as the transport that
wrote it. That is the same honesty the prefix has today, applied in more places,
and it is why `docs/incremental-delivery.md` wants piece digests — with them, a
range is verifiable by anyone, including a second process reading a partial
somebody else is still writing.

## What it unblocks

- **Parallel fetchers.** A runner can hand out ranges and record each as it
  lands. The library stops being single-stream by construction.
- **Piecewise digests**, which need somewhere to record which pieces passed.
- **Following a delegate mid-transfer** — the case in
  `docs/incremental-delivery.md` that has no home today.
- **A resume that skips what a parallel downloader already fetched**, rather
  than re-fetching everything after the first gap.

## What it costs

**A sparse file is not a contiguous one.** Truncating to a prefix and appending
is simple and hard to get wrong. Writing at offsets means the file must be
preallocated or the filesystem must support holes, `.part` size no longer
implies progress, and a reader must never infer completeness from length.
Today's rule — take the minimum of the checkpoint and the file's real size — is
a prefix-shaped rule and does not generalise. **It must be replaced, not
extended**, and that is the part most likely to be got wrong.

**Two writers, one file.** Ranges make concurrent writes to one artifact
expressible, which does not make them safe. The lease still admits one owner; a
parallel fetcher is one owner with several connections, not several owners.
Nothing here changes that, and an implementation that reads it as permission for
two owners has misread it.

## Order of work

1. `job`: the field, canonical form, merge-on-write, the content model. Go
   first, then Python and C++ — the record is a cross-language contract.
2. `download`: record ranges instead of a prefix; replace the min-with-file-size
   rule.
3. A parallel fetcher, which is the point and should come last, once the state
   it needs is expressible.

Until step 1 lands, this library fits single-stream callers only, and the README
should say so plainly rather than letting an adopter discover it by trying.
