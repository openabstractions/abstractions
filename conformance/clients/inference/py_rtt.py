"""Time one Python process's chat@1 calls at a service endpoint.

    py_rtt.py <service endpoint> <model> <calls>

Prints RTT lines for start, an observe of a retained delta, and the first part
of a stream. ABSTRACTION_IPC_LIBRARY and PYTHONPATH as for py_consumer.py.
"""
import statistics
import sys
import time

if len(sys.argv) != 4:
    print(__doc__)
    raise SystemExit(0 if sys.argv[1:] == ["--help"] else 2)

from abstraction.ipc import FrameTransport, Library
from abstraction.inference.client import Chat
import abstraction.inference.api as api

endpoint, model, calls = sys.argv[1], sys.argv[2], int(sys.argv[3])
chat = Chat(FrameTransport(Library(), endpoint, timeout=10.0))
request = api.Request(model=model, guarantees=[api.RequestGuarantee.LOCAL_ONLY],
                      messages=[api.Message(role=api.Role.USER, parts=[api.Part(kind=api.PartKind.TEXT, text="rtt")])])
starts, observes, firsts = [], [], []
for _ in range(calls):
    began = time.perf_counter()
    admission = chat.start(request)
    starts.append(time.perf_counter() - began)
    chat.observe(admission.operation, 0, 1, 65536, 5000)
    began = time.perf_counter()
    chat.observe(admission.operation, 0, 1, 65536, 0)
    observes.append(time.perf_counter() - began)
    chat.cancel(admission.operation)
    began = time.perf_counter()
    stream = chat.stream(request)
    for delta in stream:
        if delta.kind == api.DeltaKind.PART:
            firsts.append(time.perf_counter() - began)
            break
    stream.close()
for name, values in (("start", starts), ("observe", observes), ("first token", firsts)):
    print("RTT python %-11s p50=%.3f max=%.3f ms" % (name, statistics.median(values) * 1000, max(values) * 1000))
