// PanelLauncher hosts the Panel on macOS: native background questions
// and an on-demand WKWebView window on the oa-panel:// URL
// the Go "panel" binary prints. It carries no third-party code and no Xcode
// project; monitor/macos/build.sh compiles it with `swiftc` alone and places
// it beside "panel" as the bundle's own executable, so Abstraction Panel.app
// exec's the Go binary from inside itself.
//
// Go owns runtime binding, public operator answers and permission rule edits.
// This launcher renders Go's presentation and returns the chosen option over
// the owned child's pipes. The window's every request travels over the same
// pipes to the child (PanelSchemeHandler, monitor/native_panel_bridge.go);
// nothing listens.

import Cocoa
import Darwin
import IOKit
import os
import UserNotifications
import WebKit

let bundleExecutables = Bundle.main.bundleURL
    .appendingPathComponent("Contents/MacOS")
let panelBinary = bundleExecutables.appendingPathComponent("panel")

/// The line the Go binary prints once its listener is bound
/// (monitor/service_panel.go: `fmt.Println("service control panel:", url)`).
let announcePrefix = "service control panel: "
let nativeAsksPrefix = "oa-native-asks: "
let maxNativeAsksLine = 1024 * 1024

// Child events run in the ordinary application loop and in a modal loop, such
// as the failure alert's, so an external answer or child exit is never held.
// CFRunLoopPerformBlock explicitly supports cross-thread work and mode arrays.
// https://developer.apple.com/documentation/corefoundation/cfrunloopperformblock(_:_:_:)
func performPanelEvent(_ action: @escaping () -> Void) {
    let loop = CFRunLoopGetMain()
    let modes = [RunLoop.Mode.common.rawValue, RunLoop.Mode.modalPanel.rawValue] as CFArray
    CFRunLoopPerformBlock(loop, modes, action)
    CFRunLoopWakeUp(loop)
}

func exactSessionBoolean(_ value: Any?) -> Bool? {
    guard let value, CFGetTypeID(value as CFTypeRef) == CFBooleanGetTypeID(),
          let number = value as? NSNumber else { return nil }
    return number.boolValue
}

// The public caller session keys prove which GUI session this process is in.
// The only positive lock signal here is a private root-registry property:
// Apple OSS xnu IOKitKeysPrivate.h declares IOConsoleLocked as OSBoolean.
// https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/iokit/IOKit/IOKitKeysPrivate.h#L36
// It was measured on macOS 26.6.2; it is not a documented public guarantee.
// Missing, changed-type or failing evidence defers rather than assuming unlock.
func nativeSessionEvidenceAllowsPrompt(_ session: [String: Any]?, consoleLocked: Any?, uid: uid_t) -> Bool {
    guard let session,
          let user = session[kCGSessionUserIDKey as String] as? NSNumber,
          CFGetTypeID(user) == CFNumberGetTypeID(),
          !CFNumberIsFloatType(unsafeBitCast(user, to: CFNumber.self)),
          user.int64Value == Int64(uid),
          exactSessionBoolean(session[kCGSessionOnConsoleKey as String]) == true,
          exactSessionBoolean(session[kCGSessionLoginDoneKey as String]) == true,
          exactSessionBoolean(consoleLocked) == false else { return false }
    if let screenLocked = session["CGSSessionScreenIsLocked"] {
        guard exactSessionBoolean(screenLocked) == false else { return false }
    }
    return true
}

private var nativeSessionEvidenceUnavailableReported = false

private func missingNativeSessionEvidence() -> Bool {
    if !nativeSessionEvidenceUnavailableReported {
        NSLog("Abstraction Panel: native questions deferred; required macOS session evidence is unavailable")
        nativeSessionEvidenceUnavailableReported = true
    }
    return false
}

func nativeSessionUnlocked() -> Bool {
    guard CGDisplayIsAsleep(CGMainDisplayID()) == 0 else { return false }
    let root = IORegistryGetRootEntry(kIOMainPortDefault)
    guard root != IO_OBJECT_NULL else { return missingNativeSessionEvidence() }
    defer { IOObjectRelease(root) }
    let locked = IORegistryEntryCreateCFProperty(root, "IOConsoleLocked" as CFString,
                                                kCFAllocatorDefault, 0)?.takeRetainedValue()
    guard let session = CGSessionCopyCurrentDictionary() as? [String: Any],
          exactSessionBoolean(locked) != nil,
          exactSessionBoolean(session[kCGSessionOnConsoleKey as String]) != nil,
          exactSessionBoolean(session[kCGSessionLoginDoneKey as String]) != nil,
          let user = session[kCGSessionUserIDKey as String] as? NSNumber,
          CFGetTypeID(user) == CFNumberGetTypeID(),
          !CFNumberIsFloatType(unsafeBitCast(user, to: CFNumber.self)) else {
        return missingNativeSessionEvidence()
    }
    nativeSessionEvidenceUnavailableReported = false
    return nativeSessionEvidenceAllowsPrompt(session, consoleLocked: locked, uid: getuid())
}

struct NativeQuestionChoice: Decodable {
    let label: String
    let option: String
}

struct NativeQuestionPrompt: Decodable {
    let title: String
    let instruction: String
    let content: String
    let details: String
    let footer: String
    let choices: [NativeQuestionChoice]
    let defaultOption: String
    let appChoice: Bool?
    let warning: String?
    let permissionContent: String?
    let permissionPrompt: Bool?
    let iconProgram: String?
    // Go's one level choice (promptLevelFor): blue, gray, yellow or orange.
    let level: String?
    enum CodingKeys: String, CodingKey {
        case title = "Title", instruction = "Instruction", content = "Content"
        case details = "Details", footer = "Footer", choices = "Choices", defaultOption = "Default"
        case appChoice = "AppChoice"
        case warning = "Warning"
        case permissionContent = "PermissionContent"
        case permissionPrompt = "PermissionPrompt"
        case iconProgram = "IconProgram"
        case level = "Level"
    }
}

// A prompt's level badges its alert icon in the matching system color, after
// the Windows UAC banners: blue when OpenAbstractions itself asks, gray for a
// verified publisher, yellow for an unverified requester, orange for a
// high-risk request. Go chooses the level; Swift reads no prompt text for it.
// A missing or unknown level leaves the icon as it is.
func nativePromptLevelColor(_ level: String?) -> NSColor? {
    switch level {
    case "blue": return .systemBlue
    case "gray": return .systemGray
    case "yellow": return .systemYellow
    case "orange": return .systemOrange
    default: return nil
    }
}

// The level a prompt is badged with: its own when Go sent a known one. A
// permission prompt without a known level is badged yellow, as Windows draws
// it; any other prompt keeps its icon.
func nativePromptLevel(_ level: String?, permission: Bool) -> String? {
    if nativePromptLevelColor(level) != nil { return level }
    return permission ? "yellow" : nil
}

// The level's system color as an appearance draws it, in sRGB.
func nativePromptLevelColor(_ level: String?, appearance: NSAppearance) -> NSColor? {
    guard let color = nativePromptLevelColor(level) else { return nil }
    var resolved: NSColor?
    appearance.performAsCurrentDrawingAppearance { resolved = color.usingColorSpace(.sRGB) }
    return resolved
}

// The words VoiceOver reads for a level badge; the prompt's text says the same.
func nativePromptLevelDescription(_ level: String?) -> String? {
    switch level {
    case "blue": return "OpenAbstractions request"
    case "gray": return "Verified publisher"
    case "yellow": return "Publisher not verified"
    case "orange": return "High-risk request"
    default: return nil
    }
}

// The alert icon with its level badge at the lower right: a disc in the
// level's system color carrying a white shield (blue, gray) or a black
// exclamation mark (yellow, orange). The image draws at display time, so the badge follows
// the alert's light or dark appearance.
func nativeLevelIcon(_ base: NSImage?, level: String?) -> NSImage? {
    guard let color = nativePromptLevelColor(level) else { return base }
    let caution = level == "yellow" || level == "orange"
    let symbol = NSImage(systemSymbolName: caution ? "exclamationmark" : "shield.fill", accessibilityDescription: nil)?
        .withSymbolConfiguration(NSImage.SymbolConfiguration(pointSize: 15, weight: .bold))
    let image = NSImage(size: NSSize(width: 64, height: 64), flipped: false) { rect in
        base?.draw(in: rect)
        let badge = NSRect(x: rect.maxX - 30, y: rect.minY, width: 30, height: 30)
        NSColor.windowBackgroundColor.setFill()
        NSBezierPath(ovalIn: badge).fill()
        color.setFill()
        NSBezierPath(ovalIn: badge.insetBy(dx: 2, dy: 2)).fill()
        if let symbol {
            let glyph = NSRect(x: badge.midX - symbol.size.width / 2, y: badge.midY - symbol.size.height / 2,
                               width: symbol.size.width, height: symbol.size.height)
            let ink = caution ? NSColor.black : NSColor.white
            let tinted = NSImage(size: symbol.size, flipped: false) { inner in
                symbol.draw(in: inner)
                ink.set()
                inner.fill(using: .sourceAtop)
                return true
            }
            tinted.draw(in: glyph)
        }
        return true
    }
    image.accessibilityDescription = nativePromptLevelDescription(level)
    return image
}

// The requester's own icon: the innermost app bundle containing the verified
// program Go selected (none for a script host). A program outside a bundle,
// or a bundle that is gone, keeps OA's own icon.
func nativeRequesterIcon(_ program: String?) -> NSImage? {
    guard let program, program.hasPrefix("/") else { return nil }
    var url = URL(fileURLWithPath: program).standardizedFileURL.deletingLastPathComponent()
    while url.path != "/" && !url.path.isEmpty {
        if url.pathExtension.lowercased() == "app" {
            var directory: ObjCBool = false
            guard FileManager.default.fileExists(atPath: url.path, isDirectory: &directory), directory.boolValue else { return nil }
            return NSWorkspace.shared.icon(forFile: url.path)
        }
        url.deleteLastPathComponent()
    }
    return nil
}

// A permission prompt plays the person's alert sound once, when it appears.
// NSSound.beep follows the Sound settings' alert sound and alert volume.
#if PANEL_WINDOW_FIXTURE
/// Window fixtures run over SSH in the owner's console session. While this
/// is true (the default) the Panel window they open is transparent,
/// shadowless and lets clicks through, the launcher never activates, and
/// the activation policy is recorded in fixtureActivationPolicy instead of
/// applied, since a .regular policy shows a Dock icon. Fixtures read it
/// through panelActivationPolicy(). An owner-present run sets it false. The
/// shipped launcher has no such switch (test_launcher_question_panel.py).
var fixtureHidesWindow = true
var fixtureActivationPolicy = NSApplication.ActivationPolicy.accessory
#endif

/// The launcher is an ordinary app (.regular: Dock icon, app switcher, menu
/// bar) while its window is open and a background agent (.accessory) while
/// it is closed.
func setPanelActivationPolicy(_ policy: NSApplication.ActivationPolicy) {
    #if PANEL_WINDOW_FIXTURE
    if fixtureHidesWindow {
        fixtureActivationPolicy = policy
        return
    }
    #endif
    NSApp.setActivationPolicy(policy)
}

func panelActivationPolicy() -> NSApplication.ActivationPolicy {
    #if PANEL_WINDOW_FIXTURE
    if fixtureHidesWindow { return fixtureActivationPolicy }
    #endif
    return NSApp.activationPolicy()
}

#if PANEL_ASKS_FIXTURE
/// Asks fixtures run over SSH in the owner's console session. While this is
/// true (the default) nothing they present is visible or audible: question
/// panels are transparent and let clicks through, the alert sound is silent
/// and the Panel window stays ordered out. Fixtures still click buttons by
/// performClick. Only the owner-present on-top probe sets it false. The
/// shipped launcher has no such switch (test_launcher_question_panel.py
/// checks its symbols).
var fixtureHidesPresentation = true
var nativePermissionAlertSound: () -> Void = { if !fixtureHidesPresentation { NSSound.beep() } }
#else
var nativePermissionAlertSound: () -> Void = { NSSound.beep() }
#endif

struct NativeAsksFrame: Decodable {
    let kind: String
    let id: String?
    let prompt: NativeQuestionPrompt?
    let title: String?
    let instruction: String?
    let content: String?
    // A report's level; a question carries its own in prompt.
    var level: String? = nil
    // A panel-response chunk: the first carries status and headers; body is
    // base64; more says another chunk follows.
    var seq: Int? = nil
    var status: Int? = nil
    var headers: [String: String]? = nil
    var body: String? = nil
    var more: Bool? = nil
    var aborted: Bool? = nil
    // An application notification (ASK-N7).
    var notification: NativeNotification? = nil
}

/// One application notification Go prepared (ASK-N7): the app's text, the
/// service-recorded sender, and each click's arguments, which this launcher
/// returns unchanged. Go records every outcome through the service.
struct NativeNotification: Decodable {
    let title: String
    let body: String
    let sender: String
    let open: String
    let actions: [NativeNotificationAction]
    let silently: Bool
}

