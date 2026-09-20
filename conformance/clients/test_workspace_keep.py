"""The build tree a runner's --keep selects: kept, refused when occupied, temporary otherwise."""
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from workspace import build_tree


class BuildTree(unittest.TestCase):
    def test_kept_tree_survives_success_and_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            kept = Path(temporary) / 'kept'
            with build_tree('t-', str(kept)) as base:
                (base / 'client.exe').write_text('built')
            self.assertTrue((kept / 'client.exe').is_file())
            failed = Path(temporary) / 'failed'
            with self.assertRaises(RuntimeError):
                with build_tree('t-', str(failed)) as base:
                    (base / 'partial').write_text('half')
                    raise RuntimeError('build failed')
            self.assertTrue((failed / 'partial').is_file())

    def test_occupied_directory_is_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            (Path(temporary) / 'other').write_text('not this run')
            with self.assertRaises(SystemExit) as refused:
                with build_tree('t-', temporary):
                    self.fail('entered an occupied directory')
            self.assertIn('absent or empty', str(refused.exception))

    def test_without_keep_the_tree_is_removed(self):
        with tempfile.TemporaryDirectory() as root:
            with build_tree('t-', None, root) as base:
                (base / 'client.exe').write_text('built')
                seen = base
            self.assertFalse(seen.exists())

    def test_touched_runners_offer_keep(self):
        for runner in ('lifecycle_client', 'rust_services', 'js_services'):
            with self.subTest(runner=runner):
                result = subprocess.run([sys.executable, str(HERE / runner / 'run.py'), '--help'],
                                        capture_output=True, text=True, timeout=60)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('--keep DIR', result.stdout)


if __name__ == '__main__':
    unittest.main()
