"""A third peer, in a language that shares no code with the other two.

Everything on the wire comes from the generated module. The framing — connect,
one exchange, a line delimiter — is written here, because the definition
declares the bytes of an envelope and says nothing about how one is delivered.

    py -3 peer.py <generated-py-dir> <host:port> <transcript-out>
"""

import socket
import sys

sys.path.insert(0, sys.argv[1])
import rec  # noqa: E402

ADDRESS = sys.argv[2]
OUT = sys.argv[3]
MINUTE_MS = 60000
HALF_MINUTE_MS = 30000
STAMP = "2026-09-08T05:07:14.951609Z"


def exchange(**kw):
    host, port = ADDRESS.rsplit(":", 1)
    with socket.create_connection((host, int(port))) as s:
        s.sendall(rec.encode_request(rec.Request(**kw)) + b"\n")
        buf = bytearray()
        while not buf.endswith(b"\n"):
            chunk = s.recv(4096)
            if not chunk:
                break
            buf += chunk
    return rec.decode_response(bytes(buf))


def proposal():
    return rec.encode(rec.Record(
        content=["abstraction.job/base@1"],
        kind="download",
        state="pending",
        spec='{"artifact":{"bytes":23068672}}',
        progress=rec.Progress(updated_at=STAMP),
        lease=rec.Lease(expires_at=STAMP),
        created_at=STAMP,
        updated_at=STAMP,
    ))


def record(resp):
    body = resp.record
    return rec.decode(body if isinstance(body, (bytes, bytearray)) else body.encode("utf-8"))


# A record the far side wrote and this decoder will not read is a result, not a
# crash: the transcript has to be able to show which exchange the two peers
# stopped agreeing on, and a traceback shows only that one of them stopped.
def field(resp, path):
    try:
        v = record(resp)
    except rec.Refusal as refusal:
        return "unreadable: " + refusal.word
    for name in path.split("."):
        v = getattr(v, name)
    return str(v)


def yes_no(v):
    return "true" if v else "false"


def transcript():
    lines = []

    def say(op, resp, detail):
        lines.append("%s\t%s\t%s" % (op, resp.kind or "ok", detail))

    r = exchange(op="submit", record=proposal())
    say("submit", r, yes_no(r.id))
    job_id = r.id

    r = exchange(op="load", id=job_id)
    say("load", r, field(r, "state"))

    r = exchange(op="list")
    say("list", r, str(len(r.records)))

    r = exchange(op="orphans")
    say("orphans", r, str(len(r.records)))

    r = exchange(op="claimable", id=job_id)
    say("claimable", r, yes_no(r.bool))

    r = exchange(op="claim", id=job_id, owner="w1", ttl_ms=MINUTE_MS)
    say("claim", r, field(r, "lease.epoch"))
    held = record(r).lease.epoch

    r = exchange(op="renew", id=job_id, epoch=held, ttl_ms=MINUTE_MS)
    say("renew", r, field(r, "lease.epoch"))

    base = exchange(op="load", id=job_id)
    current = record(base)
    seen = rec.encode(current)
    current.progress.done = 42
    r = exchange(op="write", id=job_id, epoch=held, base=seen, record=rec.encode(current))
    say("write", r, field(r, "progress.done"))

    r = exchange(op="set_intent", id=job_id, want="cancel", by="tester")
    say("set_intent", r, field(r, "intent.want"))

    r = exchange(op="recall", id=job_id, epoch=held,
                 reason="needed elsewhere", by="tester", ttl_ms=HALF_MINUTE_MS)
    say("recall", r, field(r, "lease.recall.reason"))

    say("release", exchange(op="release", id=job_id, epoch=held), "")
    say("load-missing", exchange(op="load", id="no-such-id"), "")

    r = exchange(op="claim", id=job_id, owner="w2", ttl_ms=MINUTE_MS)
    say("reclaim", r, field(r, "lease.epoch"))

    say("claim-held", exchange(op="claim", id=job_id, owner="w3", ttl_ms=MINUTE_MS), "")
    say("renew-stale", exchange(op="renew", id=job_id, epoch=9999, ttl_ms=MINUTE_MS), "")
    return lines


with open(OUT, "w", encoding="utf-8", newline="\n") as f:
    f.write("\n".join(transcript()) + "\n")
