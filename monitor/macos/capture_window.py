"""Capture the macOS Panel window against a real, isolated runtime.

This is the macOS implementation behind the one documented command,
scripts/capture_window.py (on the Mac, from the logged-in account; over SSH
is fine):

    python3 scripts/capture_window.py --panel <built panel> \
        --runtime <built openabstractions> --out <folder>

It registers a temporary isolated runtime as a LaunchAgent, starts the Go
Panel in HTTP mode against it, and opens PanelLauncher.swift's window through
LaunchServices in the GUI session, active like a person's own launch. The
window loads oa-panel:// pages as the launcher does, and the fixture transport
answers them from that HTTP Panel in place of the Go child's pipes. That
process captures only its own windows with CGWindowListCreateImage, which
needs no Screen Recording permission for the caller's own windows
(`screencapture -l` does). It writes light and dark PNGs per page and one of
the open View menu, then removes the runtime agent, the Panel process and the
runtime state. Nothing outside the Panel's own windows is captured.

The Panel serves the shell contract (nav.json, data-skin, ?shell=native)
and links static/skin-macos.css itself. Each capture first checks that the
page loaded that stylesheet and its rules apply, and the run fails if not.
"""

from pathlib import Path
import argparse
import os
import plistlib
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time


HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
SERVICES = ["runtime-v1", "logging-v1", "config-v1", "job-acceptance-v1", "router-v1", "model-v1",
            "storage-content-v1", "asks-application-v1", "rights-authorization-v1", "credentials-v1",
            "inference-v1", "inference-remote-v1", "registry-v1", "applications-v1", "resource-table-v1", "lend-v1"]

