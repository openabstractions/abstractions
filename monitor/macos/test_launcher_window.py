"""Exercise the Panel window through the AppKit wrapper.

The first fixture covers background readiness and app reopening. The second
serves a stub of the shell contract (research/panel-redesign/SPEC.md §6:
/panel/nav.json, ?shell=native pages that set document.title) and drives the
System Settings-style window against it: sidebar, toolbar, menus, accent and
the activation policy. The window loads oa-panel:// as the launcher does; the
fixture's Panel transport forwards each request to the stub in place of the
Go child.
"""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import json
import os
import platform
import plistlib
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from urllib.parse import parse_qs, urlsplit


HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
# Window fixtures run hidden (fixtureHidesWindow): the window is
# transparent, the launcher never activates and the activation policy is
# recorded rather than applied. OA_OWNER_PRESENT=1, during an owner-present
# test the owner was told about, shows the window and applies the policy.
OWNER_PRESENT = os.environ.get("OA_OWNER_PRESENT") == "1"


def remove_bundle_folders(identifier):
    """WebKit and AppKit create these per bundle identifier; remove the fixture's."""
    for folder in ("Library/Caches", "Library/WebKit", "Library/HTTPStorages"):
        shutil.rmtree(Path.home() / folder / identifier, ignore_errors=True)


def fixture_bundle(scratch, name, identifier):
    contents = scratch / (name + ".app") / "Contents"
    macos = contents / "MacOS"
    macos.mkdir(parents=True)
    with (contents / "Info.plist").open("wb") as metadata:
        plistlib.dump({"CFBundleIdentifier": identifier, "CFBundleName": name,
                      "CFBundlePackageType": "APPL", "CFBundleExecutable": name,
                      "LSUIElement": True}, metadata)
    return macos


# The stub navigation table, in sidebar order, as the contract states it.
NAV_ITEMS = [
    {"id": "setup", "label": "Get started", "path": "/setup", "group": "main",
     "glyph": {"segoe": "", "sf": "star.fill"}},
    {"id": "status", "label": "Apps", "path": "/", "group": "main",
     "glyph": {"segoe": "", "sf": "square.grid.2x2.fill"}},
    {"id": "inference", "label": "Models", "path": "/inference", "group": "main",
     "glyph": {"segoe": "", "sf": "cpu.fill"}},
    {"id": "rights", "label": "Permissions", "path": "/rights", "group": "main",
     "glyph": {"segoe": "", "sf": "hand.raised.fill"}, "badge": 2},
    {"id": "work", "label": "Activity", "path": "/work", "group": "main",
     "glyph": {"segoe": "", "sf": "clock.fill"}},
    {"id": "card", "label": "Device", "path": "/card", "group": "main",
     "glyph": {"segoe": "", "sf": "desktopcomputer"}},
    {"id": "registry", "label": "Providers", "path": "/registry", "group": "more",
     "glyph": {"segoe": "", "sf": "server.rack"}},
    {"id": "credentials", "label": "Credentials", "path": "/credentials-page", "group": "more",
     "glyph": {"segoe": "", "sf": "key.fill"}},
    {"id": "settings", "label": "Settings", "path": "/settings", "group": "more",
     "glyph": {"segoe": "", "sf": "gearshape.fill"}},
]


