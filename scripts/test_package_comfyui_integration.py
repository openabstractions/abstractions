import subprocess
import tempfile
import unittest
import base64
import csv
import hashlib
import io
import json
import zipfile
from pathlib import Path
from unittest import mock

from package_comfyui_integration import (copy_sdk_source, matches_revision,
                                         copy_tracked_checkout, load_public_wheel_receipt, read_pypi_pins,
                                         require_native_only_wheelhouse, untracked_source_files)


class PackageSourceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="oa-package-source-")
        self.root = Path(self.temporary.name)
        (self.root / "node").mkdir()
        (self.root / "node" / "committed.py").write_text("value = 1\n", encoding="utf-8")
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Package Test")
        self.git("add", "node/committed.py")
        self.git("commit", "-qm", "base")

    def tearDown(self):
        self.temporary.cleanup()

    def git(self, *arguments):
        subprocess.run(["git", "-C", str(self.root), *arguments], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def git_output(self, *arguments):
        return subprocess.run(["git", "-C", str(self.root), *arguments], check=True,
                              capture_output=True, text=True).stdout.strip()

    def test_untracked_source_is_rejected(self):
        (self.root / "node" / "untracked.py").write_text("unsafe = True\n", encoding="utf-8")
        self.assertEqual(untracked_source_files(self.root, ["node"]), ["node/untracked.py"])

    def test_node_and_native_sources_can_have_distinct_pins(self):
        (self.root / "native").mkdir()
        (self.root / "native" / "ipc.cpp").write_text("int version = 1;\n", encoding="utf-8")
        self.git("add", "native/ipc.cpp")
        self.git("commit", "-qm", "native base")
        node_commit = self.git_output("rev-parse", "HEAD")
        (self.root / "native" / "ipc.cpp").write_text("int version = 2;\n", encoding="utf-8")
        self.git("commit", "-qam", "native update")
        native_commit = self.git_output("rev-parse", "HEAD")
        self.assertTrue(matches_revision(self.root, node_commit, ["node"]))
        self.assertTrue(matches_revision(self.root, native_commit, ["native"]))
        self.assertFalse(matches_revision(self.root, node_commit, ["native"]))

    def test_ignored_build_output_is_not_treated_as_package_source(self):
        pure = self.root / "openabstractions-flat" / "abstraction-facade" / "py"
        source_file = pure / "abstraction" / "facade" / "committed.py"
        source_file.parent.mkdir(parents=True)
        source_file.write_text("committed = True\n", encoding="utf-8")
        self.git("add", "openabstractions-flat/abstraction-facade/py")
        self.git("commit", "-qm", "pure source")
        build_file = pure / "build" / "lib" / "abstraction" / "facade" / "generated.py"
        build_file.parent.mkdir(parents=True)
        build_file.write_text("generated = True\n", encoding="utf-8")
        self.assertEqual(untracked_source_files(self.root, ["openabstractions-flat/abstraction-facade/py"]), [])
        destination = self.root / "copy"
        copy_sdk_source(pure, destination)
        self.assertTrue((destination / "abstraction" / "facade" / "committed.py").is_file())
        self.assertFalse((destination / "build" / "lib" / "abstraction" / "facade" / "generated.py").exists())

    def test_wheelhouse_rejects_arbitrary_same_name_pure_wheel(self):
        native = self.root / "abstraction_ipc-0.4.0-py3-none-win_amd64.whl"
        native.touch()
        supplied = self.root / "abstraction_asks_protocol-0.0.0-py3-none-any.whl"
        supplied.touch()
        with self.assertRaisesRegex(ValueError, "only the pinned native IPC wheel"):
            require_native_only_wheelhouse([native, supplied], native.name)

    def test_wheelhouse_requires_native_wheel(self):
        with self.assertRaisesRegex(ValueError, "missing Windows x64 native wheel"):
            require_native_only_wheelhouse([], "abstraction_ipc-0.4.0-py3-none-win_amd64.whl")

    def test_public_pins_fail_closed_until_every_wheel_is_pinned(self):
        pins = self.root / "pypi.tsv"
        pins.write_text(
            "package\trepository\tcommit\tdirectory\n"
            "abstraction-ipc\topenabstractions/abstraction-identity\t" + "a" * 40 + "\tpy\n",
            encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "missing required Comfy wheels"):
            read_pypi_pins(pins)

    def test_public_copy_excludes_ignored_untracked_files(self):
        (self.root / ".gitignore").write_text("node/ignored-source.py\n", encoding="utf-8")
        self.git("add", ".gitignore")
        self.git("commit", "-qm", "ignore generated source")
        ignored = self.root / "node" / "ignored-source.py"
        ignored.write_text("unpublished = True\n", encoding="utf-8")
        destination = self.root / "package-node"
        copy_tracked_checkout(self.root, destination)
        self.assertTrue((destination / "node" / "committed.py").is_file())
        self.assertFalse((destination / "node" / "ignored-source.py").exists())

    def test_public_receipt_is_bound_to_every_pin_and_exact_wheel_bytes(self):
        from package_comfyui_integration import PUBLIC_DISTRIBUTIONS

        source_root = self.root / "public"
        wheel_root = self.root / "wheels"
        wheel_root.mkdir()
        pin_path = self.root / "pypi.tsv"
        pin_lines = ["package\trepository\tcommit\tdirectory"]
        pins = {}
        entries = []
        for index, (distribution, wheel_prefix) in enumerate(PUBLIC_DISTRIBUTIONS.items()):
            repo = f"openabstractions/{distribution}"
            checkout = source_root / distribution
            source_dir = checkout / "py"
            source_dir.mkdir(parents=True)
            (source_dir / "source.py").write_text("# pinned source\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(checkout), "init", "-q"], check=True)
            subprocess.run(["git", "-C", str(checkout), "config", "user.email", "test@example.invalid"], check=True)
            subprocess.run(["git", "-C", str(checkout), "config", "user.name", "Package Test"], check=True)
            subprocess.run(["git", "-C", str(checkout), "add", "py/source.py"], check=True)
            subprocess.run(["git", "-C", str(checkout), "commit", "-qm", "pinned source"], check=True)
            commit = subprocess.run(["git", "-C", str(checkout), "rev-parse", "HEAD"], check=True,
                                    capture_output=True, text=True).stdout.strip()
            pins[distribution] = {"package": distribution, "repository": repo, "commit": commit, "directory": "py"}
            pin_lines.append(f"{distribution}\t{repo}\t{commit}\tpy")
            platform_tag = "win_amd64" if distribution == "abstraction-ipc" else "any"
            package_version = "0.4.0" if distribution == "abstraction-ipc" else "0.0.0"
            filename = f"{wheel_prefix}-{package_version}-py3-none-{platform_tag}.whl"
            wheel = wheel_root / filename
            wheel_dist_info = f"{wheel_prefix}-{package_version}.dist-info"
            members = {
                f"{wheel_dist_info}/METADATA": f"Metadata-Version: 2.1\nName: {distribution}\nVersion: {package_version}\n\n".encode(),
                f"{wheel_dist_info}/WHEEL": f"Wheel-Version: 1.0\nGenerator: test\nRoot-Is-Purelib: true\nTag: py3-none-{platform_tag}\n".encode(),
                "package.py": f"wheel-{index}".encode(),
            }
            record = io.StringIO(newline="")
            writer = csv.writer(record, lineterminator="\n")
            for member_name, contents in sorted(members.items()):
                digest = base64.urlsafe_b64encode(hashlib.sha256(contents).digest()).decode().rstrip("=")
                writer.writerow((member_name, f"sha256={digest}", str(len(contents))))
            writer.writerow((f"{wheel_dist_info}/RECORD", "", ""))
            members[f"{wheel_dist_info}/RECORD"] = record.getvalue().encode()
            with zipfile.ZipFile(wheel, "w") as archive:
                for member_name, contents in members.items():
                    archive.writestr(member_name, contents)
            data = wheel.read_bytes()
            entry = {"distribution": distribution, "repository": repo, "commit": commit,
                     "directory": "py", "filename": filename, "bytes": len(data),
                     "sha256": hashlib.sha256(data).hexdigest()}
            if distribution == "abstraction-ipc":
                entry["native_library_sha256"] = "b" * 64
            entries.append(entry)
        pin_path.write_text("\n".join(pin_lines) + "\n", encoding="utf-8")
        receipt_path = self.root / "wheel-inputs.json"
        receipt_path.write_text(json.dumps({"schema": 1, "candidate_source": "c" * 40,
                                            "candidate_version": "0.4.0",
                                            "run_id": 123, "run_attempt": 2,
                                            "platform": "win32-x64", "wheels": entries}), encoding="utf-8")
        loaded_pins = read_pypi_pins(pin_path)
        native = next(entry for entry in entries if entry["distribution"] == "abstraction-ipc")
        with mock.patch("py_wheels.verify_native_receipt_wheel",
                        return_value=(wheel_root / native["filename"], b"verified DLL", native)) as verify:
            wheels, receipt, native_bytes = load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                                                       "c" * 40, source_root, "0.4.0", 123, 2)
            verify.assert_called_once_with(receipt, wheel_root.resolve(),
                                           repository=native["repository"], commit=native["commit"],
                                           directory="py")
        self.assertEqual(len(wheels), len(PUBLIC_DISTRIBUTIONS))
        self.assertEqual(receipt["run_attempt"], 2)
        self.assertEqual(native_bytes, b"verified DLL")
        with self.assertRaisesRegex(ValueError, "selected candidate run"):
            load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                      "c" * 40, source_root, "0.4.1", 123, 2)
        with self.assertRaisesRegex(ValueError, "selected candidate run"):
            load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                      "c" * 40, source_root, "0.4.0", 123, 1)
        pure_entry = next(entry for entry in entries if entry["distribution"] != "abstraction-ipc")
        pure_entry["sha256"] = "d" * 64
        receipt_path.write_text(json.dumps({"schema": 1, "candidate_source": "c" * 40,
                                            "candidate_version": "0.4.0",
                                            "run_id": 123, "run_attempt": 2,
                                            "platform": "win32-x64", "wheels": entries}), encoding="utf-8")
        with mock.patch("py_wheels.verify_native_receipt_wheel",
                        return_value=(wheel_root / native["filename"], b"verified DLL", native)):
            with self.assertRaisesRegex(ValueError, "wheel bytes do not match receipt"):
                load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                          "c" * 40, source_root, "0.4.0", 123, 2)


