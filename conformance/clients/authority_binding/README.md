# Installed asks and rights C++ proof

Run `python conformance/clients/authority_binding/run.py --help`, then `--run`
with `--cmake` pointing to CMake where needed. `--race` requires a configured Go
race C compiler. The runner builds actual packages in a temporary static prefix
and an outside installed C++ consumer. It verifies logging, jobs, config, model and router packages are absent and missing required packages fail.

The Go fixture hosts the real runtime with a temporary application Book and
DecisionPolicy. The application admits a stable question, checks pending and
conflict, waits with a bound and cancellation, then exits. A separately authorized generated QuestionOperator client
answers once. Denied list/answer calls return no data; authorized history pages,
stable answer replay and a cursor gap after mutation are checked. After runtime restart, the same executable/key returns that
answer and original admission repeatedly; forgetting yields gone. Unknown keys
stay unknown. No application API supplies operator authority.

The fixture also resolves generated AuthorizationOperator and ContentReader
clients. Explicit operator authorization permits a conditional grant; the
consumer reads 150,000 bytes in three bounded chunks. A stale retry conflicts,
operator revocation refuses the next resource read, and explicit release works.
Ordinary operator calls return forbidden without provider lookup. The installed
prefix therefore contains only asks, rights, storage and their shared IPC/facade
packages.

Existing direct rights decisions change after native grants/revocation for the consumer's
received account/program. A same-account subject assertion is refused by the
explicitly refusing enforcer policy. Semantic controls reject contradictory
answers and missing/extra decision revisions.

Temporary processes, prefix and data are drained/removed. This proves Windows
source-built packages; it creates no OS installation or live awake lease.

QuestionApplication Ask/Observe and Authorization Decide/DecideFor now also run through generated descriptors and the common ResolveService factory. Existing semantic validation and receiving authorization checks remain in the fixture.
