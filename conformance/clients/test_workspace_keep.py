"""The build tree a runner's --keep selects: kept, refused when occupied, temporary otherwise."""
from pathlib import Path
import ast
import json
import os
import shutil
import runpy
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import workspace
from workspace import build_tree
from personfolders import setUpModule, tearDownModule  # noqa: E402,F401 (per-user folders in a stand-in)


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


class NativeRustSources(unittest.TestCase):
    def test_private_runtime_hosts_select_config_before_listen(self):
        for runner, name, observation in (
                ('rust_services', 'host.go', 'configservice.FileObservation(key)'),
                ('rust_jobs', 'fixture_test.go', 'configservice.FileObservation(options.ConfigUserKey)'),
                ('rust_config_asks', 'fixture_test.go', 'ConfigObservationSource: func(context.Context)'),
                ('rust_capabilities', 'fixture_test.go', 'configservice.FileObservation(filepath.Join(dir, "config", "user.json"))')):
            with self.subTest(runner=runner):
                source = (HERE / runner / name).read_text(encoding='utf-8')
                self.assertIn('casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}', source)
                self.assertIn('ConfigUserKey', source)
                self.assertRegex(source, r'ConfigWithoutMachine\s*[:=]\s*true')
                self.assertIn(observation, source)
                self.assertLess(source.index('ConfigUserKey'), source.index('.Listen('))
        peer = (HERE / 'rust_shared/host.go').read_text(encoding='utf-8')
        self.assertNotIn('host.Listen(', peer)  # raw transport peer has no config service
        receiver = (HERE / 'rust_services/run.py').read_text(encoding='utf-8')
        self.assertLess(receiver.index("HERE/'host_test.go'"), receiver.index("['go','build','-o',host"))

    def test_all_native_runners_stage_the_generated_include(self):
        source_identity = workspace.layer('abstraction-identity')
        for runner in ('rust_services', 'rust_jobs', 'rust_config_asks', 'rust_capabilities', 'rust_shared'):
            with self.subTest(runner=runner), tempfile.TemporaryDirectory() as temporary:
                base = Path(temporary)
                tree = base / 'source'
                syntax = ast.parse((HERE / runner / 'run.py').read_text(encoding='utf-8'))
                calls = [node for node in ast.walk(syntax) if isinstance(node, ast.Call)
                         and isinstance(node.func, ast.Name) and node.func.id == 'copy_native_rust_identity']
                self.assertEqual(len(calls), 1, 'one actual native staging call per runner')
                scope = runpy.run_path(str(HERE / runner / 'run.py'))
                scope.update(tree=tree, base=base)
                exec(compile(ast.fix_missing_locations(ast.Module(body=[ast.Expr(calls[0])], type_ignores=[])), '<native source staging>', 'exec'), scope)
                crate = base / 'ipc' if runner == 'rust_shared' else tree / 'openabstractions-flat/abstraction-identity/rust'
                include = crate / 'src/../../rs/abstraction/ipc/constants/rec.rs'
                self.assertEqual(include.read_bytes(), (source_identity / 'rs' / 'abstraction' / 'ipc' / 'constants' / 'rec.rs').read_bytes())
                self.assertEqual((crate / 'src/lib.rs').read_bytes(), (source_identity / 'rust' / 'src' / 'lib.rs').read_bytes())
                if runner in ('rust_jobs', 'rust_config_asks', 'rust_capabilities'):
                    pure_refusals = [n for n in ast.walk(syntax) if isinstance(n, ast.Assert)
                                     and 'abstraction-identity/rust' in ast.unparse(n.test)]
                    self.assertEqual(len(pure_refusals), 1)
                    self.assertLess(pure_refusals[0].lineno, calls[0].lineno)

    def test_missing_canonical_constants_refuses_before_copy(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'identity/rust/src').mkdir(parents=True)
            (root / 'identity/rust/src/lib.rs').write_text('source', encoding='utf-8')
            with self.assertRaises(FileNotFoundError):
                workspace.copy_native_rust_identity(root / 'identity', root / 'stage/ipc')
            self.assertFalse((root / 'stage').exists())


class InstalledJavaScriptDependencies(unittest.TestCase):
    def package(self, outside, name, dependencies=None):
        path = outside / 'node_modules/@openabstractions' / name
        path.mkdir(parents=True)
        url = 'https://github.com/openabstractions/abstraction-' + name
        metadata = dict(name='@openabstractions/' + name, version='0.0.0', license='Apache-2.0',
                        description='A source conformance fixture package', repository=dict(type='git', url='git+' + url + '.git', directory='javascript'),
                        homepage=url, bugs=dict(url=url + '/issues'), types='./index.d.mts',
                        exports={'.': dict(types='./index.d.mts', default='./index.mjs')}, dependencies=dependencies or {})
        (path / 'package.json').write_text(json.dumps(metadata), encoding='utf-8')
        (path / 'index.d.mts').write_text('export {};', encoding='utf-8')
        (path / 'index.mjs').write_text('export {};', encoding='utf-8')
        return path

    def scan(self, outside):
        env = dict(os.environ)
        for name in ('NODE_OPTIONS', 'NODE_PATH'):
            env.pop(name, None)
        return subprocess.run(['node', str(HERE / 'js_services/packages.mjs')], cwd=outside,
                              env=env, capture_output=True, text=True, timeout=30)

    def test_runtime_and_declaration_union_and_nested_modules(self):
        with tempfile.TemporaryDirectory() as temporary:
            outside = Path(temporary)
            facade = self.package(outside, 'facade', {'@openabstractions/router': '0.0.0', '@openabstractions/logging': '0.0.0'})
            self.package(outside, 'router')
            self.package(outside, 'logging')
            (facade / 'index.d.mts').write_text('import type { Placement } from "@openabstractions/router";\nexport type { Placement } from "@openabstractions/router";', encoding='utf-8')
            (facade / 'index.mjs').write_text('import { x } from "@openabstractions/logging/internal";\nimport fs from "node:fs";\nimport local from "./local.js";', encoding='utf-8')
            nested = facade / 'node_modules/foreign'
            nested.mkdir(parents=True)
            (nested / 'index.js').write_text('import x from "unrelated";', encoding='utf-8')
            result = self.scan(outside)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_missing_type_only_dependency_and_unused_dependency_refuse(self):
        for declared in ({}, {'@openabstractions/router': '0.0.0', '@openabstractions/unused': '0.0.0'}):
            with self.subTest(declared=declared), tempfile.TemporaryDirectory() as temporary:
                outside = Path(temporary)
                facade = self.package(outside, 'facade', declared)
                self.package(outside, 'router')
                (facade / 'index.d.mts').write_text('import type { Placement } from "@openabstractions/router";', encoding='utf-8')
                result = self.scan(outside)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('dependencies match', result.stderr)

    def test_declaration_ts_reexport_requires_its_dependency(self):
        with tempfile.TemporaryDirectory() as temporary:
            outside = Path(temporary)
            facade = self.package(outside, 'facade', {'@openabstractions/router': '0.0.0'})
            self.package(outside, 'router')
            (facade / 'legacy.d.ts').write_text('export type { Placement } from "@openabstractions/router";', encoding='utf-8')
            result = self.scan(outside)
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
