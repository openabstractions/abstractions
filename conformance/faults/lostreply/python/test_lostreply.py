"""Controls for lostreply.py; run with python3 test_lostreply.py."""
import json
import os
import sys
import threading
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lostreply import LostReply, preload_environment  # noqa: E402


def frame(method, key="k"):
    return json.dumps({"version": 1, "service": "abstraction.job/acceptance@1",
                       "method": method, "arguments": {"key": key}}).encode()


class Recorder:
    def __init__(self, fail=None):
        self.frames, self.fail, self.writes, self.scopes = [], fail, 0, 0

    def exchange_frame(self, value):
        self.frames.append(value)
        if self.fail:
            raise self.fail
        return b'{"version":1,"ok":true,"payload":{}}'

    def write_frame(self, value):
        self.writes += 1

    def call_scope(self):
        self.scopes += 1
        return self


class Controls(unittest.TestCase):
    def test_loses_one_committed_reply(self):
        inner = Recorder()
        fault = LostReply(inner)
        self.assertTrue(fault.exchange_frame(frame("GetHistoryWindow", key="Submit")))
        with self.assertRaises(Exception) as caught:
            fault.exchange_frame(frame("Submit"))
        self.assertTrue(isinstance(caught.exception, ConnectionResetError) or type(caught.exception).__name__ == "FrameError")
        self.assertTrue(fault.fired and fault.discarded > 0)
        self.assertEqual(len(inner.frames), 2)
        self.assertTrue(fault.exchange_frame(frame("Submit")))
        fault.write_frame(frame("Write"))
        self.assertIs(fault.call_scope(), inner)
        self.assertEqual((inner.writes, inner.scopes), (1, 1))

    def test_transport_errors_pass_through_without_firing(self):
        broken = OSError("dial failed")
        fault = LostReply(Recorder(fail=broken))
        with self.assertRaises(OSError) as caught:
            fault.exchange_frame(frame("Submit"))
        self.assertIs(caught.exception, broken)
        self.assertFalse(fault.fired)

    def test_malformed_frame_forwarded(self):
        self.assertTrue(LostReply(Recorder()).exchange_frame(b'not json "method":"Submit"'))

    def test_fires_once_under_concurrency(self):
        fault, lost, lock = LostReply(Recorder()), [], threading.Lock()

        def call():
            try:
                fault.exchange_frame(frame("Submit"))
            except Exception:
                with lock:
                    lost.append(1)
        threads = [threading.Thread(target=call) for _ in range(16)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual(len(lost), 1)

    def test_preload_environment(self):
        env = preload_environment("/fixture/lostreply.so", "/tmp/marker", "Reconcile", base={"LD_PRELOAD": "/other.so"})
        self.assertEqual(env["LD_PRELOAD"], "/fixture/lostreply.so:/other.so")
        self.assertEqual((env["OA_LOST_REPLY_METHOD"], env["OA_LOST_REPLY_MARKER"]), ("Reconcile", "/tmp/marker"))


if __name__ == "__main__":
    unittest.main()
