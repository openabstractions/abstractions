"""A Python application resolves abstraction.inference/chat@1 through the facade
over the shared native transport and calls complete() and stream().

    py_consumer.py <runtime endpoint> <runtime without inference> refused|served
    py_consumer.py <runtime endpoint> audit

audit resolves operator@1 and prints every retained audit entry, one
tab-separated line each with LF endings.

ABSTRACTION_IPC_LIBRARY names the shared abstraction_ipc library, and PYTHONPATH
holds the identity, facade and inference Python sources.
"""
import sys
import time

if len(sys.argv) not in (3, 4) or len(sys.argv) == 3 and sys.argv[2] != "audit":
    print(__doc__)
    raise SystemExit(0 if sys.argv[1:] == ["--help"] else 2)

from abstraction.facade.client import Machine, ResolutionError
from abstraction.facade import Scope
import abstraction.inference.api as api

if len(sys.argv) == 3:
    operator = Machine(sys.argv[1], timeout=10.0).resolve_inference_operator(scope=Scope.LOCAL)
    cursor, lines = 0, []
    while True:
        page = operator.audit(cursor, 256)
        if page.outcome == api.AuditOutcome.GAP:
            cursor = page.next
            continue
        assert page.outcome == api.AuditOutcome.PAGE, page.outcome
        for e in page.entries:
            lines.append("AUDIT\t%d\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n" % (e.sequence, e.route.value, e.outcome, e.reason, e.tokens_in, e.tokens_out, e.program, e.rung))
        cursor = page.next
        if page.at_end or not page.entries:
            break
    sys.stdout.buffer.write("".join(lines).encode("utf-8"))
    raise SystemExit(0)

endpoint, absent, mode = sys.argv[1:]


def request(model, text):
    return api.Request(model=model, guarantees=[api.RequestGuarantee.LOCAL_ONLY],
                       messages=[api.Message(role=api.Role.USER, parts=[api.Part(kind=api.PartKind.TEXT, text=text)])])


try:
    Machine(absent, timeout=10.0).resolve_inference(scope=Scope.LOCAL)
    raise AssertionError("a runtime without inference resolved chat@1")
except ResolutionError as error:
    assert error.status == "unavailable", error.status

chat = Machine(endpoint, timeout=10.0).resolve_inference(scope=Scope.LOCAL)
if mode == "refused":
    reply = chat.complete(request("fixture-chat:1b", "hi"))
    assert reply.outcome == api.ReplyOutcome.NOT_PERMITTED and reply.reason == "rights:not_granted", reply
    print("PASS python refused: absence=unavailable complete=%s" % reply.outcome.value)
    raise SystemExit(0)
assert mode == "served", mode
reply = chat.complete(request("fixture-chat:1b", "hi"))
assert (reply.outcome == api.ReplyOutcome.COMPLETED and [p.text for p in reply.message.parts] == ["Hello from the fixture runtime"]
        and reply.host == "ollama" and reply.usage.input == 5), reply
started = time.perf_counter()
first, text, last = None, "", None
for delta in chat.stream(request("fixture-chat:1b", "hi")):
    if delta.kind == api.DeltaKind.PART:
        if first is None:
            first = (time.perf_counter() - started) * 1000
        text += delta.part.text
    last = delta
assert text == "Hello from the fixture runtime" and last.end.outcome == api.ReplyOutcome.COMPLETED, (text, last)
held = chat.stream(request("fixture-chat:1b", "HOLD"))
for delta in held:
    if delta.kind == api.DeltaKind.PART:
        break
held.close()
denied = chat.complete(request("denied-chat", "hi"))
assert denied.outcome == api.ReplyOutcome.NOT_PERMITTED and denied.reason == "rights:not_granted", denied
print("PASS python served: complete, stream, cancel, refusal")
print("FIRST_TOKEN_MS python %.2f" % first)
