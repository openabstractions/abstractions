"""run_node_tests.py runs the Bun-only tests with the pinned Bun, or says why it skipped them."""
import io
import os
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parent))
import run_node_tests  # noqa: E402


class BunTestsTests(unittest.TestCase):
    def test_missing_pinned_bun_skips_with_the_reason(self):
        with tempfile.TemporaryDirectory() as root:
            bun = run_node_tests.pinned_bun(Path(root))
            out = io.StringIO()
            calls = []
            code = run_node_tests.run_bun_tests(bun, run=lambda *a, **k: calls.append(a), out=out)
        self.assertEqual(code, 0)
        self.assertEqual(calls, [])
        self.assertIn("skipped test/winmm-binding.test.mjs: no pinned Bun at", out.getvalue())
        self.assertIn(str(Path(".forks") / "bun-1.3.14"), out.getvalue())

    def test_pinned_bun_runs_the_bun_only_tests_and_returns_its_status(self):
        with tempfile.TemporaryDirectory() as root:
            bun = run_node_tests.pinned_bun(Path(root))
            bun.parent.mkdir(parents=True)
            bun.write_bytes(b"")
            calls = []

            def run(command, cwd, check):
                calls.append((command, cwd, check))
                return SimpleNamespace(returncode=3)

            code = run_node_tests.run_bun_tests(bun, run=run, out=io.StringIO())
        self.assertEqual(code, 3)
        self.assertEqual(calls, [([str(bun), "test", "test/winmm-binding.test.mjs"], run_node_tests.ADOPTER, False)])




def make_link(target, link):
    """A junction on Windows, a symbolic link elsewhere, as npm makes them."""
    if sys.platform == "win32":
        import _winapi
        _winapi.CreateJunction(str(target), str(link))
    else:
        os.symlink(target, link, target_is_directory=True)


class LinkCleanupTests(unittest.TestCase):
    def test_created_links_go_and_their_targets_stay(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            source = root / "openabstractions-flat" / "abstraction-storage" / "javascript"
            source.mkdir(parents=True)
            (source / "package.json").write_text("{}", encoding="utf-8")
            scope = root / "adopter" / "node_modules" / "@openabstractions"
            scope.mkdir(parents=True)
            earlier = scope / "facade"
            make_link(source, earlier)
            real = scope / "copied"
            real.mkdir()
            before = run_node_tests.package_links(root / "adopter")
            created = scope / "storage-content"
            make_link(source, created)
            out = io.StringIO()
            status = run_node_tests.remove_created_links(before, root / "adopter", out=out)
            self.assertEqual((status, out.getvalue()), (0, ""))
            self.assertFalse(os.path.lexists(created), "the link this run created stays")
            self.assertTrue((source / "package.json").is_file(), "removing the link reached its target")
            self.assertTrue(run_node_tests.is_link(earlier), "a link from before the run was removed")
            self.assertTrue(real.is_dir(), "a real directory was touched")
            with self.assertRaises(ValueError):
                run_node_tests.remove_link(real)
            run_node_tests.remove_link(earlier)


class ImportedTests(unittest.TestCase):
    def test_every_package_json_dependency_counts_as_imported(self):
        with tempfile.TemporaryDirectory() as root:
            adopter = Path(root)
            (adopter / "package.json").write_text(
                '{"dependencies": {"@openabstractions/storage-content": "0.0.0", "@openabstractions/facade": "0.0.0"}}',
                encoding="utf-8")
            names = run_node_tests.imported(adopter)
        self.assertIn("@openabstractions/storage-content", names)
        self.assertIn("@openabstractions/facade", names)
        self.assertIn("@opencode-ai/plugin", names)

    def test_the_adopters_own_dependencies_are_all_imported(self):
        names = run_node_tests.imported()
        self.assertIn("@openabstractions/storage-content", names)


if __name__ == "__main__":
    unittest.main()
