# Results

Terminal transcripts from runs done here. They are our own output, so treat
them as a record of what happened on one machine, not as independent
verification. Where a script produced the transcript it is named; the scripts
are in [`../../scripts/`](../../scripts/) and expect the layer repositories
checked out as siblings.

Usernames, machine names and LAN addresses have been replaced with
placeholders. Nothing else in these files was edited.

Transcripts from 2026-09-06 on open with five `#` lines: the tree they
measured, the state of the machine (power, plan, filesystem), the command that
produces them again, and their scope — which platforms they prove and which
they leave `UNPROVEN`. [`results.sh`](../../scripts/results.sh) writes the
ones a machine with the three toolchains can produce; `results.sh --check`
refuses when one of them measured a tree other than the committed one, so a
release cannot carry a number nobody can attribute to a commit. The rest were
written by hand from runs that need hardware — a NAS on the LAN, a packet
filter with root — and their `# re-run` line says what.

## Delegation, refusal, concurrency and power

| file | produced by | what it shows |
|---|---|---|
| [`NAS1.txt`](NAS1.txt) | hand | A 386 MB model asked for on a PC; every process of ours on the PC killed 20 s in; a NAS on the LAN finishes and verifies it 48 s later, and the first sweep after the PC comes back delivers it. DSM 6.2.4 with the package scripts run by hand; Package Center install and DSM 7 `UNPROVEN`. |
| [`NAS2.txt`](NAS2.txt) | hand | The same bytes asked for again. Before: the NAS fetched 388 MB it already held. After: a job whose only source is a dead URL completes from the held copy, which is proof that needs no byte counter. |
| [`SMB1.txt`](SMB1.txt) | hand, one test | A record read 154 s stale over SMB because the Windows redirector reuses a closed handle; every constant open stays stale for nine minutes, alternating the open sees the change within one poll. The fix's pin on local disk is at the end. |
| [`REACH1.txt`](REACH1.txt) | hand | A host refused in our file is refused by nftables for `curl`, which links nothing of ours, with our sentence in the kernel log. Linux only; the Windows rule text is rendered and unapplied. |
| [`CAS-MIXED1.txt`](CAS-MIXED1.txt) | `results.sh` | Two Go, two C++ and two Python writers making 100 changes each to one file, a reader per language spinning through every rename. A lock one of them believes differently is a lost update here. Windows; macOS `UNPROVEN`. |
| [`BEHAVIOUR1.txt`](BEHAVIOUR1.txt) | `results.sh` | Every scenario in `testdata/scenarios/` replayed by Go, Python and C++ against a store of their own, one transcript each, diffed. What diverged, what no language could run and what no page decides are printed, not skipped. |
| [`AWAKE1.txt`](AWAKE1.txt) | `results.sh` | The machine held awake while a job's lease is live, observed through the kernel's own execution-state bit rather than our store: released on release, on lapse, on the job turning terminal, and when the holder is killed. Windows; Linux reads `systemd-inhibit`; macOS `UNPROVEN`. |

## Job and download layers

| file | script | what it shows |
|---|---|---|
| [`XLANG-DOWNLOAD.txt`](XLANG-DOWNLOAD.txt) | `xlang-download.sh` | Go and Python finishing each other's downloads. One is killed mid-transfer, the other resumes from the last proven byte; digests match in both directions. |
| [`XLANG1.txt`](XLANG1.txt) | `xlang-job.sh` | Go and Python exchanging job records: claim, epoch, lease lapse, adoption. States plainly that no bytes moved. |
| [`XLANG2.txt`](XLANG2.txt) | `xlang-job.sh` | A later run of the same script, with the scope limit restated. |
| [`CONFORM1.txt`](CONFORM1.txt) | `conformance.sh` | Go, Python and C++ reading the same record, re-encoding it identically, and carrying a spec none of them has a type for. |
| [`RESUME1.txt`](RESUME1.txt) | `kill-and-resume.sh` | A download killed with SIGKILL and resumed by a separate process from the proven prefix. The bytes written past the last checkpoint are discarded. |
| [`SUPERVISOR1.txt`](SUPERVISOR1.txt) | `supervisor.sh` | A supervisor sweep adopting an orphaned job — owner dead, lease lapsed — finishing it, and a second sweep correctly doing nothing. |
| [`DEMO.txt`](DEMO.txt) | `demo.sh` | The end-to-end walkthrough: a model file, the name it claims, and what each tool does when the two disagree. |

## Content addressing and integrity

These came from `vault`, the content-addressed store that preceded
`abstraction-storage`. It is not currently published as a repository, so the
Go module paths in these transcripts do not resolve.

| file | what it shows |
|---|---|
| [`A0.txt`](A0.txt) | Build, client authentication against a real `vaultd`, and a digest mismatch refused on fetch. |
| [`B1.txt`](B1.txt) | Cost of hashing on write and on read over 8 GiB, on Windows and under WSL. Also a NAS probe that found nothing and was recorded as unavailable rather than skipped. |
| [`B2.txt`](B2.txt) | A live 806 MB fetch from HuggingFace, verified against the published digest. |
| [`H1.txt`](H1.txt) | What bounds `store.Open`: it reaches 55% of the CPU hashing ceiling, so both the filesystem and the hash are visible in the number. |
| [`H2.txt`](H2.txt) | Store policy, and `verify --all` refusing an artifact whose bytes do not match its name. |

## What incumbent tools do with a damaged model file

Each of these ships the test or script that produced it, so the method is
runnable rather than asserted.

| file | script or test | what it shows |
|---|---|---|
| [`G1.txt`](G1.txt) | `TestConformance_TruncatedGGUF` | The store refuses a truncated GGUF; llama.cpp accepts it. The top-level test fails because the subtest asserting llama.cpp refuses it does. |
| [`G1b.txt`](G1b.txt) | `TestConformance_CorruptGGUF` | The same comparison for a corrupted rather than truncated file. |
| [`G2.txt`](G2.txt) | `TestBoundary` | How much has to be removed before llama.cpp notices, and a mid-file bit flip it does not notice — it serves degenerate output instead. |
| [`G2b.txt`](G2b.txt) | `TestBoundary_SingleBitFlip` | One bit flipped at a known offset; size unchanged. |
| [`G2c.txt`](G2c.txt) | `TestBoundary_CorruptionScale` | Output quality against the size of the corruption. |
| [`G3.txt`](G3.txt) | `g3-ollama-corrupt.sh` | An Ollama blob altered in place, to find out whether Ollama re-checks the digest it stores. |
| [`C2.txt`](C2.txt) | manual | How Ollama names blobs, and a hash of one of them. |
| [`C3.txt`](C3.txt) | manual | What `llama-server --hf` verifies about a downloaded file: a large truncation is caught by the GGUF bounds check, a 16-byte truncation is not caught at all. |
