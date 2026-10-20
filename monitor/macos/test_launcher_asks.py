"""Native automatic Asks fixtures; disposable XPC registration and private state.

These checks never lock a desktop or wait for the owner's input. The public
operator fixture injects session observations to exercise dialog interruption;
actual lock/unlock and the private registry signal remain separate live checks.
"""

from pathlib import Path
import json
import os
import plistlib
import shutil
import subprocess
import sys
import tempfile
import time
import unittest


HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent


def run(*args, **kwargs):
    result = subprocess.run([str(arg) for arg in args], text=True, **kwargs)
    if result.returncode and kwargs.get("capture_output"):
        print(result.stdout + result.stderr, file=sys.stderr)
    result.check_returncode()
    return result


def fixture_source(scratch, suffix):
    path = scratch / "main.swift"
    path.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + suffix)
    return path


@unittest.skipUnless(sys.platform == "darwin", "Native AppKit/XPC Asks fixture requires macOS")
class LauncherAsks(unittest.TestCase):
    def test_typed_permission_body_is_bounded_without_losing_operations(self):
        with tempfile.TemporaryDirectory(prefix="oa-native-typed-body-") as temp:
            scratch = Path(temp)
            source = fixture_source(scratch, r'''
let data = Data(#"{"kind":"question","id":"typed","prompt":{"Title":"Permission request","Instruction":"Allow this access?","Content":"Complete permission body","PermissionContent":"Complete permission body","Warning":"All present and future saved app secrets.","Details":"Owning resource details","Footer":"Refuse changes no permissions.","Choices":[{"label":"Allow","option":"allow"},{"label":"Refuse","option":"refuse"}],"Default":"refuse"}}"#.utf8)
let frame = try! JSONDecoder().decode(NativeAsksFrame.self, from: data)
let prompt = frame.prompt!
precondition(prompt.defaultOption == "refuse" && prompt.warning!.contains("future"))
precondition(prompt.permissionContent == "Complete permission body")
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let alert = NativeQuestionPanel()
let details = NativeQuestionDetails(text: prompt.details, alert: alert)
let longBody = String(repeating: "Use; Read secret value; Read metadata; Create; Rotate; Revoke.\n", count: 1000)
let body = NativePermissionContent(text: longBody, details: details)
precondition(body.view.frame.height == 264)
let scroll = body.view.subviews.compactMap { $0 as? NSScrollView }.first!
precondition(scroll.frame.height == 220 && scroll.hasVerticalScroller)
let textView = scroll.documentView as! NSTextView
precondition(textView.string == longBody)
textView.layoutManager!.ensureLayout(for: textView.textContainer!)
precondition(textView.frame.height > scroll.contentSize.height,
             "complete text did not create a scrolling document")
precondition(body.view.subviews.contains { $0 === details.view })
let toggle = details.view.subviews.compactMap { $0 as? NSButton }.first!
toggle.performClick(nil)
precondition(body.view.frame.height == 448 && scroll.frame.minY == 228)
toggle.performClick(nil)
precondition(body.view.frame.height == 264 && scroll.frame.minY == 44)
precondition(!alert.window.isVisible)
print("bounded typed native permission fixture passed")
''')
            executable = scratch / "typed-body-fixture"
            run("swiftc", "-D", "PANEL_ASKS_FIXTURE", source, "-o", executable, timeout=90)
            completed = run(executable, capture_output=True, timeout=15)
            self.assertIn("bounded typed native permission fixture passed", completed.stdout)

    def test_prompt_level_badges_in_system_colors_light_and_dark(self):
        with tempfile.TemporaryDirectory(prefix="oa-native-asks-level-") as temp:
            scratch = Path(temp)
            source = fixture_source(scratch, r'''
let question = #"{"kind":"question","id":"l","prompt":{"Title":"Permission request","Instruction":"Allow?","Content":"Body","Details":"","Footer":"","Choices":[{"label":"Allow","option":"allow"}],"Default":"","Level":"orange"}}"#
precondition(try! JSONDecoder().decode(NativeAsksFrame.self, from: Data(question.utf8)).prompt!.level == "orange")
let report = #"{"kind":"report","id":"r","title":"OpenAbstractions","instruction":"Done","content":"","level":"blue"}"#
precondition(try! JSONDecoder().decode(NativeAsksFrame.self, from: Data(report.utf8)).level == "blue")
let old = #"{"kind":"report","id":"r","title":"OpenAbstractions","instruction":"Done","content":""}"#
precondition(try! JSONDecoder().decode(NativeAsksFrame.self, from: Data(old.utf8)).level == nil)

let light = NSAppearance(named: .aqua)!, dark = NSAppearance(named: .darkAqua)!
func rgb(_ color: NSColor) -> (CGFloat, CGFloat, CGFloat) { (color.redComponent, color.greenComponent, color.blueComponent) }
func hue(_ level: String, _ appearance: NSAppearance) -> String {
    let (r, g, b) = rgb(nativePromptLevelColor(level, appearance: appearance)!)
    if b > r + 0.3 && b > g { return "blue" }
    if abs(r - g) < 0.06 && abs(g - b) < 0.08 { return "gray" }
    if r > 0.9 && g > 0.75 && b < 0.3 { return "yellow" }
    if r > 0.9 && g > 0.45 && g < 0.72 && b < 0.3 { return "orange" }
    return "unknown \(r) \(g) \(b)"
}
for level in ["blue", "gray", "yellow", "orange"] {
    for appearance in [light, dark] {
        precondition(hue(level, appearance) == level, "\(level) in \(appearance.name.rawValue) reads as \(hue(level, appearance))")
    }
    let l = rgb(nativePromptLevelColor(level, appearance: light)!), d = rgb(nativePromptLevelColor(level, appearance: dark)!)
    precondition(l != d, "\(level) does not follow the dark appearance")
    precondition(nativePromptLevelDescription(level) != nil)
}
for unknown in [nil, "", "red", "Blue"] as [String?] {
    precondition(nativePromptLevelColor(unknown) == nil && nativePromptLevelDescription(unknown) == nil)
    precondition(nativePromptLevel(unknown, permission: true) == "yellow" && nativePromptLevel(unknown, permission: false) == nil)
}

// The badge sits at the lower right in the level's color for each appearance;
// the rest of the base icon is untouched, and no level keeps the base itself.
let base = NSImage(named: NSImage.applicationIconName)!
precondition(nativeLevelIcon(base, level: nil) === base && nativeLevelIcon(base, level: "purple") === base)
func pixel(_ image: NSImage, _ appearance: NSAppearance, x: Int, y: Int) -> NSColor {
    let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 64, pixelsHigh: 64, bitsPerSample: 8,
        samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    appearance.performAsCurrentDrawingAppearance {
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
        image.draw(in: NSRect(x: 0, y: 0, width: 64, height: 64))
        NSGraphicsContext.restoreGraphicsState()
    }
    return bitmap.colorAt(x: x, y: y)!.usingColorSpace(.sRGB)!
}
for level in ["blue", "gray", "yellow", "orange"] {
    let badged = nativeLevelIcon(base, level: level)!
    precondition(badged.accessibilityDescription == nativePromptLevelDescription(level))
    for appearance in [light, dark] {
        // Bitmap rows count from the top: (38, 49) lies inside the disc, left of its glyph.
        let seen = pixel(badged, appearance, x: 38, y: 49)
        let want = nativePromptLevelColor(level, appearance: appearance)!
        let distance = abs(seen.redComponent - want.redComponent) + abs(seen.greenComponent - want.greenComponent) + abs(seen.blueComponent - want.blueComponent)
        precondition(distance < 0.12, "\(level) badge in \(appearance.name.rawValue): \(seen) want \(want)")
        // The glyph down the disc's middle: black ink on yellow and orange, white on blue and gray.
        let glyph = (40...58).map { pixel(badged, appearance, x: 49, y: $0) }.map { $0.redComponent + $0.greenComponent + $0.blueComponent }
        if level == "yellow" || level == "orange" {
            precondition(glyph.min()! < 0.6, "\(level) badge glyph is not black ink")
        } else {
            precondition(glyph.max()! > 2.7, "\(level) badge glyph is not white ink")
        }
        let corner = pixel(badged, appearance, x: 32, y: 20), plain = pixel(base, appearance, x: 32, y: 20)
        precondition(corner == plain, "\(level) badge changed the icon outside its corner")
    }
}
print("native prompt level fixture passed")
''')
            executable = scratch / "level-fixture"
            run("swiftc", "-D", "PANEL_ASKS_FIXTURE", source, "-o", executable, timeout=90)
            completed = run(executable, capture_output=True, timeout=15)
            self.assertIn("native prompt level fixture passed", completed.stdout)

    def test_permission_prompt_shows_requester_icon_and_sounds_once(self):
        with tempfile.TemporaryDirectory(prefix="oa-native-asks-icon-") as temp:
            scratch = Path(temp)
            contents = scratch / "Icon Fixture.app/Contents"
            macos = contents / "MacOS"
            macos.mkdir(parents=True)
            with (contents / "Info.plist").open("wb") as metadata:
                plistlib.dump({"CFBundleIdentifier": "org.openabstractions.panel.asks-icon-fixture",
                              "CFBundleName": "OA Icon Fixture", "CFBundlePackageType": "APPL",
                              "CFBundleExecutable": "Icon Fixture", "LSUIElement": True}, metadata)
            child = macos / "panel"
            child.write_text("#!/bin/sh\nexec /bin/sleep 30\n")
            child.chmod(0o755)
            source = fixture_source(scratch, r'''
let calculator = "/System/Applications/Calculator.app/Contents/MacOS/Calculator"
func pixels(_ image: NSImage?) -> Data? {
    guard let image else { return nil }
    var rect = NSRect(x: 0, y: 0, width: 64, height: 64)
    return image.cgImage(forProposedRect: &rect, context: nil, hints: nil)
        .map { NSBitmapImageRep(cgImage: $0).representation(using: .png, properties: [:])! }
}
let expected = pixels(NSWorkspace.shared.icon(forFile: "/System/Applications/Calculator.app"))
precondition(expected != nil && pixels(nativeRequesterIcon(calculator)) == expected)
precondition(nativeRequesterIcon("/bin/ls") == nil, "a program outside a bundle took an icon")
precondition(nativeRequesterIcon("Calculator.app/Contents/MacOS/Calculator") == nil)
precondition(nativeRequesterIcon("/nonexistent/Gone.app/Contents/MacOS/Gone") == nil)
precondition(nativeRequesterIcon(nil) == nil && nativeRequesterIcon("") == nil)
var sounds = 0
nativePermissionAlertSound = { sounds += 1 }
extension AppDelegate {
    func checkIconAndSound() {
        sessionEvidence = { true }
        openWhenReady = false
        launchPanel()
        defer { stopPanel() }
        precondition(panel?.isRunning == true && nativeInput != nil)
        let oaIcon = pixels(NSImage(named: NSImage.applicationIconName))
        var shown: [String: (icon: Data?, sounds: Int)] = [:]
        fixturePresentation = { frame, alert in
            shown[frame.id!] = (pixels(alert.icon), sounds)
            // Every button, Cancel included, waits for the panel to arm.
            precondition(alert.buttons.allSatisfy { !$0.isEnabled }, "a button was enabled before arming")
            let timer = Timer(timeInterval: nativeQuestionArmDelay + 0.1, repeats: false) { _ in alert.buttons.last!.performClick(nil) }
            RunLoop.main.add(timer, forMode: .common)
        }
        func question(_ id: String, _ extra: String) -> NativeAsksFrame {
            let json = #"{"kind":"question","id":""# + id + #"","prompt":{"Title":"Permission request","Instruction":"Allow?","Content":"Body","Details":"","Footer":"","Choices":[{"label":"Allow","option":"allow"},{"label":"Refuse","option":"refuse"}],"Default":"refuse""# + extra + "}}"
            return try! JSONDecoder().decode(NativeAsksFrame.self, from: Data(json.utf8))
        }
        receiveNative(question("1", #","PermissionPrompt":true,"IconProgram":""# + calculator + #"""#), from: panel!)
        receiveNative(question("2", #","PermissionPrompt":true,"IconProgram":"/usr/bin/python3""#), from: panel!)
        receiveNative(question("3", #","IconProgram":""# + calculator + #"""#), from: panel!)
        receiveNative(question("4", #","PermissionPrompt":true,"IconProgram":""# + calculator + #"","Level":"orange""#), from: panel!)
        let deadline = Date().addingTimeInterval(10)
        while shown.count < 4 && Date() < deadline { RunLoop.main.run(until: Date().addingTimeInterval(0.02)) }
        precondition(shown.count == 4, "fixture prompts were not all shown")
        // A permission prompt without a level carries the yellow badge, as on Windows.
        let calculatorIcon = NSWorkspace.shared.icon(forFile: "/System/Applications/Calculator.app")
        let oaImage = NSImage(named: NSImage.applicationIconName)
        precondition(shown["4"]!.icon == pixels(nativeLevelIcon(calculatorIcon, level: "orange")) && shown["4"]!.sounds == 2,
                     "a leveled permission prompt lacks its badge")
        precondition(shown["1"]!.icon == pixels(nativeLevelIcon(calculatorIcon, level: "yellow")) && shown["1"]!.icon != expected
                     && shown["1"]!.sounds == 0, "permission prompt lacks the requester icon or its yellow badge")
        precondition(shown["2"]!.icon == pixels(nativeLevelIcon(oaImage, level: "yellow")) && shown["2"]!.sounds == 1,
                     "an unbundled program replaced OA's icon")
        precondition(shown["3"]!.icon == oaIcon && shown["3"]!.sounds == 2, "an ordinary question took an icon")
        precondition(sounds == 3, "sound count \(sounds), want one per permission prompt")
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
AppDelegate().checkIconAndSound()
print("native permission icon and sound fixture passed")
''')
            executable = macos / "Icon Fixture"
            run("swiftc", "-D", "PANEL_ASKS_FIXTURE", source, "-o", executable, timeout=90)
            completed = run(executable, capture_output=True, timeout=30)
            self.assertIn("native permission icon and sound fixture passed", completed.stdout)

    def test_replaced_child_cannot_acknowledge_through_replacement(self):
        with tempfile.TemporaryDirectory(prefix="oa-native-asks-replace-") as temp:
            scratch = Path(temp)
            contents = scratch / "Replacement Fixture.app/Contents"
            macos = contents / "MacOS"
            macos.mkdir(parents=True)
            with (contents / "Info.plist").open("wb") as metadata:
                plistlib.dump({"CFBundleIdentifier": "org.openabstractions.panel.asks-replacement-fixture",
                              "CFBundleName": "OA Replacement Fixture", "CFBundlePackageType": "APPL",
                              "CFBundleExecutable": "Replacement Fixture", "LSUIElement": True}, metadata)
            child = macos / "panel"
            child.write_text("#!/bin/sh\nexec /bin/sleep 30\n")
            child.chmod(0o755)
            source = fixture_source(scratch, r'''
extension AppDelegate {
    func checkReplacedReport() {
        sessionEvidence = { true }
        openWhenReady = false
        launchPanel()
        let original = panel!
        let captured = Pipe()
        let fd = captured.fileHandleForReading.fileDescriptor
        precondition(fcntl(fd, F_SETFL, O_NONBLOCK) == 0)
        var replacementShown = false
        var bytes = [UInt8](repeating: 0, count: 4096)
        defer { stopPanel(); stopOwnedPanel(original) }
        let old = NativeAsksFrame(kind: "report", id: "1", prompt: nil,
            title: "Old report", instruction: "Old child", content: "old")
        let next = NativeAsksFrame(kind: "report", id: "1", prompt: nil,
            title: "New report", instruction: "New child", content: "new")
        fixturePresentation = { frame, alert in
            let timer = Timer(timeInterval: 0.02, repeats: false) { _ in
                if frame.instruction == "Old child" {
                    stopOwnedPanel(original)
                    _ = self.applicationShouldHandleReopen(NSApp, hasVisibleWindows: false)
                    precondition(self.panel !== original && self.panel?.isRunning == true)
                    self.nativeInput = captured.fileHandleForWriting
                    self.receiveNative(next, from: self.panel!)
                } else {
                    precondition(Darwin.read(fd, &bytes, 4096) == -1 && errno == EAGAIN,
                                 "the old report wrote its restarted ID to the replacement")
                    // OK is disabled until the panel arms.
                    precondition(!alert.buttons[0].isEnabled, "the report's OK was enabled before arming")
                    let click = Timer(timeInterval: nativeQuestionArmDelay + 0.1, repeats: false) { _ in
                        precondition(alert.buttons[0].isEnabled)
                        alert.buttons[0].performClick(nil)
                        replacementShown = true
                    }
                    RunLoop.main.add(click, forMode: .common)
                }
            }
            RunLoop.main.add(timer, forMode: .common)
        }
        receiveNative(old, from: original)
        let deadline = Date().addingTimeInterval(2)
        while !replacementShown && Date() < deadline {
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }
        precondition(replacementShown, "replacement dialogs were stranded behind the old modal")
        let count = Darwin.read(fd, &bytes, 4096)
        precondition(count > 0)
        let lines = String(bytes: bytes.prefix(count), encoding: .utf8)!.split(separator: "\n")
        precondition(lines.count == 1)
        let response = try! JSONSerialization.jsonObject(with: Data(lines[0].utf8)) as! [String: String]
        precondition(response == ["kind": "ack", "id": "1"])
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
delegate.checkReplacedReport()
print("replaced native report fixture passed")
''')
            executable = macos / "Replacement Fixture"
            run("swiftc", "-D", "PANEL_ASKS_FIXTURE", source, "-o", executable, timeout=90)
            completed = run(executable, capture_output=True, timeout=15)
            self.assertIn("replaced native report fixture passed", completed.stdout)

    def test_session_evidence_and_bounded_native_frames(self):
        with tempfile.TemporaryDirectory(prefix="oa-native-asks-session-") as temp:
            scratch = Path(temp)
            source = fixture_source(scratch, r'''
let ownUID: uid_t = 501
// Execute the production adapter too. This development invocation can be in
// an inactive SSH login; it may allow only an exact active caller session.
let liveAllows = nativeSessionUnlocked()
if let liveSession = CGSessionCopyCurrentDictionary() as? [String: Any] {
    let ownActive = exactSessionBoolean(liveSession[kCGSessionOnConsoleKey as String]) == true &&
        exactSessionBoolean(liveSession[kCGSessionLoginDoneKey as String]) == true &&
        (liveSession[kCGSessionUserIDKey as String] as? NSNumber)?.int64Value == Int64(getuid())
    precondition(!liveAllows || ownActive)
    print("production session adapter observation: allows=\(liveAllows) callerActive=\(ownActive)")
} else { precondition(!liveAllows) }
let valid: [String: Any] = [kCGSessionUserIDKey as String: NSNumber(value: ownUID),
    kCGSessionOnConsoleKey as String: NSNumber(value: true),
    kCGSessionLoginDoneKey as String: NSNumber(value: true)]
precondition(nativeSessionEvidenceAllowsPrompt(valid, consoleLocked: NSNumber(value: false), uid: ownUID))
precondition(!nativeSessionEvidenceAllowsPrompt(valid, consoleLocked: nil, uid: ownUID))
precondition(!nativeSessionEvidenceAllowsPrompt(valid, consoleLocked: NSNumber(value: true), uid: ownUID))
precondition(!nativeSessionEvidenceAllowsPrompt(valid, consoleLocked: NSNumber(value: 0), uid: ownUID))
precondition(!nativeSessionEvidenceAllowsPrompt(valid, consoleLocked: "false", uid: ownUID))
precondition(!nativeSessionEvidenceAllowsPrompt(valid, consoleLocked: NSNumber(value: false), uid: 502))
precondition(!nativeSessionEvidenceAllowsPrompt(nil, consoleLocked: NSNumber(value: false), uid: ownUID))
for key in [kCGSessionOnConsoleKey as String, kCGSessionLoginDoneKey as String, kCGSessionUserIDKey as String] {
    var changed = valid
    changed.removeValue(forKey: key)
    precondition(!nativeSessionEvidenceAllowsPrompt(changed, consoleLocked: NSNumber(value: false), uid: ownUID))
    changed[key] = "true"
    precondition(!nativeSessionEvidenceAllowsPrompt(changed, consoleLocked: NSNumber(value: false), uid: ownUID))
}
var changed = valid
changed["CGSSessionScreenIsLocked"] = NSNumber(value: true)
precondition(!nativeSessionEvidenceAllowsPrompt(changed, consoleLocked: NSNumber(value: false), uid: ownUID))
changed["CGSSessionScreenIsLocked"] = "false"
precondition(!nativeSessionEvidenceAllowsPrompt(changed, consoleLocked: NSNumber(value: false), uid: ownUID))
changed["CGSSessionScreenIsLocked"] = NSNumber(value: false)
precondition(nativeSessionEvidenceAllowsPrompt(changed, consoleLocked: NSNumber(value: false), uid: ownUID))
precondition(nativePanelArguments(["app", "--background", "--runtime-endpoint", "xpc:test.runtime",
    "-runtime-program=/private/fixture/server", "--runtime-state-dir", "/private/fixture/state"])
    == ["-native-asks", "-runtime-endpoint", "xpc:test.runtime", "-runtime-program=/private/fixture/server",
        "-runtime-state-dir", "/private/fixture/state"])
var parser = NativeLineParser()
var frames: [NativeAsksFrame] = []
let line = Data((nativeAsksPrefix + "{\"kind\":\"dismiss\",\"id\":\"1\"}\n").utf8)
parser.consume(Data(line.prefix(4)), onPanelURL: { _ in preconditionFailure() },
    onNative: { frames.append($0) })
var data = Data(line.dropFirst(4))
data.append(Data((nativeAsksPrefix + "{\"kind\":\"dismiss\",\"id\":\"2\",\"extra\":1}\n").utf8))
data.append(Data(String(repeating: "x", count: maxNativeAsksLine + 1).utf8))
data.append(0x0A)
// The retired question-notification line is ignored like any other text.
data.append(Data("oa-question-notification: {\"id\":\"q\",\"title\":\"t\",\"body\":\"b\"}\n".utf8))
data.append(line)
// Go sends every report with its level (reportPromptLevel): it is shown.
data.append(Data((nativeAsksPrefix + "{\"kind\":\"report\",\"id\":\"1\",\"title\":\"OpenAbstractions\",\"instruction\":\"Done\",\"level\":\"blue\"}\n").utf8))
parser.consume(data, onPanelURL: { _ in preconditionFailure() },
    onNative: { frames.append($0) })
precondition(frames.count == 3 && frames.allSatisfy { $0.id == "1" })
precondition(frames[2].kind == "report" && frames[2].level == "blue", "a report with its level was dropped")
precondition(nativePanelArguments(["app", "--register-application", "/private/fixture/app.json", "--background"])
    == ["-native-asks", "-register-application", "/private/fixture/app.json"])
precondition(nativePanelArguments(["app", "-remove-application=example"]) == ["-native-asks", "-remove-application=example"])
precondition(nativeApplicationRequestRun(["app", "--register-application", "/x.json"]))
precondition(nativeApplicationRequestRun(["app", "-remove-application=example"]))
precondition(!nativeApplicationRequestRun(["app", "--background", "--runtime-endpoint", "xpc:x"]))
precondition(nativePanelArguments(["app", "-native=true", "-page", "permission-review", "-review-feature", "notifications", "-review-program", "/private/fixture/python", "-review-receipt=/private/fixture/decision.json"])
    == ["-native-asks", "-page", "permission-review", "-review-feature", "notifications", "-review-program", "/private/fixture/python", "-review-receipt=/private/fixture/decision.json"])
precondition(nativePanelArguments(["app", "-page=permission-review", "-review-feature=picture-workflow", "-review-app=selected", "-review-host=selected-image"])
    == ["-native-asks", "-page=permission-review", "-review-feature=picture-workflow", "-review-app=selected", "-review-host=selected-image"])
precondition(nativeApplicationRequestRun(["app", "-page=permission-review", "-review-feature=notifications", "-review-program=/private/fixture/python"]))
print("native session and framing fixture passed")
''')
            executable = scratch / "session-fixture"
            run("swiftc", "-D", "PANEL_ASKS_FIXTURE", source, "-o", executable, timeout=90)
            completed = run(executable, capture_output=True, timeout=15)
            self.assertIn("native session and framing fixture passed", completed.stdout)
            print(completed.stdout.strip())

    def test_public_ordinary_choices_remain_pending_during_permission_delivery(self):
        go = shutil.which("go") or str(Path.home() / "go-toolchain/go/bin/go")
        with tempfile.TemporaryDirectory(prefix="oa-native-choices-public-") as temp:
            scratch = Path(temp).resolve()
            state = scratch / "state"
            state.mkdir(mode=0o700)
            contents = scratch / "Native Choices Fixture.app/Contents"
            macos = contents / "MacOS"
            macos.mkdir(parents=True)
            with (contents / "Info.plist").open("wb") as metadata:
                plistlib.dump({"CFBundleIdentifier": "org.openabstractions.panel.native-choices-fixture",
                              "CFBundleName": "OA Native Choices Fixture", "CFBundlePackageType": "APPL",
                              "CFBundleExecutable": "Native Choices Fixture", "LSUIElement": True}, metadata)
            panel, server = macos / "panel", scratch / "fixture-server"
            environment = dict(os.environ, CGO_ENABLED="1")
            run(go, "-C", ROOT / "monitor", "build", "-o", panel, ".", env=environment, timeout=120)
            run(go, "-C", ROOT / "monitor", "build", "-o", server, "./testdata/nativeasksfixture", env=environment, timeout=120)
            source = fixture_source(scratch, r'''
extension AppDelegate {
    func configureChoiceFixture() {
        openWhenReady = false
        sessionEvidence = { true }
        var permissionShown = false
        let started = Date()
        func after(_ seconds: Double, _ action: @escaping () -> Void) {
            let timer = Timer(timeInterval: seconds, repeats: false) { _ in action() }
            RunLoop.main.add(timer, forMode: .common)
            RunLoop.main.add(timer, forMode: .modalPanel)
        }
        fixturePresentation = { frame, alert in
            precondition(self.window == nil && self.panelURL == nil, "permission answering required HTTP or a Panel")
            if frame.kind == "report" {
                preconditionFailure("successful answer produced a second dialog")
            }
            let prompt = frame.prompt!
            precondition(prompt.appChoice != true, "OA presented an application-owned choice")
            precondition(prompt.details.contains("/fixture/permission.app") && prompt.details.contains("abstraction.model/lookup"))
            precondition(!permissionShown, "permission was presented twice")
            permissionShown = true
            after(0.65) {
                let index = prompt.choices.firstIndex { $0.option == "refuse" }!
                precondition(alert.buttons[index].isEnabled)
                alert.buttons[index].performClick(nil)
            }
        }
        let finish = Timer(timeInterval: 0.1, repeats: true) { timer in
            // Observe several complete production poll intervals with all
            // ordinary requests pending. No ordinary prompt may be emitted.
            if permissionShown && Date().timeIntervalSince(started) >= 10 && self.activeDialogID == nil {
                timer.invalidate()
                print("native permission delivery preserves ordinary pending fixture passed")
                self.allowTermination = true
                NSApp.terminate(nil)
            }
        }
        RunLoop.main.add(finish, forMode: .common)
        let deadline = Timer(timeInterval: 40, repeats: false) { _ in
            fputs("ownership fixture: permissionShown=\(permissionShown)\n", stderr)
            preconditionFailure("ordinary fixture timed out")
        }
        RunLoop.main.add(deadline, forMode: .common)
        RunLoop.main.add(deadline, forMode: .modalPanel)
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
delegate.configureChoiceFixture()
app.delegate = delegate
app.run()
''')
            executable = macos / "Native Choices Fixture"
            run("swiftc", "-D", "PANEL_ASKS_FIXTURE", source, "-o", executable, timeout=90)
            prefix = "org.openabstractions.test.native-choices." + str(os.getpid())
            domain, label = "gui/" + str(os.getuid()), prefix + ".job"
            service, job = domain + "/" + label, scratch / "job.plist"
            job.write_bytes(plistlib.dumps({"Label": label,
                "ProgramArguments": [str(server), prefix, str(state), str(panel), "choices"],
                "MachServices": {prefix + "." + name: True for name in ("runtime", "logging", "config", "asks")},
                "RunAtLoad": True, "StandardOutPath": str(scratch / "stdout"), "StandardErrorPath": str(scratch / "stderr")}))
            awake = subprocess.Popen(["/usr/bin/caffeinate", "-i", "-w", str(os.getpid())])
            registered = False
            try:
                run("launchctl", "bootstrap", domain, job, timeout=15)
                registered = True
                deadline = time.monotonic() + 15
                while not (state / "ready.json").exists():
                    if time.monotonic() > deadline:
                        self.fail("ordinary fixture runtime not ready: " + (scratch / "stderr").read_text())
                    time.sleep(0.1)
                environment["OA_NATIVE_FIXTURE_STATE"] = str(state)
                completed = run(executable, "--background", "--runtime-endpoint", "xpc:" + prefix + ".runtime",
                    "--runtime-program", server, "--runtime-state-dir", state,
                    env=environment, capture_output=True, timeout=45)
                self.assertIn("native permission delivery preserves ordinary pending fixture passed", completed.stdout)
                observed = json.loads((state / "choice-observations.json").read_text())
                self.assertEqual(set(observed), {"answer", "close", "stale", "expire"})
                for choice in observed.values():
                    self.assertEqual(choice["Outcome"], "pending")
                    self.assertIsNone(choice.get("Answer"))
                records = json.loads((state / "questions.json").read_text())["asks"]
                permission = [record for record in records if record["about"] == "/fixture/permission.app"]
                self.assertEqual(len(permission), 1)
                self.assertEqual(permission[0]["option"], "refuse")
                admission = json.loads((state / "choice-admission.json").read_text())
                self.assertEqual(admission["before"], "forbidden")
                self.assertEqual(admission["grantAction"], "abstraction.asks/question.ask")
                self.assertEqual(admission["grantResource"], "account")
                self.assertEqual(admission["grantProgram"], str(server))
                self.assertEqual((state / "rights-before.json").read_bytes(), (state / "policy.json").read_bytes(),
                                 "ordinary requests or permission refusal changed Rights")
            finally:
                try:
                    if registered: run("launchctl", "bootout", service, timeout=15)
                finally:
                    awake.terminate()
                    awake.wait(timeout=5)
                if (scratch / "stderr").exists():
                    print((scratch / "stderr").read_text(), file=sys.stderr)

    def test_public_operator_answer_staleness_lock_and_close(self):
        go = shutil.which("go")
        if not go:
            candidate = Path.home() / "go-toolchain/go/bin/go"
            if candidate.exists():
                go = str(candidate)
        self.assertIsNotNone(go, "Go toolchain required for native public operator fixture")
        with tempfile.TemporaryDirectory(prefix="oa-native-asks-public-") as temp:
            scratch = Path(temp).resolve()
            state = scratch / "state"
            state.mkdir(mode=0o700)
            contents = scratch / "Native Asks Fixture.app/Contents"
            macos = contents / "MacOS"
            macos.mkdir(parents=True)
            with (contents / "Info.plist").open("wb") as metadata:
                plistlib.dump({"CFBundleIdentifier": "org.openabstractions.panel.native-asks-fixture",
                              "CFBundleName": "OA Native Asks Fixture", "CFBundlePackageType": "APPL",
                              "CFBundleExecutable": "Native Asks Fixture", "LSUIElement": True}, metadata)
            panel = macos / "panel"
            server = scratch / "fixture-server"
            environment = dict(os.environ, CGO_ENABLED="1")
            run(go, "-C", ROOT / "monitor", "build", "-o", panel, ".", env=environment, timeout=120)
            run(go, "-C", ROOT / "monitor", "build", "-o", server, "./testdata/nativeasksfixture",
                env=environment, timeout=120)
            source = fixture_source(scratch, r'''
extension AppDelegate {
    func configureNativeFixture() {
        openWhenReady = false
        sessionEvidence = { true }
        let state = URL(fileURLWithPath: ProcessInfo.processInfo.environment["OA_NATIVE_FIXTURE_STATE"]!)
        var interrupted = false
        var answered = false
        var stale = false
        var closed = false
        var windowStage = 0
        var firstWindow: NSWindow?
        var firstURL: URL?
        func after(_ seconds: Double, _ action: @escaping () -> Void) {
            let timer = Timer(timeInterval: seconds, repeats: false) { _ in action() }
            RunLoop.main.add(timer, forMode: .common)
            RunLoop.main.add(timer, forMode: .modalPanel)
        }
        fixturePresentation = { frame, alert in
            precondition(self.window == nil && self.panelURL == nil, "native answering opened or required a Panel")
            fputs("native fixture frame: \(frame.kind) \(frame.prompt?.details ?? frame.instruction ?? "")\n", stderr)
            if frame.kind == "report" {
                preconditionFailure("successful answer produced a second dialog")
            }
            let prompt = frame.prompt!
            precondition(prompt.details.contains("abstraction.model/lookup") && prompt.details.contains("hf"))
            precondition(prompt.content.contains("Publisher:") && prompt.footer.contains("Refuse"))
            precondition(prompt.defaultOption == "refuse")
            precondition(alert.buttons.allSatisfy { !$0.isEnabled }, "buttons were not armed safely")
            if prompt.details.contains("/fixture/answer.app") {
                if !interrupted {
                    interrupted = true
                    after(0.05) {
                        self.sessionEvidence = { false }
                        self.refreshSession()
                        after(0.2) { self.sessionEvidence = { true }; self.refreshSession() }
                    }
                } else {
                    answered = true
                    after(0.65) {
                        let index = prompt.choices.firstIndex { $0.option == "refuse" }!
                        precondition(alert.buttons[index].isEnabled)
                        precondition(alert.window.defaultButtonCell === alert.buttons[index].cell)
                        alert.buttons[index].performClick(nil)
                    }
                }
            } else if prompt.details.contains("/fixture/stale.app") {
                stale = true
                after(0.05) { try! Data("stale".utf8).write(to: state.appendingPathComponent("stale-now")) }
            } else if prompt.details.contains("/fixture/close.app") {
                closed = true
                after(0.65) { alert.buttons.last!.performClick(nil) }
            } else { preconditionFailure("unexpected service question") }
        }
        let finish = Timer(timeInterval: 0.1, repeats: true) { timer in
            if interrupted && answered && stale && closed && self.activeDialogID == nil {
                if windowStage == 0 {
                    precondition(self.panel?.isRunning == true && self.panelURL == nil && self.window == nil)
                    windowStage = 1
                    self.requestPanel("questions")
                } else if windowStage == 1 && self.webView?.url?.path == "/rights" {
                    precondition(self.webView?.url?.query?.contains("shell=native") == true, "questions opened without the native shell")
                    precondition(["/", "/start"].contains(self.panelURL?.path ?? ""), "a questions-first open changed the overview base")
                    firstWindow = self.window
                    firstURL = self.panelURL
                    windowStage = 2
                    self.requestPanel("overview")
                } else if windowStage == 2, let currentURL = self.webView?.url,
                          currentURL.fragment == nil && currentURL.path == "/" && self.webView?.isLoading == false {
                    precondition(self.window === firstWindow && self.panelURL == firstURL &&
                                 currentURL == panelPageURL(firstURL!, path: "/"),
                                 "overview reopen changed the listener or reused the Questions page")
                    timer.invalidate()
                    print("native public operator fixture passed")
                    self.allowTermination = true
                    NSApp.terminate(nil)
                }
            }
        }
        RunLoop.main.add(finish, forMode: .common)
        let deadline = Timer(timeInterval: 40, repeats: false) { _ in
            fputs("native fixture state: interrupted=\(interrupted) answered=\(answered) stale=\(stale) closed=\(closed) windowStage=\(windowStage) child=\(self.panel?.isRunning == true) unlocked=\(self.sessionUnlocked) url=\(self.panelURL?.absoluteString ?? "none") frames=\(self.dialogFrames.count)\n", stderr)
            preconditionFailure("native fixture timed out")
        }
        RunLoop.main.add(deadline, forMode: .common)
        RunLoop.main.add(deadline, forMode: .modalPanel)
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
delegate.configureNativeFixture()
app.delegate = delegate
app.run()
''')
            executable = macos / "Native Asks Fixture"
            run("swiftc", "-D", "PANEL_ASKS_FIXTURE", source, "-o", executable, timeout=90)
            prefix = "org.openabstractions.test.native-asks." + str(os.getpid())
            label = prefix + ".job"
            domain = "gui/" + str(os.getuid())
            service = domain + "/" + label
            job = scratch / "job.plist"
            job.write_bytes(plistlib.dumps({"Label": label,
                "ProgramArguments": [str(server), prefix, str(state), str(panel)],
                "MachServices": {prefix + "." + name: True for name in ("runtime", "logging", "config", "asks")},
                "RunAtLoad": True, "StandardOutPath": str(scratch / "stdout"),
                "StandardErrorPath": str(scratch / "stderr")}))
            awake = subprocess.Popen(["/usr/bin/caffeinate", "-i", "-w", str(os.getpid())])
            registered = False
            try:
                run("launchctl", "bootstrap", domain, job, timeout=15)
                registered = True
                deadline = time.monotonic() + 15
                while not (state / "ready.json").exists():
                    if time.monotonic() > deadline:
                        self.fail("fixture runtime not ready: " + (scratch / "stderr").read_text())
                    time.sleep(0.1)
                environment["OA_NATIVE_FIXTURE_STATE"] = str(state)
                completed = run(executable, "--background", "--runtime-endpoint", "xpc:" + prefix + ".runtime",
                    "--runtime-program", server, "--runtime-state-dir", state,
                    env=environment, capture_output=True, timeout=45)
                self.assertIn("native public operator fixture passed", completed.stdout)
                ids = json.loads((state / "ready.json").read_text())
                records = json.loads((state / "questions.json").read_text())["asks"]
                decisions = {record["id"]: record.get("option", "") for record in records}
                self.assertEqual(decisions[ids["answer"]], "refuse")
                self.assertEqual(decisions[ids["stale"]], "refuse")
                self.assertEqual(decisions[ids["close"]], "")
                self.assertEqual((state / "stale-outcome").read_text(), "answered")
            finally:
                try:
                    if registered:
                        run("launchctl", "bootout", service, timeout=15)
                finally:
                    awake.terminate()
                    awake.wait(timeout=5)
                if (scratch / "stderr").exists():
                    diagnostics = (scratch / "stderr").read_text()
                    if diagnostics:
                        print(diagnostics, file=sys.stderr)


if __name__ == "__main__":
    unittest.main()
