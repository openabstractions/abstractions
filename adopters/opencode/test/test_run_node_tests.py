"""run_node_tests.py removes only the package links it created."""
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import run_node_tests  # noqa: E402
from personfolders import setUpModule, tearDownModule  # noqa: E402,F401 (per-user folders in a stand-in)


def make_link(target, link):
    """A junction on Windows, a symbolic link elsewhere, as npm makes them."""
    if sys.platform == "win32":
        import _winapi
        _winapi.CreateJunction(str(target), str(link))
    else:
        os.symlink(target, link, target_is_directory=True)


class ServeNativeEnvironmentTests(unittest.TestCase):
    def test_node_prerequisite_runs_the_normal_exact_source_producer(self):
        with patch.object(run_node_tests,'missing',return_value=[]), \
             patch.object(run_node_tests,'package_links',return_value=set()), \
             patch.object(run_node_tests,'remove_created_links',return_value=0), \
             patch.object(run_node_tests,'isolated_environment',return_value={}), \
             patch.object(run_node_tests.sys,'platform','win32'), \
             patch.object(run_node_tests.shutil,'which',return_value='npm'), \
             patch.object(run_node_tests.subprocess,'run',return_value=type('Result',(),{'returncode':0})()) as run:
            self.assertEqual(run_node_tests.main(['--run','--no-install']),0)
        self.assertEqual(run.call_args_list[0].args[0][-2:],['--run','--force'])

    def setUp(self):
        from types import SimpleNamespace
        self.area=tempfile.TemporaryDirectory();self.addCleanup(self.area.cleanup)
        self.root=Path(self.area.name);self.adopter=self.root/'adopters/opencode'
        self.adopter.mkdir(parents=True)
        self.addon=self.root/'oa_ipc_node.node';self.addon.write_bytes(b'inert native output')
        packages={}
        for name in ('@ai-sdk/provider','jsonc-parser','@opencode-ai/plugin','zod'):
            folder=self.adopter/'node_modules'/name;folder.mkdir(parents=True)
            (folder/'package.json').write_text(json.dumps({'name':name,'version':'1.0.0'}))
            packages['node_modules/'+name]={'version':'1.0.0','integrity':'sha512-locked'}
        (self.adopter/'package-lock.json').write_text(json.dumps({'packages':packages}))
        self.native=SimpleNamespace(ROOT=self.root,ADDON=self.addon,current=lambda:True,
            runtime_import_owner=lambda:SimpleNamespace(problems=lambda path:[]),
            validate_native_hosts=lambda path:0)
        self.enterContext(patch.object(run_node_tests,'ROOT',self.root))
        self.enterContext(patch.object(run_node_tests,'ADOPTER',self.adopter))
        self.enterContext(patch.dict(sys.modules,build_node_addon=self.native))

    def test_owned_outputs_replace_ambient_bindings(self):
        env=run_node_tests.serve_native_environment({'ABSTRACTION_IPC_NODE':'ambient-appmodel.node',
            'OA_AI_SDK_PROVIDER_MODULES':'ambient','PRESERVE':'yes'})
        self.assertEqual(env['ABSTRACTION_IPC_NODE'],str(self.addon.resolve()))
        self.assertEqual(env['OA_AI_SDK_PROVIDER_MODULES'],str((self.adopter/'node_modules').resolve()))
        self.assertEqual(env['PRESERVE'],'yes')

    def test_stale_wrong_source_wrong_abi_and_lock_refuse(self):
        self.native.current=lambda:False
        with self.assertRaisesRegex(ValueError,'current source'):run_node_tests.serve_native_environment({})
        self.native.current=lambda:True;self.native.ROOT=self.root/'other'
        with self.assertRaisesRegex(ValueError,'current source'):run_node_tests.serve_native_environment({})
        self.native.ROOT=self.root;self.native.validate_native_hosts=lambda path:1
        with self.assertRaisesRegex(ValueError,'ABI'):run_node_tests.serve_native_environment({})
        self.native.validate_native_hosts=lambda path:0
        manifest=self.adopter/'node_modules/jsonc-parser/package.json'
        manifest.write_text(json.dumps({'name':'jsonc-parser','version':'2.0.0'}))
        with self.assertRaisesRegex(ValueError,'differs from its lock'):run_node_tests.serve_native_environment({})
        manifest.unlink()
        with self.assertRaisesRegex(ValueError,'locked OpenCode module'):run_node_tests.serve_native_environment({})

    def test_actual_zod_shadow_location_must_match_lock_and_package_roots_cannot_link(self):
        modules=self.adopter/'node_modules'
        nested=modules/'@opencode-ai/plugin/node_modules/zod';nested.mkdir(parents=True)
        manifest=nested/'package.json';manifest.write_text(json.dumps({'name':'zod','version':'2.0.0'}))
        with self.assertRaisesRegex(ValueError,'plugin/node_modules/zod'):
            run_node_tests.serve_native_environment({})
        lock=self.adopter/'package-lock.json';data=json.loads(lock.read_text())
        data['packages']['node_modules/@opencode-ai/plugin/node_modules/zod']={'version':'1.0.0','integrity':'sha512-locked'}
        lock.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError,'differs from its lock.*plugin/node_modules/zod'):
            run_node_tests.serve_native_environment({})
        manifest.write_text(json.dumps({'name':'zod','version':'1.0.0'}))
        self.assertEqual(run_node_tests.serve_native_environment({})['OA_AI_SDK_PROVIDER_MODULES'],str(modules.resolve()))
        local=modules/'jsonc-parser';foreign=self.root/'unowned-jsonc'
        local.rename(foreign);make_link(foreign,local)
        self.addCleanup(run_node_tests.remove_link,local)
        with self.assertRaisesRegex(ValueError,'linked or contains a reparse point'):
            run_node_tests.serve_native_environment({})


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
        self.assertIn("@openabstractions/download-request", names)
        self.assertIn("@openabstractions/job-acceptance", names)