class NodeImportInventoryTests(unittest.TestCase):
    """Every OA Python package the ComfyUI node imports is packed beside it.

    The node imports these lazily, so a missing wheel shows only when a person
    runs the feature: OAVideoGenerate's model list read abstraction.router, which
    the integration did not pack.
    """

    # Imported only behind its own switch and not packed yet: oa_card
    # (ABSTRACTION_COMFYUI_CARD, resource leases).
    UNPACKED_OPT_IN = {"resource"}

    def test_every_imported_oa_package_has_a_wheel(self):
        import re
        import package_comfyui_integration as integration

        root = Path(integration.__file__).resolve().parents[1]
        node = root / "openabstractions-flat" / "adopter-comfyui"
        imported = set()
        for path in node.glob("*.py"):
            if path.name.startswith("test_"):
                continue
            imported |= set(re.findall(r"^\s*(?:from|import) abstraction\.([a-z_]+)",
                                       path.read_text(encoding="utf-8"), re.M))
        packed = {Path(relative).parent.name.removeprefix("abstraction-")
                  for relative in integration.PURE_SOURCES.values()}
        packed |= {"ipc"}  # the native wheel, built from abstraction-identity/py
        self.assertIn("router", imported)
        self.assertIn("logging", packed)
        self.assertEqual(sorted(imported - packed - self.UNPACKED_OPT_IN), [])
        self.assertEqual(len(integration.PURE_WHEELS), len(integration.PURE_SOURCES))
        self.assertEqual(set(integration.PUBLIC_DISTRIBUTIONS.values()) - {"abstraction_ipc"},
                         set(integration.PURE_SOURCES))


if __name__ == "__main__":
    unittest.main()
