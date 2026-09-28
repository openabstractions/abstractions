"""Outside Python jobs client for a service-owned partial-range restart."""
import hashlib
import io
import sys
import time

sys.path.insert(0, sys.argv[1])
from abstraction.facade.client import Machine  # noqa: E402
from abstraction.facade import Scope  # noqa: E402
import abstraction.job.acceptance as job  # noqa: E402
import abstraction.download.request as download  # noqa: E402

endpoint, mode = sys.argv[2:4]
jobs = Machine(endpoint, timeout=90).resolve_jobs(scope=Scope.LOCAL)

if mode == "submit":
    url, size, digest, key = sys.argv[4:8]
    window = jobs.get_history_window()
    identity = job.RequestIdentity(key=key, history_epoch=window.history_epoch)
    request = download.Request(artifact=download.Artifact(digest=digest, size=int(size)),
                               sources=[download.Source(scheme="http", locator=url)])
    result = jobs.submit(job.Submission(identity=identity, kind="download", spec=download.encode(request)))
    if result.outcome != "accepted" or result.receipt is None:
        raise RuntimeError("request refused: " + result.outcome)
    print("ACCEPTED", identity.history_epoch, result.receipt.operation_id, result.receipt.logical_owner)
elif mode == "result":
    key, epoch, operation = sys.argv[4:7]
    identity = job.RequestIdentity(key=key, history_epoch=epoch)
    recovered = jobs.reconcile(identity)
    if recovered.outcome != "accepted" or recovered.receipt.operation_id != operation:
        raise RuntimeError("receipt changed")
    deadline = time.monotonic() + 60
    while True:
        observed = jobs.observe_work(identity)
        if observed.outcome != "observed":
            raise RuntimeError("operation unobservable: " + observed.outcome)
        if observed.snapshot.receipt.operation_id != operation:
            raise RuntimeError("operation changed")
        if observed.snapshot.state == "complete":
            break
        if observed.snapshot.state in ("failed", "cancelled") or time.monotonic() >= deadline:
            raise RuntimeError("operation did not complete: " + observed.snapshot.state)
        time.sleep(0.05)
    output = io.BytesIO()
    count = jobs.copy_result(identity, output)
    body = output.getvalue()
    if count != len(body):
        raise RuntimeError("copy count differs")
    print("RESULT", count, hashlib.sha256(body).hexdigest())
else:
    raise RuntimeError("unknown mode")
