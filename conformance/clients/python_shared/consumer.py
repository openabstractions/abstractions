"""Executed with -I from an empty application directory using installed packages."""
import pathlib
import os
import sys
import threading
import time
from unittest.mock import patch
sys.path.insert(0, sys.argv[1])
from abstraction.ipc import Library, FrameTransport, FrameError, CANCELLED, TIMEOUT, INVALID_ARGUMENT, UNTRUSTED, ServerExpectation
from abstraction.facade.client import Machine, ResolutionError
from abstraction.facade import rec as wire
from abstraction.logging import rec as logging

assert pathlib.Path(wire.__file__).is_relative_to(sys.argv[1])
assert pathlib.Path(logging.__file__).is_relative_to(sys.argv[1])
lib = Library() # fixture configures the installed prefix; no cwd DLL search
assert pathlib.Path(sys.argv[2]).resolve()==pathlib.Path(lib._dll._name).resolve()
endpoint, mode = sys.argv[3:5]
server_identity = ServerExpectation(1 if os.name=="nt" else 2, sys.argv[5], sys.argv[6])
if mode=="bootstrap":
    assert lib.runtime_endpoint()==endpoint, (lib.runtime_endpoint(),endpoint)
    print("PASS bootstrap")
    raise SystemExit(0)
assert lib.runtime_endpoint()==endpoint

# A forged resolver reference must be refused before its endpoint is contacted.
for changed, expected in [({"contract":"wrong"},"invalid_resolution"),
                          ({"guarantees":[]},"invalid_resolution"),
                          ({"scope":"remote"},"invalid_resolution"),
                          ({"transport":"wrong"},"unsupported_transport")]:
    fields=dict(provider="fixture",capability="abstraction.logging",contract="abstraction.logging/sink@1",
                guarantees=["required"],scope="local",transport="oa-framed-local@1",endpoint="never-contact")
    fields.update(changed)
    class Resolver:
        def Resolve(self, request):
            return wire.ResolveResult(status="resolved",reference=wire.ServiceReference(**fields))
    machine=Machine(endpoint,lib)
    try:
        with patch("abstraction.facade.client.wire.ResolverClient", return_value=Resolver()):
            machine.resolve_log(guarantees=["required"],scope="local")
        raise AssertionError("invalid resolver reference accepted")
    except ResolutionError as error:
        assert error.status==expected,(error.status,expected)

if mode == "log":
    bad = ServerExpectation(server_identity.principal_kind, server_identity.principal, server_identity.program+".wrong")
    try:
        Machine(endpoint, lib, server=bad).resolve_log()
        raise AssertionError("wrong runtime image accepted")
    except FrameError as error:
        assert error.status == UNTRUSTED, error.status
    machine = Machine(library=lib, server=server_identity, deadline=time.monotonic()+3)
    logger = machine.resolve_log(scope="local")
    logger.Write(logging.Record(schema=1, time="2026-09-12T12:00:00.000000Z", level=2,
                                msg="python shared IPC ✓", attrs={"component":"outside-consumer"}))
    history = machine.resolve_log_reader(scope="local")
    page = history.Read("", 1, 65536)
    assert page.outcome == "page" and len(page.records) == 1, page
    assert page.records[0].msg == "python shared IPC ✓"
    assert page.records[0].attrs["component"] == "outside-consumer"
    end = history.Read(page.next, 1, 65536)
    assert end.outcome == "page" and end.at_end and not end.records
    assert history.Read("foreign:0", 1, 65536).outcome == "gap"
    assert history.Read("", 1, 1).outcome == "record_too_large"
    try:
        machine.resolve_log(guarantees=["unsupported-test-guarantee"])
        raise AssertionError("requirements silently weakened")
    except ResolutionError as e:
        assert e.status == "unmet_requirements", e.status
elif mode == "absent":
    try:
        Machine(endpoint, lib, timeout=0.2).resolve_log()
        raise AssertionError("absent runtime succeeded")
    except FrameError:
        pass
elif mode == "oversized":
    try:
        FrameTransport(lib, endpoint).exchange_frame(b"x")
        raise AssertionError("oversized response accepted")
    except FrameError as e:
        assert e.status == INVALID_ARGUMENT
elif mode == "cancel":
    with lib.cancellation() as token:
        timer = threading.Timer(0.1, token.signal); timer.start()
        try:
            FrameTransport(lib, endpoint, timeout=2, cancellation=token).exchange_frame(b"x")
            raise AssertionError("cancelled wait succeeded")
        except FrameError as e:
            assert e.status == CANCELLED, e.status
        finally:
            timer.join()
else:
    raise AssertionError(mode)

# These invalid/expired requests perform no endpoint I/O.
try:
    FrameTransport(lib, endpoint, deadline=time.monotonic()-1).exchange_frame(b"x")
    raise AssertionError("expired wait succeeded")
except FrameError as e:
    assert e.status == TIMEOUT
with lib.cancellation() as token:
    token.signal()
    try:
        FrameTransport(lib, endpoint, cancellation=token).exchange_frame(b"x")
        raise AssertionError("pre-cancelled wait succeeded")
    except FrameError as e:
        assert e.status == CANCELLED
print("PASS", mode)
