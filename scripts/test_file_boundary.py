"""Owned release-stage cleanup across Windows path and link boundaries."""

import os
from pathlib import Path
import stat
import tempfile
import unittest

from scripts.file_boundary import extended_path, remove_tree


class FileBoundaryTests(unittest.TestCase):
    @unittest.skipUnless(os.name == "nt", "Windows path spelling")
    def test_unc_path_uses_extended_unc_form(self):
        self.assertEqual(str(extended_path(r"\\server\share\stage")),
                         r"\\?\UNC\server\share\stage")
        self.assertEqual(str(extended_path(r"\\?\C:\stage")), r"\\?\C:\stage")

    def test_deep_read_only_tree_removes_without_touching_sibling(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            root = parent / "owned"
            deep = root / ("a" * 80) / ("b" * 80) / ("c" * 80)
            target = extended_path(deep / "asset.js.map")
            target.parent.mkdir(parents=True)
            target.write_bytes(b"fixture")
            os.chmod(target, stat.S_IREAD)
            sibling = parent / "keep.txt"
            sibling.write_bytes(b"untouched")
            remove_tree(root)
            self.assertFalse(root.exists())
            self.assertEqual(sibling.read_bytes(), b"untouched")

    def test_linked_root_is_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            outside = parent / "outside"
            outside.mkdir()
            (outside / "keep.txt").write_text("keep")
            link = parent / "link"
            try:
                link.symlink_to(outside, target_is_directory=True)
            except (OSError, NotImplementedError):
                self.skipTest("directory symlink creation is unavailable")
            with self.assertRaisesRegex(ValueError, "linked removal root"):
                remove_tree(link)
            self.assertEqual((outside / "keep.txt").read_text(), "keep")