class StubShell(BaseHTTPRequestHandler):
    """Serves the shell contract: nav.json behind the key, pages by path."""

    requests = []

    def log_message(self, *args):
        pass

    def do_GET(self):
        parts = urlsplit(self.path)
        query = parse_qs(parts.query)
        if query.get("k") != ["fixture"]:
            self.send_error(403)
            return
        if parts.path == "/panel/nav.json":
            self.reply("application/json", json.dumps({"items": NAV_ITEMS, "selected": "status"}))
            return
        if parts.path == "/static/skin-macos.css":
            self.reply("text/css", (ROOT / "monitor/static/skin-macos.css").read_text())
            return
        label = next((item["label"] for item in NAV_ITEMS if item["path"] == parts.path), None)
        if label is None:
            self.send_error(404)
            return
        StubShell.requests.append(self.path)
        native = query.get("shell") == ["native"]
        nav = "" if native else '<nav class="oa-nav">HTML navigation</nav>'
        heading = "" if native else "<h1>" + label + "</h1>"
        # The root as S1 writes it, with the server's default accent inline;
        # the host's injection must replace all three variables.
        self.reply("text/html", '<!doctype html><html lang="en" data-platform="macos" data-skin="macos" data-shell="' +
                   ("native" if native else "html") + '" style="--oa-accent:#0078D4;--oa-accent-light2:#60CDFF;'
                   '--oa-accent-dark1:#005FB8"><head><meta charset="utf-8">'
                   "<title>" + label + '</title><link rel="stylesheet" href="/static/skin-macos.css?k=fixture">'
                   '</head><body>' + nav + '<main class="oa-content">' + heading +
                   '<section class="oa-group"><h2>Fixture</h2><div class="oa-row"><input id="field" type="text">'
                   '<a id="next" href="/inference?k=fixture">Models</a></div></section></main></body></html>')

    def reply(self, kind, text):
        data = text.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", kind + "; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


