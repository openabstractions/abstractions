"""Installed Python job client for the Linux lifecycle fixture.

Run with python3 -I python_client.py PACKAGES MODE ...; PACKAGES holds the
installed identity, facade, job and download-request packages, and
ABSTRACTION_IPC_PREFIX names the installed shared IPC library. It uses default
bootstrap and installation-selected trust and prints the same tokens as the C++
application: IDENTITY, ACCEPTED, UNKNOWN, RECONCILED, NOT_ACCEPTED, RESULT, ERROR.

  submit URL SIZE DIGEST KEY
  result KEY EPOCH OPERATION SECONDS
"""
import io
import sys
import time

sys.path.insert(0, sys.argv[1])
from abstraction.ipc import FrameError  # noqa: E402
from abstraction.facade.client import Machine  # noqa: E402
from abstraction.job.acceptance import rec as job  # noqa: E402
from abstraction.download.request import rec as download  # noqa: E402

RECONCILIATION = ["abstraction.job/reconciliation@1"]


def bind(seconds):
    return Machine(deadline=time.monotonic() + seconds).resolve_job_operations(guarantees=RECONCILIATION, scope="local")


def report(label, result):
    if result.outcome == "accepted" and result.receipt is not None:
        r = result.receipt
        print(label, r.operation_id, r.logical_owner, r.identity.history_epoch, r.identity.key, flush=True)
    else:
        print("NOT_ACCEPTED", result.outcome, result.reason, flush=True)


def main():
    mode, args = sys.argv[2], sys.argv[3:]
    if mode == "submit" and len(args) == 4:
        url, size, digest, key = args
        jobs = bind(15)
        window = jobs.GetHistoryWindow()
        identity = job.RequestIdentity(key=key, history_epoch=window.history_epoch)
        artifact = download.Artifact(digest="" if digest == "-" else digest, size=int(size))
        request = download.Request(artifact=artifact, sources=[download.Source(scheme="http", locator=url)])
        submission = job.Submission(identity=identity, kind="download", spec=download.encode(request),
                                    required_guarantees=RECONCILIATION)
        print("IDENTITY", identity.key, identity.history_epoch, flush=True)
        try:
            report("ACCEPTED", jobs.Submit(submission))
        except FrameError as error:
            print("UNKNOWN frame", error.status, flush=True)
            report("RECONCILED", bind(15).Reconcile(identity))
        return 0
    if mode == "result" and len(args) == 4:
        key, epoch, operation, seconds = args
        deadline = time.monotonic() + int(seconds)
        jobs = bind(int(seconds))
        identity = job.RequestIdentity(key=key, history_epoch=epoch)
        recovered = jobs.Reconcile(identity)
        report("RECONCILED", recovered)
        if recovered.outcome != "accepted" or recovered.receipt.operation_id != operation:
            return 4
        while True:
            observed = jobs.ObserveWork(identity)
            if observed.outcome != "observed":
                raise RuntimeError("operation unobservable: " + observed.outcome)
            if observed.snapshot.receipt.operation_id != operation:
                raise RuntimeError("operation changed")
            if observed.snapshot.state == "complete":
                break
            if observed.snapshot.state in ("failed", "cancelled"):
                print("FAILED", observed.snapshot.state, flush=True)
                return 5
            if time.monotonic() >= deadline:
                raise RuntimeError("observation budget expired")
            time.sleep(0.1)
        output = io.BytesIO()
        copied = jobs.CopyResult(identity, output)
        body = output.getvalue()
        if copied != len(body):
            raise RuntimeError("copy count differs")
        print("RESULT", len(body), body.hex(), flush=True)
        return 0
    print("unknown mode", file=sys.stderr)
    return 2


if __name__ == "__main__":
    try:
        sys.exit(main())
    except FrameError as error:
        print("ERROR frame", error.status, error, flush=True)
        sys.exit(6)
    except Exception as error:
        print("ERROR", type(error).__name__, error, flush=True)
        sys.exit(7)
