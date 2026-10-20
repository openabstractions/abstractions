"""Compile and exercise the launcher's stdout parser and application notifications."""

from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent


@unittest.skipUnless(sys.platform == "darwin", "Swift AppKit fixture requires macOS")
class LauncherNotifications(unittest.TestCase):
    def test_split_lines_and_retired_question_notifications(self):
        # Questions never arrive as notifications: the retired
        # oa-question-notification line reaches no handler.
        with tempfile.TemporaryDirectory() as scratch:
            scratch = Path(scratch)
            fixture = scratch / "main.swift"
            fixture.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + r'''var parser = NativeLineParser()
var urls: [String] = []
var frames: [NativeAsksFrame] = []
let retired = Data("oa-question-notification: {\"id\":\"q-1\",\"title\":\"Terminal\",\"body\":\"wants access\"}\n".utf8)
let urlLine = Data((announcePrefix + "http://127.0.0.1:8734/?k=key\n").utf8)
parser.consume(Data(urlLine.prefix(9)), onPanelURL: { urls.append($0) }, onNative: { frames.append($0) })
var secondChunk = Data(urlLine.dropFirst(9))
secondChunk.append(retired)
secondChunk.append(Data((nativeAsksPrefix + "{bad json}\n").utf8))
secondChunk.append(retired)
parser.consume(secondChunk, onPanelURL: { urls.append($0) }, onNative: { frames.append($0) })
precondition(urls == ["http://127.0.0.1:8734/?k=key"])
precondition(frames.isEmpty)
print("notification parser fixture passed")
''')
            executable = scratch / "launcher-notification-fixture"
            subprocess.run(["swiftc", "-D", "PANEL_NOTIFICATION_FIXTURE", str(fixture),
                            "-o", str(executable)], check=True, timeout=90)
            completed = subprocess.run([str(executable)], check=True, capture_output=True,
                                       text=True, timeout=10)
            self.assertIn("notification parser fixture passed", completed.stdout)


    def test_application_notification_frames_categories_and_replies(self):
        # ASK-N7: a notification frame decodes through the bounded parser; its
        # category carries each prepared click argument with Turn off
        # destructive; replies return the arguments, the open arguments for a
        # body click and the record for a close; a notification that is not
        # one of these has no reply.
        with tempfile.TemporaryDirectory() as scratch:
            scratch = Path(scratch)
            fixture = scratch / "main.swift"
            fixture.write_text((HERE / "PanelLauncher.swift").read_text() + "\n" + r'''import UserNotifications
var parser = NativeLineParser()
var frames: [NativeAsksFrame] = []
let payload: [String: Any] = ["kind": "notification", "id": "a1", "notification": [
    "title": "Download finished", "body": "slow.bin is ready.", "sender": "pretend-lemonade", "open": "oa-notification:id=a1&t=t1",
    "actions": [["title": "Copy now", "args": "oa-notification:a=copy&id=a1&t=t1"],
                ["title": "Turn off notifications from pretend-lemonade", "args": "oa-notification:id=a1&off=1&t=t1"]],
    "silently": false]]
let data = try! JSONSerialization.data(withJSONObject: payload)
parser.consume(Data((nativeAsksPrefix + String(data: data, encoding: .utf8)! + "\n").utf8), onPanelURL: { _ in }, onNative: { frames.append($0) })
precondition(frames.count == 1 && frames[0].kind == "notification")
let n = frames[0].notification!
let category = appNotificationCategory("a1", n)
precondition(category.identifier == "oa.n.a1" && category.actions.count == 2)
precondition(category.actions[0].identifier == "oa-notification:a=copy&id=a1&t=t1")
precondition(category.actions[1].options.contains(.destructive))
precondition(category.options.contains(.customDismissAction))
let content = appNotificationContent("a1", n)
precondition(content.subtitle == "From pretend-lemonade" && content.categoryIdentifier == "oa.n.a1")
let info = content.userInfo
let click = appNotificationReply(category: "oa.n.a1", action: "oa-notification:a=copy&id=a1&t=t1", userInfo: info)!
precondition(click["kind"] as! String == "notification-click" && click["args"] as! String == "oa-notification:a=copy&id=a1&t=t1")
let body = appNotificationReply(category: "oa.n.a1", action: UNNotificationDefaultActionIdentifier, userInfo: info)!
precondition(body["args"] as! String == "oa-notification:id=a1&t=t1")
let closed = appNotificationReply(category: "oa.n.a1", action: UNNotificationDismissActionIdentifier, userInfo: info)!
precondition(closed["kind"] as! String == "notification-closed" && closed["id"] as! String == "a1")
precondition(appNotificationReply(category: "", action: UNNotificationDefaultActionIdentifier, userInfo: [:]) == nil)
// Step logging names the error's domain and code and each response kind.
precondition(notificationErrorText(nil) == "ok")
let refused = NSError(domain: "UNErrorDomain", code: 1, userInfo: [NSLocalizedDescriptionKey: "Notifications are not allowed for this application"])
precondition(notificationErrorText(refused) == "UNErrorDomain 1: Notifications are not allowed for this application")
precondition(notificationResponseKind(UNNotificationDismissActionIdentifier) == "close")
precondition(notificationResponseKind(UNNotificationDefaultActionIdentifier) == "body")
precondition(notificationResponseKind("oa-notification:a=copy") == "action")
notificationLog("fixture step logging")
print("application notification fixture passed")
''')
            executable = scratch / "launcher-app-notification-fixture"
            subprocess.run(["swiftc", "-D", "PANEL_NOTIFICATION_FIXTURE", str(fixture),
                            "-o", str(executable)], check=True, timeout=90)
            completed = subprocess.run([str(executable)], check=True, capture_output=True,
                                       text=True, timeout=10)
            self.assertIn("application notification fixture passed", completed.stdout)


if __name__ == "__main__":
    unittest.main()