SERVED_FIXTURE = r'''
extension AppDelegate {
    func spin(_ seconds: Double, until condition: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(seconds)
        while !condition() && Date() < deadline {
            if let event = NSApp.nextEvent(matching: .any, until: Date().addingTimeInterval(0.05),
                                           inMode: .default, dequeue: true) {
                NSApp.sendEvent(event)
            }
        }
        return condition()
    }

    func evaluate(_ script: String) -> Any? {
        var result: Any?
        var done = false
        webView!.evaluateJavaScript(script) { value, _ in result = value; done = true }
        precondition(spin(5) { done }, "script did not finish: \(script)")
        return result
    }

    func press(_ characters: String) -> Bool {
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
                                     timestamp: ProcessInfo.processInfo.systemUptime,
                                     windowNumber: window!.windowNumber, context: nil,
                                     characters: characters, charactersIgnoringModifiers: characters,
                                     isARepeat: false, keyCode: 0)!
        return NSApp.mainMenu!.performKeyEquivalent(with: event)
    }

    func settled(_ path: String) -> Bool {
        webView!.url?.path == path && !webView!.isLoading
    }

    func query(_ name: String) -> String? {
        URLComponents(url: webView!.url!, resolvingAgainstBaseURL: false)!.queryItems?.first { $0.name == name }?.value
    }

    func checkServedWindow(_ base: URL) {
        // Pure contract helpers.
        precondition(panelNavURL(base)!.absoluteString == base.absoluteString.replacingOccurrences(of: "/?", with: "/panel/nav.json?"))
        let rights = panelPageURL(base, path: "/rights#waiting")!
        precondition(rights.path == "/rights" && rights.fragment == "waiting" && rights.query == "k=fixture&shell=native", "\(rights)")
        precondition(panelPageURL(base, path: "https://example.com/") == nil && panelPageURL(base, path: "//example.com/x") == nil)
        precondition(panelNativeShellURL(rights) == nil)
        precondition(panelNativeShellURL(URL(string: "oa-panel://panel/work?k=a&shell=web")!)!.query == "k=a&shell=native")
        precondition(panelNativeShellURL(URL(string: "http://localhost:1/work?k=a")!) == nil, "a loopback page is not the Panel")
        precondition(localize("oa-panel://panel/start?k=a") != nil && localize("http://127.0.0.1:8734/?k=a") == nil)
        precondition(panelNativeShellURL(URL(string: "https://example.com/?k=a")!) == nil)
        let decoded = try! JSONDecoder().decode(PanelNav.self, from: Data(#"{"items":[{"id":"a","label":"A","path":"/","group":"main","glyph":{"segoe":"x","sf":"star"}},{"id":"b","label":"B","path":"/apps","group":"more","glyph":{"segoe":"y","sf":"gear"},"badge":3}],"selected":"a"}"#.utf8))
        precondition(decoded.items[1].badge == 3 && decoded.items[0].badge == nil && decoded.selected == "a")
        precondition(panelNavSelection(decoded.items, url: URL(string: "oa-panel://panel/?k=x")!) == "a")
        precondition(panelNavSelection(decoded.items, url: URL(string: "oa-panel://panel/apps/opencode?k=x")!) == "b")
        precondition(panelNavSelection(decoded.items, url: URL(string: "oa-panel://panel/other")!) == nil)
        precondition(panelAccentHex(NSAppearance(named: .aqua)!).count == 7)

        // The window: split view with a source-list sidebar, unified toolbar.
        panelURL = base
        openPanel()
        let win = window!
        precondition(win.isVisible && panelActivationPolicy() == .regular, "an open window keeps the app regular")
        precondition(win.contentRect(forFrameRect: win.frame).size == panelDefaultContentSize, "default size \(win.frame)")
        precondition(win.contentMinSize == panelMinimumContentSize)
        precondition(win.styleMask.contains(.fullSizeContentView) && win.toolbarStyle == .unified)
        precondition(win.toolbar!.items.map(\.itemIdentifier) == [.sidebarTrackingSeparator, panelBackItemIdentifier, panelForwardItemIdentifier],
                     "toolbar \(win.toolbar!.items.map(\.itemIdentifier))")
        precondition(win.toolbar!.items.dropFirst().allSatisfy { $0.isNavigational })
        let split = win.contentViewController as! NSSplitViewController
        precondition(split.splitViewItems.count == 2 && split.splitViewItems[0].behavior == .sidebar)
        let side = sidebar!
        precondition(side.outline.style == .sourceList && side.outline.rowSizeStyle == .default)
        precondition(side.search.superview === side.view)

        precondition(spin(15) { side.rows.count == 10 && settled("/") && win.title == "Apps" && side.selectedID == "status" },
                     "rows \(side.rows.count) title \(win.title) selected \(String(describing: side.selectedID))")
        precondition(side.rows[6].item == nil && !side.outlineView(side.outline, shouldSelectItem: side.rows[6]),
                     "the group gap is a separator row")
        precondition(query("shell") == "native" && query("k") == "fixture")
        let guide = win.contentLayoutGuide as! NSLayoutGuide
        win.layoutIfNeeded()
        let top = webView!.convert(webView!.bounds, to: nil).maxY
        precondition(abs(top - guide.frame.maxY) < 1, "web view top \(top) guide \(guide.frame)")

        // Tiles and the badge.
        let rightsRow = side.rows.firstIndex { $0.item?.id == "rights" }!
        let cell = side.outline.view(atColumn: 0, row: rightsRow, makeIfNecessary: true) as! PanelNavCell
        precondition(cell.badge.stringValue == "2" && cell.tile.image?.size == NSSize(width: 20, height: 20))

        // Clicking a row loads that page; its document.title becomes the window title.
        side.outline.selectRowIndexes(IndexSet(integer: rightsRow), byExtendingSelection: false)
        precondition(spin(10) { settled("/rights") && win.title == "Permissions" }, "row click")
        precondition(query("shell") == "native")
        precondition(webView!.backForwardList.backList.map { $0.url.path } == ["/"],
                     "history after row click \(webView!.backForwardList.backList.map(\.url))")

        // A page link without shell=native is reloaded with it, and the sidebar follows.
        _ = evaluate("document.getElementById('next').click(); true")
        precondition(spin(10) { settled("/inference") && query("shell") == "native" && side.selectedID == "inference" },
                     "in-page link \(String(describing: webView!.url))")

        // Back through the toolbar item, then Forward through the menu.
        precondition(spin(5) { backItem!.isEnabled }, "back stays disabled")
        precondition(webView!.backForwardList.backList.map { $0.url.path } == ["/", "/rights"],
                     "history \(webView!.backForwardList.backList.map(\.url))")
        precondition(NSApp.sendAction(backItem!.action!, to: backItem!.target, from: backItem), "toolbar back action")
        precondition(spin(10) { settled("/rights") && side.selectedID == "rights" },
                     "toolbar back \(String(describing: webView!.url)) \(String(describing: side.selectedID)) \(webView!.backForwardList.backList.map(\.url))")

        // Menus: App, Edit, View (pages, Back), Window.
        let menu = NSApp.mainMenu!
        precondition(menu.items.map(\.title) == ["Abstraction Panel", "Edit", "View", "Window"], "\(menu.items.map(\.title))")
        let edit = menu.items[1].submenu!
        for (key, action) in [("c", #selector(NSText.copy(_:))), ("v", #selector(NSText.paste(_:))), ("x", #selector(NSText.cut(_:))), ("a", #selector(NSText.selectAll(_:)))] {
            precondition(edit.items.contains { $0.keyEquivalent == key && $0.action == action && $0.target == nil }, "edit \(key)")
        }
        let view = menu.items[2].submenu!
        precondition(Array(view.items.prefix(9).map(\.title)) == ["Get started", "Apps", "Models", "Permissions", "Activity", "Device", "Providers", "Credentials", "Settings"])
        precondition(Array(view.items.prefix(9).map(\.keyEquivalent)) == ["1", "2", "3", "4", "5", "6", "7", "8", "9"])
        precondition(view.items.contains { $0.title == "Back" && $0.keyEquivalent == "[" })
        precondition(NSApp.windowsMenu === menu.items[3].submenu)
        precondition(press("5") && spin(10) { settled("/work") && side.selectedID == "work" }, "⌘5")
        precondition(press("[") && spin(10) { settled("/rights") }, "⌘[")

        // The host's accent, injected and re-injected on a system colour change.
        let colors = panelAccentColors(NSApp.effectiveAppearance)
        let expected = [colors.accent, colors.light2, colors.dark1].joined(separator: " ")
        let accent = "['--oa-accent', '--oa-accent-light2', '--oa-accent-dark1'].map(function (p) { return getComputedStyle(document.documentElement).getPropertyValue(p).trim().toUpperCase(); }).join(' ')"
        precondition(spin(5) { (self.evaluate(accent) as? String) == expected },
                     "accent \(String(describing: evaluate(accent))) expected \(expected)")
        _ = evaluate("['--oa-accent', '--oa-accent-light2', '--oa-accent-dark1'].forEach(function (p) { document.documentElement.style.setProperty(p, '#123456'); }); true")
        NotificationCenter.default.post(name: NSColor.systemColorsDidChangeNotification, object: nil)
        precondition(spin(5) { (self.evaluate(accent) as? String) == expected }, "accent re-injection")
        precondition(evaluate("document.querySelector('.oa-nav, .oa-content > h1') === null") as? Bool == true)

        // Search filters page names; Return opens the first match.
        side.search.stringValue = "mod"
        side.controlTextDidChange(Notification(name: NSControl.textDidChangeNotification, object: side.search))
        precondition(side.rows.map { $0.item?.id } == ["inference"], "search \(side.rows.map { $0.item?.id })")
        side.search.stringValue = ""
        side.controlTextDidChange(Notification(name: NSControl.textDidChangeNotification, object: side.search))
        precondition(side.rows.count == 10 && side.selectedID == "rights")

        // Questions open on the Permissions page.
        _ = evaluate("location.href = '/card?k=fixture&shell=native'; true")
        precondition(spin(10) { settled("/card") })
        openQuestions()
        precondition(spin(10) { settled("/rights") && side.selectedID == "rights" }, "openQuestions")

        // Closing the window returns the launcher to a background agent.
        win.close()
        precondition(panelActivationPolicy() == .accessory, "close kept the Dock icon")
        openPanel()
        precondition(window === win && panelActivationPolicy() == .regular)
        win.close()
    }

    /// ⌘C and ⌘V reach a page field through the Edit menu. Exits 3 when no
    /// GUI session lets the window become key (an SSH-only login).
    func checkEditKeys(_ base: URL) {
        panelURL = base
        openPanel()
        let win = window!
        precondition(spin(15) { settled("/") }, "first page")
        let pasteboard = NSPasteboard.general
        let saved = (pasteboard.pasteboardItems ?? []).map { item -> NSPasteboardItem in
            let copy = NSPasteboardItem()
            for type in item.types { if let data = item.data(forType: type) { copy.setData(data, forType: type) } }
            return copy
        }
        defer {
            pasteboard.clearContents()
            if !saved.isEmpty { pasteboard.writeObjects(saved) }
        }
        win.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        guard spin(5, until: { win.isKeyWindow }) else {
            print("edit keys: the window cannot become key")
            exit(3)
        }
        win.makeFirstResponder(webView)
        _ = evaluate("var f = document.getElementById('field'); f.value = 'copied from the page'; f.focus(); f.select(); true")
        pasteboard.clearContents()
        precondition(press("c") && spin(5) { pasteboard.string(forType: .string) == "copied from the page" }, "⌘C")
        pasteboard.clearContents()
        pasteboard.setString("pasted into the page", forType: .string)
        _ = evaluate("var f = document.getElementById('field'); f.value = ''; f.focus(); true")
        precondition(press("v") && spin(5) { (self.evaluate("document.getElementById('field').value") as? String) == "pasted into the page" }, "⌘V")
        win.close()
    }
}
// Hidden unless the owner is present: the edit-keys mode needs a real key
// window, and the owner-present run also checks the applied policy.
fixtureHidesWindow = CommandLine.arguments.count < 4 || CommandLine.arguments[3] != "owner-present"
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
app.finishLaunching()
let delegate = AppDelegate()
delegate.forwardPanelRequests(to: URL(string: CommandLine.arguments[1])!)
let panelBase = URL(string: "oa-panel://panel/?k=fixture")!
if CommandLine.arguments[2] == "edit" {
    precondition(!fixtureHidesWindow, "edit keys need a key window: owner-present only")
    delegate.checkEditKeys(panelBase)
    print("edit keys fixture passed")
} else {
    delegate.checkServedWindow(panelBase)
    print("served window fixture passed")
}
'''


