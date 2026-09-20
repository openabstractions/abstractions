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
from abstraction.facade import Scope
from abstraction.facade.jobs import Jobs
import abstraction.job.acceptance as job
import abstraction.download.request as download

assert importlib.util.find_spec("abstraction.logging") is None, "unrelated logging package present"
assert importlib.util.find_spec("abstraction.config") is None, "unrelated config package present"

library=Library()
machine=Machine(sys.argv[2],library,timeout=3)
client=machine.resolve_jobs(scope=Scope.LOCAL)
window=client.get_history_window()
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
try:client.submit(submission)
except FrameError as error:assert error.status==DISCONNECTED
else:raise AssertionError("reply-loss fixture did not run")
recovered=client.reconcile(identity)
assert recovered.outcome=="accepted" and recovered.receipt.logical_owner==window.logical_owner
assert client.owner==window.logical_owner
receipt=recovered.receipt
signal=library.cancellation()
try:
    signal.signal()
    waiting=client.with_waiting(cancellation=signal)
    try:waiting.observe_work(identity)
    except FrameError as error:assert error.status==CANCELLED
    else:raise AssertionError("cancelled wait succeeded")
finally:signal.close()
restored=Jobs.restore(client.endpoint,client.owner,library=library,
                      required_guarantees=receipt.accepted_guarantees,timeout=3)
os.environ['ABSTRACTION_RUNTIME_ENDPOINT']='must-not-rediscover'
end=time.monotonic()+10
while True:
    observation=restored.observe_work(identity)
    assert observation.outcome=="observed"
    assert not observation.snapshot.cancellation_requested
    if observation.snapshot.state=="complete":break
    assert time.monotonic()<end,observation.snapshot.state
    time.sleep(.01)
output=io.BytesIO()
count=restored.copy_result(identity,output)
expected=bytes(range(256))*1025
assert count==len(expected) and output.getvalue()==expected
unknown=restored.reconcile(job.RequestIdentity(key="never-submitted",history_epoch=window.history_epoch+"-unavailable"))
assert unknown.outcome=="unknown"
request.sources[0].locator += "/different"
conflict=restored.submit(job.Submission(identity=identity,kind="download",spec=download.encode(request)))
assert conflict.outcome=="key_conflict",conflict.outcome
assert restored.cancel_work(identity).outcome=="already_terminal"
inventory=machine.resolve_job_inventory(scope=Scope.LOCAL)
page=inventory.list_work("",1)
assert page.outcome=="page" and len(page.snapshots)==1
assert page.snapshots[0].receipt.operation_id==receipt.operation_id
# No caller label was sent: the runtime derived one from the source URL, whose
# host is the fixture's loopback address and whose path is empty (JOB-A12).
assert page.snapshots[0].label=="127.0.0.1" and page.snapshots[0].label_derived,page.snapshots[0]
if not page.complete:
    replay=inventory.list_work(page.next,1)
    again=inventory.list_work(page.next,1)
    assert replay.next==again.next and replay.complete==again.complete
# The runtime's cost source reports a metered path. A request with network
# unmetered requires network-cost@1, waits with the word network:metered, and
# is cancelled with nothing fetched (download DL-N2 to DL-N6, JOB-A15).
constrained=download.Request(artifact=download.Artifact(digest="",size=0),
                             sources=[download.Source(scheme="http",locator=sys.argv[3]+"/constrained")],
                             constraints=download.Constraints(network=download.Network.UNMETERED))
waiting_identity=job.RequestIdentity(key="waiting-python-key",history_epoch=window.history_epoch)
accepted=restored.submit(job.Submission(identity=waiting_identity,kind="download",spec=download.encode(constrained),
                                        required_guarantees=list(download.NETWORK_COST_GUARANTEES)))
assert accepted.outcome=="accepted" and download.NETWORK_COST_GUARANTEES[0] in accepted.receipt.accepted_guarantees,accepted
end=time.monotonic()+10
while True:
    snapshot=restored.observe_work(waiting_identity).snapshot
    # The word is written while the lease is held; the work reads pending once
    # the lease is released.
    if snapshot.waiting=="network:metered" and snapshot.state=="pending":
        break
    assert snapshot.state in ("pending","running"),snapshot
    assert time.monotonic()<end,snapshot
    time.sleep(.02)
assert restored.cancel_work(waiting_identity).outcome=="requested"
while True:
    snapshot=restored.observe_work(waiting_identity).snapshot
    if snapshot.state=="cancelled":
        assert snapshot.waiting=="",snapshot
        break
    assert time.monotonic()<end,snapshot
    time.sleep(.02)
# The runtime admits the credential hf and refuses applying it as revoked: the
# operation fails permanently with cause credential and the applier outcome.
# A name the runtime does not hold is refused at admission with no receipt
# (job JOB-A8, JOB-A16; download DL-K1).
named=download.Request(artifact=download.Artifact(digest="",size=0),
                       sources=[download.Source(scheme="http",locator=sys.argv[3]+"/credential",credential="hf")])
credential_identity=job.RequestIdentity(key="credential-python-key",history_epoch=window.history_epoch)
accepted=restored.submit(job.Submission(identity=credential_identity,kind="download",spec=download.encode(named),
                                        required_guarantees=list(download.CREDENTIAL_GUARANTEES)))
assert accepted.outcome=="accepted",accepted
while True:
    snapshot=restored.observe_work(credential_identity).snapshot
    if snapshot.state=="failed":
        break
    assert snapshot.state in ("pending","running"),snapshot
    assert time.monotonic()<end+10,snapshot
    time.sleep(.02)
failure=snapshot.failure
assert failure.classification==job.FailureClass.PERMANENT and failure.cause==job.FailureCause.CREDENTIAL,failure
assert failure.message=="download attempt failed: credential:revoked:hf",failure
missing=download.Request(artifact=download.Artifact(digest="",size=0),
                         sources=[download.Source(scheme="http",locator=sys.argv[3]+"/missing",credential="missing")])
refused=restored.submit(job.Submission(identity=job.RequestIdentity(key="missing-python-key",history_epoch=window.history_epoch),
                                       kind="download",spec=download.encode(missing),required_guarantees=list(download.CREDENTIAL_GUARANTEES)))
assert refused.outcome=="invalid" and refused.reason=="credential:unknown:missing" and refused.receipt is None,refused
print("PASS: real lost reply/reconcile, fixed restored owner, wait cancellation distinct, exact bounded bytes, unknown/conflict, inventory, derived label, network:metered wait, credential cause and admission refusal")
