"""A Python application resolves the credentials holder and applier through the
facade over the shared native transport. It holds no holder.read rule and is no
designated enforcer: both calls read forbidden, and no reply carries a secret.

    py_consumer.py <runtime endpoint> <account> [secret ...]

ABSTRACTION_IPC_LIBRARY names the shared abstraction_ipc library, and PYTHONPATH
holds the identity, facade and credentials Python sources.
"""
import sys

if len(sys.argv) < 3:
    print(__doc__)
    raise SystemExit(0 if sys.argv[1:] == ["--help"] else 2)

from abstraction.facade.client import Machine
from abstraction.facade import Scope
import abstraction.credentials.api as credentials

endpoint, account, secrets = sys.argv[1], sys.argv[2], sys.argv[3:]
machine = Machine(endpoint, timeout=10.0)
holder = machine.resolve_credentials(scope=Scope.LOCAL)
applier = machine.resolve_credentials_applier(scope=Scope.LOCAL)
page = holder.list(cursor="", limit=64)
assert page.outcome == credentials.PageOutcome.FORBIDDEN and not page.records, page
usage = credentials.Use(subject=credentials.Subject(account=account, program=sys.executable),
                        consumer="abstraction.download/http-execution@1", name="hf", target="huggingface.co")
applied = applier.apply(usage=usage)
assert applied.outcome == credentials.ApplyOutcome.FORBIDDEN and not applied.headers, applied
replies = repr((page, applied))
assert not any(secret in replies for secret in secrets), "a reply carries a secret"
print("PASS python: list=%s apply=%s" % (page.outcome.value, applied.outcome.value))