class PreparationTests(unittest.TestCase):
    def test_metadata_priming_is_exact_spec_only_and_checks_returned_integrity(self):
        sys.path.insert(0, str(run_node_tests.ROOT / 'scripts'))
        import npm_packages
        entry = {'version':'1.2.3', 'resolved':'https://registry.npmjs.org/@owned/pkg/-/pkg-1.2.3.tgz', 'integrity':'sha512-owned'}
        packages = {'node_modules/@owned/pkg':entry}
        for variant in ('valid', 'offline', 'integrity', 'url', 'name', 'version', 'missing'):
            with self.subTest(variant=variant):
                dist = {'name':'@owned/pkg', 'version':'1.2.3', 'integrity':entry['integrity'], 'tarball':entry['resolved']}
                if variant == 'integrity': dist['integrity'] = 'sha512-wrong'
                if variant == 'url': dist['tarball'] = 'https://registry.npmjs.org/other/-/other-1.2.3.tgz'
                if variant == 'name': dist['name'] = 'wrong'
                if variant == 'version': dist['version'] = '1.2.99'
                with patch.object(run_node_tests, 'prepared_dependency_packages', return_value=({},packages)), \
                     patch.object(npm_packages, 'verify_cached_archives'), \
                     patch.object(npm_packages, 'npm_content_cache', return_value=Path('owned-effective')), \
                     patch.object(npm_packages, 'npm_bundled_modules', return_value=Path('owned-npm')), \
                     patch.object(run_node_tests, 'finish_dependency_cache') as finish, \
                     patch.object(run_node_tests, 'prepare_dependencies') as installer, \
                     patch.object(run_node_tests.subprocess, 'run', return_value=type('Result', (), {'stdout':json.dumps(dist)})(),
                                  side_effect=ValueError('missing metadata') if variant == 'missing' else None) as child:
                    if variant in ('valid', 'offline'):
                        run_node_tests.prime_dependency_metadata(Path('source'),Path('owned'),environment={'OWNED':'fixture'},offline_only=variant=='offline')
                        finish.assert_called_once()
                    else:
                        with self.assertRaises(ValueError):
                            run_node_tests.prime_dependency_metadata(Path('source'),Path('owned'),environment={'OWNED':'fixture'})
                        finish.assert_not_called()
                    installer.assert_not_called()
                    argv = child.call_args.args[0]
                    self.assertEqual(argv[0:2], ['node','-e'])
                    self.assertEqual(argv[3], str(Path('owned-npm/pacote')))
                    self.assertEqual(argv[4:], ['@owned/pkg@1.2.3',str(Path('owned-effective')),'1' if variant in ('valid','offline') else '0','1' if variant in ('valid','offline') else '0'])
                    self.assertEqual(child.call_count, 4 if variant == 'valid' else 2 if variant == 'offline' else 1)
                    self.assertIn('registry:\'https://registry.npmjs.org\'', argv[2])
                    self.assertEqual(child.call_args.kwargs['env'], {'OWNED':'fixture','npm_config_update_notifier':'false'})

    def test_filled_cache_admission_resumes_without_installer_or_download(self):
        sys.path.insert(0, str(run_node_tests.ROOT / 'scripts'))
        import npm_packages
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); source = root / 'input'; source.mkdir()
            destination = root / 'filled'; (destination / 'source').mkdir(parents=True)
            (destination / 'cache').mkdir(); (destination / 'cache/owned').write_bytes(b'owned')
            for name in ('package.json', 'package-lock.json', 'shrinkwrap.js'):
                (source / name).write_text('{}', encoding='utf-8')
                (destination / 'source' / name).write_text('{}', encoding='utf-8')
            with patch.object(run_node_tests, 'prepare_dependencies') as installer, \
                 patch.object(npm_packages, 'verify_cached_archives') as verify, \
                 patch.object(run_node_tests.subprocess, 'run', return_value=type('Result', (), {'stdout':'{"packages":{}}'})()) as child:
                run_node_tests.finish_dependency_cache(source, destination, environment={'OWNED':'fixture'})
            installer.assert_not_called(); self.assertEqual(child.call_count, 1)
            self.assertEqual(child.call_args.args[0][0], 'node')
            self.assertEqual(verify.call_count, 1)
            self.assertTrue((destination / 'receipt.json').is_file())

    def test_preparation_reuses_scripts_disabled_installer_without_test_or_build(self):
        with patch.object(run_node_tests.shutil, 'which', return_value='npm.cmd'), \
             patch.object(run_node_tests.subprocess, 'run') as run:
            run.return_value.returncode = 0
            self.assertEqual(run_node_tests.prepare_dependencies(Path('owned'), Path('repo'),
                             environment={'OWNED': 'fixture'}, linked=('package',)), 0)
        argv = run.call_args.args[0]
        self.assertEqual(argv, ['npm.cmd', 'install', '--ignore-scripts', '--no-save', '--no-audit', '--no-fund', str(Path('repo/package'))])
        self.assertEqual(run.call_args.kwargs['cwd'], Path('owned'))
        self.assertEqual(run.call_count, 1)

    def test_prepare_only_option_does_not_enter_build_or_tests(self):
        with patch.object(run_node_tests, 'prepare_dependency_cache') as prepare, \
             patch.object(run_node_tests.subprocess, 'run') as run:
            self.assertEqual(run_node_tests.main(['--prepare-cache', 'owned']), 0)
        prepare.assert_called_once_with(run_node_tests.ADOPTER, run_node_tests.ROOT, Path('owned'))
        run.assert_not_called()

    def test_full_preparation_seam_pins_source_and_cleans_links_on_failures(self):
        import subprocess
        sys.path.insert(0, str(run_node_tests.ROOT / 'scripts'))
        import npm_packages
        for variant in ('success', 'validator', 'install', 'manifest', 'lock', 'integrity', 'cleanup'):
            with self.subTest(variant=variant), tempfile.TemporaryDirectory() as temp:
                root = Path(temp); source = root / 'input'; source.mkdir()
                for name in ('package.json', 'package-lock.json', 'shrinkwrap.js'):
                    (source / name).write_text('{}', encoding='utf-8')
                destination = root / 'prepared'
                def child(argv, **kwargs):
                    self.assertFalse(any(k.upper() in ('NODE_PATH', 'NODE_OPTIONS') for k in kwargs['env']))
                    self.assertTrue(Path(kwargs['env']['HOME']).is_relative_to(destination))
                    if argv[0] == 'node':
                        if variant == 'validator': raise subprocess.CalledProcessError(1, argv)
                        if 'cwd' in kwargs:
                            (kwargs['cwd'] / 'npm-shrinkwrap.json').write_text(json.dumps({'packages': {}}), encoding='utf-8')
                    return type('Result', (), {'returncode':0, 'stdout':json.dumps({'packages': {}})})()
                def install(adopter, _root, **kwargs):
                    self.assertEqual(kwargs['environment']['npm_config_omit'], 'optional')
                    cache = Path(kwargs['environment']['npm_config_cache']); cache.mkdir()
                    (cache / 'owned').write_bytes(b'cache')
                    if variant in ('manifest', 'lock'):
                        name = 'package.json' if variant == 'manifest' else 'package-lock.json'
                        (adopter / name).write_text('changed', encoding='utf-8')
                    return 1 if variant == 'install' else 0
                with patch.dict(os.environ, {'NODE_OPTIONS':'test-owned-preload', 'NODE_PATH':'test-owned-path'}), \
                     patch.object(run_node_tests.subprocess, 'run', side_effect=child), \
                     patch.object(run_node_tests, 'prepare_dependencies', side_effect=install), \
                     patch.object(run_node_tests, 'remove_created_links', return_value=1 if variant == 'cleanup' else 0) as cleanup, \
                     patch.object(npm_packages, 'npm_content_cache', return_value=Path('owned-effective')), \
                     patch.object(npm_packages, 'verify_cached_archives', side_effect=ValueError('incomplete') if variant == 'integrity' else None):
                    if variant == 'success':
                        run_node_tests.prepare_dependency_cache(source, run_node_tests.ROOT, destination)
                        self.assertTrue((destination / 'receipt.json').is_file())
                    else:
                        with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                            run_node_tests.prepare_dependency_cache(source, run_node_tests.ROOT, destination)
                        self.assertFalse((destination / 'receipt.json').exists())
                    self.assertEqual(cleanup.call_count, 0 if variant == 'validator' else 1)

    # 2026-11-01: a plain test run reached the installed runtime and raised a
    # real first-use question on the owner's screen. No test child may run
    # without an unserved or fixture endpoint.
    def test_isolated_environment_names_an_unserved_endpoint_and_refuses_the_installed_one(self):
        windows = run_node_tests.isolated_environment({'PATH': 'p'}, platform='win32', pid=42)
        self.assertEqual(windows['ABSTRACTION_RUNTIME_ENDPOINT'], '\\\\.\\pipe\\oa-opencode-test-unserved-42')
        self.assertEqual(windows['PATH'], 'p')
        unix = run_node_tests.isolated_environment({}, platform='linux', pid=42)
        self.assertTrue(unix['ABSTRACTION_RUNTIME_ENDPOINT'].endswith('oa-opencode-test-unserved-42.sock'))
        fixture = {'ABSTRACTION_RUNTIME_ENDPOINT': '\\\\.\\pipe\\oa-fixture-7'}
        self.assertEqual(run_node_tests.isolated_environment(fixture), fixture)
        for installed in ('\\\\.\\pipe\\openabstractions-user-S-1-5-21-1-2-3-1001-runtime-v1',
                          '/run/user/1000/openabstractions-user-runtime-v1.sock'):
            self.assertIsNone(run_node_tests.isolated_environment({'ABSTRACTION_RUNTIME_ENDPOINT': installed}))

    def test_every_test_child_runs_isolated(self):
        with patch.object(run_node_tests.shutil, 'which', return_value='npm.cmd'), \
             patch.object(run_node_tests, 'missing', return_value=[]), \
             patch.object(run_node_tests, 'package_links', return_value=set()), \
             patch.object(run_node_tests, 'remove_created_links', return_value=False), \
             patch.dict(run_node_tests.os.environ, {}, clear=False), \
             patch.object(run_node_tests.subprocess, 'run') as run:
            run_node_tests.os.environ.pop('ABSTRACTION_RUNTIME_ENDPOINT', None)
            run.return_value.returncode = 0
            self.assertEqual(run_node_tests.main(['--run', '--also', 'adopters/opencode/installer/test_config_lifecycle.mjs']), 0)
        tests = [call for call in run.call_args_list if 'test' in call.args[0] or '--test' in call.args[0]]
        self.assertEqual(len(tests), 2)
        for call in tests:
            endpoint = call.kwargs['env']['ABSTRACTION_RUNTIME_ENDPOINT']
            self.assertIn('oa-opencode-test-unserved-', endpoint)
        self.assertEqual(tests[1].args[0][:3], ['node', '--import', './runtime-isolation.mjs'])
        with patch.object(run_node_tests.shutil, 'which', return_value='npm.cmd'), \
             patch.object(run_node_tests, 'missing', return_value=[]), \
             patch.object(run_node_tests, 'package_links', return_value=set()), \
             patch.object(run_node_tests, 'remove_created_links', return_value=False), \
             patch.dict(run_node_tests.os.environ, {'ABSTRACTION_RUNTIME_ENDPOINT': '\\\\.\\pipe\\openabstractions-user-S-1-runtime-v1'}), \
             patch.object(run_node_tests.subprocess, 'run') as run:
            run.return_value.returncode = 0
            self.assertEqual(run_node_tests.main(['--run']), 2)
        self.assertFalse(any('test' in call.args[0] for call in run.call_args_list))

    @unittest.skipUnless(shutil.which("node"), "Node is required for the test launcher fixture")
    def test_npm_test_loads_isolation_and_selects_tests_without_running_fixtures(self):
        scripts = json.loads((run_node_tests.ADOPTER / 'package.json').read_text(encoding='utf-8'))['scripts']
        self.assertEqual(scripts['test'], 'node ./test/run-node-tests.mjs')
        with tempfile.TemporaryDirectory() as area:
            root = Path(area)
            (root / "test/fixtures").mkdir(parents=True)
            (root / "test/nested").mkdir()
            shutil.copyfile(run_node_tests.ADOPTER / "test/run-node-tests.mjs", root / "test/run-node-tests.mjs")
            (root / "runtime-isolation.mjs").write_text("globalThis.ownedPreload = true;", encoding="utf-8")
            for name in ("root.test.mjs", "test/nested/child.test.mjs"):
                (root / name).write_text(
                    "if (!globalThis.ownedPreload || !process.execArgv.includes('--preserve-symlinks')) process.exit(7);\n"
                    f"console.log('ran {name}');\n", encoding="utf-8")
            for name in ("test/fixtures/upstream.test.mjs", "test/helper.mjs"):
                (root / name).write_text("process.exit(9);", encoding="utf-8")
            child = subprocess.run(["node", "./test/run-node-tests.mjs"], cwd=root, capture_output=True, text=True,
                                   encoding="utf-8", timeout=30, check=True)
        self.assertIn("ran root.test.mjs", child.stdout)
        self.assertIn("ran test/nested/child.test.mjs", child.stdout)


if __name__ == "__main__":
    unittest.main()