struct NativeNotificationAction: Decodable {
    let title: String
    let args: String
}

/// The category of one application notification. Each notification has its
/// own: its actions carry its own click arguments.
let appNotificationCategoryPrefix = "oa.n."
let maxAppNotifications = 32

func appNotificationCategory(_ id: String, _ n: NativeNotification) -> UNNotificationCategory {
    let actions = n.actions.prefix(4).map { action -> UNNotificationAction in
        let options: UNNotificationActionOptions = action.title.hasPrefix("Turn off") ? [.destructive] : [.foreground]
        return UNNotificationAction(identifier: action.args, title: action.title, options: options)
    }
    return UNNotificationCategory(identifier: appNotificationCategoryPrefix + id, actions: Array(actions),
                                  intentIdentifiers: [], options: [.customDismissAction])
}

func appNotificationContent(_ id: String, _ n: NativeNotification) -> UNMutableNotificationContent {
    let content = UNMutableNotificationContent()
    content.title = n.title
    content.subtitle = "From " + n.sender
    content.body = n.body
    content.categoryIdentifier = appNotificationCategoryPrefix + id
    content.userInfo = ["oa-id": id, "oa-open": n.open]
    if n.silently {
        if #available(macOS 12.0, *) { content.interruptionLevel = .passive }
    } else {
        content.sound = .default
    }
    return content
}

/// Notification steps go to the unified log with public text: NSLog lines from
/// this launcher never reached it on the 2026-10-07 Mac runs. Read them with
/// log show --predicate 'subsystem == "org.openabstractions.panel" AND category == "notifications"'
let notificationLogHandle = OSLog(subsystem: "org.openabstractions.panel", category: "notifications")

func notificationLog(_ message: String) {
    os_log("%{public}@", log: notificationLogHandle, type: .default, message)
}

/// An error's domain, code and description, or "ok" for none.
func notificationErrorText(_ error: Error?) -> String {
    guard let error else { return "ok" }
    let ns = error as NSError
    return "\(ns.domain) \(ns.code): \(ns.localizedDescription)"
}

func notificationResponseKind(_ action: String) -> String {
    switch action {
    case UNNotificationDismissActionIdentifier: return "close"
    case UNNotificationDefaultActionIdentifier: return "body"
    default: return "action"
    }
}

/// The reply a click on an application notification sends Go, or nil for a
/// notification that is not one; questions never arrive as notifications. A
/// close carries the record; any other click carries its prepared arguments.
func appNotificationReply(category: String, action: String, userInfo: [AnyHashable: Any]) -> [String: Any]? {
    guard category.hasPrefix(appNotificationCategoryPrefix), let id = userInfo["oa-id"] as? String else { return nil }
    switch action {
    case UNNotificationDismissActionIdentifier:
        return ["kind": "notification-closed", "id": id]
    case UNNotificationDefaultActionIdentifier:
        guard let open = userInfo["oa-open"] as? String else { return nil }
        return ["kind": "notification-click", "args": open]
    default:
        return ["kind": "notification-click", "args": action]
    }
}

// The expected runtime remains a Go trust input. Forward explicit isolated
// binding intact rather than selecting the installed runtime for this child.
// An installer's -register-application or -remove-application reaches the
// child the same way: Go asks that one confirmation on the question panel.
let nativeApplicationRequestFlags = ["register-application", "remove-application"]
let nativePermissionReviewFlags = ["review-feature", "review-program", "review-app", "review-host", "review-receipt"]

func nativePermissionReviewRun(_ arguments: [String]) -> Bool {
    arguments.contains { argument in
        let normalized = argument.drop(while: { $0 == "-" })
        return nativePermissionReviewFlags.contains { normalized == $0 || normalized.hasPrefix($0 + "=") }
    }
}

func nativePanelArguments(_ arguments: [String]) -> [String] {
    let flags = ["runtime-endpoint", "runtime-program", "runtime-state-dir"] + nativeApplicationRequestFlags +
        (nativePermissionReviewRun(arguments) ? ["page"] + nativePermissionReviewFlags : [])
    var result = ["-native-asks"]
    var index = 1
    while index < arguments.count {
        let argument = arguments[index]
        let normalized = argument.drop(while: { $0 == "-" })
        if flags.contains(String(normalized)) {
            result.append("-" + String(normalized))
            if index + 1 < arguments.count {
                index += 1
                result.append(arguments[index])
            }
        } else if flags.contains(where: { normalized.hasPrefix($0 + "=") }) {
            result.append("-" + String(normalized))
        }
        index += 1
    }
    return result
}

/// True when this run carries an installer's request: the launcher then
/// opens no window and exits with its child's status once the child ends.
func nativeApplicationRequestRun(_ arguments: [String]) -> Bool {
    nativePermissionReviewRun(arguments) || nativePanelArguments(arguments).contains { argument in
        nativeApplicationRequestFlags.contains { argument == "-" + $0 || argument.hasPrefix("-" + $0 + "=") }
    }
}

/// Incremental, bounded parser for the Go child's stdout: the Panel's
/// readiness line and the private native frames. It ignores unrelated
/// startup text, malformed JSON and overlong lines.
struct NativeLineParser {
    private var buffer = Data()
    private var droppingOversizeLine = false

    mutating func consume(_ data: Data, onPanelURL: (String) -> Void,
                          onNative: (NativeAsksFrame) -> Void = { _ in }) {
        for byte in data {
            if droppingOversizeLine {
                if byte == 0x0A { droppingOversizeLine = false }
                continue
            }
            if byte == 0x0A {
                if buffer.last == 0x0D { buffer.removeLast() }
                parse(buffer, onPanelURL: onPanelURL, onNative: onNative)
                buffer.removeAll(keepingCapacity: true)
                continue
            }
            buffer.append(byte)
            if buffer.count > maxNativeAsksLine {
                buffer.removeAll(keepingCapacity: true)
                droppingOversizeLine = true
            }
        }
    }

    private func parse(_ line: Data, onPanelURL: (String) -> Void,
                       onNative: (NativeAsksFrame) -> Void) {
        guard let text = String(data: line, encoding: .utf8) else { return }
        if text.hasPrefix(announcePrefix) {
            onPanelURL(String(text.dropFirst(announcePrefix.count)))
            return
        }
        guard text.hasPrefix(nativeAsksPrefix) else { return }
        // "level" is a report's header level: Go sends one with every report.
        let payload = Data(text.dropFirst(nativeAsksPrefix.count).utf8)
        guard let object = try? JSONSerialization.jsonObject(with: payload),
              let fields = object as? [String: Any],
              Set(fields.keys).isSubset(of: Set(["kind", "id", "prompt", "title", "instruction", "content", "level",
                                                 "seq", "status", "headers", "body", "more", "aborted", "notification"])),
              let frame = try? JSONDecoder().decode(NativeAsksFrame.self, from: payload) else { return }
        onNative(frame)
    }
}

// launchctl bootout sends SIGTERM to this launcher. Route it through AppKit's
// normal termination path so the Process started below is stopped before the
// launcher exits. A signal handler cannot safely call AppKit or Process.
/// True when a quit Apple Event carries a logout, restart or shutdown reason
/// (kAEQuitReason). The Dock, the Cmd-Tab switcher and the Quit menu item
/// send none.
func panelQuitEndsLauncher(_ event: NSAppleEventDescriptor?) -> Bool {
    guard let reason = event?.attributeDescriptor(forKeyword: AEKeyword(kAEQuitReason))?.enumCodeValue else {
        return false
    }
    let session = [kAELogOut, kAEReallyLogOut, kAEShowRestartDialog, kAERestart, kAEShowShutdownDialog, kAEShutDown]
    return session.map { OSType($0) }.contains(reason)
}

func installTerminationSignalHandler(_ terminate: @escaping () -> Void) -> DispatchSourceSignal {
    signal(SIGTERM, SIG_IGN)
    let source = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .global(qos: .utility))
    source.setEventHandler { performPanelEvent(terminate) }
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

/// The Panel window's origin. The scheme reaches no network stack: every
/// request goes to PanelSchemeHandler (monitor/panel_resource.go names it).
let panelScheme = "oa-panel"
let panelHost = "panel"

/// The Go child prints its start page as oa-panel://panel/<path>?k=<key>.
/// Any other announcement is refused, so the window never loads a network
/// URL the child did not mean.
func localize(_ printed: String) -> URL? {
    guard let url = URL(string: printed), isPanelOrigin(url) else { return nil }
    return url
}

/// True for a URL this launcher's own WKWebView should keep navigating:
/// the Panel's own page. Anything else — a help link's
/// target="_blank" anchor, in practice (monitor/help.go) — opens in the
/// system's default browser instead, and the Panel window stays put.
func isPanelOrigin(_ url: URL) -> Bool {
    url.scheme == panelScheme && url.host == panelHost && url.user == nil && url.port == nil
}

/// The request headers the Panel's routes read; WebKit's others stay here.
let panelRequestHeaders: Set<String> = ["x-panel-key", "content-type", "origin"]

/// A request body larger than this cannot fit one channel line.
let maxPanelRequestBody = 640 * 1024

/// The launcher's own reads (the sidebar's nav.json) are held whole; a larger
/// answer fails rather than growing without bound. WebKit tasks stream their
/// chunks and hold nothing here.
let maxPanelLauncherRead = 4 * 1024 * 1024

/// One Panel request, as the launcher sends it to the Go child.
struct PanelRequest {
    let id: String
    let method: String
    let url: URL
    let headers: [String: String]
    let body: Data
}

/// One Panel request in flight: a WebKit task, or the sidebar's own read.
final class PanelPending {
    let task: WKURLSchemeTask?
    let done: ((Int, Data) -> Void)?
    var status = 0
    var body = Data()
    var nextSeq = 0
    init(task: WKURLSchemeTask?, done: ((Int, Data) -> Void)?) {
        self.task = task
        self.done = done
    }
}

/// Answers the window's oa-panel:// requests through the Go child.
final class PanelSchemeHandler: NSObject, WKURLSchemeHandler {
    weak var owner: AppDelegate?
    func webView(_ webView: WKWebView, start task: WKURLSchemeTask) { owner?.startPanelTask(task) }
    func webView(_ webView: WKWebView, stop task: WKURLSchemeTask) { owner?.stopPanelTask(task) }
}

final class AppDelegate: NSObject, NSApplicationDelegate, WKUIDelegate, WKNavigationDelegate, UNUserNotificationCenterDelegate {
    private var window: NSWindow?
    private var webView: WKWebView?
    private var panel: Process?
    private var panelURL: URL?
    private var nativeInput: FileHandle?
    private var sessionTimer: Timer?
    private var workspaceObservers: [NSObjectProtocol] = []
    private var screenObserver: NSObjectProtocol?
    private var workspaceSessionActive = true
    private var screenAwake = true
    private var sessionUnlocked = false
    // Filled by the platform adapter only when its required evidence is
    // available. Graphics access or an active login alone is insufficient.
    private var sessionEvidence: () -> Bool = nativeSessionUnlocked
    private var questionPanel: NativeQuestionPanel?
    private var activeDialogID: String?
    private var dialogFrames: [NativeAsksFrame] = []
    private var handlingDialog = false
    private var requestedPage = "overview"
    #if PANEL_ASKS_FIXTURE
    private var fixturePresentation: ((NativeAsksFrame, NativeQuestionPanel) -> Void)?
    #endif
    private var terminationSignal: DispatchSourceSignal?
    // An installer's request run opens no window and ends with its child.
    private let applicationRequestRun = nativeApplicationRequestRun(ProcessInfo.processInfo.arguments)
    private lazy var openWhenReady = !applicationRequestRun && !ProcessInfo.processInfo.arguments.contains("--background")
    // Set by launchd's SIGTERM; a quit the person makes keeps the agent.
    var allowTermination = false
    private var terminating = false
    // Application notifications (ASK-N7): their categories and clicks that
    // arrived before the Go child could read them.
    private var appCategories: [String: UNNotificationCategory] = [:]
    private var pendingNativeReplies: [[String: Any]] = []
    private var requestedPath: String?
    private var notificationPermissionUnavailable = false
    // The System Settings-style window (research/panel-redesign/SPEC.md §5).
    private var sidebar: PanelSidebar?
    private var navItems: [PanelNavItem] = []
    private var webObservations: [NSKeyValueObservation] = []
    private var appearanceObservation: NSKeyValueObservation?
    private var colorObserver: NSObjectProtocol?
    private var backItem: NSToolbarItem?
    private var forwardItem: NSToolbarItem?
    private var panelMenu: NSMenu?
    private var viewMenu: NSMenu?
    private let schemeHandler = PanelSchemeHandler()
    private var pendingPanel: [String: PanelPending] = [:]
    private var nextPanelRequest = 0
    /// Delivers one Panel request. The launcher sends it to its Go child; a
    /// window fixture replaces it to answer from a stub.
    var panelTransport: ((PanelRequest) -> Void)?

