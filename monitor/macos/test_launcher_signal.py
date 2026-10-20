"""Exercise the launcher's owned-child shutdown in ordinary and modal loops."""

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
        self.check_sigterm_reaps_owned_child(modal=False)

    def test_sigterm_during_native_modal_reaps_only_the_owned_panel_child(self):
        self.check_sigterm_reaps_owned_child(modal=True)

    def check_sigterm_reaps_owned_child(self, modal):
        with tempfile.TemporaryDirectory() as scratch:
            scratch = Path(scratch)
            fixture = scratch / "main.swift"
            start = """let app = NSApplication.shared
app.setActivationPolicy(.accessory)
""" if modal else ""
            # The modal loop is the thing tested; its alert is the fixture's
            # own, transparent and click-through so nothing shows on screen.
            wait = """let alert = NSAlert()
alert.messageText = "Owned signal fixture"
alert.addButton(withTitle: "Cancel")
alert.window.alphaValue = 0
alert.window.hasShadow = false
alert.window.ignoresMouseEvents = true
let ready = Timer(timeInterval: 0.05, repeats: false) { _ in
    print(child.processIdentifier)
    fflush(stdout)
}
RunLoop.main.add(ready, forMode: .modalPanel)
withExtendedLifetime(terminationSignal) { _ = alert.runModal() }
""" if modal else """print(child.processIdentifier)
fflush(stdout)
withExtendedLifetime(terminationSignal) { RunLoop.main.run() }
"""
            fixture.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + start + """let child = Process()
child.executableURL = URL(fileURLWithPath: "/bin/sleep")
child.arguments = ["30"]
try child.run()
let terminationSignal = installTerminationSignalHandler {
    stopOwnedPanel(child)
    exit(0)
}
""" + wait)
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
