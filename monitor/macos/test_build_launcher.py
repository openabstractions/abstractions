"""Offline public-layout and pinned native compiler-input receiving controls."""
import json
import os
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

import build_launcher as owner


class NativeBuildInputs(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="oa-native-build-inputs-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.sources = self.root / "sources"
        self.roster = self.root / "pypi.tsv"
        self.provenance = self.root / "module-provenance.json"
        self.rows = ["package\trepository\tcommit\tdirectory"]
        self.selected = []
        files = {
            "abstraction-identity": ["cpp/src/client.cpp", "cpp/link-dependencies.txt", "xpc_native_darwin.c", "xpc_native.h", "sealed_resource_native.h"],
            "abstraction-facade": ["cpp/abstraction/facade/applications/rec.h", "cpp/include/abstraction/facade/appmodel.h",
                                   "cpp/src/appmodel.cpp", "cpp/src/appmodel_mcp.cpp", "cpp/src/appmodel_session.cpp"],
            "abstraction-download": ["cpp/abstraction/download/request/rec.h"],
            "abstraction-router": ["cpp/abstraction/router/rec.h"],
            "abstraction-rights": ["cpp/abstraction/rights/api/rec.h"],
        }
        for name, paths in files.items():
            checkout = self.sources / name
            checkout.mkdir(parents=True)
            for relative in paths:
                path = checkout / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("macos cxx-runtime c++\n" if relative.endswith("link-dependencies.txt") else "// inert source fixture\n", newline="\n")
            self.git(checkout, "init", "--quiet")
            self.git(checkout, "add", ".")
            message = self.root / (name + "-message.txt")
            message.write_text("inert native source fixture\n")
            self.git(checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                     "-c", "commit.gpgsign=false", "commit", "-F", str(message), "--quiet")
            commit = self.git(checkout, "rev-parse", "HEAD").strip()
            self.rows.append(f"fixture-{name}\topenabstractions/{name}\t{commit}\tpy")
            self.selected.append({"repository": "https://github.com/openabstractions/" + name, "expected_commit": commit})
        self.roster.write_text("\n".join(self.rows) + "\n")
        self.provenance.write_text(json.dumps({"schema": 1, "modules": [{"selected": self.selected}]}))

    def git(self, root, *args):
        return subprocess.run(["git", "-C", str(root), "-c", "core.hooksPath=" + str(self.root / "no-hooks"), *args],
                              capture_output=True, text=True, check=True).stdout

    def inputs(self):
        return owner.native_source_inputs(self.sources, self.roster, self.provenance)

    def test_monitor_only_public_layout_refuses_without_ambient_lookup(self):
        with self.assertRaisesRegex(ValueError, "explicit source root"):
            owner.build(Path(__file__).with_name("PanelLauncher.swift"), self.root / "panel", "arm64", "12.0")
        with self.assertRaisesRegex(ValueError, "missing or linked"):
            owner.native_source_inputs(self.root / "monitor-only", self.roster, self.provenance)

    def test_declared_native_closure_and_universal_targets(self):
        inputs = self.inputs()
        self.assertEqual(set(inputs["repositories"]), {row.split("/")[-1] for row in (r["repository"] for r in self.selected)})
        self.assertIn("abstraction-identity/sealed_resource_native.h", inputs["files"])
        rights_header = "abstraction-rights/cpp/abstraction/rights/api/rec.h"
        self.assertEqual(inputs["files"][rights_header], owner.hashlib.sha256((self.sources / rights_header).read_bytes()).hexdigest())
        for arch in ("arm64", "x86_64"):
            output = self.root / arch / "Panel"
            real = subprocess.run
            def compiler(command, **kwargs):
                if command[0] in ("clang", "clang++", "swiftc"):
                    return subprocess.CompletedProcess(command, 0)
                return real(command, **kwargs)
            with patch.object(owner.subprocess, "run", side_effect=compiler) as run:
                owner.build(Path(__file__).with_name("PanelLauncher.swift"), output, arch, "12.0",
                            self.sources, self.roster, self.provenance)
            commands = [call.args[0] for call in run.call_args_list if call.args[0][0] in ("clang", "clang++", "swiftc")]
            self.assertEqual(len(commands), 8)
            self.assertTrue(all(command[command.index("-target") + 1] == arch + "-apple-macos12.0" for command in commands))
            self.assertTrue(all("openabstractions-flat" not in str(command) for command in commands))
            rights_include = str(self.sources / "abstraction-rights/cpp")
            self.assertTrue(all(rights_include in command for command in commands if command[0] in ("clang", "clang++")))
            receipt = json.loads(output.with_name("Panel.native-inputs.json").read_bytes())
            self.assertEqual(receipt["files"], inputs["files"])
            self.assertIn("NativePresence.cpp", receipt["launcher_files"])
            self.assertIn("NativeHost.cpp", receipt["launcher_files"])
            self.assertTrue(any("appmodel_session.cpp" in str(command) for command in commands))
            self.assertTrue(any("NativeHost.h" in str(command) for command in commands))

    def test_moved_dirty_contradictory_and_missing_inputs_refuse(self):
        self.selected[0]["expected_commit"] = "f" * 40
        self.provenance.write_text(json.dumps({"modules": [{"selected": self.selected}]}))
        with self.assertRaisesRegex(ValueError, "provenance"): self.inputs()
        self.selected[0]["expected_commit"] = self.rows[1].split("\t")[2]
        self.provenance.write_text(json.dumps({"modules": [{"selected": self.selected}]}))
        path = self.sources / "abstraction-identity/xpc_native.h"
        path.write_text("// changed before compile\n")
        with self.assertRaisesRegex(ValueError, "dirty"): self.inputs()
        self.git(path.parent, "checkout", "--", "xpc_native.h")
        self.roster.write_text("\n".join(self.rows[:-1]) + "\n")
        with self.assertRaisesRegex(ValueError, "complete generated closure"): self.inputs()
        self.roster.write_text("\n".join(self.rows) + "\n")
        self.git(path.parent, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-F",
                 str(self.root / "abstraction-identity-message.txt"))
        with self.assertRaisesRegex(ValueError, "moved"): self.inputs()

    def test_source_change_during_compile_produces_no_new_receipt(self):
        real = subprocess.run
        changed = False
        def compiler(command, **kwargs):
            nonlocal changed
            if command[0] in ("clang", "clang++", "swiftc"):
                if not changed:
                    (self.sources / "abstraction-identity/xpc_native.h").write_text("// moved while compiling\n")
                    changed = True
                return subprocess.CompletedProcess(command, 0)
            return real(command, **kwargs)
        output = self.root / "changed/Panel"
        with patch.object(owner.subprocess, "run", side_effect=compiler), self.assertRaisesRegex(ValueError, "dirty"):
            owner.build(Path(__file__).with_name("PanelLauncher.swift"), output, "arm64", "12.0",
                        self.sources, self.roster, self.provenance)
        self.assertFalse(output.with_name("Panel.native-inputs.json").exists())

    def test_selected_old_facade_without_core_refuses_before_compiler(self):
        checkout=self.sources/'abstraction-facade'
        missing=checkout/'cpp/src/appmodel_session.cpp'
        missing.unlink()
        self.git(checkout,'add','-u')
        self.git(checkout,'-c','user.name=Fixture','-c','user.email=fixture@example.invalid','-c','commit.gpgsign=false','commit','-F',str(self.root/'abstraction-facade-message.txt'),'--quiet')
        current=self.git(checkout,'rev-parse','HEAD').strip()
        self.roster.write_text(self.roster.read_text().replace(self.rows[2].split('\t')[2],current))
        self.selected[1]['expected_commit']=current
        self.provenance.write_text(json.dumps({'modules':[{'selected':self.selected}]}))
        real=subprocess.run
        def no_compiler(command,**kwargs):
            if command[0] in ('clang','clang++','swiftc'):self.fail('missing native core reached compiler')
            return real(command,**kwargs)
        with patch.object(owner.subprocess,'run',side_effect=no_compiler),self.assertRaisesRegex(FileNotFoundError,'appmodel_session.cpp'):
            owner.build(Path(__file__).with_name('PanelLauncher.swift'),self.root/'old/Panel','arm64','12.0',self.sources,self.roster,self.provenance)

    def test_repository_root_uses_filesystem_identity(self):
        real = subprocess.run
        def different_root(command, **kwargs):
            if command[-1] == "--show-toplevel":
                return subprocess.CompletedProcess(command, 0, stdout=str(self.sources) + "\n")
            return real(command, **kwargs)
        with patch.object(owner.subprocess, "run", side_effect=different_root), self.assertRaisesRegex(ValueError, "own selected repository"):
            self.inputs()
        with patch.object(owner, "same_existing_file", return_value=True) as samefile:
            self.inputs()
        self.assertEqual(samefile.call_count, 5)
        with patch.object(owner, "same_existing_file", side_effect=PermissionError("denied")):
            with self.assertRaises(PermissionError):
                self.inputs()

    def test_source_git_disables_ambient_and_local_execution_config(self):
        hook = self.root / "owned-fsmonitor.sh"
        marker = self.root / "fsmonitor-executed"
        hook.write_text("#!/bin/sh\nprintf observed > \"$PWD/../../fsmonitor-executed\"\nprintf '\\0'\n", encoding="utf-8", newline="\n")
        hook.chmod(0o700)
        configured_hook = '"' + hook.as_posix() + '"'
        with patch.dict(os.environ, {"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.fsmonitor", "GIT_CONFIG_VALUE_0": configured_hook}):
            self.inputs()
        self.assertFalse(marker.exists(), "source admission executed ambient fsmonitor")
        self.git(self.sources / "abstraction-identity", "config", "core.fsmonitor", configured_hook)
        self.inputs()
        self.assertFalse(marker.exists(), "source admission executed repository-local fsmonitor")

    def test_foreign_repository_index_and_config_do_not_redirect_source_admission(self):
        expected = self.inputs()
        foreign = self.sources / "abstraction-router"
        index = self.root / "foreign-index"
        index.write_bytes(b"owned unrelated index fixture")
        config = self.root / "foreign-config"
        config.write_text("[core]\nfsmonitor = unsupported-foreign-monitor\n", encoding="utf-8")
        environment = {"GIT_DIR": str(foreign / ".git"), "GIT_WORK_TREE": str(foreign),
                       "GIT_INDEX_FILE": str(index), "GIT_CONFIG_GLOBAL": str(config)}
        with patch.dict(os.environ, environment), patch.object(owner.subprocess, "run", wraps=subprocess.run) as run:
            self.assertEqual(self.inputs(), expected)
            self.assertEqual({key: os.environ[key] for key in environment}, environment)
        self.assertEqual(len(run.call_args_list), 15)
        self.assertTrue(all(call.kwargs["timeout"] == 30 for call in run.call_args_list))
        self.assertEqual(index.read_bytes(), b"owned unrelated index fixture")

    def test_published_monitor_caller_uses_its_shipped_git_owner(self):
        published = self.root / "published-monitor/macos"
        published.mkdir(parents=True)
        for name in ("build_launcher.py", "git_env.py", "file_boundary.py", "PanelLauncher.swift", "NativePresence.cpp", "NativePresence.h", "NativeHost.cpp", "NativeHost.h"):
            shutil.copyfile(Path(__file__).with_name(name), published / name)
        code = '''import runpy,subprocess,sys
from unittest.mock import patch
sys.path.insert(0,sys.argv[1])
import file_boundary
assert sys._openabstractions_spawn_guard is file_boundary.ExtendedPathSpawnRefused
marked=chr(92)*2+"?"+chr(92)+"owned-fixture"
try: subprocess.run([sys.executable,"-c","raise AssertionError()",marked],check=True)
except file_boundary.ExtendedPathSpawnRefused: pass
else: raise AssertionError("shipped boundary did not refuse extended child argument")
real=subprocess.run
def compiler(command,**kwargs):
    if command[0] in ('clang','clang++','swiftc'): return subprocess.CompletedProcess(command,0)
    return real(command,**kwargs)
sys.argv=['build_launcher.py',*sys.argv[2:]]
with patch('subprocess.run',side_effect=compiler): runpy.run_module('build_launcher',run_name='__main__')
'''
        output = self.root / "published-output/Panel"
        result = subprocess.run([sys.executable, "-I", "-c", code, str(published), "--output", str(output),
                                 "--arch", "arm64", "--minimum", "12.0", "--native-source-root", str(self.sources),
                                 "--native-source-roster", str(self.roster), "--native-provenance", str(self.provenance)],
                                capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        received = json.loads(output.with_name("Panel.native-inputs.json").read_bytes())
        self.assertEqual(received["files"], self.inputs()["files"])
        self.assertEqual(received["launcher_files"]["git_env.py"], owner.hashlib.sha256((published / "git_env.py").read_bytes()).hexdigest())
        self.assertEqual(received["launcher_files"]["file_boundary.py"], owner.hashlib.sha256((published / "file_boundary.py").read_bytes()).hexdigest())
        self.assertFalse((published.parent / "scripts").exists())


if __name__ == "__main__":
    unittest.main()
