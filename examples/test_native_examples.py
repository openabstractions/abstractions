"""Source compilation and inert receiving for native example producer outputs."""
import ctypes as C
from ctypes import wintypes as W
import json
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from personfolders import setUpModule, tearDownModule  # noqa: F401 (per-user folders in a stand-in)


class NativeExampleProducerTests(unittest.TestCase):
    def sanitizer_sources(self):
        from examples import build_native_examples as producer
        actual_root=producer.ROOT
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            calls=[]
            def run(argv,**kwargs):
                calls.append(list(map(str,argv)))
                if '--build' in argv:
                    (Path(argv[2])/'abstraction_facade_appmodel.dll').write_bytes(b'inert library')
            tools={name:'fixture-'+name for name in ('cmake','compiler','linker','ninja')}
            def execute(argv,**kwargs):
                if '--build' in argv:Path(argv[2]).mkdir(parents=True)
                return run(argv,**kwargs)
            with mock.patch.object(producer,'ROOT',root),mock.patch.object(producer,'appmodel_tools',return_value=tools), \
                    mock.patch.object(producer,'run',side_effect=execute), \
                    mock.patch.object(producer,'validate_portable_windows_dll'), \
                    mock.patch.object(producer.shutil,'which',side_effect=lambda name:'fixture-'+name):
                producer.build(root/'output','fixture-go')
            sanitizers=[argv for argv in calls if '-sanitize-coff' in argv]
            self.assertEqual(len(sanitizers),2)
            for argv in sanitizers:
                selected={Path(value).name for value in argv[2:-2]}
                self.assertEqual(selected,{'main.go','native.go','agent_release.go'})
            return [tuple(actual_root/Path(value).relative_to(root) for value in argv[2:-2])
                    for argv in sanitizers]

    def test_each_example_sanitizer_compiles_complete_generator_closure(self):
        self.sanitizer_sources()

    def test_real_caller_resource_source_lists_compile_with_selected_sdk(self):
        if not os.environ.get('OA_WINDOWS_GO_SDK_RECEIPT'):
            if sys.platform == 'win32':
                self.fail('OA_WINDOWS_GO_SDK_RECEIPT is required for Windows resource source compilation')
            self.skipTest('selected Go SDK receipt not supplied')
        root=Path(__file__).resolve().parents[1]
        private=(root/'CLAUDE.md').is_file() and (root/'scripts/split.manifest').is_file()
        sdk_path=root/('scripts/windows_go_sdk.py' if private else 'tools/windows_go_sdk.py')
        spec=importlib.util.spec_from_file_location('example_resource_sdk',sdk_path)
        sdk=importlib.util.module_from_spec(spec);spec.loader.exec_module(sdk)
        receipt=sdk.validate_sdk_receipt(json.loads(Path(os.environ['OA_WINDOWS_GO_SDK_RECEIPT']).read_bytes()))
        environment=sdk.sdk_environment(receipt,os.environ)
        selected=self.sanitizer_sources()
        if private:
            from adopters.comfyui.installer import build_installer as builder
            selected.append(tuple(builder.backend_resource_tool_sources(root/'installer/apps')))
        expected={path.name:path.read_bytes() for path in selected[0]}
        for sources in set(selected):
            self.assertEqual({path.name:path.read_bytes() for path in sources},expected)
            result=subprocess.run([receipt['go'],'run','-trimpath',*map(str,sources),'-h'],
                cwd=root,env=environment,capture_output=True,text=True,timeout=120)
            self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            self.assertIn('-sanitize-coff',result.stderr)
        sdk.validate_sdk_receipt(receipt)

    def test_real_compile_requires_selected_receipt_on_windows(self):
        case=type(self)('test_real_caller_resource_source_lists_compile_with_selected_sdk')
        with mock.patch.dict(os.environ,{},clear=True),mock.patch.object(sys,'platform','win32'):
            result=unittest.TestResult()
            case.run(result)
        self.assertEqual(result.testsRun,1)
        self.assertEqual(len(result.failures),1)
        self.assertFalse(result.errors)
        self.assertFalse(result.skipped)
        self.assertIn('OA_WINDOWS_GO_SDK_RECEIPT is required',result.failures[0][1])

    def test_real_compile_without_receipt_skips_on_other_platforms(self):
        case=type(self)('test_real_caller_resource_source_lists_compile_with_selected_sdk')
        with mock.patch.dict(os.environ,{},clear=True),mock.patch.object(sys,'platform','linux'):
            result=unittest.TestResult()
            case.run(result)
        self.assertEqual(result.testsRun,1)
        self.assertEqual(len(result.skipped),1)
        self.assertFalse(result.errors)
        self.assertFalse(result.failures)