@unittest.skipUnless(sys.platform == "darwin", "Swift AppKit window fixture requires macOS")
class LauncherWindow(unittest.TestCase):
    def test_background_readiness_reopen_and_close_preserve_the_presenter(self):
        with tempfile.TemporaryDirectory() as scratch:
            scratch = Path(scratch)
            macos = fixture_bundle(scratch, "Window Fixture", "org.openabstractions.panel.window-fixture")
            child = macos / "panel"
            child.write_text("#!/bin/sh\nprintf '%s\\n' 'service control panel: oa-panel://panel/?k=fixture'\nexec /bin/sleep 30\n")
            child.chmod(0o755)
            fixture = scratch / "main.swift"
            fixture.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + r'''fixtureHidesWindow = ProcessInfo.processInfo.environment["OA_OWNER_PRESENT"] != "1"
extension AppDelegate {
    func checkBackgroundWindow() throws {
        let previous = Process()
        previous.executableURL = URL(fileURLWithPath: "/bin/sleep")
        previous.arguments = ["30"]
        try previous.run()
        panel = previous
        defer { stopOwnedPanel(panel); stopOwnedPanel(previous) }
        openWhenReady = false
        let url = URL(string: "oa-panel://panel/?k=fixture")!
        ready(url, from: previous)
        precondition(window == nil, "background readiness opened a window")
        stopOwnedPanel(previous)
        _ = applicationShouldHandleReopen(NSApplication.shared, hasVisibleWindows: false)
        precondition(panel !== previous && panel?.isRunning == true && panelURL == nil,
                     "reopen used a stopped child's stale URL")
        precondition(window == nil && openWhenReady, "reopen before readiness was lost")
        let child = panel!
        ready(url, from: child)
        precondition(window?.isVisible == true, "replacement readiness lost the reopen request")
        precondition(panelActivationPolicy() == .regular, "an open window keeps the app regular")
        ready(URL(string: "oa-panel://panel/?k=old")!, from: previous)
        panelExited(1, from: previous)
        precondition(panelURL == url, "a prior child's queued events replaced current readiness")
        let firstWindow = window
        window?.close()
        precondition(panelActivationPolicy() == .accessory, "closing the window kept the Dock icon")
        precondition(child.isRunning && panelURL == url, "closing the window stopped notification delivery")
        _ = applicationShouldHandleReopen(NSApplication.shared, hasVisibleWindows: false)
        precondition(window === firstWindow && window?.isVisible == true, "reopen did not reuse the window")
        window?.close()
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
try delegate.checkBackgroundWindow()
print("background window fixture passed")
''')
            executable = macos / "Window Fixture"
            subprocess.run(["swiftc", "-D", "PANEL_WINDOW_FIXTURE", str(fixture),
                            "-o", str(executable)], check=True, timeout=90)
            completed = subprocess.run([str(executable)], check=True, capture_output=True,
                                       text=True, timeout=15)
            self.assertIn("background window fixture passed", completed.stdout)

    def run_served(self, mode):
        """Builds the served fixture once, then runs one mode against a fresh stub."""
        cls = type(self)
        if getattr(cls, "served_executable", None) is None:
            cls.served_scratch = tempfile.TemporaryDirectory()
            scratch = Path(cls.served_scratch.name)
            macos = fixture_bundle(scratch, "Served Window Fixture", "org.openabstractions.panel.served-window-fixture")
            fixture = scratch / "main.swift"
            fixture.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + SERVED_FIXTURE)
            executable = macos / "Served Window Fixture"
            subprocess.run(["swiftc", "-D", "PANEL_WINDOW_FIXTURE", str(fixture),
                            "-o", str(executable)], check=True, timeout=120)
            cls.served_executable = executable
        StubShell.requests = []
        server = ThreadingHTTPServer(("127.0.0.1", 0), StubShell)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        presence = "owner-present" if OWNER_PRESENT else "hidden"
        try:
            base = "http://localhost:%d/?k=fixture" % server.server_address[1]
            if mode == "window":
                completed = subprocess.run([str(cls.served_executable), base, mode, presence],
                                           capture_output=True, text=True, timeout=90)
            else:
                # LaunchServices opens the fixture in the GUI session as a
                # person's launch would, so its window can become key.
                logs = Path(cls.served_scratch.name)
                for name in ("edit.out", "edit.err"):
                    (logs / name).write_text("")
                opened = subprocess.run(["/usr/bin/open", "-n", "-W", "--stdout", str(logs / "edit.out"),
                                         "--stderr", str(logs / "edit.err"),
                                         str(cls.served_executable.parent.parent.parent), "--args", base, mode, presence],
                                        capture_output=True, text=True, timeout=120)
                output = (logs / "edit.out").read_text()
                completed = subprocess.CompletedProcess(opened.args,
                    0 if "edit keys fixture passed" in output else 3 if "cannot become key" in output else 1,
                    output, opened.stderr + (logs / "edit.err").read_text())
        finally:
            server.shutdown()
            server.server_close()
        self.assertTrue(StubShell.requests)
        for request in StubShell.requests:
            query = parse_qs(urlsplit(request).query)
            self.assertEqual(query.get("shell"), ["native"], request)
            self.assertEqual(query.get("k"), ["fixture"], request)
        return completed

    @classmethod
    def tearDownClass(cls):
        if getattr(cls, "served_scratch", None) is not None:
            cls.served_scratch.cleanup()
            cls.served_scratch = cls.served_executable = None
        for identifier in ("org.openabstractions.panel.window-fixture",
                           "org.openabstractions.panel.served-window-fixture"):
            remove_bundle_folders(identifier)

    def test_settings_window_follows_the_shell_contract(self):
        completed = self.run_served("window")
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        self.assertIn("served window fixture passed", completed.stdout)

    @unittest.skipUnless(OWNER_PRESENT, "makes the launcher the active app with a key window and uses the "
                         "clipboard: owner-present tests only (OA_OWNER_PRESENT=1)")
    def test_edit_keys_reach_a_page_field(self):
        completed = self.run_served("edit")
        if completed.returncode == 3:
            self.skipTest("no GUI session: the window cannot become key (run from the logged-in session)")
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        self.assertIn("edit keys fixture passed", completed.stdout)

    def test_panel_responses_fail_closed(self):
        """An aborted answer, a chunk out of order and a launcher read over its
        bound each fail the request; ordered chunks complete it. No window,
        clipboard or focus is used."""
        with tempfile.TemporaryDirectory() as scratch:
            scratch = Path(scratch)
            fixture = scratch / "main.swift"
            fixture.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + r'''extension AppDelegate {
    func checkResponses() {
        var sent: [String] = []
        panelTransport = { request in sent.append(request.id) }
        var outcomes: [String: (Int, Int)] = [:]
        func ask(_ name: String) -> String {
            sendPanelRequest(method: "GET", url: URL(string: "oa-panel://panel/panel/nav.json?k=x")!, headers: [:], body: Data(),
                             PanelPending(task: nil) { status, data in outcomes[name] = (status, data.count) })
            return sent.last!
        }
        let ordered = ask("ordered")
        receivePanelResponse(ordered, seq: 0, status: 200, headers: [:], body: Data("ab".utf8), more: true)
        receivePanelResponse(ordered, seq: 1, status: 0, headers: [:], body: Data("cd".utf8), more: false)
        precondition(outcomes["ordered"]! == (200, 4), "ordered \(String(describing: outcomes["ordered"]))")
        let aborted = ask("aborted")
        receivePanelResponse(aborted, seq: 0, status: 0, headers: [:], body: Data(), more: false, aborted: true)
        precondition(outcomes["aborted"]! == (0, 0), "aborted")
        let late = ask("late")
        receivePanelResponse(late, seq: 1, status: 200, headers: [:], body: Data("x".utf8), more: false)
        precondition(outcomes["late"]! == (0, 0), "chunk 1 before chunk 0")
        let repeated = ask("repeated")
        receivePanelResponse(repeated, seq: 0, status: 200, headers: [:], body: Data("x".utf8), more: true)
        receivePanelResponse(repeated, seq: 0, status: 200, headers: [:], body: Data("x".utf8), more: false)
        precondition(outcomes["repeated"]! == (0, 0), "chunk 0 twice")
        let large = ask("large")
        let chunk = Data(count: 512 * 1024)
        for seq in 0..<9 { receivePanelResponse(large, seq: seq, status: 200, headers: [:], body: chunk, more: true) }
        precondition(outcomes["large"]! == (0, 0), "a launcher read over its bound")
        receivePanelResponse(large, seq: 9, status: 200, headers: [:], body: chunk, more: false)
        precondition(outcomes["large"]! == (0, 0) && pendingPanel.isEmpty, "a failed request stays failed")
    }
}
let app = NSApplication.shared
let delegate = AppDelegate()
delegate.checkResponses()
print("panel responses fixture passed")
''')
            executable = scratch / "Responses Fixture"
            subprocess.run(["swiftc", "-D", "PANEL_WINDOW_FIXTURE", str(fixture), "-o", str(executable)],
                           check=True, timeout=120)
            completed = subprocess.run([str(executable)], capture_output=True, text=True, timeout=30)
            self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
            self.assertIn("panel responses fixture passed", completed.stdout)

    def test_launcher_builds_for_the_declared_minimum_macos(self):
        # minimum-macos.txt, beside the launcher, is its one minimum version;
        # the installer's own test holds Info.plist's LSMinimumSystemVersion to
        # it, so this published test reads only files its repository publishes.
        declared = (HERE / "minimum-macos.txt").read_text(encoding="utf-8").strip()
        self.assertRegex(declared, r"^\d+\.\d+$")
        self.assertIn("minimum-macos.txt", (HERE / "build.sh").read_text(encoding="utf-8"),
                      "build.sh does not build for the declared minimum")
        target = "%s-apple-macos%s" % (platform.machine(), declared)
        subprocess.run(["swiftc", "-typecheck", "-target", target, str(HERE / "PanelLauncher.swift")],
                       check=True, timeout=120)


if __name__ == "__main__":
    unittest.main()
