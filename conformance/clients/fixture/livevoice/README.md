# Live voice transport measurement

Run `python run.py --help` for options. `--measure` builds an isolated Go host
and measures Go and Python clients over the production identity-bound transport
with generated request/reply codecs. Python uses the shared native IPC library.
Windows builds use the Visual Studio developer environment named in
`conformance/clients/workspace.py`. Linux uses its native CMake toolchain.

Append sends 6400-byte PCM16 frames (200 ms at 16 kHz, mono) on a fixed schedule.
A concurrent Observe call receives each echoed frame byte for byte. The fake
HTTP upstream waits 5 ms. A production inference provider and native chat client
concurrently stream fake upstream tokens every 10 ms.

The instrument measures each call's full local round trip, including generated
codec work. It subtracts the server-measured backend wait from Append and the
server-measured long-poll wait from Observe. These are conservative local-call
overheads for the two directions, not synchronized one-way clock measurements.
Each p99 must be below 200 ms. Default runs collect 100 frames after five warmups.
`--frames 5` is a smoke test, not qualification.

The negative control is:

```sh
python run.py --measure --language go --frames 3 --warmup 0 --fault-delay-ms 250
```

It adds 250 ms outside the excluded wait and must fail the latency gate.
`--results PATH` retains measured JSON and the source revision on successful runs.
The runner cleans up its child processes and build directory. It installs no
runtime registration. The fixture schema is measurement-only; `live@1` remains
a separate implementation step after Windows and Linux measurements pass.