CAPTURE = r'''
typealias CreateWindowImage = @convention(c) (CGRect, UInt32, CGWindowID, UInt32) -> Unmanaged<CGImage>?

extension AppDelegate {
    func spin(_ seconds: Double, until condition: () -> Bool = { false }) -> Bool {
        let deadline = Date().addingTimeInterval(seconds)
        while !condition() && Date() < deadline {
            if let event = NSApp.nextEvent(matching: .any, until: Date().addingTimeInterval(0.05),
                                           inMode: .default, dequeue: true) {
                NSApp.sendEvent(event)
            }
        }
        return condition()
    }

    // The macOS 15+ SDK marks CGWindowListCreateImage unavailable; it still
    // captures the caller's own windows without Screen Recording permission.
    func captureWindow(_ id: CGWindowID, _ path: String) {
        guard let symbol = dlsym(UnsafeMutableRawPointer(bitPattern: -2), "CGWindowListCreateImage") else {
            print("capture unavailable: CGWindowListCreateImage missing"); return
        }
        let create = unsafeBitCast(symbol, to: CreateWindowImage.self)
        guard let image = create(.null, 1 << 3, id, 1 << 0)?.takeRetainedValue(),
              let png = NSBitmapImageRep(cgImage: image).representation(using: .png, properties: [:]) else {
            print("capture failed: \(path)"); return
        }
        try! png.write(to: URL(fileURLWithPath: path))
        print("captured \(path) \(image.width)x\(image.height)")
    }

    /// The page's own link loaded skin-macos.css and its rules apply.
    func skinLoaded() -> Bool {
        var loaded = false
        var done = false
        let script = "Array.prototype.some.call(document.styleSheets, function (s) {" +
            " return /\\/static\\/skin-macos\\.css$/.test(s.href || '') && s.cssRules.length > 0; })" +
            " && document.documentElement.getAttribute('data-skin') === 'macos'" +
            " && getComputedStyle(document.documentElement).getPropertyValue('--oa-mac-caption-size').trim() === '11px'"
        webView!.evaluateJavaScript(script) { value, _ in loaded = (value as? Bool) == true; done = true }
        _ = spin(5) { done }
        return loaded
    }

    func captureAll(base: URL, out: String) {
        var failures: [String] = []
        for (mode, name) in [("light", NSAppearance.Name.aqua), ("dark", NSAppearance.Name.darkAqua)] {
            NSApp.appearance = NSAppearance(named: name)
            _ = ensureWindow()
            _ = spin(0.5)
            panelURL = base
            openPanel()
            _ = spin(3) { NSApp.isActive && window!.isKeyWindow }
            print("\(mode): active \(NSApp.isActive) key \(window!.isKeyWindow)")
            for (slug, path) in [("apps", "/"), ("permissions", "/rights"), ("models", "/inference"),
                                 ("device", "/card"), ("settings", "/settings")] {
                loadPage(path)
                _ = spin(15) { webView?.url?.path == path && webView?.isLoading == false }
                _ = spin(2.5)
                if !skinLoaded() { failures.append("\(mode) \(path)") }
                captureWindow(CGWindowID(window!.windowNumber), out + "/\(mode)-\(slug).png")
            }
            if mode == "light", let view = NSApp.mainMenu?.items.first(where: { $0.title == "View" })?.submenu {
                let tracking = Timer(timeInterval: 0.8, repeats: false) { _ in
                    let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as? [[String: Any]] ?? []
                    for info in list where (info[kCGWindowOwnerPID as String] as? Int32) == getpid() {
                        let id = info[kCGWindowNumber as String] as! CGWindowID
                        if id != CGWindowID(self.window!.windowNumber) { self.captureWindow(id, out + "/light-view-menu.png") }
                    }
                    view.cancelTracking()
                }
                RunLoop.main.add(tracking, forMode: .eventTracking)
                view.popUp(positioning: nil, at: NSPoint(x: 260, y: window!.contentView!.bounds.height - 60), in: window!.contentView)
            }
            window?.close()
        }
        print(failures.isEmpty ? "skin-macos.css loaded on every page" : "skin-macos.css missing: \(failures)")
        FileManager.default.createFile(atPath: out + "/done", contents: Data(failures.isEmpty ? "ok".utf8 : "skin".utf8))
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
app.finishLaunching()
let delegate = AppDelegate()
let arguments = CommandLine.arguments
// The window loads oa-panel:// as the launcher does; its requests are
// answered by the HTTP-mode Panel this tool started.
delegate.forwardPanelRequests(to: URL(string: arguments[1])!)
delegate.captureAll(base: panelWindowURL(URL(string: arguments[1])!), out: arguments[2])
'''


def announced_url(stream, seconds):
    """The Panel's announced URL, or None once the deadline passes. A reader
    thread keeps a silent or half-written line from blocking past it."""
    lines = queue.Queue()

    def read():
        for line in stream:
            lines.put(line)
        lines.put(None)

    threading.Thread(target=read, daemon=True).start()
    deadline = time.monotonic() + seconds
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return None
        try:
            line = lines.get(timeout=remaining)
        except queue.Empty:
            return None
        if line is None:
            return None
        if line.startswith("service control panel: "):
            return line.split(": ", 1)[1].strip().replace("127.0.0.1", "localhost")


def run(*args, **kwargs):
    return subprocess.run([str(arg) for arg in args], check=True, **kwargs)


