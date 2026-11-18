"""The Panel launcher reports a Panel that cannot start or stopped as a
notification; it opens no alert of its own."""

from pathlib import Path
import re
import unittest


HERE = Path(__file__).resolve().parent


class LauncherFailureNotice(unittest.TestCase):
    def test_panel_failure_is_a_notification(self):
        source = (HERE / "PanelLauncher.swift").read_text(encoding="utf-8")
        body = re.search(r"private func reportPanelFailure\(_ message: String\) \{(.*?)\n    \}\n", source, re.S)
        self.assertIsNotNone(body, "reportPanelFailure is missing")
        body = body.group(1)
        self.assertNotIn("NSAlert", body)
        self.assertNotIn("runModal", body)
        self.assertIn("UNNotificationRequest(identifier: panelFailureNotification", body)
        # No other code path in the launcher opens a modal alert.
        self.assertIsNone(re.search(r"\bNSAlert\(\)", source))


if __name__ == "__main__":
    unittest.main()
