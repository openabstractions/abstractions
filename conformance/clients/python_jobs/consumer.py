"""Outside installed Python application. Receives bootstrap and HTTP fixture only."""
import io
import json
import os
import sys
import time
import importlib.util
sys.path.insert(0,sys.argv[1])
from abstraction.ipc import Library, FrameError, DISCONNECTED, CANCELLED
from abstraction.facade.client import Machine
from abstraction.facade.jobs import Jobs
from abstraction.job.acceptance import rec as job
from abstraction.download.request import rec as download

assert importlib.util.find_spec("abstraction.logging") is None, "unrelated logging package present"
assert importlib.util.find_spec("abstraction.config") is None, "unrelated config package present"

library=Library()
machine=Machine(sys.argv[2],library,timeout=3)
client=machine.resolve_jobs(scope="local")
window=client.GetHistoryWindow()
identity=job.RequestIdentity(key="retained-python-key",history_epoch=window.history_epoch)
request=download.Request(artifact=download.Artifact(digest=sys.argv[4],size=int(sys.argv[5])),
                         sources=[download.Source(scheme="http",locator=sys.argv[3])])
submission=job.Submission(identity=identity,kind="download",spec=download.encode(request))
# This test transport discards one real received response before generated decode.
# It supplies no invented acceptance result and sends no extra request.
class LostReply:
    def __init__(self,inner):self.inner=inner;self.lost=False
    def exchange_frame(self,frame):
        reply=self.inner.exchange_frame(frame)
        if json.loads(frame)['method']=='Submit' and not self.lost:
            self.lost=True
            raise FrameError(DISCONNECTED,"fixture dropped actual acceptance reply")
        return reply
lost=LostReply(client._transport)
client._acceptance=job.RecoverableAcceptanceClient(lost)
try:client.Submit(submission)
except FrameError as error:assert error.status==DISCONNECTED
else:raise AssertionError("reply-loss fixture did not run")
recovered=client.Reconcile(identity)
assert recovered.outcome=="accepted" and recovered.receipt.logical_owner==window.logical_owner
assert client.owner==window.logical_owner
receipt=recovered.receipt
signal=library.cancellation()
try:
    signal.signal()
    waiting=client.with_waiting(cancellation=signal)
    try:waiting.ObserveWork(identity)
    except FrameError as error:assert error.status==CANCELLED
    else:raise AssertionError("cancelled wait succeeded")
finally:signal.close()
restored=Jobs.restore(client.endpoint,client.owner,library=library,
                      required_guarantees=receipt.accepted_guarantees,timeout=3)
os.environ['ABSTRACTION_RUNTIME_ENDPOINT']='must-not-rediscover'
end=time.monotonic()+10
while True:
    observation=restored.ObserveWork(identity)
    assert observation.outcome=="observed"
    assert not observation.snapshot.cancellation_requested
    if observation.snapshot.state=="complete":break
    assert time.monotonic()<end,observation.snapshot.state
    time.sleep(.01)
output=io.BytesIO()
count=restored.CopyResult(identity,output)
expected=bytes(range(256))*1025
assert count==len(expected) and output.getvalue()==expected
unknown=restored.Reconcile(job.RequestIdentity(key="never-submitted",history_epoch=window.history_epoch+"-unavailable"))
assert unknown.outcome=="unknown"
request.sources[0].locator += "/different"
conflict=restored.Submit(job.Submission(identity=identity,kind="download",spec=download.encode(request)))
assert conflict.outcome=="key_conflict",conflict.outcome
assert restored.CancelWork(identity).outcome=="already_terminal"
inventory=machine.resolve_job_inventory(scope="local")
page=inventory.ListWork("",1)
assert page.outcome=="page" and len(page.snapshots)==1
assert page.snapshots[0].receipt.operation_id==receipt.operation_id
if not page.complete:
    replay=inventory.ListWork(page.next,1)
    again=inventory.ListWork(page.next,1)
    assert replay.next==again.next and replay.complete==again.complete
print("PASS: real lost reply/reconcile, fixed restored owner, wait cancellation distinct, exact bounded bytes, unknown/conflict, inventory")
