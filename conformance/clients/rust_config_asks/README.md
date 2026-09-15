# Outside Rust config and asks client proof

Run `python conformance/clients/rust_config_asks/run.py --help`, then
`--run --cmake <cmake>` (add `--race` for the Go race detector). The runner works
on Windows (MSVC, named pipes) and Linux (GCC, Unix sockets). On a Linux host
without a usable `.git`, export `OA_SOURCE_REVISION` for the source being run.

The runner copies current sources into a temporary tree. It tests
`abstraction-facade-config` and `abstraction-facade-asks` with only the facade
core, their generated protocols and the pure frame trait present. Storage, jobs,
logging, download and rights sources are absent. It then installs a static
`abstraction_ipc` prefix with CMake, builds the outside consumer against it
through the facade-native connector, and checks Cargo's native link lines
against the installed `link-dependencies.txt`.

The Go fixture hosts a private runtime with an isolated home, an explicit edit
policy read from a mode file, and an application Book whose operator policy
admits only the probe executable. The consumer:

- replaces the user rung (`applied`), observes `conflict` for a stale revision,
  and receives `forbidden` and `unavailable` with empty snapshots while the
  revision stays unchanged;
- asks and observes questions, including replay and content conflict;
- lists, answers, replays and conflicts as the authorized operator;
- retires a pending question with its record, replays without a record, and
  observes `gone` for Observe and Ask, with `unknown` for unknown IDs.

A copy of the probe at another path is refused list, answer and retire, and the
answer stays recorded. No owner service, product installer or network is used.
