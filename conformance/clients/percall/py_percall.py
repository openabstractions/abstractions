"""Time N identical caller@1 Observe calls from one Python process at a runtime
resolver endpoint through the shared native
abstraction_ipc library: single opens a connection per call, session keeps one.

    py_percall.py <runtime endpoint> <calls> <warmup> single|session

ABSTRACTION_IPC_LIBRARY names the shared library; PYTHONPATH holds the identity
and facade Python sources. Prints one PERCALL line; see percall/README.md.
"""
import math
import sys
import time

if len(sys.argv) != 5 or sys.argv[4] not in ("single", "session"):
    print(__doc__)
    raise SystemExit(0 if sys.argv[1:] == ["--help"] else 2)

from abstraction.ipc import FrameTransport, Library
import abstraction.facade as wire

endpoint, calls, warmup, session = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4] == "session"
client = wire.CallerClient(FrameTransport(Library(), endpoint, timeout=10.0, sessions=session))
samples, first, code = [], 0.0, ""
for i in range(warmup + calls):
    began = time.perf_counter()
    observed = client.observe()
    took = (time.perf_counter() - began) * 1000
    if observed.outcome != wire.CallerOutcome.OBSERVED:
        raise SystemExit("observe outcome: %s" % observed.outcome)
    if i == 0:
        first = took
        code = next((a.proof for a in observed.attributes if a.attribute == "code"), "")
    if i >= warmup:
        samples.append(took)
samples.sort()


def rank(p):
    return samples[max(0, math.ceil(len(samples) * p) - 1)]


print("PERCALL %s calls=%d first=%.3f p50=%.3f p90=%.3f p99=%.3f max=%.3f code=%s"
      % ("python-session" if session else "python", len(samples), first, rank(0.5), rank(0.9), rank(0.99), samples[-1], code))
