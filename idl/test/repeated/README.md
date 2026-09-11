# repeated

The repeated record [DEF-A9] and the string map [DEF-A10], proved the same way
everything else in this profile is proved: one definition, five backends, one
byte comparison.

`../run.ps1` proves the profile on `abstraction-job/job.thrift`, which has neither shape.
`repeated.thrift` is the smallest definition that reaches every rule the two
additions touch — a document with a required and an optional repeated field, an
element carrying a string map and an opaque value, and `depth_limit = "5"` so
that a document can exceed the limit *inside* a repeated element in ten lines
rather than sixty. It feeds no committed artefact and appears in no line of
`scripts/generate.targets`.

## Run it

    powershell -File idl/test/repeated/run.ps1 -Scratch <dir>

Needs `go`. Python, node, rustc and MSVC are each optional and a missing one is
reported `UNPROVEN`, never skipped. Writes only under the scratch directory.

Three things fail it:

- a corpus file whose refusal word is not the one its filename says;
- two languages that refuse the same input with a different word or at a
  different byte;
- an accepted document that any language re-encodes to different bytes, or that
  does not re-encode to the fixture itself.

That last one is the encoder's whole argument. A fixture is written in the one
legal spelling, so decode-then-encode has to reproduce it; `accept.wide.*` is
the exception, deliberately not in that spelling, and is only asked to agree
across languages.

## What may break

- **The corpus filename is the expected word.** `depth_exceeded.inside-element`
  expects `depth_exceeded`. Renaming a file changes what it asserts.
- **The depth limit is 5 and three fixtures sit on it.**
  `accept.deepest-element` is at exactly the limit and
  `depth_exceeded.inside-element` is one level past it. Changing
  `depth_limit` in the definition moves both verdicts.
- **The offsets are not written down anywhere.** Nothing asserts an absolute
  byte; what is asserted is that five implementations name the same one. A
  fixture edited by one byte moves every offset and nothing notices, which is
  correct — the claim is agreement, not a number.
