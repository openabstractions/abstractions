"""Offline proof that Node addon inputs are fixed archives, not moving tools."""

import hashlib
import io
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import node_build_inputs as inputs


class NodeBuildInputs(unittest.TestCase):
    def fixtures(self, root: Path):
        headers = root / inputs.HEADERS_NAME
        with tarfile.open(headers, "w:gz") as archive:
            for name in inputs.HEADER_FILES:
                data = ("header " + name).encode()
                member = tarfile.TarInfo("package/include/" + name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
            license_text = b"MIT License\nfixture\n"
            member = tarfile.TarInfo("package/LICENSE")
            member.size = len(license_text)
            archive.addfile(member, io.BytesIO(license_text))
        bundle = root / inputs.NODE_WINDOWS_NAME
        with zipfile.ZipFile(bundle, "w") as archive:
            archive.writestr(f"node-v{inputs.NODE_VERSION}-win-x64/node.exe", b"fixture runtime")
        library = root / inputs.NODE_LIBRARY_NAME
        library.write_bytes(b"!<arch>\nfixture")
        return headers, bundle, library

    def test_offline_archives_produce_exact_headers_import_library_and_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            headers, bundle, library = self.fixtures(root)
            with mock.patch.object(inputs, "HEADERS_SHA256", hashlib.sha256(headers.read_bytes()).hexdigest()), \
                 mock.patch.object(inputs, "NODE_WINDOWS_SHA256", hashlib.sha256(bundle.read_bytes()).hexdigest()), \
                 mock.patch.object(inputs, "NODE_LIBRARY_SHA256", hashlib.sha256(library.read_bytes()).hexdigest()), \
                 mock.patch.object(inputs, "NODE_LIBRARY_SIZE", library.stat().st_size), \
                 mock.patch.object(inputs.urllib.request, "urlopen", side_effect=AssertionError("network")):
                receipt = inputs.prepare(root, windows=True)
            self.assertEqual(receipt["nodeVersion"], inputs.NODE_VERSION)
            self.assertEqual((root / "node.lib").read_bytes(), b"!<arch>\nfixture")
            self.assertEqual(receipt["nodeImportLibraryUrl"], inputs.NODE_LIBRARY_URL)
            for name in inputs.HEADER_FILES:
                self.assertTrue((root / "node-api-headers/include" / name).is_file())
            self.assertTrue((root / "node-api-headers/LICENSE").read_bytes().startswith(b"MIT License"))

    def test_changed_archive_refuses_before_extraction(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            headers, bundle, library = self.fixtures(root)
            with mock.patch.object(inputs, "HEADERS_SHA256", hashlib.sha256(headers.read_bytes()).hexdigest()), \
                 mock.patch.object(inputs, "NODE_WINDOWS_SHA256", "0" * 64), \
                 mock.patch.object(inputs, "NODE_LIBRARY_SHA256", hashlib.sha256(library.read_bytes()).hexdigest()), \
                 mock.patch.object(inputs, "NODE_LIBRARY_SIZE", library.stat().st_size):
                with self.assertRaisesRegex(ValueError, "reviewed SHA-256"):
                    inputs.prepare(root, windows=True)
            self.assertFalse((root / "node.lib").exists())

    def test_changed_official_import_library_refuses_before_extraction(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            headers, bundle, library = self.fixtures(root)
            with mock.patch.object(inputs, "HEADERS_SHA256", hashlib.sha256(headers.read_bytes()).hexdigest()), \
                 mock.patch.object(inputs, "NODE_WINDOWS_SHA256", hashlib.sha256(bundle.read_bytes()).hexdigest()), \
                 mock.patch.object(inputs, "NODE_LIBRARY_SIZE", library.stat().st_size):
                with self.assertRaisesRegex(ValueError, "reviewed SHA-256"):
                    inputs.prepare(root, windows=True)
            self.assertFalse((root / "node.lib").exists())


if __name__ == "__main__":
    unittest.main()
