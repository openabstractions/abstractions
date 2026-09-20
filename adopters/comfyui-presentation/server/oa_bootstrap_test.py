import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from oa_bootstrap import load_config, MAX_CONFIG


class BootstrapTest(unittest.TestCase):
    def config(self):
        return {
            "schema": 1,
            "comfy_main": "C:/fixture/main.py",
            "working_directory": "C:/fixture/base",
            "arguments": ["--cpu"],
            "environment": {
                "OA_COMFY_OA_MODE": "1",
                "OA_COMFY_BRIDGE_TOKEN": "x" * 32,
            },
            "python_paths": ["C:/fixture/site-packages"],
            "dll_directories": [],
            "venv_prefix": "C:/fixture/venv",
            "pid_file": "C:/fixture/process.pid",
        }

    def write(self, value):
        directory = tempfile.TemporaryDirectory()
        path = Path(directory.name) / "config.json"
        path.write_text(json.dumps(value), encoding="utf-8")
        return directory, path

    def test_accepts_only_bounded_allowlisted_configuration(self):
        directory, path = self.write(self.config())
        try:
            self.assertEqual(load_config(path), self.config())
        finally:
            directory.cleanup()

        invalid = self.config()
        invalid["environment"]["PATH"] = "untrusted"
        directory, path = self.write(invalid)
        try:
            with self.assertRaises(ValueError):
                load_config(path)
        finally:
            directory.cleanup()

    def test_refuses_oversized_config_before_json_decode(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_bytes(b" " * (MAX_CONFIG + 1))
            with self.assertRaisesRegex(ValueError, "16384-byte bound"):
                load_config(path)

    def test_dll_directories_are_bounded(self):
        invalid = self.config()
        invalid["dll_directories"] = ["x"] * 9
        directory, path = self.write(invalid)
        try:
            with self.assertRaisesRegex(ValueError, "DLL directories"):
                load_config(path)
        finally:
            directory.cleanup()

    def test_runs_main_with_configured_prefix_path_environment_and_pid(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            packages = root / "packages"
            packages.mkdir()
            (packages / "fixture_module.py").write_text("VALUE = 'loaded'\n", encoding="utf-8")
            output = root / "output.json"
            (root / "sibling_module.py").write_text("VALUE = 'sibling'\n", encoding="utf-8")
            main = root / "main.py"
            main.write_text(
                "import fixture_module,json,os,sibling_module,sys\n"
                f"open({str(output)!r},'w',encoding='utf-8').write(json.dumps({{"
                "'module':fixture_module.VALUE,'sibling':sibling_module.VALUE,"
                "'prefix':sys.prefix,'mode':os.environ.get('OA_COMFY_OA_MODE')}))\n",
                encoding="utf-8")
            pid_file = root / "process.pid"
            config = self.config()
            config.update({
                "comfy_main": str(main), "working_directory": str(root),
                "python_paths": [str(packages)], "venv_prefix": str(root),
                "pid_file": str(pid_file),
            })
            config_file = root / "config.json"
            config_file.write_text(json.dumps(config), encoding="utf-8")
            subprocess.run([sys.executable, "-I", str(Path(__file__).with_name("oa_bootstrap.py")),
                            str(config_file)], check=True, timeout=10)
            self.assertEqual(json.loads(output.read_text(encoding="utf-8")), {
                "module": "loaded", "sibling": "sibling",
                "prefix": str(root), "mode": "1"})
            self.assertGreater(int(pid_file.read_text(encoding="ascii")), 0)
            self.assertTrue((root / "server.log").is_file())


if __name__ == "__main__":
    unittest.main()
