# Using other people's code

The policy, not the record. Written 2026-09-05, before the first adoption
from Chromium, go-winio, or anything else outside this organisation — while
it is still a rule applied to a hypothetical, not a rule applied
retroactively to a fight already in progress. The record itself is
[`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md); that file explains the
three cases (copied code, a dependency, a learned technique). This file
covers only case 1 and case 2 — taking something with a licence attached.
Case 3, reading and learning, has no legal gate; cite it and move on.

## What is acceptable to take code from

**Permissive, and fine:** MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC.
Every one of these asks for the same two things — keep the copyright
notice, and don't claim you wrote it — and none of them reaches forward into
the licence of the project that takes the code. Apache-2.0 additionally
carries an explicit patent grant, which the others don't, but that only ever
gives you more than MIT/BSD/ISC give you, never less. Taking from a
permissively-licensed project needs no special approval beyond following the
steps below.

**Not acceptable, without a licensing conversation first:** GPL (any
version), LGPL, AGPL, and anything shipped with no licence at all.

- **No licence at all is not "public domain."** Code without a licence file
  is "all rights reserved" by default in every jurisdiction this project
  operates in. There is nothing permissive about silence.
- **GPL and AGPL** are copyleft: code that links against them, in the
  legal sense of "forms a combined work," has to be relicensed under the
  same copyleft terms if you distribute it. That is incompatible with
  shipping Apache-2.0 libraries — it would mean either relicensing this
  project's own code or not shipping the combined result at all.
- **LGPL is the one people think is safe and isn't, here, specifically.**
  LGPL's compromise is: dynamic linking is fine, static linking obligates you
  to let the end user re-link against a different version of the LGPL
  component (typically by shipping your object files or an equivalent).
  That compromise assumes a runtime environment built around shared
  libraries. Two of this project's three languages don't have one:
  - **Go statically links by default, as a deliberate, permanent design
    decision of the Go toolchain** — not a build flag anyone here chooses.
    There is no dynamic-linking escape hatch to stay on the easy side of
    LGPL's line.
  - **C++ template and header-only libraries are "compiled into your
    program," not linked against, for LGPL's purposes** — the moment an
    LGPL header is included and instantiated, the analysis collapses into
    the same one as static linking, regardless of what the `.so` situation
    looks like.

  So "it's only LGPL, not full GPL" does not make a dependency safe for
  `abstraction-job`'s C++ or any Go module in this organisation. Treat it as
  copyleft for our purposes and don't take it without asking.

If a project you want to learn from turns out to be GPL/AGPL/LGPL or
unlicensed: you can still read it (case 3 — see `THIRD_PARTY_NOTICES.md`),
you cannot copy or adapt its code (case 1) or link it in as a dependency
(case 2) without a separate decision that changes this policy.

## What a contributor must do when they copy code

Three things, every time, not two-out-of-three:

1. **A provenance header on the file**, at the top, before any code:
   - the original project name and URL,
   - the original copyright notice, verbatim,
   - the original licence identifier,
   - what was changed, if anything, from the original.
2. **An entry in `THIRD_PARTY_NOTICES.md`**, under "code copied or adapted,"
   in the same change that adds the file. Not a follow-up. A file with a
   provenance header and no ledger entry is a stale ledger the day it's
   merged.
3. **The original copyright line stays in the file.** Don't replace it with
   an `openabstractions` copyright line, and don't delete it because the file
   was "substantially rewritten" — if enough of the original remains that
   this policy applies at all, the notice stays. Add an `openabstractions`
   line alongside it for the parts that are new; don't remove theirs.

### Worked example

Say a future contributor adapts Chromium's partial-`Content-Range` parsing
state machine into `abstraction-download/go/bits/range.go`. The top of the
file:

```go
// Adapted from Chromium's content_range_parser
// (//net/http/http_util.cc, ParseContentRange), retrieved 2026-11-03.
//
// Copyright 2008 The Chromium Authors
// Licensed under the BSD 3-Clause License; see
// https://chromium.googlesource.com/chromium/src/+/main/LICENSE
//
// Changed from the original: reworked as a single function returning
// (start, end, total, ok) instead of populating an output struct, and the
// malformed-header cases return an error instead of logging and continuing.
//
// This file's own additions are:
// Copyright 2026 the openabstractions contributors
// Licensed under the Apache License, Version 2.0; see LICENSE.

package bits
```

And the matching row goes into `THIRD_PARTY_NOTICES.md` §1 in the same
commit — project, licence, what was taken, where it now lives. That row and
this header have to keep saying the same thing; if one changes, change the
other in the same commit.

### What this is not for

Coincidental similarity is not adaptation. If you independently write a
`Content-Range` parser because RFC 7233 only allows so many reasonable
shapes, there is nothing to attribute — the RFC is the source, and RFCs are
not "someone else's project" for these purposes. The test is honest: did you
have their file open, or a memory of reading it, while you wrote this one?
If yes, it's case 1 or case 3 in `THIRD_PARTY_NOTICES.md`, not nothing.

## Dependencies (case 2, briefly)

Adding a dependency — `go get`, a `CMakeLists.txt` `FetchContent`, a `pip
install` — doesn't need a provenance header (nothing was copied into this
project's source), but it does need the same-commit discipline: the
`THIRD_PARTY_NOTICES.md` §2 row goes in with the `go.mod`/`CMakeLists.txt`
change that adds it, not after. Check the licence against the acceptable
list above before adding it, not after it's already in three other
`go.mod` files.

## NOTICE files

Every repository ships a `NOTICE` file (Apache-2.0 requires anyone
redistributing this software to reproduce it). Keep it to what the licence
actually needs reproduced: the project name, the copyright line, and a
pointer to the licence — plus, for a repository that compiles in a
third-party dependency, a pointer to `THIRD_PARTY_NOTICES.md`. Nothing else.
Marketing copy, build instructions, and changelogs belong in `README.md`; a
`NOTICE` file is a legal artifact that gets copied verbatim into other
people's distributions, and the more that's in it, the more of this
project's prose ends up, unedited, inside somebody else's product.
