"""The capture tool's wait for the Panel's URL keeps its deadline."""

from pathlib import Path
import importlib.util
import os
import time
import unittest


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("macos_capture_window", HERE / "capture_window.py")
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class AnnouncedURL(unittest.TestCase):
    def pipe(self, text):
        read, write = os.pipe()
        os.write(write, text.encode())
        stream = os.fdopen(read, "r")
        self.addCleanup(stream.close)
        self.addCleanup(os.close, write)
        return stream

    def test_a_half_written_line_does_not_outlast_the_deadline(self):
        stream = self.pipe("service control panel: http://127.0.0.1:1/?k=")
        started = time.monotonic()
        self.assertIsNone(capture.announced_url(stream, 0.5))
        self.assertLess(time.monotonic() - started, 2)

    def test_the_announced_url_is_returned_on_localhost(self):
        stream = self.pipe("starting\nservice control panel: http://127.0.0.1:41/start?k=abc\n")
        self.assertEqual(capture.announced_url(stream, 5), "http://localhost:41/start?k=abc")


if __name__ == "__main__":
    unittest.main()