    func applicationDidFinishLaunching(_ notification: Notification) {
        UNUserNotificationCenter.current().delegate = self
        let center = NSWorkspace.shared.notificationCenter
        workspaceObservers.append(center.addObserver(forName: NSWorkspace.sessionDidResignActiveNotification,
                                                     object: nil, queue: .main) { [weak self] _ in
            self?.workspaceSessionActive = false
            self?.interruptSession()
        })
        workspaceObservers.append(center.addObserver(forName: NSWorkspace.sessionDidBecomeActiveNotification,
                                                     object: nil, queue: .main) { [weak self] _ in
            self?.workspaceSessionActive = true
            self?.refreshSession()
        })
        workspaceObservers.append(center.addObserver(forName: NSWorkspace.screensDidSleepNotification,
                                                     object: nil, queue: .main) { [weak self] _ in
            self?.screenAwake = false
            self?.interruptSession()
        })
        workspaceObservers.append(center.addObserver(forName: NSWorkspace.screensDidWakeNotification,
                                                     object: nil, queue: .main) { [weak self] _ in
            self?.screenAwake = true
            self?.refreshSession()
        })
        // A display added, removed or rearranged moves an open question back
        // onto the pointer's screen.
        screenObserver = NotificationCenter.default.addObserver(forName: NSApplication.didChangeScreenParametersNotification,
                                                                object: nil, queue: .main) { [weak self] _ in
            self?.questionPanel?.place()
        }
        launchPanel()
        sessionTimer = Timer(timeInterval: 0.25, repeats: true) { [weak self] _ in self?.refreshSession() }
        if let sessionTimer {
            RunLoop.main.add(sessionTimer, forMode: .common)
            RunLoop.main.add(sessionTimer, forMode: .modalPanel)
        }
        // Install after Process.run so the child keeps the normal SIGTERM
        // disposition rather than inheriting this dispatch source's SIG_IGN.
        terminationSignal = installTerminationSignalHandler { [weak self] in
            self?.allowTermination = true
            NSApp.terminate(nil)
        }
    }

