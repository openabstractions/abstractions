# fit

Scores the code the generator emits, per language, against that language's own
conventions. How well the notation's own *vocabulary* fits a language is a
different question; this scores the *output*. They must not be averaged.

Two halves, never mixed:

- **Tool** — the language's own community. `gofmt`, `go vet`, `rustfmt`,
  `rustc -D warnings`, `python -m compileall`, `node --check`. A tool that
  refuses the file is a **hard zero** for that language and no rubric term buys
  it back.
- **Rubric** — ours. Six terms, taken from a survey of what each language's own
  code looks like: naming, namespace, errors, construction, absence, opaque.
  `errors` carries
  double weight. 14 points in all. Weaker evidence, and every line it prints
  says so.

Nothing is installed and nothing is fetched. A language whose tool is not on the
machine is reported `UNPROVEN`, never a pass.

## Run it

    sh idl/fit/fit.sh /tmp/fit-scratch

Writes only under the scratch directory you name. Takes about half a minute,
most of it `rustc`. Add a definition path to score a different one, or set
`FIT_GEN` to point at a copy of the generator — that is how one generator is
scored before and after a change, in one session against one tree, which is what
makes the two halves comparable.

The gate runs it in [`scripts/check.sh`](../../scripts/check.sh) and vetoes
only: a language whose points **fall** below the floor recorded for it is red
and is named; a language whose points rise is a note asking for the floor to be
raised. The score may veto and may never authorise.

## What may break

- **C++ has no compiler on this machine**, so its tool half is UNPROVEN and it
  currently carries the highest number in the table on the least evidence. Do
  not read it as a rank against the four that were tested. `clang-format` is
  present and is deliberately not a gate: C++ has no canonical formatter the way
  Go and Rust do, so failing a file against LLVM style would be our taste
  wearing a tool's clothes.
- **The rubric greps the emitted source.** A backend that changes its output
  shape can make a term silently stop matching, which scores 2 by default in
  some terms and 0 in others. A term that never moves across a real change is
  the thing to distrust first.
- **`absence` is the one term that is not purely a measurement of the code.** It
  reads `omit = "absent"` out of the definition and asks whether the emitted
  type honours it, so editing the definition moves the term while the emitted
  bytes stand still — measured, and it was the whole content of one before-and-
  after pair. That is a real weakness and it
  is also how the instrument found something: a term that moves with no diff
  under it is the signal that the definition is claiming what the code does not
  carry. Any new term should be checked against the same question, and a score
  that moves with an empty diff should be read as a claim about the definition
  rather than an improvement to the output.
