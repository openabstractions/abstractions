// PanelLauncher hosts the Panel on macOS: an NSStatusItem menu-bar item
// (Open Panel, Questions, Quit) and a WKWebView window on the loopback URL
// the Go "panel" binary prints. It carries no third-party code and no Xcode
// project; monitor/macos/build.sh compiles it with `swiftc` alone and places
// it beside "panel" as the bundle's own executable, so Abstraction Panel.app
// exec's the Go binary from inside itself.
//
// The Go binary owns every capability: the loopback HTTP server, the per-run
// key, the account boundary. This launcher owns the window and the menu, and
// reads the printed URL off the child's stdout instead of choosing one, the
// way monitor/tray_windows.go reads win.RunHidden's own bound listener.

import Cocoa
import Darwin
import WebKit

let bundleExecutables = Bundle.main.bundleURL
    .appendingPathComponent("Contents/MacOS")
let panelBinary = bundleExecutables.appendingPathComponent("panel")

/// The line the Go binary prints once its listener is bound
/// (monitor/service_panel.go: `fmt.Println("service control panel:", url)`).
let announcePrefix = "service control panel: "

// launchctl bootout sends SIGTERM to this launcher. Route it through AppKit's
// normal termination path so the Process started below is stopped before the
// launcher exits. A signal handler cannot safely call AppKit or Process.
func installTerminationSignalHandler(_ terminate: @escaping () -> Void) -> DispatchSourceSignal {
    signal(SIGTERM, SIG_IGN)
    let source = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
    source.setEventHandler(handler: terminate)
    source.resume()
    return source
}

// The child is this launcher's own Process. Bound its cooperative shutdown
// inside the LaunchAgent's ten-second ExitTimeOut, then reap it before exit.
func stopOwnedPanel(_ process: Process?) {
    guard let process, process.isRunning else { return }
    process.terminate()
    let deadline = Date().addingTimeInterval(3)
    while process.isRunning && Date() < deadline {
        Thread.sleep(forTimeInterval: 0.05)
    }
    if process.isRunning {
        kill(process.processIdentifier, SIGKILL)
    }
    process.waitUntilExit()
}

/// 127.0.0.1 is what the Go binary binds; WKWebView's default configuration
/// treats "localhost" as a local address for App Transport Security's
/// automatic exception and treats a numeric loopback literal less
/// predictably across OS versions. The port and the key travel unchanged.
func localize(_ printed: String) -> URL? {
    guard var parts = URLComponents(string: printed) else { return nil }
    if parts.host == "127.0.0.1" { parts.host = "localhost" }
    return parts.url
}

/// True for a URL this launcher's own WKWebView should keep navigating:
/// the Panel's own loopback page. Anything else — a help link's
/// target="_blank" anchor, in practice (monitor/help.go) — opens in the
/// system's default browser instead, and the Panel window stays put.
func isPanelOrigin(_ url: URL) -> Bool {
    guard let host = url.host else { return false }
    return host == "127.0.0.1" || host == "localhost"
}

