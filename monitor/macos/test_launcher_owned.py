"""Inert owned-channel controls: no runtime, application launch or window."""
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent

FIXTURE = r'''
func require(_ condition: @autoclosure () -> Bool, _ message: String) {
    if !condition() { fatalError(message) }
}
require(validNativeOwnedHostArguments(["Panel", "-owned-host", "-native", "-quiet", "-page", "credentials"]), "fixed recipe")
require(!validNativeOwnedHostArguments(["Panel", "-owned-host", "-runtime-endpoint", "forged"]), "no runtime override")
require(nativePanelArguments(["Panel", "-owned-host"]) == ["-native-asks", "-native-owned-child"], "private child only")
let input = Pipe(), output = Pipe()
let bootstrap = DispatchSemaphore(value: 0), received = DispatchSemaphore(value: 0)
let ended = DispatchSemaphore(value: 0), withdrawn = DispatchSemaphore(value: 0)
let recordsLock = NSLock()
var records: [[String: Any]] = []
let channel = NativeOwnedChannel(input: input.fileHandleForReading, output: output.fileHandleForWriting,
    send: { fields in
        recordsLock.lock(); records.append(fields); recordsLock.unlock()
        switch fields["kind"] as? String {
        case "owned-bootstrap": bootstrap.signal()
        case "owned-input": received.signal()
        case "owned-end": ended.signal()
        default: break
        }
        return true
    }, createPresence: { OpaquePointer(bitPattern: 1) },
    announcePresence: { _, destination, capacity in
        let text = Array("runtime-issued-instance".utf8CString)
        require(capacity >= text.count, "bounded instance output")
        for (index, byte) in text.enumerated() { destination[index] = byte }
        return 0
    }, closePresence: { _ in withdrawn.signal() })
channel.start()
require(bootstrap.wait(timeout: .now() + 2) == .success, "Announce precedes child bootstrap")
let opaque = Data([0, 0, 0, 12, 0x7b, 0x0a, 0x0d, 0x22, 0x3c, 0x3e, 0x26, 0x5c, 0x22, 0xc3, 0xa9, 0x7d])
try input.fileHandleForWriting.write(contentsOf: opaque)
require(received.wait(timeout: .now() + 2) == .success, "owned input delivered")
recordsLock.lock()
let inputBody = records.first(where: { $0["kind"] as? String == "owned-input" })?["body"] as? String
let instance = records.first(where: { $0["kind"] as? String == "owned-bootstrap" })?["id"] as? String
recordsLock.unlock()
require(instance == "runtime-issued-instance", "actual returned handle")
require(inputBody.flatMap { Data(base64Encoded: $0) } == opaque, "input bytes preserved")
require(channel.receive(opaque.base64EncodedString()), "owned output delivered")
let outputBytes = try output.fileHandleForReading.read(upToCount: opaque.count)
require(outputBytes == opaque, "output bytes preserved")
try input.fileHandleForWriting.close()
require(ended.wait(timeout: .now() + 2) == .success, "EOF retires mediation")
require(withdrawn.wait(timeout: .now() + 2) == .success, "presence closes once")
require(!channel.live(), "EOF remains terminal")
channel.start(); channel.retire()
require(!channel.receive(opaque.base64EncodedString()), "no post-retirement output")
require(withdrawn.wait(timeout: .now() + 0.05) == .timedOut, "no repeat withdrawal")
recordsLock.lock()
require(records.filter { $0["kind"] as? String == "owned-bootstrap" }.count == 1, "no replay or new instance")
recordsLock.unlock()

let missingInput = Pipe(), missingOutput = Pipe(), missingEnd = DispatchSemaphore(value: 0)
var missingBootstrap = false
let missing = NativeOwnedChannel(input: missingInput.fileHandleForReading, output: missingOutput.fileHandleForWriting,
    send: { fields in
        if fields["kind"] as? String == "owned-bootstrap" { missingBootstrap = true }
        if fields["kind"] as? String == "owned-end" { missingEnd.signal() }
        return true
    }, createPresence: { nil })
missing.start()
require(missingEnd.wait(timeout: .now() + 2) == .success, "unavailable same-main presence retires")
require(!missingBootstrap && !missing.live(), "no child identity substitution")
print("owned channel controls passed")
'''


@unittest.skipUnless(sys.platform == "darwin", "Swift native channel requires macOS")
class OwnedChannel(unittest.TestCase):
    def test_opaque_lifecycle_and_missing_presence(self):
        with tempfile.TemporaryDirectory(prefix="oa-panel-owned-fixture-") as directory:
            root = Path(directory)
            source = root / "main.swift"
            source.write_text((HERE / "PanelLauncher.swift").read_text(encoding="utf-8") + "\n" + FIXTURE,
                              encoding="utf-8")
            program = root / "owned-fixture"
            subprocess.run(["swiftc", "-D", "PANEL_WINDOW_FIXTURE", "-module-cache-path", str(root / "modules"), str(source), "-o", str(program)],
                           check=True, timeout=120)
            outcome = subprocess.run([str(program)], capture_output=True, text=True, timeout=15)
            self.assertEqual(outcome.returncode, 0, outcome.stdout + outcome.stderr)
            self.assertIn("owned channel controls passed", outcome.stdout)


if __name__ == "__main__":
    unittest.main()