    // The launcher presents native questions in the background, and its
    // LaunchAgent has no KeepAlive: a quit from the Dock, the Cmd-Tab switcher
    // or the menu closes the window and keeps the agent. It exits for logout,
    // restart and shutdown (the quit event's reason) and for launchd's SIGTERM.
    // Force Quit sends SIGKILL, which no process can intercept.
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if allowTermination || panelQuitEndsLauncher(NSAppleEventManager.shared().currentAppleEvent) {
            return .terminateNow
        }
        window?.close()
        setPanelActivationPolicy(.accessory)
        return .terminateCancel
    }

    func applicationWillTerminate(_ notification: Notification) {
        terminating = true
        sessionTimer?.invalidate()
        for observer in workspaceObservers { NSWorkspace.shared.notificationCenter.removeObserver(observer) }
        workspaceObservers.removeAll()
        if let screenObserver { NotificationCenter.default.removeObserver(screenObserver) }
        dismissNativeDialog()
        stopPanel()
    }

    // Finder and `open` send a reopen event to the running app. Keep the
    // request while its child announces readiness, then show the same window.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        openWhenReady = true
        if panel?.isRunning != true {
            launchPanel()
        } else if panelURL != nil {
            openPanel()
            openWhenReady = false
        } else {
            requestPanel("overview")
        }
        return false
    }

    // MARK: - The Go binary

    /// Starts panel as a child process and reads its announced URL off
    /// stdout. It is a child, not an exec replacement: this process stays
    /// alive to own notifications and the on-demand window.
    private func launchPanel() {
        failPanelRequests()
        dialogFrames.removeAll()
        dismissNativeDialog()
        try? nativeInput?.close()
        nativeInput = nil
        panelURL = nil
        panel = nil
        let process = Process()
        process.executableURL = panelBinary
        process.arguments = nativePanelArguments(ProcessInfo.processInfo.arguments)
        process.environment = ProcessInfo.processInfo.environment

        let stdout = Pipe()
        process.standardOutput = stdout
        process.standardError = FileHandle.standardError
        let stdin = Pipe()
        process.standardInput = stdin

        // Each child owns its parser. Bytes and queued events from a previous
        // child cannot affect the replacement started by an app reopen.
        var lineParser = NativeLineParser()
        stdout.fileHandleForReading.readabilityHandler = { [weak self, weak process] handle in
            let data = handle.availableData
            guard !data.isEmpty, let self, let process else { return }
            lineParser.consume(data, onPanelURL: { printed in
                guard let url = localize(printed) else { return }
                performPanelEvent { self.ready(url, from: process) }
            }, onNative: { frame in
                performPanelEvent { self.receiveNative(frame, from: process) }
            })
        }

        process.terminationHandler = { [weak self] proc in
            performPanelEvent { self?.panelExited(proc.terminationStatus, from: proc) }
        }

        do {
            try process.run()
            panel = process
            nativeInput = stdin.fileHandleForWriting
            sessionUnlocked = currentSessionUnlocked()
            sendNative(["kind": "session", "unlocked": sessionUnlocked])
            let queued = pendingNativeReplies
            pendingNativeReplies.removeAll()
            for reply in queued { sendNative(reply) }
            if openWhenReady { requestPanel("overview") }
        } catch {
            reportPanelFailure("Could not start the Panel: \(error.localizedDescription)")
        }
    }

    private func stopPanel() {
        try? nativeInput?.close()
        nativeInput = nil
        stopOwnedPanel(panel)
    }

    private func ready(_ url: URL, from process: Process) {
        guard panel === process else { return }
        panelURL = url
        if openWhenReady {
            openWhenReady = false
            if let path = requestedPath {
                requestedPath = nil
                openPath(path)
            } else if requestedPage == "questions" { openQuestions() } else { openPanel() }
        }
    }

    /// Opens one Panel page by path, such as Activity at a job a
    /// notification's Open Activity action named.
    private func openPath(_ path: String) {
        guard panelURL != nil else {
            requestedPath = path
            openWhenReady = true
            sendNative(["kind": "open-panel", "page": "activity"])
            return
        }
        loadPage(path)
        showPanelWindow()
        refreshNav()
    }

    private func panelExited(_ status: Int32, from process: Process) {
        guard panel === process else { return }
        if applicationRequestRun {
            // The installer reads the outcome from this run's exit status.
            questionLog("application request run ended (status \(status))")
            exit(status)
        }
        nativeInput = nil
        failPanelRequests()
        dialogFrames.removeAll()
        dismissNativeDialog()
        panelURL = nil
        reportPanelFailure("The Panel stopped (status \(status)). Open Abstraction Panel again to restart it.")
    }

    private func reportPanelFailure(_ message: String) {
        NSLog("Abstraction Panel: %@", message)
        guard !terminating, openWhenReady || window?.isVisible == true else { return }
        openWhenReady = false
        #if PANEL_ASKS_FIXTURE
        if fixtureHidesPresentation { return }
        #endif
        #if PANEL_WINDOW_FIXTURE
        if fixtureHidesWindow { return }
        #endif
        let alert = NSAlert()
        alert.messageText = "Abstraction Panel unavailable"
        alert.informativeText = message
        alert.runModal()
    }

    // MARK: - Native Asks presentation (no policy or operator calls here)

    private func sendNative(_ fields: [String: Any]) {
        guard let nativeInput, panel?.isRunning == true,
              var data = try? JSONSerialization.data(withJSONObject: fields),
              data.count < maxNativeAsksLine else { return }
        data.append(0x0A)
        do { try nativeInput.write(contentsOf: data) }
        catch {
            NSLog("Abstraction Panel: native reply channel failed: %@", error.localizedDescription)
            sessionUnlocked = false
            dismissNativeDialog()
            try? nativeInput.close()
            self.nativeInput = nil
        }
    }

    private func refreshSession() {
        let unlocked = currentSessionUnlocked()
        guard unlocked != sessionUnlocked else { return }
        sessionUnlocked = unlocked
        sendNative(["kind": "session", "unlocked": unlocked])
        if !unlocked { dismissNativeDialog() }
    }

    private func currentSessionUnlocked() -> Bool {
        return nativeInput != nil && panel?.isRunning == true &&
            workspaceSessionActive && screenAwake && sessionEvidence()
    }

    private func interruptSession() {
        sessionUnlocked = false
        sendNative(["kind": "session", "unlocked": false])
        dismissNativeDialog()
    }

    private func requestPanel(_ page: String) {
        requestedPage = page
        openWhenReady = true
        if panelURL != nil {
            openWhenReady = false
            if page == "questions" { openQuestions() } else { openPanel() }
        } else {
            sendNative(["kind": "open-panel", "page": page])
        }
    }

    private func receiveNative(_ frame: NativeAsksFrame, from process: Process) {
        guard panel === process else { return }
        switch frame.kind {
        case "dismiss":
            dialogFrames.removeAll { $0.id == frame.id }
            if activeDialogID == frame.id { dismissNativeDialog() }
        case "open-panel":
            if frame.content == "questions" { requestPanel("questions") }
            if let path = frame.content, path.hasPrefix("/work?") { openPath(path) }
        case "notification":
            guard let id = frame.id, !id.isEmpty, let notification = frame.notification else { return }
            deliverAppNotification(id, notification)
        case "notification-remove":
            guard let id = frame.id, !id.isEmpty else { return }
            removeAppNotification(id)
        case "panel-response":
            guard let id = frame.id, let body = Data(base64Encoded: frame.body ?? "") else { return }
            receivePanelResponse(id, seq: frame.seq ?? 0, status: frame.status ?? 0,
                                 headers: frame.headers ?? [:], body: body, more: frame.more ?? false,
                                 aborted: frame.aborted ?? false)
        case "question", "report":
            guard let id = frame.id, !id.isEmpty else { return }
            guard dialogFrames.count < 8 else {
                sendNative(["kind": "failed", "id": id, "error": "Native dialog queue limit reached"])
                return
            }
            questionLog("\(id) frame received (\(frame.kind))")
            dialogFrames.append(frame)
            refreshNav()
            showNextNativeDialog(from: process)
        default:
            NSLog("Abstraction Panel: unknown native presentation")
        }
    }

    /// Closes the open question without an answer. Its completion runs on the
    /// next turn of the run loop, after the caller (a session change, a
    /// replaced child, a dismiss frame) has finished, as a modal dialog's
    /// code after runModal did.
    private func dismissNativeDialog() {
        questionPanel?.abort()
    }

    /// Shows the next queued question or report on OA's on-top question panel
    /// (NativeQuestionPanel). The panel is not modal: the launcher keeps
    /// reading frames and session changes while it is open, and the answer
    /// arrives in the completion below.
    private func showNextNativeDialog(from process: Process) {
        guard !handlingDialog, !dialogFrames.isEmpty, panel === process else { return }
        let frame = dialogFrames.removeFirst()
        guard let id = frame.id else { return }
        refreshSession()
        guard sessionUnlocked else {
            questionLog("\(id) deferred: session locked or asleep")
            sendNative(["kind": "deferred", "id": id])
            return
        }
        handlingDialog = true
        activeDialogID = id
        let question = NativeQuestionPanel()
        questionPanel = question
        // Every path through the completion ends here, once.
        let finish = {
            self.questionPanel = nil
            self.activeDialogID = nil
            self.handlingDialog = false
            performPanelEvent {
                if let current = self.panel { self.showNextNativeDialog(from: current) }
            }
        }
        question.icon = NSImage(named: NSImage.applicationIconName)
        if frame.kind == "report" {
            question.window.title = frame.title ?? "OpenAbstractions"
            question.messageText = frame.instruction ?? "Question outcome"
            question.informativeText = frame.content ?? ""
            let ok = question.addButton(withTitle: "OK")
            ok.keyEquivalent = "\r"
            question.onArm = { ok.isEnabled = true }
            question.icon = nativeLevelIcon(question.icon, level: frame.level)
            #if PANEL_ASKS_FIXTURE
            fixturePresentation?(frame, question)
            #endif
            presentQuestion(question, id: id) { _ in
                defer { finish() }
                guard self.panel === process else { return }
                self.refreshSession()
                self.sendNative(["kind": self.sessionUnlocked ? "ack" : "deferred", "id": id])
            }
            return
        }
        guard let prompt = frame.prompt, !prompt.choices.isEmpty,
              Set(prompt.choices.map { $0.option }).count == prompt.choices.count,
              prompt.choices.allSatisfy({ !$0.label.isEmpty && !$0.option.isEmpty }) else {
            sendNative(["kind": "failed", "id": id, "error": "The question presentation is invalid"])
            finish()
            return
        }
        question.window.title = prompt.title
        question.messageText = prompt.instruction
        question.informativeText = [prompt.content, prompt.footer].filter { !$0.isEmpty }.joined(separator: "\n\n")
        if let warning = prompt.warning, !warning.isEmpty { question.alertStyle = .warning }
        if prompt.appChoice == true {
            guard prompt.choices.count <= 3 else {
                sendNative(["kind": "failed", "id": id, "error": "The app choice presentation is invalid"])
                finish()
                return
            }
            question.alertStyle = .informational
            let choose = question.addButton(withTitle: "Choose")
            let close = question.addButton(withTitle: "Close")
            close.keyEquivalent = "\u{1b}"
            question.window.defaultButtonCell = close.cell as? NSButtonCell
            let selection = NativeAppChoiceSelection(prompt: prompt, alert: question, choose: choose)
            question.accessoryView = selection.view
            question.retained.append(selection)
            question.icon = nativeLevelIcon(question.icon, level: prompt.level)
            question.onArm = {
                close.isEnabled = true
                selection.arm()
            }
            question.onDisarm = { selection.disarm() }
            #if PANEL_ASKS_FIXTURE
            fixturePresentation?(frame, question)
            #endif
            presentQuestion(question, id: id) { response in
                defer { finish() }
                self.refreshSession()
                guard self.panel === process else { return }
                guard self.sessionUnlocked else {
                    self.sendNative(["kind": "deferred", "id": id])
                    return
                }
                if response == .alertFirstButtonReturn, let option = selection.selectedOption {
                    self.sendNative(["kind": "answer", "id": id, "option": option])
                } else {
                    self.sendNative(["kind": "close", "id": id])
                }
            }
            return
        }
        for choice in prompt.choices {
            question.addButton(withTitle: choice.label)
        }
        let cancel = question.addButton(withTitle: "Cancel")
        cancel.keyEquivalent = "\u{1b}"
        question.window.defaultButtonCell = nil
        let details = NativeQuestionDetails(text: prompt.details, alert: question)
        question.retained.append(details)
        if let content = prompt.permissionContent, !content.isEmpty {
            question.informativeText = [prompt.warning ?? "", prompt.footer].filter { !$0.isEmpty }.joined(separator: "\n\n")
            let permissionBody = NativePermissionContent(text: content, details: details)
            question.retained.append(permissionBody)
            question.accessoryView = permissionBody.view
        } else if !prompt.details.isEmpty {
            question.accessoryView = details.view
        }
        question.onArm = { [weak question] in
            guard let question else { return }
            for button in question.buttons { button.isEnabled = true }
            let defaultIndex = prompt.choices.firstIndex { $0.option == prompt.defaultOption }
            let safe = defaultIndex.map { question.buttons[$0] } ?? cancel
            question.window.defaultButtonCell = safe.cell as? NSButtonCell
        }
        question.onDisarm = { [weak question] in question?.window.defaultButtonCell = nil }
        let permissionPrompt = prompt.permissionPrompt == true
        if permissionPrompt, let icon = nativeRequesterIcon(prompt.iconProgram) {
            question.icon = icon
        }
        question.icon = nativeLevelIcon(question.icon, level: nativePromptLevel(prompt.level, permission: permissionPrompt))
        #if PANEL_ASKS_FIXTURE
        fixturePresentation?(frame, question)
        #endif
        if permissionPrompt { nativePermissionAlertSound() }
        presentQuestion(question, id: id) { response in
            defer { finish() }
            self.refreshSession()
            guard self.panel === process else { return }
            guard self.sessionUnlocked else {
                self.sendNative(["kind": "deferred", "id": id])
                return
            }
            let index = response.rawValue - NSApplication.ModalResponse.alertFirstButtonReturn.rawValue
            if index >= 0 && index < prompt.choices.count {
                self.sendNative(["kind": "answer", "id": id, "option": prompt.choices[index].option])
            } else {
                self.sendNative(["kind": "close", "id": id])
            }
        }
    }

    /// Puts the panel on top of every app, Space and full-screen window on
    /// the pointer's screen without taking keyboard focus, arms it, and logs
    /// where it landed (questionPanelVisibility). completion runs once, with
    /// the clicked button or .abort.
    private func presentQuestion(_ question: NativeQuestionPanel, id: String,
                                 completion: @escaping (NSApplication.ModalResponse) -> Void) {
        let frontmost = NSWorkspace.shared.frontmostApplication
        question.onArmed = { questionLog("\(id) armed") }
        question.onRearm = { questionLog("\(id) moved to another screen: disarmed, arming again") }
        question.present { response in
            switch response {
            case .abort: questionLog("\(id) closed without an answer")
            default: questionLog("\(id) answered with button \(response.rawValue - NSApplication.ModalResponse.alertFirstButtonReturn.rawValue)")
            }
            completion(response)
        }
        // The window server shows the panel on its next frame; read the
        // window list once it has.
        let probe = Timer(timeInterval: 0.3, repeats: false) { [weak question] _ in
            guard let question, question.isPresented else { return }
            let seen = liveQuestionPanelVisibility(question.window)
            let now = NSWorkspace.shared.frontmostApplication
            questionLog("\(id) panel shown: \(seen.summary) level \(question.window.level.rawValue) " +
                "activeSpace \(question.window.isOnActiveSpace) screen \(question.window.screen?.localizedName ?? "none") " +
                "frontmost \(now?.bundleIdentifier ?? "none") focusKept \(now?.processIdentifier == frontmost?.processIdentifier) " +
                "key \(question.window.isKeyWindow) policy \(NSApp.activationPolicy().rawValue)")
        }
        RunLoop.main.add(probe, forMode: .common)
    }

    // MARK: - Application notifications (ASK-N7)

    // Each step of an application notification is logged with the record id
    // (notificationLog), so a run shows where delivery stopped:
    // log show --predicate 'subsystem == "org.openabstractions.panel" AND category == "notifications"'
    private func deliverAppNotification(_ id: String, _ notification: NativeNotification) {
        notificationLog("\(id) frame received (\(notification.actions.count) actions)")
        guard !notificationPermissionUnavailable else {
            notificationLog("\(id) not posted: permission unavailable")
            return
        }
        let center = UNUserNotificationCenter.current()
        center.getNotificationSettings { [weak self] settings in
            guard let self else { return }
            notificationLog("\(id) authorization status \(settings.authorizationStatus.rawValue), alert setting \(settings.alertSetting.rawValue)")
            switch settings.authorizationStatus {
            case .authorized, .provisional, .ephemeral:
                DispatchQueue.main.async { self.postAppNotification(id, notification) }
            case .notDetermined:
                notificationLog("\(id) requesting authorization")
                center.requestAuthorization(options: [.alert, .sound]) { granted, error in
                    notificationLog("\(id) authorization granted=\(granted) error=\(notificationErrorText(error))")
                    DispatchQueue.main.async {
                        if granted { self.postAppNotification(id, notification) }
                        else { self.notificationPermissionUnavailable = true }
                    }
                }
            default:
                DispatchQueue.main.async {
                    self.notificationPermissionUnavailable = true
                    notificationLog("\(id) not posted: status \(settings.authorizationStatus.rawValue)")
                }
            }
        }
    }

    private func postAppNotification(_ id: String, _ notification: NativeNotification) {
        guard appCategories[id] != nil || appCategories.count < maxAppNotifications else {
            notificationLog("\(id) not posted: notification limit reached")
            return
        }
        appCategories[id] = appNotificationCategory(id, notification)
        let center = UNUserNotificationCenter.current()
        center.setNotificationCategories(Set(appCategories.values))
        // Reading the categories back orders the post after their registration.
        center.getNotificationCategories { categories in
            notificationLog("\(id) categories registered (\(categories.count))")
            center.add(UNNotificationRequest(identifier: id, content: appNotificationContent(id, notification), trigger: nil)) { error in
                notificationLog("\(id) add result: \(notificationErrorText(error))")
            }
        }
    }

    private func removeAppNotification(_ id: String) {
        let center = UNUserNotificationCenter.current()
        center.removeDeliveredNotifications(withIdentifiers: [id])
        center.removePendingNotificationRequests(withIdentifiers: [id])
        if appCategories.removeValue(forKey: id) != nil {
            center.setNotificationCategories(Set(appCategories.values))
        }
        notificationLog("\(id) removed")
    }

    /// A reply for the Go child, held until it runs when the system launched
    /// this app to deliver a click.
    private func sendNativeOrQueue(_ reply: [String: Any]) {
        if nativeInput != nil, panel?.isRunning == true {
            sendNative(reply)
        } else if pendingNativeReplies.count < 32 {
            pendingNativeReplies.append(reply)
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        let content = response.notification.request.content
        let reply = appNotificationReply(category: content.categoryIdentifier, action: response.actionIdentifier, userInfo: content.userInfo)
        notificationLog("\(response.notification.request.identifier) response received (\(notificationResponseKind(response.actionIdentifier)))")
        DispatchQueue.main.async {
            if let reply { self.sendNativeOrQueue(reply) }
            completionHandler()
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        notificationLog("\(notification.request.identifier) presenting while running")
        completionHandler([.banner, .list, .sound])
    }

    // MARK: - The window

    // System Settings layout (research/panel-redesign/SPEC.md §5, macOS): a
    // native source-list sidebar fed by /panel/nav.json, the page in a
    // WKWebView beside it under a unified toolbar that carries Back/Forward
    // and the page's own document.title. Pages load with shell=native, so the
    // page draws neither its HTML navigation nor its h1.
    private func ensureWindow() -> WKWebView {
        if let webView { return webView }
        let configuration = WKWebViewConfiguration()
        schemeHandler.owner = self
        configuration.setURLSchemeHandler(schemeHandler, forURLScheme: panelScheme)
        configuration.userContentController.addUserScript(accentUserScript())
        let view = WKWebView(frame: NSRect(x: 0, y: 0, width: 745, height: 680), configuration: configuration)
        view.uiDelegate = self
        view.navigationDelegate = self

        let sidebar = PanelSidebar()
        sidebar.onSelect = { [weak self] item in self?.showSidebarPage(item) }
        let split = NSSplitViewController()
        let sidebarItem = NSSplitViewItem(sidebarWithViewController: sidebar)
        sidebarItem.canCollapse = false
        sidebarItem.minimumThickness = 215
        sidebarItem.maximumThickness = 320
        split.addSplitViewItem(sidebarItem)
        split.addSplitViewItem(NSSplitViewItem(viewController: PanelDetail(webView: view)))

        let win = NSWindow(
            contentRect: NSRect(origin: .zero, size: panelDefaultContentSize),
            styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
            backing: .buffered,
            defer: false)
        win.contentViewController = split
        win.setContentSize(panelDefaultContentSize)
        win.contentMinSize = panelMinimumContentSize
        win.title = "Abstraction Panel"
        let toolbar = NSToolbar(identifier: "org.openabstractions.panel.toolbar")
        toolbar.delegate = self
        toolbar.displayMode = .iconOnly
        toolbar.allowsUserCustomization = false
        win.toolbar = toolbar
        win.toolbarStyle = .unified
        win.titleVisibility = .visible
        win.delegate = self
        win.tabbingMode = .disallowed
        // Arrow keys move through the pages, as in System Settings.
        win.initialFirstResponder = sidebar.outline
        win.center()
        win.isReleasedWhenClosed = false

        webObservations = [
            view.observe(\.title, options: [.new]) { [weak self] web, _ in self?.showTitle(web.title) },
            view.observe(\.url, options: [.new]) { [weak self] _, _ in self?.syncSidebarSelection() },
            view.observe(\.canGoBack, options: [.initial, .new]) { [weak self] web, _ in self?.backItem?.isEnabled = web.canGoBack },
            view.observe(\.canGoForward, options: [.initial, .new]) { [weak self] web, _ in self?.forwardItem?.isEnabled = web.canGoForward },
        ]
        colorObserver = NotificationCenter.default.addObserver(forName: NSColor.systemColorsDidChangeNotification,
                                                               object: nil, queue: .main) { [weak self] _ in
            self?.injectAccent()
        }
        appearanceObservation = NSApp.observe(\.effectiveAppearance, options: [.new]) { [weak self] _, _ in
            DispatchQueue.main.async { self?.injectAccent() }
        }

        webView = view
        window = win
        self.sidebar = sidebar
        return view
    }

    /// Loads one Panel path (a nav item's `path`) with the key and shell=native.
    private func loadPage(_ path: String) {
        guard let panelURL, let url = panelPageURL(panelURL, path: path) else { return }
        ensureWindow().load(URLRequest(url: url))
    }

    /// A sidebar choice opens its page, including from a page below it (an
    /// app page under Apps); the page already on screen stays as it is.
    private func showSidebarPage(_ item: PanelNavItem) {
        guard let panelURL, let target = panelPageURL(panelURL, path: item.path) else { return }
        if let here = webView?.url, here.path == target.path, here.fragment == target.fragment { return }
        loadPage(item.path)
    }

    // While the window is open the launcher is an ordinary app: Dock icon,
    // menu bar and key equivalents. It returns to a background agent when the
    // window closes, since native questions keep running without it.
    private func showPanelWindow() {
        #if PANEL_ASKS_FIXTURE
        if fixtureHidesPresentation {
            // The window loads its page but never reaches the screen, the
            // Dock or the active app.
            window?.orderOut(nil)
            return
        }
        #endif
        setPanelActivationPolicy(.regular)
        if panelMenu == nil { panelMenu = makePanelMenu() }
        if NSApp.mainMenu !== panelMenu { NSApp.mainMenu = panelMenu }
        #if PANEL_WINDOW_FIXTURE
        if fixtureHidesWindow {
            // On screen for AppKit and WebKit, invisible to the person, and
            // the app in front keeps focus.
            window?.alphaValue = 0
            window?.hasShadow = false
            window?.ignoresMouseEvents = true
            window?.orderFrontRegardless()
            return
        }
        #endif
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc private func openPanel() {
        guard let panelURL else { return }
        loadPage(panelURL.path.isEmpty ? "/" : panelURL.path)
        showPanelWindow()
        refreshNav()
    }

    /// Waiting questions live on the Permissions page.
    @objc private func openQuestions() {
        guard panelURL != nil else { return }
        loadPage("/rights")
        showPanelWindow()
        refreshNav()
    }

    private func showTitle(_ title: String?) {
        let text = title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        window?.title = text.isEmpty ? "Abstraction Panel" : text
    }

    // MARK: - Accent colour

    private func accentUserScript() -> WKUserScript {
        WKUserScript(source: panelAccentScript(panelAccentColors(NSApp.effectiveAppearance)),
                     injectionTime: .atDocumentStart, forMainFrameOnly: true)
    }

    /// Re-injects --oa-accent after the person changes the accent colour or
    /// the appearance, into future documents and the one on screen.
    private func injectAccent() {
        guard let webView else { return }
        let controller = webView.configuration.userContentController
        controller.removeAllUserScripts()
        controller.addUserScript(accentUserScript())
        webView.evaluateJavaScript(panelAccentScript(panelAccentColors(NSApp.effectiveAppearance)), completionHandler: nil)
    }

    // MARK: - Sidebar data

    /// Reads the sidebar from the shell's own navigation table. A failed read
    /// keeps the rows already shown.
    private func refreshNav() {
        guard webView != nil, let base = panelURL, let url = panelNavURL(base) else { return }
        sendPanelRequest(method: "GET", url: url, headers: [:], body: Data(),
                         PanelPending(task: nil) { [weak self] status, data in
            guard let self, self.panelURL == base, status == 200,
                  let nav = try? JSONDecoder().decode(PanelNav.self, from: data) else { return }
            self.applyNav(nav)
        })
    }

    // MARK: - Panel requests over the child's pipes

    func startPanelTask(_ task: WKURLSchemeTask) {
        let request = task.request
        guard let url = request.url, isPanelOrigin(url) else {
            task.didFailWithError(URLError(.unsupportedURL))
            return
        }
        var body = request.httpBody ?? Data()
        if body.isEmpty, let stream = request.httpBodyStream {
            stream.open()
            var chunk = [UInt8](repeating: 0, count: 65536)
            while body.count <= maxPanelRequestBody {
                let read = stream.read(&chunk, maxLength: chunk.count)
                if read <= 0 { break }
                body.append(chunk, count: read)
            }
            stream.close()
        }
        guard body.count <= maxPanelRequestBody else {
            task.didFailWithError(URLError(.dataLengthExceedsMaximum))
            return
        }
        let headers = (request.allHTTPHeaderFields ?? [:]).filter { panelRequestHeaders.contains($0.key.lowercased()) }
        sendPanelRequest(method: request.httpMethod ?? "GET", url: url, headers: headers, body: body,
                         PanelPending(task: task, done: nil))
    }

    func stopPanelTask(_ task: WKURLSchemeTask) {
        guard let id = pendingPanel.first(where: { $0.value.task === task })?.key else { return }
        pendingPanel[id] = nil
        if panelTransport == nil { sendNative(["kind": "panel-cancel", "id": id]) }
    }

    private func sendPanelRequest(method: String, url: URL, headers: [String: String], body: Data,
                                  _ pending: PanelPending) {
        nextPanelRequest += 1
        let id = String(nextPanelRequest)
        pendingPanel[id] = pending
        if let panelTransport {
            panelTransport(PanelRequest(id: id, method: method, url: url, headers: headers, body: body))
            return
        }
        guard nativeInput != nil, panel?.isRunning == true else {
            failPanelRequest(id)
            return
        }
        sendNative(["kind": "panel-request", "id": id, "method": method, "url": url.absoluteString,
                    "headers": headers, "body": body.base64EncodedString()])
        if nativeInput == nil { failPanelRequest(id) }
    }

    /// One chunk of a Panel response. The first carries status and headers;
    /// the last finishes the request. Chunks arrive in order with chunk 0
    /// first; anything else, or an aborted request, fails the request.
    func receivePanelResponse(_ id: String, seq: Int, status: Int, headers: [String: String], body: Data, more: Bool,
                              aborted: Bool = false) {
        guard let pending = pendingPanel[id] else { return }
        guard seq == pending.nextSeq, !aborted else {
            failPanelRequest(id)
            return
        }
        pending.nextSeq += 1
        if seq == 0 {
            pending.status = status
            if let task = pending.task, let url = task.request.url,
               let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers) {
                task.didReceive(response)
            }
        }
        if let task = pending.task {
            if !body.isEmpty { task.didReceive(body) }
        } else {
            guard pending.body.count + body.count <= maxPanelLauncherRead else {
                failPanelRequest(id)
                return
            }
            pending.body.append(body)
        }
        guard !more else { return }
        pendingPanel[id] = nil
        pending.task?.didFinish()
        pending.done?(pending.status, pending.body)
    }

    private func failPanelRequest(_ id: String) {
        guard let pending = pendingPanel.removeValue(forKey: id) else { return }
        pending.task?.didFailWithError(URLError(.networkConnectionLost))
        pending.done?(0, Data())
    }

    /// The child is gone or replaced: no answer will come.
    private func failPanelRequests() {
        for id in Array(pendingPanel.keys) { failPanelRequest(id) }
    }

    private func applyNav(_ nav: PanelNav) {
        let changed = nav.items != navItems
        navItems = nav.items
        sidebar?.update(nav.items)
        let current = webView?.url.flatMap { panelNavSelection(nav.items, url: $0) }
        sidebar?.select(current ?? nav.selected)
        if changed { rebuildPageMenu() }
    }

    private func syncSidebarSelection() {
        guard let url = webView?.url, let id = panelNavSelection(navItems, url: url) else { return }
        sidebar?.select(id)
    }

    // MARK: - Menus

    private func makePanelMenu() -> NSMenu {
        let main = NSMenu()
        let name = "Abstraction Panel"
        func submenu(_ title: String, _ items: [NSMenuItem]) -> NSMenu {
            let menu = NSMenu(title: title)
            for item in items { menu.addItem(item) }
            let holder = NSMenuItem(title: title, action: nil, keyEquivalent: "")
            holder.submenu = menu
            main.addItem(holder)
            return menu
        }
        func item(_ title: String, _ action: Selector?, _ key: String = "",
                  _ modifiers: NSEvent.ModifierFlags = .command, target: AnyObject? = nil) -> NSMenuItem {
            let entry = NSMenuItem(title: title, action: action, keyEquivalent: key)
            entry.keyEquivalentModifierMask = modifiers
            entry.target = target
            return entry
        }
        _ = submenu(name, [
            item("About \(name)", #selector(NSApplication.orderFrontStandardAboutPanel(_:))),
            .separator(),
            item("Settings…", #selector(showSettingsPage(_:)), ",", target: self),
            .separator(),
            item("Hide \(name)", #selector(NSApplication.hide(_:)), "h"),
            item("Hide Others", #selector(NSApplication.hideOtherApplications(_:)), "h", [.command, .option]),
            item("Show All", #selector(NSApplication.unhideAllApplications(_:))),
            .separator(),
            // applicationShouldTerminate keeps the background agent running.
            item("Quit \(name)", #selector(NSApplication.terminate(_:)), "q"),
        ])
        _ = submenu("Edit", [
            item("Undo", Selector(("undo:")), "z"),
            item("Redo", Selector(("redo:")), "z", [.command, .shift]),
            .separator(),
            item("Cut", #selector(NSText.cut(_:)), "x"),
            item("Copy", #selector(NSText.copy(_:)), "c"),
            item("Paste", #selector(NSText.paste(_:)), "v"),
            item("Select All", #selector(NSText.selectAll(_:)), "a"),
            .separator(),
            item("Find…", #selector(focusSidebarSearch(_:)), "f", target: self),
        ])
        viewMenu = submenu("View", [
            .separator(),
            item("Back", #selector(panelGoBack(_:)), "[", target: self),
            item("Forward", #selector(panelGoForward(_:)), "]", target: self),
            item("Reload Page", #selector(panelReload(_:)), "r", target: self),
        ])
        let windowMenu = submenu("Window", [
            item("Close", #selector(NSWindow.performClose(_:)), "w"),
            item("Minimize", #selector(NSWindow.performMiniaturize(_:)), "m"),
            item("Zoom", #selector(NSWindow.performZoom(_:))),
            .separator(),
            item("Bring All to Front", #selector(NSApplication.arrangeInFront(_:))),
        ])
        NSApp.windowsMenu = windowMenu
        panelMenu = main
        rebuildPageMenu()
        return main
    }

    /// The View menu lists the sidebar's pages first, ⌘1 to ⌘9 in order.
    private func rebuildPageMenu() {
        guard let viewMenu else { return }
        while let first = viewMenu.items.first, first.tag == panelPageMenuTag {
            viewMenu.removeItem(first)
        }
        for (index, nav) in navItems.enumerated().reversed() {
            let key = index < 9 ? String(index + 1) : ""
            let entry = NSMenuItem(title: nav.label, action: #selector(selectPageFromMenu(_:)), keyEquivalent: key)
            entry.keyEquivalentModifierMask = .command
            entry.target = self
            entry.tag = panelPageMenuTag
            entry.representedObject = nav.path
            viewMenu.insertItem(entry, at: 0)
        }
    }

    @objc private func selectPageFromMenu(_ sender: NSMenuItem) {
        guard let path = sender.representedObject as? String else { return }
        loadPage(path)
    }

    @objc private func showSettingsPage(_ sender: Any?) {
        guard let settings = navItems.first(where: { $0.id == "settings" }) else { return }
        loadPage(settings.path)
        showPanelWindow()
    }

    @objc private func panelGoBack(_ sender: Any?) { webView?.goBack() }
    @objc private func panelGoForward(_ sender: Any?) { webView?.goForward() }
    @objc private func panelReload(_ sender: Any?) { webView?.reload() }
    @objc private func focusSidebarSearch(_ sender: Any?) {
        guard let search = sidebar?.search else { return }
        window?.makeFirstResponder(search)
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
        // A page's own link or script navigation keeps the native shell.
        if let url = navigationAction.request.url, navigationAction.targetFrame?.isMainFrame == true,
           (navigationAction.request.httpMethod ?? "GET") == "GET",
           navigationAction.navigationType != .backForward,
           let native = panelNativeShellURL(url) {
            decisionHandler(.cancel)
            webView.load(URLRequest(url: native))
            return
        }
        decisionHandler(.allow)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        syncSidebarSelection()
        refreshNav()
    }
}

extension AppDelegate: NSToolbarDelegate, NSWindowDelegate, NSMenuItemValidation {
    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.sidebarTrackingSeparator, panelBackItemIdentifier, panelForwardItemIdentifier]
    }

    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        toolbarDefaultItemIdentifiers(toolbar)
    }

    func toolbar(_ toolbar: NSToolbar, itemForItemIdentifier itemIdentifier: NSToolbarItem.Identifier,
                 willBeInsertedIntoToolbar flag: Bool) -> NSToolbarItem? {
        let back = itemIdentifier == panelBackItemIdentifier
        guard back || itemIdentifier == panelForwardItemIdentifier else { return nil }
        let item = NSToolbarItem(itemIdentifier: itemIdentifier)
        item.label = back ? "Back" : "Forward"
        item.toolTip = back ? "Show the previous page" : "Show the next page"
        item.image = NSImage(systemSymbolName: back ? "chevron.left" : "chevron.right",
                             accessibilityDescription: item.label)
        item.isBordered = true
        item.isNavigational = true
        item.autovalidates = false
        item.target = self
        item.action = back ? #selector(panelGoBack(_:)) : #selector(panelGoForward(_:))
        item.isEnabled = back ? webView?.canGoBack == true : webView?.canGoForward == true
        if back { backItem = item } else { forwardItem = item }
        return item
    }

    func windowWillClose(_ notification: Notification) {
        guard (notification.object as? NSWindow) === window else { return }
        setPanelActivationPolicy(.accessory)
    }

    func windowDidBecomeKey(_ notification: Notification) {
        guard (notification.object as? NSWindow) === window else { return }
        refreshNav()
    }

    func validateMenuItem(_ menuItem: NSMenuItem) -> Bool {
        switch menuItem.action {
        case #selector(panelGoBack(_:)): return webView?.canGoBack == true
        case #selector(panelGoForward(_:)): return webView?.canGoForward == true
        case #selector(showSettingsPage(_:)): return navItems.contains { $0.id == "settings" }
        default: return true
        }
    }
}

// MARK: - Shell contract (research/panel-redesign/SPEC.md §6)

let panelDefaultContentSize = NSSize(width: 960, height: 680)
let panelMinimumContentSize = NSSize(width: 820, height: 560)
let panelPageMenuTag = 7100
let panelBackItemIdentifier = NSToolbarItem.Identifier("org.openabstractions.panel.back")
let panelForwardItemIdentifier = NSToolbarItem.Identifier("org.openabstractions.panel.forward")

struct PanelNavGlyph: Decodable, Equatable {
    let segoe: String?
    let sf: String?
}

/// One sidebar row of `GET /panel/nav.json`. `group` is "main" or "more";
/// a change of group draws the separator. `badge` counts waiting questions.
struct PanelNavItem: Decodable, Equatable {
    let id: String
    let label: String
    let path: String
    let group: String
    let glyph: PanelNavGlyph?
    let badge: Int?
}

struct PanelNav: Decodable, Equatable {
    let items: [PanelNavItem]
    let selected: String?
}

private func panelKeyItems(_ base: URL) -> [URLQueryItem] {
    let key = URLComponents(url: base, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "k" }?.value
    return key.map { [URLQueryItem(name: "k", value: $0)] } ?? []
}

/// `/panel/nav.json?k=<key>` on the announced Panel origin.
func panelNavURL(_ base: URL) -> URL? {
    guard var parts = URLComponents(url: base, resolvingAgainstBaseURL: false) else { return nil }
    parts.path = "/panel/nav.json"
    parts.queryItems = panelKeyItems(base)
    parts.fragment = nil
    return parts.url
}

/// A same-origin Panel path with the run's key and shell=native. A path that
/// names a scheme or host is refused, so the sidebar cannot leave the Panel.
func panelPageURL(_ base: URL, path: String) -> URL? {
    guard var parts = URLComponents(url: base, resolvingAgainstBaseURL: false),
          let target = URLComponents(string: path),
          target.scheme == nil, target.host == nil, target.path.hasPrefix("/") else { return nil }
    parts.path = target.path
    var query = (target.queryItems ?? []).filter { $0.name != "k" && $0.name != "shell" }
    query += panelKeyItems(base)
    query.append(URLQueryItem(name: "shell", value: "native"))
    parts.queryItems = query
    parts.fragment = target.fragment
    return parts.url
}

/// The same Panel URL with shell=native added, or nil when it already has it.
func panelNativeShellURL(_ url: URL) -> URL? {
    guard isPanelOrigin(url), var parts = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
    var query = parts.queryItems ?? []
    guard !query.contains(where: { $0.name == "shell" && $0.value == "native" }) else { return nil }
    query.removeAll { $0.name == "shell" }
    query.append(URLQueryItem(name: "shell", value: "native"))
    parts.queryItems = query
    return parts.url
}

/// The sidebar row for the page on screen: the item whose path and fragment
/// match, else the item with that path, else the deepest item whose path
/// contains this page (an app page under Apps).
func panelNavSelection(_ items: [PanelNavItem], url: URL) -> String? {
    guard let here = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
    var samePath: String?
    var parent: (id: String, length: Int)?
    for item in items {
        guard let target = URLComponents(string: item.path) else { continue }
        if target.path == here.path {
            if let fragment = target.fragment {
                if fragment == here.fragment { return item.id }
            } else if samePath == nil {
                samePath = item.id
            }
        } else if target.path != "/", here.path.hasPrefix(target.path + "/"),
                  target.path.count > (parent?.length ?? 0) {
            parent = (item.id, target.path.count)
        }
    }
    return samePath ?? parent?.id
}

/// The accent colour as the hex value CSS reads, resolved for an appearance.
func panelAccentHex(_ appearance: NSAppearance) -> String {
    var hex = "#007AFF"
    appearance.performAsCurrentDrawingAppearance {
        if let color = NSColor.controlAccentColor.usingColorSpace(.sRGB) {
            func byte(_ value: CGFloat) -> Int { Int((min(max(value, 0), 1) * 255).rounded()) }
            hex = String(format: "#%02X%02X%02X", byte(color.redComponent), byte(color.greenComponent), byte(color.blueComponent))
        }
    }
    return hex
}

/// The three accent variables the Panel root carries (SPEC §6): the accent
/// for the current appearance, Light2 (the accent fill in dark mode) and
/// Dark1 (the accent fill in light mode). macOS resolves controlAccentColor
/// per appearance, so each is the system's own value.
struct PanelAccent: Equatable {
    let accent: String
    let light2: String
    let dark1: String
}

func panelAccentColors(_ appearance: NSAppearance) -> PanelAccent {
    PanelAccent(accent: panelAccentHex(appearance),
                light2: panelAccentHex(NSAppearance(named: .darkAqua) ?? appearance),
                dark1: panelAccentHex(NSAppearance(named: .aqua) ?? appearance))
}

/// Sets the root's accent variables. It runs at document start, before the
/// page defines oaSetAccent, and again on a change.
func panelAccentScript(_ colors: PanelAccent) -> String {
    "(function (root) { if (!root) return;" +
        " root.style.setProperty('--oa-accent', '\(colors.accent)');" +
        " root.style.setProperty('--oa-accent-light2', '\(colors.light2)');" +
        " root.style.setProperty('--oa-accent-dark1', '\(colors.dark1)'); })(document.documentElement);"
}

// Tile colours follow the sidebar order's existing Panel palette
// (static/panel.css .oa-icon-*); an unknown page takes system grey.
let panelTileColors: [String: NSColor] = [
    "setup": NSColor(srgbRed: 0.61, green: 0.35, blue: 0.77, alpha: 1),
    "status": NSColor(srgbRed: 0.20, green: 0.48, blue: 0.83, alpha: 1),
    "apps": NSColor(srgbRed: 0.20, green: 0.48, blue: 0.83, alpha: 1),
    "inference": NSColor(srgbRed: 0.46, green: 0.40, blue: 0.80, alpha: 1),
    "models": NSColor(srgbRed: 0.46, green: 0.40, blue: 0.80, alpha: 1),
    "rights": NSColor(srgbRed: 0.15, green: 0.54, blue: 0.46, alpha: 1),
    "permissions": NSColor(srgbRed: 0.15, green: 0.54, blue: 0.46, alpha: 1),
    "work": NSColor(srgbRed: 0.74, green: 0.42, blue: 0.19, alpha: 1),
    "activity": NSColor(srgbRed: 0.74, green: 0.42, blue: 0.19, alpha: 1),
    "card": NSColor(srgbRed: 0.33, green: 0.46, blue: 0.55, alpha: 1),
    "device": NSColor(srgbRed: 0.33, green: 0.46, blue: 0.55, alpha: 1),
    "registry": NSColor(srgbRed: 0.33, green: 0.51, blue: 0.29, alpha: 1),
    "providers": NSColor(srgbRed: 0.33, green: 0.51, blue: 0.29, alpha: 1),
    "credentials": NSColor(srgbRed: 0.64, green: 0.48, blue: 0.14, alpha: 1),
    "settings": NSColor(srgbRed: 0.42, green: 0.47, blue: 0.57, alpha: 1),
]

/// A 20 px rounded tile in the page's colour with its white SF Symbol, the
/// System Settings sidebar icon.
func panelNavTile(_ item: PanelNavItem) -> NSImage {
    let color = panelTileColors[item.id] ?? .systemGray
    let image = NSImage(size: NSSize(width: 20, height: 20), flipped: false) { rect in
        let path = NSBezierPath(roundedRect: rect, xRadius: 5, yRadius: 5)
        let top = color.blended(withFraction: 0.18, of: .white) ?? color
        NSGradient(starting: top, ending: color)?.draw(in: path, angle: -90)
        let configuration = NSImage.SymbolConfiguration(pointSize: 11, weight: .semibold)
            .applying(NSImage.SymbolConfiguration(paletteColors: [.white]))
        let symbol = NSImage(systemSymbolName: item.glyph?.sf ?? "", accessibilityDescription: nil)
            ?? NSImage(systemSymbolName: "circle.fill", accessibilityDescription: nil)
        if let glyph = symbol?.withSymbolConfiguration(configuration) {
            let size = glyph.size
            glyph.draw(in: NSRect(x: (rect.width - size.width) / 2, y: (rect.height - size.height) / 2,
                                  width: size.width, height: size.height))
        }
        return true
    }
    image.accessibilityDescription = item.label
    return image
}

/// The sidebar: a search field over a source list of the Panel's pages.
final class PanelSidebar: NSViewController, NSOutlineViewDataSource, NSOutlineViewDelegate, NSSearchFieldDelegate {
    /// An outline row: a page, or the gap between the main and "more" groups.
    final class Row: NSObject {
        let item: PanelNavItem?
        init(_ item: PanelNavItem?) { self.item = item }
    }

    let outline = NSOutlineView()
    let search = NSSearchField()
    private(set) var items: [PanelNavItem] = []
    private(set) var rows: [Row] = []
    private(set) var selectedID: String?
    var onSelect: ((PanelNavItem) -> Void)?
    private var syncing = false

    override func loadView() {
        let root = NSView(frame: NSRect(x: 0, y: 0, width: 215, height: panelDefaultContentSize.height))
        search.placeholderString = "Search"
        search.sendsSearchStringImmediately = true
        search.delegate = self
        search.target = self
        search.action = #selector(searchChanged(_:))
        search.translatesAutoresizingMaskIntoConstraints = false

        let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("page"))
        column.resizingMask = .autoresizingMask
        outline.addTableColumn(column)
        outline.outlineTableColumn = column
        outline.columnAutoresizingStyle = .lastColumnOnlyAutoresizingStyle
        outline.headerView = nil
        outline.style = .sourceList
        outline.rowSizeStyle = .default
        outline.floatsGroupRows = false
        outline.indentationPerLevel = 0
        outline.dataSource = self
        outline.delegate = self
        outline.setAccessibilityLabel("Panel pages")

        let scroll = NSScrollView()
        scroll.documentView = outline
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.translatesAutoresizingMaskIntoConstraints = false
        root.addSubview(search)
        root.addSubview(scroll)
        NSLayoutConstraint.activate([
            search.topAnchor.constraint(equalTo: root.safeAreaLayoutGuide.topAnchor, constant: 4),
            search.leadingAnchor.constraint(equalTo: root.leadingAnchor, constant: 10),
            search.trailingAnchor.constraint(equalTo: root.trailingAnchor, constant: -10),
            scroll.topAnchor.constraint(equalTo: search.bottomAnchor, constant: 8),
            scroll.leadingAnchor.constraint(equalTo: root.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: root.trailingAnchor),
            scroll.bottomAnchor.constraint(equalTo: root.bottomAnchor),
        ])
        view = root
    }

    func update(_ items: [PanelNavItem]) {
        self.items = items
        reload()
    }

    func select(_ id: String?) {
        selectedID = id
        syncing = true
        applySelection()
        syncing = false
    }

    /// Search filters page names; an empty search shows both groups.
    private func reload() {
        _ = view
        let query = search.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        var result: [Row] = []
        var group: String?
        for item in items {
            if !query.isEmpty {
                if item.label.localizedCaseInsensitiveContains(query) { result.append(Row(item)) }
                continue
            }
            if let group, group != item.group { result.append(Row(nil)) }
            group = item.group
            result.append(Row(item))
        }
        rows = result
        syncing = true
        outline.reloadData()
        applySelection()
        syncing = false
    }

    private func applySelection() {
        if let selectedID, let index = rows.firstIndex(where: { $0.item?.id == selectedID }) {
            outline.selectRowIndexes(IndexSet(integer: index), byExtendingSelection: false)
            outline.scrollRowToVisible(index)
        } else {
            outline.deselectAll(nil)
        }
    }

    @objc private func searchChanged(_ sender: NSSearchField) { reload() }

    func controlTextDidChange(_ notification: Notification) { reload() }

    /// Return in the search field opens the first match.
    func control(_ control: NSControl, textView: NSTextView, doCommandBy commandSelector: Selector) -> Bool {
        guard commandSelector == #selector(NSResponder.insertNewline(_:)),
              let first = rows.first(where: { $0.item != nil })?.item else { return false }
        selectedID = first.id
        select(first.id)
        onSelect?(first)
        return true
    }

    func outlineView(_ outlineView: NSOutlineView, numberOfChildrenOfItem item: Any?) -> Int {
        item == nil ? rows.count : 0
    }

    func outlineView(_ outlineView: NSOutlineView, child index: Int, ofItem item: Any?) -> Any { rows[index] }

    func outlineView(_ outlineView: NSOutlineView, isItemExpandable item: Any) -> Bool { false }

    func outlineView(_ outlineView: NSOutlineView, shouldSelectItem item: Any) -> Bool {
        (item as? Row)?.item != nil
    }

    func outlineView(_ outlineView: NSOutlineView, heightOfRowByItem item: Any) -> CGFloat {
        (item as? Row)?.item == nil ? 10 : outlineView.rowHeight
    }

    func outlineView(_ outlineView: NSOutlineView, viewFor tableColumn: NSTableColumn?, item: Any) -> NSView? {
        guard let row = item as? Row else { return nil }
        guard let nav = row.item else { return NSView() }
        let cell = outlineView.makeView(withIdentifier: PanelNavCell.identifier, owner: self) as? PanelNavCell ?? PanelNavCell()
        cell.configure(nav)
        return cell
    }

    func outlineViewSelectionDidChange(_ notification: Notification) {
        guard !syncing else { return }
        let index = outline.selectedRow
        guard index >= 0, index < rows.count, let item = rows[index].item else { return }
        selectedID = item.id
        onSelect?(item)
    }
}

/// The coloured tile keeps its colours on the sidebar material; vibrancy
/// would blend it with the background.
final class PanelTileView: NSImageView {
    override var allowsVibrancy: Bool { false }
}

/// A sidebar row: tile, page name and, for waiting questions, a count badge.
final class PanelNavCell: NSTableCellView {
    static let identifier = NSUserInterfaceItemIdentifier("panel-nav")
    let tile = PanelTileView()
    let label = NSTextField(labelWithString: "")
    let badge = NSTextField(labelWithString: "")
    private let badgeBackground = NSView()

    init() {
        super.init(frame: NSRect(x: 0, y: 0, width: 200, height: 28))
        identifier = Self.identifier
        tile.imageScaling = .scaleNone
        label.lineBreakMode = .byTruncatingTail
        label.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        badge.font = .systemFont(ofSize: 11, weight: .semibold)
        badge.textColor = .white
        badge.alignment = .center
        badgeBackground.wantsLayer = true
        badgeBackground.layer?.cornerRadius = 8
        badgeBackground.layer?.backgroundColor = NSColor.systemRed.cgColor
        for view in [tile, label, badgeBackground, badge] { view.translatesAutoresizingMaskIntoConstraints = false }
        addSubview(tile)
        addSubview(label)
        addSubview(badgeBackground)
        badgeBackground.addSubview(badge)
        imageView = tile
        textField = label
        NSLayoutConstraint.activate([
            tile.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 2),
            tile.centerYAnchor.constraint(equalTo: centerYAnchor),
            tile.widthAnchor.constraint(equalToConstant: 20),
            tile.heightAnchor.constraint(equalToConstant: 20),
            label.leadingAnchor.constraint(equalTo: tile.trailingAnchor, constant: 7),
            label.centerYAnchor.constraint(equalTo: centerYAnchor),
            label.trailingAnchor.constraint(lessThanOrEqualTo: badgeBackground.leadingAnchor, constant: -6),
            badgeBackground.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -6),
            badgeBackground.centerYAnchor.constraint(equalTo: centerYAnchor),
            badgeBackground.heightAnchor.constraint(equalToConstant: 16),
            badgeBackground.widthAnchor.constraint(greaterThanOrEqualToConstant: 16),
            badge.leadingAnchor.constraint(equalTo: badgeBackground.leadingAnchor, constant: 5),
            badge.trailingAnchor.constraint(equalTo: badgeBackground.trailingAnchor, constant: -5),
            badge.centerYAnchor.constraint(equalTo: badgeBackground.centerYAnchor),
        ])
    }

    required init?(coder: NSCoder) { nil }

    func configure(_ item: PanelNavItem) {
        tile.image = panelNavTile(item)
        label.stringValue = item.label
        if let count = item.badge, count > 0 {
            badge.stringValue = String(count)
            badgeBackground.isHidden = false
            setAccessibilityLabel("\(item.label), \(count) waiting")
        } else {
            badge.stringValue = ""
            badgeBackground.isHidden = true
            setAccessibilityLabel(item.label)
        }
    }
}

/// The page pane: the WKWebView pinned below the unified toolbar through the
/// window's contentLayoutGuide, while the sidebar runs full height.
final class PanelDetail: NSViewController {
    let webView: WKWebView
    private var pinned = false

    init(webView: WKWebView) {
        self.webView = webView
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { nil }

    override func loadView() {
        let root = NSView(frame: NSRect(x: 0, y: 0, width: 745, height: panelDefaultContentSize.height))
        webView.translatesAutoresizingMaskIntoConstraints = false
        root.addSubview(webView)
        NSLayoutConstraint.activate([
            webView.leadingAnchor.constraint(equalTo: root.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: root.trailingAnchor),
            webView.bottomAnchor.constraint(equalTo: root.bottomAnchor),
        ])
        view = root
    }

    override func viewWillAppear() {
        super.viewWillAppear()
        guard !pinned else { return }
        pinned = true
        let top = (view.window?.contentLayoutGuide as? NSLayoutGuide)?.topAnchor ?? view.safeAreaLayoutGuide.topAnchor
        webView.topAnchor.constraint(equalTo: top).isActive = true
    }
}

// MARK: - OA's on-top question panel

/// Question steps go to the unified log with public text, as notification
/// steps do. Read them with
/// log show --predicate 'subsystem == "org.openabstractions.panel" AND category == "questions"'
let questionLogHandle = OSLog(subsystem: "org.openabstractions.panel", category: "questions")

func questionLog(_ message: String) {
    os_log("%{public}@", log: questionLogHandle, type: .default, message)
}

/// Answer buttons stay disabled this long after a question appears, so a
/// click or keystroke meant for another window cannot answer it.
let nativeQuestionArmDelay: TimeInterval = 0.5

/// The question panel's level: above every normal and floating window, as a
/// system modal panel is (NSWindow.Level.modalPanel).
let questionPanelLevel = NSWindow.Level.modalPanel

/// The question panel's place on a screen: centred across it, its middle at
/// two thirds of the visible height (the upper third, where a system alert
/// sits), kept inside the visible frame.
func questionPanelOrigin(size: NSSize, visibleFrame: NSRect) -> NSPoint {
    let x = max(visibleFrame.minX, visibleFrame.midX - size.width / 2)
    var y = visibleFrame.minY + visibleFrame.height * 2 / 3 - size.height / 2
    y = max(visibleFrame.minY, min(y, visibleFrame.maxY - size.height))
    return NSPoint(x: x.rounded(), y: y.rounded())
}

/// The screen that holds the pointer: where the person is looking.
func questionPanelScreen() -> NSScreen? {
    let pointer = NSEvent.mouseLocation
    return NSScreen.screens.first { NSMouseInRect(pointer, $0.frame, false) } ?? NSScreen.main
}

/// Where the question panel landed, from the window server's on-screen
/// window list (design §4.13). occluders counts other processes' visible
/// windows in front of it, at its level or below, that overlap it: the app
/// windows that could hide it. 0 means no app covers it. Windows above its
/// level are system UI the person sees through or past: the menu bar, and
/// the Dock's screen-wide layer-20 window measured on macOS 26.
struct QuestionPanelVisibility: Equatable {
    var found = false
    var onScreen = false
    var layer = 0
    var occluders = 0

    var summary: String {
        "found \(found) onScreen \(onScreen) layer \(layer) occluders \(occluders)"
    }
}

/// Reads only each window's number, owner, layer, bounds and alpha, which
/// the list gives without Screen Recording permission; never a window's
/// name or contents. windows is front to back, as
/// CGWindowListCopyWindowInfo returns it.
func questionPanelVisibility(_ windows: [[String: Any]], window: Int, pid: Int32) -> QuestionPanelVisibility {
    func number(_ entry: [String: Any], _ key: CFString) -> Int? { (entry[key as String] as? NSNumber)?.intValue }
    func bounds(_ entry: [String: Any]) -> CGRect? {
        guard let dictionary = entry[kCGWindowBounds as String] as? NSDictionary else { return nil }
        return CGRect(dictionaryRepresentation: dictionary as CFDictionary)
    }
    guard let index = windows.firstIndex(where: { number($0, kCGWindowNumber) == window }),
          let own = bounds(windows[index]) else { return QuestionPanelVisibility() }
    var seen = QuestionPanelVisibility(found: true)
    seen.onScreen = (windows[index][kCGWindowIsOnscreen as String] as? NSNumber)?.boolValue ?? true
    seen.layer = number(windows[index], kCGWindowLayer) ?? 0
    for entry in windows[..<index] {
        guard number(entry, kCGWindowOwnerPID).map(Int32.init) != pid,
              number(entry, kCGWindowLayer) ?? 0 <= seen.layer,
              ((entry[kCGWindowAlpha as String] as? NSNumber)?.doubleValue ?? 1) > 0,
              let other = bounds(entry), other.intersects(own) else { continue }
        seen.occluders += 1
    }
    return seen
}

func liveQuestionPanelVisibility(_ window: NSWindow) -> QuestionPanelVisibility {
    let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
    return questionPanelVisibility(list, window: window.windowNumber, pid: getpid())
}

/// The panel window. It never activates the launcher: the app in front keeps
/// its key window and keyboard focus until the person clicks the panel,
/// which then takes keys (Esc, Return) without activating the launcher.
final class QuestionPanelWindow: NSPanel {
    var onCancel: (() -> Void)?
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
    override func cancelOperation(_ sender: Any?) { onCancel?() }
}

/// OA's on-top question panel: every rights and access question and its
/// report on macOS (Asks CONTRACT, routing by ask type). It replaces NSAlert
/// and runModal, which bring no window in front for an app that is not
/// active (NSApplication.requestUserAttention). The window is a
/// non-activating panel at the modal-panel level on every Space, beside
/// full-screen windows, on the pointer's screen. It keeps NSAlert's shape:
/// an icon, a bold message, informative text, one accessory view and buttons,
/// the first rightmost; a button's tag is its index.
///
/// Every button, Cancel and Close included, is disabled until the panel has
/// been on one screen for nativeQuestionArmDelay; onArm then enables what
/// the question allows. A move to another screen disables them again and
/// restarts the delay, so a click or key meant for the window that was under
/// the pointer cannot answer.
final class NativeQuestionPanel: NSObject {
    let window: QuestionPanelWindow
    var messageText = ""
    var informativeText = ""
    var icon: NSImage?
    var accessoryView: NSView?
    var alertStyle: NSAlert.Style = .informational
    private(set) var buttons: [NSButton] = []
    /// Objects the accessory view's controls call back into.
    var retained: [AnyObject] = []
    /// Enables the buttons the question allows once armed; onDisarm undoes
    /// what onArm set beyond the buttons (the default button, a selection).
    var onArm: (() -> Void)?
    var onDisarm: (() -> Void)?
    /// Step logging: armed, and disarmed by a move to another screen.
    var onArmed: (() -> Void)?
    var onRearm: (() -> Void)?
    private(set) var armed = false
    private var armTimer: Timer?
    private var placedScreen: NSRect?
    private var screenObserver: NSObjectProtocol?
    private var completion: ((NSApplication.ModalResponse) -> Void)?
    var isPresented: Bool { completion != nil }

    override init() {
        window = QuestionPanelWindow(contentRect: NSRect(x: 0, y: 0, width: 460, height: 160),
                                     styleMask: [.titled, .nonactivatingPanel], backing: .buffered, defer: false)
        super.init()
        window.level = questionPanelLevel
        window.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        window.hidesOnDeactivate = false
        window.isReleasedWhenClosed = false
        window.becomesKeyOnlyIfNeeded = false
        window.animationBehavior = .alertPanel
        window.onCancel = { [weak self] in
            self?.buttons.first { $0.keyEquivalent == "\u{1b}" && $0.isEnabled }?.performClick(nil)
        }
        // A drag to another display moves the panel without place().
        screenObserver = NotificationCenter.default.addObserver(forName: NSWindow.didChangeScreenNotification,
                                                                object: window, queue: .main) { [weak self] _ in
            guard let self, let frame = self.window.screen?.frame else { return }
            self.screenChanged(to: frame)
        }
    }

    deinit {
        armTimer?.invalidate()
        if let screenObserver { NotificationCenter.default.removeObserver(screenObserver) }
    }

    @discardableResult
    func addButton(withTitle title: String) -> NSButton {
        let button = NSButton(title: title, target: self, action: #selector(pressed(_:)))
        button.bezelStyle = .rounded
        button.keyEquivalent = ""
        button.isEnabled = false
        button.tag = buttons.count
        buttons.append(button)
        return button
    }

    @objc private func pressed(_ sender: NSButton) {
        guard armed else { return }
        finish(NSApplication.ModalResponse(rawValue: NSApplication.ModalResponse.alertFirstButtonReturn.rawValue + sender.tag))
    }

    /// Shows the panel; completion runs once with the clicked button.
    func present(_ completion: @escaping (NSApplication.ModalResponse) -> Void) {
        self.completion = completion
        layout()
        place()
        startArming()
        #if PANEL_ASKS_FIXTURE
        if fixtureHidesPresentation {
            window.alphaValue = 0
            window.ignoresMouseEvents = true
            window.orderFrontRegardless()
            return
        }
        #endif
        window.orderFrontRegardless()
        NSAccessibility.post(element: window, notification: .announcementRequested,
                             userInfo: [.announcement: messageText,
                                        .priority: NSAccessibilityPriorityLevel.high.rawValue])
    }

    /// Disables every button and arms the panel after the delay.
    private func startArming() {
        armTimer?.invalidate()
        armed = false
        for button in buttons { button.isEnabled = false }
        onDisarm?()
        let timer = Timer(timeInterval: nativeQuestionArmDelay, repeats: false) { [weak self] _ in
            guard let self, self.isPresented else { return }
            self.armed = true
            self.onArm?()
            self.onArmed?()
        }
        RunLoop.main.add(timer, forMode: .common)
        armTimer = timer
    }

    private func screenChanged(to frame: NSRect) {
        defer { placedScreen = frame }
        guard isPresented, let previous = placedScreen, previous != frame else { return }
        onRearm?()
        startArming()
    }

    /// Hides the panel now and completes it with .abort on the next turn of
    /// the run loop.
    func abort() {
        guard let completion else { return }
        self.completion = nil
        armTimer?.invalidate()
        window.orderOut(nil)
        performPanelEvent { completion(.abort) }
    }

    private func finish(_ response: NSApplication.ModalResponse) {
        guard let completion else { return }
        self.completion = nil
        armTimer?.invalidate()
        window.orderOut(nil)
        completion(response)
    }

    /// Moves the panel onto the pointer's screen.
    func place() {
        guard let screen = questionPanelScreen() else { return }
        place(visibleFrame: screen.visibleFrame, screenFrame: screen.frame)
    }

    /// Moves the panel onto the screen with these frames; a different screen
    /// than before disarms an open panel and arms it again.
    func place(visibleFrame: NSRect, screenFrame: NSRect) {
        window.setFrameOrigin(questionPanelOrigin(size: window.frame.size, visibleFrame: visibleFrame))
        screenChanged(to: screenFrame)
    }

    private func label(_ text: String, bold: Bool, width: CGFloat) -> NSView {
        let field = NSTextField(wrappingLabelWithString: text)
        field.font = bold ? .boldSystemFont(ofSize: NSFont.systemFontSize) : .systemFont(ofSize: NSFont.smallSystemFontSize)
        field.preferredMaxLayoutWidth = width
        let fitted = field.cell?.cellSize(forBounds: NSRect(x: 0, y: 0, width: width, height: .greatestFiniteMagnitude)).height ?? 0
        guard fitted > 320 else {
            field.frame = NSRect(x: 0, y: 0, width: width, height: ceil(fitted))
            return field
        }
        // Long text scrolls inside a bounded view; the panel stays on screen.
        let scroll = NSScrollView(frame: NSRect(x: 0, y: 0, width: width, height: 320))
        let textView = NSTextView(frame: scroll.bounds)
        textView.string = text
        textView.font = field.font
        textView.isEditable = false
        textView.isSelectable = true
        textView.drawsBackground = false
        textView.isVerticallyResizable = true
        textView.textContainer?.widthTracksTextView = true
        scroll.documentView = textView
        scroll.hasVerticalScroller = true
        scroll.drawsBackground = false
        return scroll
    }

    /// Lays the content out again: after a change to the text, the buttons or
    /// the accessory's height (Details). An open panel keeps its top edge.
    func layout() {
        let margin: CGFloat = 20, iconSize: CGFloat = 64, gap: CGFloat = 16, spacing: CGFloat = 12
        let textWidth = max(340, accessoryView?.frame.width ?? 0)
        let textX = margin + iconSize + gap
        let width = textX + textWidth + margin
        for button in buttons {
            button.sizeToFit()
            button.frame.size.width = max(button.frame.width + 12, 88)
        }
        let rowWidth = buttons.reduce(0) { $0 + $1.frame.width } + CGFloat(max(buttons.count - 1, 0)) * spacing
        let stacked = buttons.count > 3 || rowWidth > textWidth
        let root = NSView(frame: .zero)
        var y = margin
        if stacked {
            for (index, button) in buttons.enumerated() {
                button.frame = NSRect(x: textX, y: y + CGFloat(buttons.count - 1 - index) * 32, width: textWidth, height: 28)
                root.addSubview(button)
            }
            y += CGFloat(buttons.count) * 32 - 4
        } else {
            var x = width - margin
            for button in buttons {
                x -= button.frame.width
                button.frame.origin = NSPoint(x: x, y: y)
                x -= spacing
                root.addSubview(button)
            }
            y += buttons.first?.frame.height ?? 0
        }
        y += 16
        if let accessoryView {
            accessoryView.frame.origin = NSPoint(x: textX, y: y)
            root.addSubview(accessoryView)
            y += accessoryView.frame.height + spacing
        }
        if !informativeText.isEmpty {
            let info = label(informativeText, bold: false, width: textWidth)
            info.frame.origin = NSPoint(x: textX, y: y)
            root.addSubview(info)
            y += info.frame.height + 6
        }
        let message = label(messageText, bold: true, width: textWidth)
        message.frame.origin = NSPoint(x: textX, y: y)
        root.addSubview(message)
        y += message.frame.height
        let height = max(y + margin, margin + iconSize + margin)
        let iconView = NSImageView(frame: NSRect(x: margin, y: height - margin - iconSize, width: iconSize, height: iconSize))
        iconView.image = icon ?? NSImage(named: NSImage.applicationIconName)
        iconView.imageScaling = .scaleProportionallyUpOrDown
        root.addSubview(iconView)
        // Setting contentView sizes the view to the window, so size the
        // window afterwards from the computed size.
        let size = NSSize(width: width, height: height)
        root.frame = NSRect(origin: .zero, size: size)
        let top = window.frame.maxY
        window.contentView = root
        window.setContentSize(size)
        if isPresented { window.setFrameTopLeftPoint(NSPoint(x: window.frame.minX, y: top)) }
    }
}

// App-owned choices use a native radio list. Nothing is selected initially;
// Choose requires a deliberate selection and the existing arming delay.
final class NativeAppChoiceSelection: NSObject {
    let view: NSView
    private let choices: [NativeQuestionChoice]
    private var radios: [NSButton] = []
    private weak var choose: NSButton?
    private weak var alert: NativeQuestionPanel?
    private var armed = false
    private var selected: Int?
    private let details: NativeQuestionDetails
    var selectedOption: String? { selected.map { choices[$0].option } }

    init(prompt: NativeQuestionPrompt, alert: NativeQuestionPanel, choose: NSButton) {
        choices = prompt.choices
        self.choose = choose
        self.alert = alert
        details = NativeQuestionDetails(text: prompt.details, alert: alert)
        view = NSView(frame: NSRect(x: 0, y: 0, width: 540, height: CGFloat(choices.count * 32 + 36)))
        super.init()
        details.onHeightChange = { [weak self] height in
            guard let self else { return }
            self.view.frame.size.height = height + CGFloat(self.choices.count * 56 + 8)
            for (index, radio) in self.radios.enumerated() {
                radio.frame.origin.y = height + CGFloat((self.choices.count - index - 1) * 56 + 8)
            }
        }
        for (index, choice) in choices.enumerated() {
            let radio = NSButton(radioButtonWithTitle: choice.label, target: self, action: #selector(selectChoice(_:)))
            radio.tag = index
            radio.state = .off
            radio.cell?.wraps = true
            radio.cell?.usesSingleLineMode = false
            radio.frame = NSRect(x: 0, y: CGFloat((choices.count - index - 1) * 56 + 36), width: 540, height: 48)
            view.addSubview(radio)
            radios.append(radio)
        }
        view.addSubview(details.view)
        view.frame.size.height = CGFloat(choices.count * 56 + 36)
    }

    @objc private func selectChoice(_ sender: NSButton) {
        selected = sender.tag
        for radio in radios { radio.state = radio === sender ? .on : .off }
        updateChoose()
    }

    func arm() { armed = true; updateChoose() }
    func disarm() { armed = false; updateChoose() }
    private func updateChoose() {
        choose?.isEnabled = armed && selected != nil
        if armed && selected != nil {
            alert?.window.defaultButtonCell = choose?.cell as? NSButtonCell
        }
    }
}

// The complete trusted permission body stays inside the native viewport. Go
// supplies the separate warning and answer effects that remain visible above it.
final class NativePermissionContent {
    let view = NSView(frame: NSRect(x: 0, y: 0, width: 540, height: 264))
    private let scroll = NSScrollView(frame: NSRect(x: 0, y: 44, width: 540, height: 220))

    init(text: String, details: NativeQuestionDetails) {
        let textView = NSTextView(frame: scroll.bounds)
        textView.string = text
        textView.isEditable = false
        textView.isSelectable = true
        textView.font = NSFont.systemFont(ofSize: NSFont.systemFontSize)
        textView.isVerticallyResizable = true
        textView.textContainer?.widthTracksTextView = true
        scroll.documentView = textView
        scroll.hasVerticalScroller = true
        view.addSubview(scroll)
        view.addSubview(details.view)
        details.onHeightChange = { [weak self] height in
            guard let self else { return }
            self.scroll.frame.origin.y = height + 16
            self.view.frame.size.height = height + 236
        }
    }
}

// Exact service-authored values remain available behind a native disclosure.
// Swift does not interpret paths, actions, publishers or permission effects.
final class NativeQuestionDetails: NSObject {
    let view = NSView(frame: NSRect(x: 0, y: 0, width: 540, height: 28))
    private let scroll = NSScrollView(frame: NSRect(x: 0, y: 0, width: 540, height: 180))
    private weak var alert: NativeQuestionPanel?
    private var expanded = false
    var onHeightChange: ((CGFloat) -> Void)?

    init(text: String, alert: NativeQuestionPanel) {
        self.alert = alert
        super.init()
        let textView = NSTextView(frame: scroll.bounds)
        textView.string = text
        textView.isEditable = false
        textView.isSelectable = true
        textView.font = NSFont.systemFont(ofSize: NSFont.smallSystemFontSize)
        textView.isVerticallyResizable = true
        textView.textContainer?.widthTracksTextView = true
        scroll.documentView = textView
        scroll.hasVerticalScroller = true
        scroll.isHidden = true
        view.addSubview(scroll)
        let toggle = NSButton(frame: NSRect(x: 0, y: 2, width: 160, height: 24))
        toggle.title = "Details"
        toggle.setButtonType(.onOff)
        toggle.bezelStyle = .disclosure
        toggle.target = self
        toggle.action = #selector(toggleDetails(_:))
        view.addSubview(toggle)
    }

    @objc private func toggleDetails(_ sender: NSButton) {
        expanded.toggle()
        scroll.isHidden = !expanded
        view.frame.size.height = expanded ? 212 : 28
        sender.frame.origin.y = expanded ? 186 : 2
        onHeightChange?(view.frame.size.height)
        alert?.layout()
    }
}

#if PANEL_WINDOW_FIXTURE
extension AppDelegate {
    /// Window fixtures and the capture tool (monitor/macos/capture_window.py)
    /// answer the window's oa-panel:// requests from an HTTP Panel or stub
    /// at origin, in place of the Go child's pipes. The window itself still
    /// loads only oa-panel:// URLs.
    func forwardPanelRequests(to origin: URL) {
        let target = URLComponents(url: origin, resolvingAgainstBaseURL: false)!
        panelTransport = { [weak self] request in
            var parts = URLComponents(url: request.url, resolvingAgainstBaseURL: false)!
            parts.scheme = target.scheme
            parts.host = target.host
            parts.port = target.port
            var http = URLRequest(url: parts.url!)
            http.httpMethod = request.method
            for (name, value) in request.headers { http.setValue(value, forHTTPHeaderField: name) }
            if !request.body.isEmpty { http.httpBody = request.body }
            URLSession.shared.dataTask(with: http) { data, response, _ in
                let reply = response as? HTTPURLResponse
                var headers: [String: String] = [:]
                for (name, value) in reply?.allHeaderFields ?? [:] { headers["\(name)"] = "\(value)" }
                DispatchQueue.main.async {
                    self?.receivePanelResponse(request.id, seq: 0, status: reply?.statusCode ?? 502,
                                               headers: headers, body: data ?? Data(), more: false)
                }
            }.resume()
        }
    }
}

/// The same page under the window's origin: oa-panel://panel/<path>?<query>.
func panelWindowURL(_ http: URL) -> URL {
    var parts = URLComponents(url: http, resolvingAgainstBaseURL: false)!
    parts.scheme = panelScheme
    parts.host = panelHost
    parts.port = nil
    return parts.url!
}
#endif

#if !PANEL_SIGNAL_FIXTURE && !PANEL_NOTIFICATION_FIXTURE && !PANEL_WINDOW_FIXTURE && !PANEL_ASKS_FIXTURE
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
#endif