final class AppDelegate: NSObject, NSApplicationDelegate, WKUIDelegate, WKNavigationDelegate {
    private var statusItem: NSStatusItem?
    private var window: NSWindow?
    private var webView: WKWebView?
    private var panel: Process?
    private var panelURL: URL?
    private var terminationSignal: DispatchSourceSignal?
    private var openItem: NSMenuItem?
    private var questionsItem: NSMenuItem?

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildStatusItem()
        launchPanel()
        // Install after Process.run so the child keeps the normal SIGTERM
        // disposition rather than inheriting this dispatch source's SIG_IGN.
        terminationSignal = installTerminationSignalHandler { NSApp.terminate(nil) }
    }

    func applicationWillTerminate(_ notification: Notification) {
        stopPanel()
    }

    // MARK: - Menu bar

    private func buildStatusItem() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        // MenuBarIcon(@2x).png (monitor/macos/build.sh, from the brand mark
        // at monitor/icon/mark-16.png and mark-32.png) as a template image:
        // AppKit recolors it for the light and dark menu bar and for a
        // selected/highlighted state, the same way every other status item
        // does. A build that skipped `go generate ./monitor/icon/...`, or
        // an older bundle, carries no such image; the title falls back to
        // the same glyph this item always showed.
        if let image = Bundle.main.image(forResource: "MenuBarIcon") {
            image.isTemplate = true
            item.button?.image = image
        } else {
            item.button?.title = "◐"
        }
        item.button?.toolTip = "Abstraction Panel"

        let menu = NSMenu()
        let open = NSMenuItem(title: "Open Panel", action: #selector(openPanel), keyEquivalent: "")
        open.target = self
        open.isEnabled = false
        let questions = NSMenuItem(title: "Questions", action: #selector(openQuestions), keyEquivalent: "")
        questions.target = self
        questions.isEnabled = false
        let quit = NSMenuItem(title: "Quit", action: #selector(quit), keyEquivalent: "q")
        quit.target = self

        menu.addItem(open)
        menu.addItem(questions)
        menu.addItem(NSMenuItem.separator())
        menu.addItem(quit)
        item.menu = menu

        statusItem = item
        openItem = open
        questionsItem = questions
    }

    // MARK: - The Go binary

    /// Starts panel as a child process and reads its announced URL off
    /// stdout. It is a child, not an exec replacement: this process stays
    /// alive to own the menu bar item and the window.
    private func launchPanel() {
        let process = Process()
        process.executableURL = panelBinary
        process.arguments = []
        process.environment = ProcessInfo.processInfo.environment

        let stdout = Pipe()
        process.standardOutput = stdout
        process.standardError = Pipe()

        var announced = false
        stdout.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, !announced else { return }
            guard let text = String(data: data, encoding: .utf8) else { return }
            for line in text.split(separator: "\n") {
                guard line.hasPrefix(announcePrefix) else { continue }
                let printed = String(line.dropFirst(announcePrefix.count))
                guard let url = localize(printed) else { continue }
                announced = true
                DispatchQueue.main.async { self?.ready(url) }
                break
            }
        }

        process.terminationHandler = { [weak self] proc in
            DispatchQueue.main.async { self?.panelExited(proc.terminationStatus) }
        }

        do {
            try process.run()
            panel = process
        } catch {
            NSLog("Abstraction Panel: could not start %@: %@", panelBinary.path, "\(error)")
        }
    }

    private func stopPanel() {
        stopOwnedPanel(panel)
    }

    private func ready(_ url: URL) {
        panelURL = url
        openItem?.isEnabled = true
        questionsItem?.isEnabled = true
    }

    private func panelExited(_ status: Int32) {
        panelURL = nil
        openItem?.isEnabled = false
        questionsItem?.isEnabled = false
        statusItem?.button?.toolTip = "Abstraction Panel — stopped"
    }

    // MARK: - The window

    private func ensureWindow() -> WKWebView {
        if let webView { return webView }
        let configuration = WKWebViewConfiguration()
        let view = WKWebView(frame: NSRect(x: 0, y: 0, width: 960, height: 640), configuration: configuration)
        view.uiDelegate = self
        view.navigationDelegate = self

        let win = NSWindow(
            contentRect: view.frame,
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false)
        win.title = "Abstraction Panel"
        win.contentView = view
        win.contentMinSize = NSSize(width: 960, height: 640)
        win.center()
        win.isReleasedWhenClosed = false

        webView = view
        window = win
        return view
    }

    @objc private func openPanel() {
        guard let panelURL else { return }
        let view = ensureWindow()
        view.load(URLRequest(url: panelURL))
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc private func openQuestions() {
        guard let panelURL else { return }
        // The single-page app's own fragment navigation opens straight to
        // the Questions section, the way monitor/tray_windows.go's
        // `url + "#questions"` does.
        var withFragment = panelURL
        if var components = URLComponents(url: panelURL, resolvingAgainstBaseURL: false) {
            components.fragment = "questions"
            withFragment = components.url ?? panelURL
        }
        let view = ensureWindow()
        view.load(URLRequest(url: withFragment))
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc private func quit() {
        stopPanel()
        NSApp.terminate(nil)
    }

    // MARK: - External links open in the system browser

    /// A help link's target="_blank" anchor (monitor/help.go) asks WebKit for
    /// a new window through this delegate method. Opening it in the system
    /// browser instead, and returning nil, leaves the Panel with the one
    /// window it already has.
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = navigationAction.request.url, !isPanelOrigin(url) {
            NSWorkspace.shared.open(url)
        }
        return nil
    }

    /// Catches a plain (non target="_blank") link to a non-loopback host
    /// too: canceled here, and opened in the system browser instead.
    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if let url = navigationAction.request.url, navigationAction.targetFrame?.isMainFrame == true, !isPanelOrigin(url) {
            NSWorkspace.shared.open(url)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }
}

#if !PANEL_SIGNAL_FIXTURE
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
#endif