def bootstrap(domain, plist, label):
    run("launchctl", "bootstrap", domain, plist, timeout=15)
    return domain + "/" + label


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--panel", required=True, type=Path, help="built Go panel binary (monitor)")
    parser.add_argument("--runtime", required=True, type=Path, help="built openabstractions binary (serve)")
    parser.add_argument("--out", required=True, type=Path, help="folder for the PNG captures")
    options = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("macOS only")
    options.out.mkdir(parents=True, exist_ok=True)
    out = options.out.resolve()
    uid = os.getuid()
    domain = "gui/%d" % uid
    name = "s11-capture-%d" % os.getpid()
    agents = []
    panel = None
    with tempfile.TemporaryDirectory(prefix="oa-panel-capture-") as temp:
        scratch = Path(temp).resolve()
        state = scratch / "state"
        state.mkdir(mode=0o700)
        try:
            contents = scratch / "Panel Capture.app/Contents"
            (contents / "MacOS").mkdir(parents=True)
            with (contents / "Info.plist").open("wb") as metadata:
                plistlib.dump({"CFBundleIdentifier": "org.openabstractions.panel.capture",
                              "CFBundleName": "Abstraction Panel", "CFBundlePackageType": "APPL",
                              "CFBundleExecutable": "Panel Capture", "LSUIElement": True}, metadata)
            source = scratch / "main.swift"
            source.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + CAPTURE)
            executable = contents / "MacOS/Panel Capture"
            run("swiftc", "-D", "PANEL_WINDOW_FIXTURE", source, "-o", executable, timeout=180)

            label = "org.openabstractions.test." + name + ".runtime"
            runtime_plist = scratch / "runtime.plist"
            runtime_plist.write_bytes(plistlib.dumps({
                "Label": label,
                "ProgramArguments": [str(options.runtime.resolve()), "serve", "runtime", "--xpc", "--isolated", name,
                                     "--state-dir", str(state), "--operator-program", str(options.panel.resolve())],
                "MachServices": {"com.openabstractions.isolated.%s.%s" % (name, service): True for service in SERVICES},
                "RunAtLoad": True, "KeepAlive": False, "ProcessType": "Interactive",
                "StandardOutPath": str(scratch / "runtime.out"), "StandardErrorPath": str(scratch / "runtime.err")}))
            agents.append(bootstrap(domain, runtime_plist, label))
            endpoint = "xpc:com.openabstractions.isolated.%s.runtime-v1" % name
            runtime_log = scratch / "runtime.err"
            deadline = time.monotonic() + 30
            while not (runtime_log.exists() and
                       "ABSTRACTION_RUNTIME_ENDPOINT=" + endpoint in runtime_log.read_text(errors="replace")):
                if time.monotonic() > deadline:
                    raise SystemExit("isolated runtime not ready")
                time.sleep(0.2)

            with open(scratch / "panel.err", "w") as panel_log:
                panel = subprocess.Popen([str(options.panel.resolve()), "-open=false", "-addr", "127.0.0.1:0",
                                          "-runtime-endpoint", endpoint, "-runtime-program", str(options.runtime.resolve()),
                                          "-runtime-state-dir", str(state)],
                                         stdout=subprocess.PIPE, stderr=panel_log, text=True)
            url = announced_url(panel.stdout, 30)
            if url is None:
                raise SystemExit("Panel did not announce its URL within 30 s")

            # LaunchServices starts the window app in the GUI session as a
            # launch the person could have made, so its window can become key.
            (out / "done").unlink(missing_ok=True)
            run("/usr/bin/open", "-n", "-W", "--stdout", scratch / "capture.out", "--stderr", scratch / "capture.err",
                contents.parent, "--args", url, out, timeout=240)
            print((scratch / "capture.out").read_text(errors="replace") if (scratch / "capture.out").exists() else "")
            if not (out / "done").exists():
                print((scratch / "capture.err").read_text(errors="replace") if (scratch / "capture.err").exists() else "",
                      file=sys.stderr)
                raise SystemExit("capture did not finish")
            verdict = (out / "done").read_text()
            (out / "done").unlink()
            if verdict != "ok":
                raise SystemExit("the pages did not load static/skin-macos.css through their own link")
        finally:
            for service in reversed(agents):
                subprocess.run(["launchctl", "bootout", service], timeout=15)
            if panel is not None:
                panel.terminate()
                try:
                    panel.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    panel.kill()
                    panel.wait(timeout=10)
                panel.stdout.close()
            for folder in ("Library/Caches", "Library/WebKit", "Library/HTTPStorages"):
                shutil.rmtree(Path.home() / folder / "org.openabstractions.panel.capture", ignore_errors=True)


if __name__ == "__main__":
    main()
