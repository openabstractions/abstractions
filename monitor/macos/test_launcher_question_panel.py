"""OA's on-top question panel: placement, window traits and the visibility probe.

Asks fixtures (PANEL_ASKS_FIXTURE) show nothing on the owner's screen: their
panels are transparent and silent (fixtureHidesPresentation). The live
on-top check is the one exception. It shows one panel for about a second in
the console session and reads the window server's on-screen list (numbers,
owners, layers, bounds; never names, which would need Screen Recording). It
runs only with OA_OWNER_PRESENT=1, during an owner-present test the owner was
told about, and exits 3, skipped, without a console GUI session.
"""

from pathlib import Path
import os
import subprocess
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent


def launcher_symbols(scratch, *defines):
    """Compile the launcher with defines and return its symbol table. It
    builds without optimisation, which keeps every global's symbol: -O (the
    release build, build.sh) folds the switch away even in a fixture build.
    The #if decides what is compiled; optimisation only removes code, so a
    switch absent here is absent from the release binary too."""
    # The module name shares no word with the switch's name: Swift's mangling
    # abbreviates a repeated word, which would hide the name from the check.
    executable = scratch / ("launcher" + "".join("-" + d for d in defines).lower())
    command = ["swiftc", "-Onone", "-module-name", "OALauncher"]
    for define in defines:
        command += ["-D", define]
    subprocess.run(command + [str(HERE / "PanelLauncher.swift"), "-o", str(executable)], check=True, timeout=180)
    return subprocess.run(["nm", "-a", str(executable)], check=True, capture_output=True, text=True).stdout


def build_and_run(suffix, name, timeout=20):
    with tempfile.TemporaryDirectory(prefix="oa-question-panel-") as temp:
        scratch = Path(temp)
        source = scratch / "main.swift"
        source.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + suffix)
        executable = scratch / name
        subprocess.run(["swiftc", "-D", "PANEL_ASKS_FIXTURE", str(source), "-o", str(executable)],
                       check=True, timeout=120)
        return subprocess.run([str(executable)], capture_output=True, text=True, timeout=timeout)


