"""A person's quit keeps the launcher; logout, restart and shutdown end it.

The fixture runs the real AppDelegate with its window open (.regular) and
sends itself kAEQuitApplication Apple Events, the event the Dock and the
Cmd-Tab switcher send. Sending to its own process needs no Automation
permission. A quit with no reason must close the window, return to
.accessory and leave the process and its owned child running; a quit with
the logout reason must let it exit and reap the child. It runs hidden
(fixtureHidesWindow: the policy is the one the launcher sets, recorded
instead of applied) unless OA_OWNER_PRESENT=1.
"""

from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
IDENTIFIER = "org.openabstractions.panel.quit-fixture"

FIXTURE = r'''
func sendQuit(reason: OSType?) {
    let target = NSAppleEventDescriptor(processIdentifier: getpid())
    let event = NSAppleEventDescriptor.appleEvent(withEventClass: AEEventClass(kCoreEventClass),
                                                  eventID: AEEventID(kAEQuitApplication),
                                                  targetDescriptor: target,
                                                  returnID: AEReturnID(kAutoGenerateReturnID),
                                                  transactionID: AETransactionID(kAnyTransactionID))
    if let reason {
        event.setAttribute(NSAppleEventDescriptor(enumCode: reason), forKeyword: AEKeyword(kAEQuitReason))
    }
    do { _ = try event.sendEvent(options: [.noReply], timeout: 5) }
    catch { print("quit event failed: \(error)"); exit(4) }
}

// Hidden unless the owner is present: the window is transparent, the
// launcher never activates, and the policy is recorded (fixtureHidesWindow).
fixtureHidesWindow = ProcessInfo.processInfo.environment["OA_OWNER_PRESENT"] != "1"
precondition(!panelQuitEndsLauncher(nil))
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
    precondition(panelActivationPolicy() == .regular, "the window did not open")
    sendQuit(reason: nil)
    DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
        precondition(panelActivationPolicy() == .accessory, "a reasonless quit kept the Dock icon")
        print("reasonless quit kept the launcher")
        fflush(stdout)
        sendQuit(reason: kAELogOut)
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) {
            print("logout quit did not end the launcher")
            exit(5)
        }
    }
}
app.run()
'''


@unittest.skipUnless(sys.platform == "darwin", "Swift AppKit quit fixture requires macOS")
class LauncherQuit(unittest.TestCase):
    def tearDown(self):
        for folder in ("Library/Caches", "Library/WebKit", "Library/HTTPStorages"):
            shutil.rmtree(Path.home() / folder / IDENTIFIER, ignore_errors=True)

    def test_person_quit_keeps_the_agent_and_logout_ends_it(self):
        with tempfile.TemporaryDirectory() as scratch:
            scratch = Path(scratch)
            contents = scratch / "Quit Fixture.app/Contents"
            macos = contents / "MacOS"
            macos.mkdir(parents=True)
            with (contents / "Info.plist").open("wb") as metadata:
                plistlib.dump({"CFBundleIdentifier": IDENTIFIER, "CFBundleName": "OA Quit Fixture",
                              "CFBundlePackageType": "APPL", "CFBundleExecutable": "Quit Fixture",
                              "LSUIElement": True}, metadata)
            marker = scratch / "child.pid"
            child = macos / "panel"
            child.write_text("#!/bin/sh\necho $$ > '%s'\n"
                             "printf '%%s\\n' 'service control panel: oa-panel://panel/?k=fixture'\n"
                             "exec /bin/sleep 60\n" % marker)
            child.chmod(0o755)
            source = scratch / "main.swift"
            source.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + FIXTURE)
            executable = macos / "Quit Fixture"
            subprocess.run(["swiftc", "-D", "PANEL_WINDOW_FIXTURE", str(source), "-o", str(executable)],
                           check=True, timeout=120)
            completed = subprocess.run([str(executable)], capture_output=True, text=True, timeout=30)
            self.assertIn("reasonless quit kept the launcher", completed.stdout, completed.stdout + completed.stderr)
            self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
            pid = int(marker.read_text().strip())
            alive = subprocess.run(["/bin/kill", "-0", str(pid)], capture_output=True)
            self.assertNotEqual(alive.returncode, 0, "the logout quit left the owned child running")


if __name__ == "__main__":
    unittest.main()
