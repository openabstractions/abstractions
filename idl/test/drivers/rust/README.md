# A job store in Rust, on the generated record codec

`replay.rs` is a conformance driver in the sense of `conformance/DRIVER.md`:
it declares `store`, keeps records under `<workdir>/jobs/<id>.json` through
the encoder and decoder that `idl/gen` emits from `abstraction-job/job.thrift`, and
answers the store, watch and awake operations. It is the fourth implementer
of the job contract and was written from the pages alone.

Build, from a PowerShell with `go` and `rustc` on the path:

    powershell -File idl/test/drivers/rust/build.ps1 -Scratch <dir>

Judge it:

    sh conformance/run.sh --scenarios conformance/scenarios --contracts <pages> --no-fixture -- <dir>/replay.exe

where `<pages>` holds `job.md`, `watch.md`, `download.md` and `identity.md`
as `conformance/contracts.list` names them.

What may break: `hold` takes a real power request on Windows and only
bookkeeping elsewhere; the record lock `<id>.json.lock` is not taken, so two
processes on one store are not this driver's claim.

What this driver proves and what it does not: the generated reader accepting
`abstraction.job/terminal@1` in `critical` is the codec recognising a
declaration. Whether this store refuses a finished job's own holder is decided
in `write` and `claim`, judged by the `terminal` and `recall` scenarios, and
nothing generated from `job.thrift` has an opinion on it.
