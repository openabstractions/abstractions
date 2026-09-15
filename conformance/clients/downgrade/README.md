# Downgrade refusal

`python conformance/clients/downgrade/run.py` prints help. `--run` judges what the
previous release does with stores current sources write, and what the current
storage check does with the same stores.

The previous job and download modules come from `published.tsv`, or from
`--previous-job` and `--previous-download`. They are built outside `go.work` from
the module proxy, so a run needs the network. `writer.go` and `previous.go` carry
`//go:build ignore`. `workspace.go_template_binary` strips that constraint, writes
each template into a generated module, and names the binary apart from the
module directory. Build trees live under `.build` on Windows and in the system
temporary directory elsewhere.
The writer's replace directives for current sources come from `go.work` and each
workspace module's `require` directives (`workspace.go_replace_closure`).
The current check runs `openabstractions storage check --read-only`, which takes
no host guard, so every store must be byte-identical afterwards.

`scripts/check.sh --downgrade` runs it as an opt-in section.
`python conformance/clients/downgrade/test_run.py` checks the parsing and verdict
helpers offline.
