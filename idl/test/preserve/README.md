# Unknown-field preservation fixture

Run from the repository root with existing Go, Python, Node, Rust and C++ tools:

```powershell
py -B idl/test/preserve/run.py --help
py -B idl/test/preserve/run.py --scratch .build/idl-preserve
```

`--language` and `--duplicates` can select individual backends or duplicate-key
policies. The default tests both policies across every available backend, and
reports missing toolchains as UNPROVEN. Explicitly selecting a missing language
fails. On Windows, C++ uses the existing MSVC installation; `VCVARS` overrides
the batch-file path. Nothing is installed or downloaded.
`--escape ascii` exercises ASCII string escaping; the default is minimal.

The 17 JSON inputs cover decoded duplicate keys, leading U+FEFF in keys and known
strings, nested/repeated/empty preserving structs, grant/drop and refuse scopes,
required/type checks, and depth boundaries.
Accepted documents are edited through known fields before re-encoding. An
independent JSON-token comparison checks unknown value spelling and member order;
it also checks known-field order followed by UTF-8 extra-key order, including
non-BMP keys. All successful backend outputs must be byte-identical and stable
on another round trip.

`writes.tsv` supplies 22 direct-write counterexamples: known-field collisions
(including omitted fields), every zero-like value, raw token spelling, malformed
JSON, duplicate names, invalid strings, trailing data, and nesting limits at
root, child and repeated-child depth. Rust runs 21 because its safe `String`
cannot represent the invalid-key case; invalid raw UTF-8 values are tested there.
The normal generator Go tests run the direct-write fixture as well.

The `last` policy is derived from the same test-only schema, removing and
renumbering the now-unreachable duplicate-key refusal. It accepts duplicate raw
object members without rewriting them, following the existing opaque-value
policy. At the extra-field map level the final decoded-key entry wins.

Raw fixtures intentionally contain invalid UTF-8 and whitespace. Their local
Git attributes disable text normalization. No product schema opts into preserve.
