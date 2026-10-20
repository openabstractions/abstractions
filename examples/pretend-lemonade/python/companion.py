"""The journey's second client language: a Python companion to pretend-lemonade.

It writes attributed events through the Python logging adapter, asks the same
runtime for the same kind of download job and tries to read the account's log
history. Run by python.exe, it is identified as that interpreter, a script host:
no account-wide default covers it (VISION 2026-10-07), so its download is
refused unless the person writes a rule for the interpreter itself, and its
history read is refused without the history permission.

    python companion.py --runtime-program PATH URL SIZE sha256:DIGEST

ABSTRACTION_RUNTIME_ENDPOINT names the runtime and --runtime-program its
executable, verified as this account's. The OA Python packages and the native
IPC library come from the environment: PYTHONPATH (or an installed set) and
ABSTRACTION_IPC_PREFIX or ABSTRACTION_IPC_LIBRARY. Exit status 0 means every
step reported its outcome; the outcomes themselves are printed one per line.
"""
import argparse
import hashlib
import logging
import os
import subprocess
import sys
import time


def account_principal():
    """The current account as the IPC library's server expectation names it."""
    if sys.platform == "win32":
        out = subprocess.run(["whoami", "/user", "/fo", "csv", "/nh"], capture_output=True, text=True, check=True).stdout
        return 1, out.strip().split(",")[-1].strip('"')
    return 2, str(os.getuid())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--runtime-program", required=True, help="absolute path of the runtime executable")
    parser.add_argument("url")
    parser.add_argument("size", type=int)
    parser.add_argument("digest")
    args = parser.parse_args(argv)
    endpoint = os.environ.get("ABSTRACTION_RUNTIME_ENDPOINT", "")
    if not endpoint or not os.path.isabs(args.runtime_program):
        parser.error("needs ABSTRACTION_RUNTIME_ENDPOINT and an absolute --runtime-program")

    from abstraction.ipc import ServerExpectation
    from abstraction.facade import Scope
    from abstraction.facade.client import Machine
    from abstraction.logging.handler import ServiceHandler
    import abstraction.download.request as request
    from abstraction.job.acceptance import RequestIdentity, Submission

    kind, principal = account_principal()
    server = ServerExpectation(principal_kind=kind, principal=principal, program=os.path.normpath(args.runtime_program))

    def machine():
        return Machine(endpoint, server=server, timeout=10.0)

    handler = ServiceHandler(machine().resolve_log(scope=Scope.LOCAL), program=sys.executable)
    log = logging.getLogger("pretend-lemonade.companion")
    log.addHandler(handler)
    log.setLevel(logging.INFO)
    log.propagate = False
    log.info("companion started")
    print("program: " + sys.executable)

    jobs = machine().resolve_jobs(scope=Scope.LOCAL)
    window = jobs.get_history_window()
    key = "pretend-lemonade-py-" + hashlib.sha256("\n".join((args.url, str(args.size), args.digest)).encode()).hexdigest()
    identity = RequestIdentity(key=key, history_epoch=window.history_epoch)
    spec = request.encode(request.Request(artifact=request.Artifact(size=args.size, digest=args.digest),
                                          sources=[request.Source(scheme="http", locator=args.url)]))
    accepted = jobs.submit(Submission(identity=identity, kind="download", spec=spec))
    outcome = str(getattr(accepted.outcome, "value", accepted.outcome))
    print("download: " + outcome + (" " + accepted.reason if accepted.reason else ""))
    log.info("download " + outcome)

    try:
        page = machine().resolve_log_reader(scope=Scope.LOCAL).read("", 16, 65536)
        print("history: " + str(getattr(page.outcome, "value", page.outcome)))
    except Exception as error:  # a refused reader raises a typed service error
        print("history: refused (%s)" % type(error).__name__ + (": " + str(error) if str(error) else ""))

    counts = handler.close()
    print("log records written: %d of %d" % (counts.written, counts.accepted))
    return 0


if __name__ == "__main__":
    sys.exit(main())