@unittest.skipUnless(sys.platform == 'win32' and os.environ.get('OA_NATIVE_EXAMPLES_DIR'),
                     'normal Windows example producer output not supplied')
class NativeExamples(unittest.TestCase):
    def test_embedded_manifest_and_owned_host(self):
        root = Path(os.environ['OA_NATIVE_EXAMPLES_DIR'])
        kernel = C.WinDLL('kernel32', use_last_error=True)
        kernel.LoadLibraryExW.argtypes = [W.LPCWSTR, W.HANDLE, W.DWORD]
        kernel.LoadLibraryExW.restype = W.HMODULE
        kernel.FindResourceExW.argtypes = [W.HMODULE, W.LPCWSTR, W.LPCWSTR, W.WORD]
        kernel.FindResourceExW.restype = W.HANDLE
        kernel.SizeofResource.argtypes = [W.HMODULE, W.HANDLE]
        kernel.SizeofResource.restype = W.DWORD
        kernel.LoadResource.argtypes = [W.HMODULE, W.HANDLE]
        kernel.LoadResource.restype = W.HANDLE
        kernel.LockResource.argtypes = [W.HANDLE]
        kernel.LockResource.restype = C.c_void_p
        kernel.FreeLibrary.argtypes = [W.HMODULE]
        kernel.FreeLibrary.restype = W.BOOL
        frames = (b'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}\n'
                  b'{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n'
                  b'{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"foreign.command","arguments":{}}}\n')
        for name in ('pretend-lemonade', 'logging-quickstart'):
            with self.subTest(name=name):
                image = root/(name+'.exe')
                # DATA-only resource extraction. This establishes bytes, no signature or authority.
                handle = kernel.LoadLibraryExW(str(image), None, 0x20 | 2)
                self.assertTrue(handle)
                try:
                    resource = kernel.FindResourceExW(handle, C.cast(C.c_void_p(10), W.LPCWSTR), 'OA_APPLICATION_MANIFEST', 0)
                    self.assertTrue(resource)
                    size = kernel.SizeofResource(handle, resource)
                    self.assertTrue(0 < size <= 262144)
                    raw = C.string_at(kernel.LockResource(kernel.LoadResource(handle, resource)), size)
                    self.assertEqual(raw, (Path(__file__).parent/name/'application-manifest.json').read_bytes())
                    manifest = json.loads(raw)
                    self.assertEqual(manifest['offers'], {'version': 'appmodel@1', 'commands': []})
                    self.assertEqual(manifest['profiles'], {'entries': []})
                    self.assertEqual(manifest['host']['arguments'], ['--owned-host'])
                finally:
                    self.assertTrue(kernel.FreeLibrary(handle))
                # Startup announcement requires the signed registration and native
                # directory. Exercise the same host binding in its owning Go
                # corpus; these inert image checks never discover a user runtime.
                for arguments, payload in ((['--owned-host', 'extra'], b''),):
                    refused = subprocess.run([str(image), *arguments], input=payload, capture_output=True, timeout=10)
                    self.assertNotEqual(refused.returncode, 0)
                with tempfile.TemporaryDirectory(prefix='oa-example-missing-native-') as temporary:
                    standin = Path(temporary)/image.name
                    standin.write_bytes(image.read_bytes())
                    refused = subprocess.run([str(standin), '--owned-host'], input=b'', capture_output=True, timeout=10)
                    self.assertNotEqual(refused.returncode, 0, 'missing application-local DLL was accepted')
                    self.assertFalse(refused.stdout)


if __name__ == '__main__':
    unittest.main()