@unittest.skipUnless(sys.platform == "darwin", "AppKit question panel fixture requires macOS")
class QuestionPanel(unittest.TestCase):
    def test_shipped_launcher_has_no_fixture_switch(self):
        # The release source configuration (build.sh, installer payload: no
        # defines) compiles no fixture switch; the fixture configuration
        # does, so the check cannot pass by a symbol going missing.
        with tempfile.TemporaryDirectory(prefix="oa-question-panel-symbols-") as temp:
            scratch = Path(temp)
            release = launcher_symbols(scratch)
            for define, switch in (("PANEL_ASKS_FIXTURE", "fixtureHidesPresentation"),
                                   ("PANEL_WINDOW_FIXTURE", "fixtureHidesWindow"),
                                   ("PANEL_WINDOW_FIXTURE", "fixtureActivationPolicy")):
                self.assertIn(switch, launcher_symbols(scratch, define), define)
                self.assertNotIn(switch, release)

    def test_placement_traits_buttons_and_probe(self):
        completed = build_and_run(r'''
// Placement: centred across the pointer's screen, its middle at two thirds
// of the visible height, inside the visible frame on any screen.
precondition(questionPanelOrigin(size: NSSize(width: 460, height: 200),
    visibleFrame: NSRect(x: 0, y: 25, width: 1440, height: 875)) == NSPoint(x: 490, y: 508))
precondition(questionPanelOrigin(size: NSSize(width: 460, height: 1000),
    visibleFrame: NSRect(x: 0, y: 25, width: 1440, height: 875)).y == 25)
precondition(questionPanelOrigin(size: NSSize(width: 460, height: 200),
    visibleFrame: NSRect(x: -1920, y: 0, width: 1920, height: 1050)) == NSPoint(x: -1190, y: 600))

// The probe counts other processes' visible windows in front that overlap,
// at the panel's level or below; system UI above it (the Dock's screen-wide
// layer-20 window, the menu bar) does not hide it.
func entry(_ number: Int, pid: Int32, layer: Int, _ bounds: CGRect, alpha: Double = 1) -> [String: Any] {
    [kCGWindowNumber as String: NSNumber(value: number), kCGWindowOwnerPID as String: NSNumber(value: pid),
     kCGWindowLayer as String: NSNumber(value: layer), kCGWindowAlpha as String: NSNumber(value: alpha),
     kCGWindowBounds as String: bounds.dictionaryRepresentation as NSDictionary,
     kCGWindowIsOnscreen as String: NSNumber(value: true)]
}
let mine = CGRect(x: 490, y: 300, width: 460, height: 200)
let list = [entry(1, pid: 50, layer: 25, CGRect(x: 0, y: 0, width: 1440, height: 24)),
            entry(2, pid: 60, layer: 8, CGRect(x: 600, y: 350, width: 100, height: 100), alpha: 0),
            entry(3, pid: 99, layer: 8, CGRect(x: 0, y: 0, width: 600, height: 400)),
            entry(10, pid: 99, layer: 8, mine),
            entry(4, pid: 70, layer: 0, CGRect(x: 0, y: 0, width: 1440, height: 900))]
precondition(questionPanelVisibility(list, window: 10, pid: 99) == QuestionPanelVisibility(found: true, onScreen: true, layer: 8, occluders: 0))
let dock = entry(6, pid: 81, layer: 20, CGRect(x: 0, y: 0, width: 1440, height: 900))
let covered = [dock, entry(5, pid: 80, layer: 8, CGRect(x: 500, y: 310, width: 50, height: 50))] + list
precondition(questionPanelVisibility(covered, window: 10, pid: 99).occluders == 1)
precondition(questionPanelVisibility([dock] + list, window: 10, pid: 99).occluders == 0)
precondition(!questionPanelVisibility(list, window: 11, pid: 99).found)

// The window: a non-activating panel at the modal-panel level on every
// Space and beside full-screen windows, which stays when the app deactivates.
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let q = NativeQuestionPanel()
let w = q.window
precondition(w.styleMask.contains(.nonactivatingPanel) && w.styleMask.contains(.titled) && !w.styleMask.contains(.closable))
precondition(w.level == .modalPanel && w.collectionBehavior.contains(.canJoinAllSpaces) && w.collectionBehavior.contains(.fullScreenAuxiliary))
precondition(!w.hidesOnDeactivate && !w.becomesKeyOnlyIfNeeded && w.canBecomeKey && !w.canBecomeMain)
q.messageText = "Allow pretend-app to read one named secret?"
q.informativeText = "Refuse changes no permissions."
let allow = q.addButton(withTitle: "Allow")
let refuse = q.addButton(withTitle: "Don't Allow")
let cancel = q.addButton(withTitle: "Cancel")
cancel.keyEquivalent = "\u{1b}"
q.layout()
precondition(allow.frame.minX > refuse.frame.minX && refuse.frame.minX > cancel.frame.minX, "the first button is not rightmost")
precondition([allow, refuse, cancel].map(\.tag) == [0, 1, 2])
// Details grow the panel; the buttons stay below the accessory.
let details = NativeQuestionDetails(text: "Exact request", alert: q)
q.accessoryView = details.view
q.retained.append(details)
q.layout()
let before = w.frame.height
details.view.subviews.compactMap { $0 as? NSButton }.first!.performClick(nil)
print("details: before \(before) after \(w.frame.height) details \(details.view.frame) allow \(allow.frame)"); fflush(stdout)
precondition(w.frame.height > before, "details did not grow the panel")
precondition(details.view.frame.minY > allow.frame.maxY, "the buttons are not below the details")
// Esc presses the escape button; the panel hides and completes once. In a
// fixture it is transparent and lets clicks through while it is up.
var responses: [NSApplication.ModalResponse] = []
var armedCount = 0
q.onArm = { for button in q.buttons { button.isEnabled = true } }
q.onArmed = { armedCount += 1 }
q.present { responses.append($0) }
precondition(fixtureHidesPresentation && w.alphaValue == 0 && w.ignoresMouseEvents, "a fixture panel is visible")
// Every button, Cancel included, waits for the panel to arm: Esc and a
// click before then answer nothing.
precondition(q.buttons.allSatisfy { !$0.isEnabled } && !q.armed, "a button was enabled before arming")
w.cancelOperation(nil)
allow.performClick(nil)
precondition(responses.isEmpty, "the panel answered before it was armed")
RunLoop.main.run(until: Date().addingTimeInterval(nativeQuestionArmDelay + 0.1))
precondition(q.armed && armedCount == 1 && q.buttons.allSatisfy { $0.isEnabled }, "the panel did not arm")
// A move to another screen disarms every button and arms again after the
// full delay; a move within the same screen does not.
let screenA = NSRect(x: 0, y: 0, width: 1440, height: 900), screenB = NSRect(x: 1440, y: 0, width: 1920, height: 1080)
// screenA is not this Mac's screen: the first move is to another screen.
q.place(visibleFrame: screenA, screenFrame: screenA)
precondition(!q.armed && q.buttons.allSatisfy { !$0.isEnabled }, "a move to another screen kept the panel armed")
RunLoop.main.run(until: Date().addingTimeInterval(nativeQuestionArmDelay + 0.1))
precondition(q.armed && armedCount == 2, "the panel did not arm again")
q.place(visibleFrame: screenA.insetBy(dx: 0, dy: 20), screenFrame: screenA)
precondition(q.armed && q.buttons.allSatisfy { $0.isEnabled }, "a move within one screen disarmed the panel")
q.place(visibleFrame: screenB, screenFrame: screenB)
precondition(!q.armed && q.buttons.allSatisfy { !$0.isEnabled }, "a move to another screen kept the panel armed")
w.cancelOperation(nil)
precondition(responses.isEmpty, "Esc answered on the new screen before arming")
RunLoop.main.run(until: Date().addingTimeInterval(nativeQuestionArmDelay / 2))
precondition(!q.armed, "the panel armed again before the full delay")
RunLoop.main.run(until: Date().addingTimeInterval(nativeQuestionArmDelay / 2 + 0.1))
precondition(q.armed && armedCount == 3, "the panel did not arm again")
w.cancelOperation(nil)
allow.performClick(nil)
precondition(responses == [NSApplication.ModalResponse(rawValue: NSApplication.ModalResponse.alertFirstButtonReturn.rawValue + 2)] && !w.isVisible)
// A dismissal completes with .abort once, on the next turn of the loop.
let report = NativeQuestionPanel()
report.addButton(withTitle: "OK")
var aborted = 0
report.present { precondition($0 == .abort); aborted += 1 }
report.abort()
report.abort()
precondition(aborted == 0 && !report.window.isVisible)
RunLoop.main.run(until: Date().addingTimeInterval(0.1))
precondition(aborted == 1)
print("question panel fixture passed")
''', "question-panel-fixture")
        if completed.returncode:
            print(completed.stdout + completed.stderr, file=sys.stderr)
        self.assertEqual(completed.returncode, 0)
        self.assertIn("question panel fixture passed", completed.stdout)

    @unittest.skipUnless(os.environ.get("OA_OWNER_PRESENT") == "1",
                         "shows a panel on the owner's screen: owner-present tests only (OA_OWNER_PRESENT=1)")
    def test_panel_is_on_top_without_taking_focus(self):
        completed = build_and_run(r'''
fixtureHidesPresentation = false
guard let session = CGSessionCopyCurrentDictionary() as? [String: Any],
      exactSessionBoolean(session[kCGSessionOnConsoleKey as String]) == true else {
    print("no console GUI session")
    exit(3)
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
app.finishLaunching()
let front = NSWorkspace.shared.frontmostApplication
let q = NativeQuestionPanel()
q.window.title = "Question panel probe"
q.messageText = "OpenAbstractions question panel probe"
q.informativeText = "This closes by itself."
let ok = q.addButton(withTitle: "OK")
q.onArm = { ok.isEnabled = true }
q.present { _ in }
RunLoop.main.run(until: Date().addingTimeInterval(0.6))
let seen = liveQuestionPanelVisibility(q.window)
let now = NSWorkspace.shared.frontmostApplication
let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
for entry in windows.prefix(while: { ($0[kCGWindowNumber as String] as? NSNumber)?.intValue != q.window.windowNumber })
    where ((entry[kCGWindowLayer as String] as? NSNumber)?.intValue ?? 0) <= q.window.level.rawValue {
    print("in front at or below its level: owner \(entry[kCGWindowOwnerName as String] ?? "?") layer \(entry[kCGWindowLayer as String] ?? "?") alpha \(entry[kCGWindowAlpha as String] ?? "?") bounds \(entry[kCGWindowBounds as String] ?? "?")")
}
print("panel frame \(q.window.frame) windowNumber \(q.window.windowNumber)")
print("probe: \(seen.summary) activeSpace \(q.window.isOnActiveSpace) key \(q.window.isKeyWindow) active \(NSApp.isActive) frontmost \(now?.bundleIdentifier ?? "none") was \(front?.bundleIdentifier ?? "none")")
fflush(stdout)
q.abort()
precondition(seen.found && seen.onScreen, "the panel is not in the on-screen window list")
precondition(seen.layer == q.window.level.rawValue, "the panel is not at the modal-panel level")
precondition(seen.occluders == 0, "another app's window covers the panel")
precondition(q.window.isOnActiveSpace, "the panel is not on the active Space")
precondition(!NSApp.isActive && !q.window.isKeyWindow && now?.processIdentifier == front?.processIdentifier,
             "showing the question took focus from the app in front")
print("question panel on-top fixture passed")
''', "question-panel-live")
        if completed.returncode == 3:
            self.skipTest(completed.stdout.strip())
        print(completed.stdout.strip())
        if completed.returncode:
            print(completed.stderr, file=sys.stderr)
        self.assertEqual(completed.returncode, 0)
        self.assertIn("question panel on-top fixture passed", completed.stdout)


if __name__ == "__main__":
    unittest.main()
