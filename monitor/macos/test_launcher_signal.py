"""Compile the launcher's signal/child path and exercise it without AppKit.run."""

import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent


@unittest.skipUnless(sys.platform == "darwin", "Swift AppKit fixture requires macOS")
class LauncherTermination(unittest.TestCase):
    def test_sigterm_reaps_only_the_owned_panel_child(self):
        with tempfile.TemporaryDirectory() as scratch:
            scratch = Path(scratch)
            fixture = scratch / "main.swift"
            fixture.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + """let child = Process()
child.executableURL = URL(fileURLWithPath: "/bin/sleep")
child.arguments = ["30"]
try child.run()
let terminationSignal = installTerminationSignalHandler {
    stopOwnedPanel(child)
    exit(0)
}
print(child.processIdentifier)
fflush(stdout)
withExtendedLifetime(terminationSignal) { RunLoop.main.run() }
""")
            executable = scratch / "launcher-signal-fixture"
            subprocess.run(["swiftc", "-D", "PANEL_SIGNAL_FIXTURE", str(fixture),
                            "-o", str(executable)], check=True, timeout=90)
            unrelated = subprocess.Popen(["/bin/sleep", "30"])
            launcher = subprocess.Popen([str(executable)], stdout=subprocess.PIPE, text=True)
            child_pid = None
            try:
                ready, _, _ = select.select([launcher.stdout], [], [], 8)
                self.assertTrue(ready, "launcher fixture did not report its child PID")
                child_pid = int(launcher.stdout.readline().strip())
                os.kill(launcher.pid, signal.SIGTERM)
                self.assertEqual(launcher.wait(timeout=8), 0)
                with self.assertRaises(ProcessLookupError):
                    os.kill(child_pid, 0)
                self.assertIsNone(unrelated.poll())
            finally:
                if launcher.poll() is None:
                    launcher.terminate()
                    launcher.wait(timeout=8)
                launcher.stdout.close()
                if child_pid is not None:
                    try:
                        os.kill(child_pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                unrelated.terminate()
                unrelated.wait(timeout=8)


if __name__ == "__main__":
    unittest.main()
