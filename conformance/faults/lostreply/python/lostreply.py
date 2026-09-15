"""Lose the reply to one committed request frame in Python client fixtures.

LostReply wraps a transport with exchange_frame(frame) -> bytes. The wrapped
exchange runs to completion, so the service has processed the request and
answered; the caller receives a connection-loss error instead of the reply. It
fires once, for the first frame whose JSON "method" member equals the method.

preload_environment builds the environment for a subprocess whose frames pass
through the native IPC library, loading the shared lostreply.c shim.
"""
import json
import os
import threading


def _connection_lost(message):
    try:
        from abstraction.ipc import DISCONNECTED, FrameError
    except ImportError:
        return ConnectionResetError(message)
    return FrameError(DISCONNECTED, message)


def _method(frame):
    try:
        value = json.loads(frame)
    except (TypeError, ValueError, UnicodeDecodeError):
        return None
    return value.get("method") if isinstance(value, dict) else None


class LostReply:
    """Transport wrapper; forwards call_scope and write_frame to the inner transport."""

    def __init__(self, inner, method="Submit", error=_connection_lost):
        self.inner, self.method, self._error = inner, method or "Submit", error
        self._lock = threading.Lock()
        self.fired = False
        self.discarded = 0

    def exchange_frame(self, frame):
        reply = self.inner.exchange_frame(frame)
        with self._lock:
            if not self.fired and _method(frame) == self.method:
                self.fired, self.discarded = True, len(reply)
                raise self._error("fixture discarded the committed " + self.method + " reply")
        return reply

    def write_frame(self, frame):
        return self.inner.write_frame(frame)

    def call_scope(self):
        return self.inner.call_scope()


def preload_environment(shim, marker=None, method="Submit", base=None):
    """Environment for a native-IPC subprocess with the lostreply.c shim preloaded."""
    env = dict(os.environ if base is None else base)
    preload = env.get("LD_PRELOAD")
    env["LD_PRELOAD"] = str(shim) + (":" + preload if preload else "")
    env["OA_LOST_REPLY_METHOD"] = method
    if marker is not None:
        env["OA_LOST_REPLY_MARKER"] = str(marker)
    return env
