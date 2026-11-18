"""Owned release-stage cleanup across Windows path and link boundaries."""

import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.file_boundary import extended_path, remove_tree, relative_beneath_existing, windows_build_environment, same_existing_file
from scripts import file_boundary as boundary
sys.path.insert(0, str(Path(__file__).resolve().parent))
from personfolders import setUpModule, tearDownModule  # noqa: F401

class SpawnGuardTests(unittest.TestCase):
    def setUp(self):
        self.extended = "\\\\?\\C:\\private-guard-fixture"

    def probe(self, body, *arguments):
        # Each probe owns its irreversible audit hook and any Python children.
        prelude = ("import os, pathlib, subprocess, sys\n"
                   "sys.path.insert(0, sys.argv[1])\n")
        return subprocess.run([sys.executable, '-I', '-B', '-c', prelude + body,
                               str(Path(__file__).resolve().parents[1]), *arguments],
                              capture_output=True, text=True, timeout=20)

    def test_plain_path_preserves_child_spelling(self):
        for source, expected in ((r'\\?\C:\a b', r'C:\a b'),
                                 (r'\\?\UNC\server\share\a', r'\\server\share\a'),
                                 (r'C:\plain', r'C:\plain')):
            self.assertEqual(str(boundary.plain_path(source)), expected)

    def test_argv_forms_refuse_with_positions_without_values(self):
        for argument in (self.extended, self.extended.encode(), Path(self.extended), r'\??\C:\secret'):
            for argv in ([sys.executable, argument], (sys.executable, argument)):
                reason = boundary.spawn_refusal('subprocess.Popen', (None, argv, None, {}))
                self.assertIn('argument 1', reason)
                self.assertNotIn('private-guard-fixture', reason)
                self.assertNotIn('secret', reason)
        reason = boundary.spawn_refusal('subprocess.Popen', (self.extended, [], self.extended, {}))
        self.assertIn('executable', reason)
        self.assertIn('working directory', reason)
        self.assertNotIn(self.extended, reason)

    @unittest.skipUnless(os.name == 'nt', 'Windows command-line API')
    def test_windows_command_line_uses_system_parser_and_redacts_values(self):
        argv = ['program with spaces.exe', 'ordinary "quoted" value', self.extended, 'C:\\plain\\']
        self.assertEqual(boundary._command_line_arguments(subprocess.list2cmdline(argv)), argv)
        reason = boundary.spawn_refusal('subprocess.Popen', (None, subprocess.list2cmdline(argv), None, {}))
        self.assertIn('argument 2', reason)
        self.assertNotIn('ordinary', reason)
        self.assertNotIn('private-guard-fixture', reason)

    def test_event_shapes_and_unrelated_events(self):
        cases = [('os.system', (self.extended,)), ('os.exec', (self.extended, [], {})),
                 ('os.posix_spawn', ('plain', [self.extended], {})),
                 ('os.spawn', (0, 'plain', [self.extended], {})),
                 ('os.startfile', (self.extended,)),
                 ('os.startfile/2', ('plain', None, None, self.extended, 1)),
                 ('_winapi.CreateProcess', (self.extended, 'unreliable', None))]
        for event, arguments in cases:
            with self.subTest(event=event):
                self.assertIn(event, boundary.spawn_refusal(event, arguments))
        self.assertIsNone(boundary.spawn_refusal('open', (self.extended, 'r', 0)))
        # Explicitly retain CPython 3.12's direct CreateProcess argv limitation.
        self.assertIsNone(boundary.spawn_refusal('_winapi.CreateProcess', ('plain', self.extended, None),
                                               inherited=boundary.environment_entries(os.environ)))

    def test_inherited_environment_is_variable_bound_and_redacted(self):
        inherited = boundary.environment_entries({'PATH': self.extended})
        self.assertIsNone(boundary.spawn_refusal('subprocess.Popen', (None, [], None, {'PATH': self.extended}), inherited))
        if os.name == 'nt':
            self.assertIsNone(boundary.spawn_refusal('subprocess.Popen', (None, [], None, {'Path': self.extended}), inherited))
        for env in ({'OTHER': self.extended}, {'PATH': self.extended + os.pathsep + r'\\?\C:\added'}):
            reason = boundary.spawn_refusal('subprocess.Popen', (None, [], None, env), inherited)
            self.assertIn('environment variable', reason)
            self.assertNotIn('private-guard-fixture', reason)
            self.assertNotIn('added', reason)

    def test_extended_inputs_stop_before_owned_marker_child(self):
        body = ("from scripts import file_boundary as b\n"
                "root = pathlib.Path(sys.argv[2]); marker = root/'started'\n"
                "argv = [sys.executable, '-I', '-B', '-c', 'import pathlib,sys;pathlib.Path(sys.argv[1]).touch()', str(marker)]\n"
                "bad = chr(92)*2+'?'+chr(92)+'C:'+chr(92)+'sensitive'\n"
                "for kw, extra in [({}, [bad]), ({'cwd':bad}, []), ({'env':dict(os.environ, OA_GUARD_SECRET=bad)}, [])]:\n"
                " try: subprocess.run(argv+extra, check=True, **kw)\n"
                " except b.ExtendedPathSpawnRefused as e:\n"
                "  assert 'sensitive' not in str(e)\n"
                " else: raise AssertionError('guard did not refuse')\n"
                "assert not marker.exists()\n"
                "subprocess.run(argv+[str(b.plain_path(b.extended_path(root)))], check=True)\n"
                "assert marker.is_file()\n")
        with tempfile.TemporaryDirectory(prefix='oa guard marker ') as temporary:
            done = self.probe(body, temporary)
            self.assertEqual(done.returncode, 0, done.stderr)

    def test_first_import_snapshot_and_cleaned_popen_environment(self):
        body = ("bad = chr(92)*2+'?'+chr(92)+'C:'+chr(92)+'session-private'\n"
                "os.environ['OA_SESSION'] = bad\n"
                "from scripts import file_boundary as b\n"
                "cmd = [sys.executable, '-I', '-B', '-c', 'pass']\n"
                "subprocess.run(cmd, check=True)\n"
                "try: subprocess.run(cmd, env=dict(os.environ, OA_COPY=bad), check=True)\n"
                "except b.ExtendedPathSpawnRefused: pass\n"
                "else: raise AssertionError('copied session entry admitted')\n"
                "os.environ['OA_ADDED'] = bad\n"
                "try: subprocess.run(cmd, check=True)\n"
                "except b.ExtendedPathSpawnRefused: pass\n"
                "else: raise AssertionError('added entry admitted')\n"
                "clean = dict(os.environ); del clean['OA_ADDED']\n"
                "subprocess.run(cmd, env=clean, check=True)\n")
        done = self.probe(body)
        self.assertEqual(done.returncode, 0, done.stderr)

    def test_both_import_names_install_one_hook_and_share_refusal(self):
        body = ("sys.path.insert(0, str(pathlib.Path(sys.argv[1])/'scripts'))\n"
                "calls=[]; real=sys.addaudithook\n"
                "def add(hook): calls.append(hook); real(hook)\n"
                "sys.addaudithook=add\n"
                "import file_boundary as a\n"
                "import scripts.file_boundary as b\n"
                "a.install_spawn_guard(); b.install_spawn_guard()\n"
                "assert len(calls)==1 and a.ExtendedPathSpawnRefused is b.ExtendedPathSpawnRefused\n"
                "bad=chr(92)*2+'?'+chr(92)+'C:'\n"
                "try: subprocess.run([sys.executable,'-c','pass',bad])\n"
                "except b.ExtendedPathSpawnRefused: pass\n"
                "else: raise AssertionError('second alias lost guard')\n")
        done = self.probe(body)
        self.assertEqual(done.returncode, 0, done.stderr)

    def test_actual_system_and_spawn_are_refused(self):
        body = ("from scripts import file_boundary as b\n"
                "bad=chr(92)*2+'?'+chr(92)+'C:'\n"
                "for call in [lambda:os.system(bad), lambda:os.spawnv(os.P_WAIT,sys.executable,[sys.executable,'-c','pass',bad])]:\n"
                " try: call()\n"
                " except b.ExtendedPathSpawnRefused: pass\n"
                " else: raise AssertionError('native event admitted')\n")
        done = self.probe(body)
        self.assertEqual(done.returncode, 0, done.stderr)

    def test_refusal_is_not_a_missing_tool_error(self):
        self.assertTrue(issubclass(boundary.ExtendedPathSpawnRefused, subprocess.SubprocessError))
        self.assertFalse(issubclass(boundary.ExtendedPathSpawnRefused, OSError))


@unittest.skipUnless(os.name == 'nt', 'selected Windows build Python role')
class SelectedPythonRoleTests(unittest.TestCase):
    def setUp(self):
        self.area = tempfile.TemporaryDirectory(prefix='oa-selected-python-')
        self.addCleanup(self.area.cleanup)
        self.root = Path(self.area.name)
        self.python = self.root/'python.exe'; self.python.write_bytes(b'inert selected Python')
        self.expected = self.checksum(self.python)

    @staticmethod
    def checksum(path):
        import hashlib
        raw = Path(path).read_bytes()
        return len(raw), hashlib.sha256(raw).hexdigest()

    def refusal(self, python, *, extra=(), cwd=None, env=None):
        return boundary.spawn_refusal('subprocess.Popen',
            (str(python), [str(python), '-I', '-S', *extra], cwd, {} if env is None else env))

    def test_selected_role_admits_only_executable_and_first_argument(self):
        with boundary._selected_python_executable(self.python, self.expected, self.checksum) as python:
            self.assertTrue(same_existing_file(self.python, python))
            self.assertIsNone(self.refusal(python, cwd=str(self.root)))
            line = subprocess.list2cmdline([str(python), '-I', '-S'])
            self.assertIsNone(boundary.spawn_refusal('subprocess.Popen', (None, line, str(self.root), {})))
            for options in ({'extra':(str(python),)}, {'cwd':str(extended_path(self.root))},
                            {'env':{'TEMP':str(extended_path(self.root))}}):
                with self.subTest(options=options): self.assertIsNotNone(self.refusal(python, **options))
            self.assertIsNotNone(self.refusal(extended_path(self.root/'other-python.exe')))
            self.assertIsNotNone(boundary.spawn_refusal('_winapi.CreateProcess', (str(python), '', None)))
        self.assertIsNotNone(self.refusal(python))

    def test_selected_role_rechecks_hash_and_identity_and_rejects_bad_selection(self):
        for expected in ((self.expected[0]+1,self.expected[1]), (self.expected[0],'0'*64)):
            with self.subTest(expected=expected), self.assertRaises(ValueError):
                with boundary._selected_python_executable(self.python, expected, self.checksum): pass
        with boundary._selected_python_executable(self.python, self.expected, self.checksum) as python:
            before = self.python.stat()
            self.python.write_bytes(b'INERT selected Python')
            os.utime(self.python, ns=(before.st_atime_ns, before.st_mtime_ns))
            with self.assertRaises(ValueError): self.refusal(python)
        self.assertIsNotNone(self.refusal(python))
        with self.assertRaises(ValueError):
            with boundary._selected_python_executable(Path('python.exe'), self.expected, self.checksum): pass
        alias = self.root/'alias'; alias.mkdir()
        os.link(self.python, alias/'python.exe')
        with self.assertRaises(ValueError):
            with boundary._selected_python_executable(alias/'python.exe', self.checksum(self.python), self.checksum): pass

    def test_selected_role_refuses_reparse_ancestry_and_replaced_identity(self):
        from types import SimpleNamespace
        original = Path.lstat
        ancestor = extended_path(self.root)
        def linked(path, *args, **kwargs):
            if path == ancestor:
                return SimpleNamespace(st_mode=stat.S_IFDIR, st_file_attributes=stat.FILE_ATTRIBUTE_REPARSE_POINT)
            return original(path, *args, **kwargs)
        with patch.object(Path, 'lstat', linked), self.assertRaisesRegex(ValueError, 'linked'):
            with boundary._selected_python_executable(self.python, self.expected, self.checksum): pass
        other = self.root/'other.exe'; other.write_bytes(self.python.read_bytes())
        real_stat = Path.stat
        with boundary._selected_python_executable(self.python, self.expected, self.checksum) as python:
            with patch.object(Path, 'stat', lambda path, *a, **kw: real_stat(other) if path == python else real_stat(path, *a, **kw)):
                with self.assertRaisesRegex(ValueError, 'changed'): self.refusal(python)
            with patch.object(Path, 'lstat', linked), self.assertRaisesRegex(ValueError, 'linked'):
                self.refusal(python)

    def test_selected_role_is_not_nested_shared_with_threads_or_leaked_on_error(self):
        import threading
        with self.assertRaises(KeyboardInterrupt):
            with boundary._selected_python_executable(self.python, self.expected, self.checksum) as python:
                with self.assertRaises(ValueError):
                    with boundary._selected_python_executable(self.python, self.expected, self.checksum): pass
                observations = []
                worker = threading.Thread(target=lambda: observations.append(self.refusal(python)))
                worker.start(); worker.join()
                self.assertIsNotNone(observations[0])
                self.assertIsNone(self.refusal(python))
                raise KeyboardInterrupt()
        self.assertIsNotNone(self.refusal(python))

    def test_import_aliases_share_scoped_role_and_clear_judged_spawn_on_exit(self):
        import file_boundary as alias
        self.assertIs(alias._python_role, boundary._python_role)
        self.assertIs(alias._judged_popen, boundary._judged_popen)
        with alias._selected_python_executable(self.python, self.expected, self.checksum) as python:
            self.assertIsNone(self.refusal(python))
            boundary._judged_popen.value = ('fixture executable', 'fixture cwd')
        self.assertIsNone(boundary._judged_popen.value)
        self.assertIsNotNone(self.refusal(python))


class FileBoundaryTests(unittest.TestCase):
    def test_outside_identity_is_distinct_from_malformed_or_unreadable_names(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)/'root';root.mkdir();outside=Path(temporary)/'outside';outside.mkdir()
            with self.assertRaises(boundary.OutsideRoot):boundary.relative_beneath_missing(outside/'leaf',root)
            with self.assertRaises(ValueError) as malformed:boundary.relative_beneath_missing(root/'../outside',root)
            self.assertNotIsInstance(malformed.exception,boundary.OutsideRoot)
            with patch('scripts.file_boundary.os.path.samefile',side_effect=PermissionError('denied')):
                with self.assertRaises(PermissionError):boundary.relative_beneath_missing(root/'leaf',root)

    def test_missing_child_uses_existing_boundary_and_preserves_failures(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)/'root'; root.mkdir()
            target = root/'missing'/'leaf'
            self.assertEqual(boundary.missing_child(root, 'missing/leaf'), target)
            self.assertFalse(target.exists())
            for member in ('../outside', '.', ''):
                with self.subTest(member=member), self.assertRaises(ValueError):
                    boundary.missing_child(root, member)
            with self.assertRaises(FileNotFoundError):
                boundary.missing_child(root/'absent', 'leaf')
            with patch('scripts.file_boundary.os.path.samefile', side_effect=PermissionError('denied')):
                with self.assertRaises(PermissionError):
                    boundary.missing_child(root, 'missing/leaf')
            file = root/'file'; file.write_bytes(b'x')
            with self.assertRaises((ValueError, NotADirectoryError)):
                boundary.missing_child(root, 'file/leaf')

    def test_missing_child_refuses_linked_ancestor_and_case_twin_escape(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)/'CaseRoot'; root.mkdir()
            twin = Path(temporary)/'caseroot'
            try:
                twin.mkdir()
            except FileExistsError:
                self.assertEqual(boundary.relative_beneath_missing(twin/'leaf', root), Path('leaf'))
            else:
                with self.assertRaises(ValueError):
                    boundary.relative_beneath_missing(twin/'leaf', root)
            from types import SimpleNamespace
            original = Path.lstat
            linked = boundary.extended_path(root)
            def observe(path, *args, **kwargs):
                if path == linked:
                    return SimpleNamespace(st_mode=stat.S_IFDIR, st_file_attributes=0x400)
                return original(path, *args, **kwargs)
            with patch.object(Path, 'lstat', observe), self.assertRaisesRegex(ValueError, 'reparse'):
                boundary.missing_child(root, 'missing/leaf')

    def test_ordinary_path_checks_leaf_type_and_all_ancestors(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); file = root/'input'; file.write_bytes(b'x')
            boundary.ordinary_path(file, kind='file')
            with self.assertRaises(ValueError):
                boundary.ordinary_path(root, kind='file')
            with self.assertRaises(ValueError):
                boundary.ordinary_path(file, kind='directory')
            with patch.object(Path, 'lstat', side_effect=PermissionError('denied')):
                with self.assertRaises(PermissionError):
                    boundary.ordinary_path(file, kind='file')
            with patch.object(Path, 'lstat', side_effect=FileNotFoundError('absent volume')):
                with self.assertRaises(FileNotFoundError):
                    boundary.ordinary_path(file, allow_missing=True)

    @unittest.skipUnless(os.name == 'nt', 'Windows deep-path temporary directory')
    def test_deep_windows_environment_checks_local_path_and_returns_ordinary_spelling(self):
        with tempfile.TemporaryDirectory() as temporary:
            deep = extended_path(Path(temporary)/('a'*90)/('b'*90)/('c'*90))
            deep.mkdir(parents=True)
            env = windows_build_environment(deep)
            self.assertEqual(env['TEMP'], str(boundary.plain_path(deep)))
            self.assertTrue(all(value == str(boundary.plain_path(value)) for value in env.values()))
            remove_tree(Path(temporary)/('a'*90))
    def test_existing_file_equality_uses_identity_and_preserves_errors(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, copy, alias = root/'source', root/'copy', root/'alias'
            source.write_bytes(b'equal bytes'); copy.write_bytes(b'equal bytes')
            os.link(source, alias)
            self.assertTrue(same_existing_file(source, source))
            self.assertTrue(same_existing_file(source, alias))
            self.assertFalse(same_existing_file(source, copy))
            with self.assertRaises(FileNotFoundError):
                same_existing_file(source, root/'missing')
            with patch('scripts.file_boundary.os.path.samefile', side_effect=PermissionError('denied')):
                with self.assertRaises(PermissionError):
                    same_existing_file(source, copy)

    @unittest.skipUnless(os.name=='nt','Windows native directory owner')
    def test_windows_build_environment_uses_only_native_facts_and_owned_temp(self):
        import ctypes
        from unittest.mock import Mock
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);windows=root/'actual-windows';windows.mkdir()
            def query(buffer,length):buffer.value=str(windows);return len(str(windows))
            fake=Mock(GetWindowsDirectoryW=query)
            hostile={key:'C:/foreign' for key in ('SystemRoot','WINDIR','PATH','COMSPEC','PYTHONHOME',
                'LD_PRELOAD','DYLD_INSERT_LIBRARIES')}
            with patch.dict(os.environ,hostile),patch.object(ctypes,'WinDLL',return_value=fake) as load:
                self.assertEqual(windows_build_environment(root),{'SystemRoot':str(windows),'WINDIR':str(windows),
                    'PATH':str(windows/'System32'),'TEMP':str(root),'TMP':str(root)})
                load.assert_called_once_with('kernel32.dll',use_last_error=True,winmode=0x800)

    @unittest.skipUnless(os.name=='nt','Windows native directory owner')
    def test_windows_build_environment_refuses_failed_or_foreign_native_proof(self):
        import ctypes
        from unittest.mock import Mock
        with tempfile.TemporaryDirectory() as temporary:
            for length,value in ((0,''),(32768,''),(8,'relative')):
                def query(buffer,size):buffer.value=value;return length
                with self.subTest(length=length),patch.object(ctypes,'WinDLL',return_value=Mock(GetWindowsDirectoryW=query)):
                    with self.assertRaisesRegex(ValueError,'Windows directory proof'):
                        windows_build_environment(Path(temporary))

    def test_existing_ancestry_distinguishes_names_of_a_hardlinked_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'root'; root.mkdir()
            inside = root / 'nested' / 'data'; inside.parent.mkdir(); inside.write_bytes(b'input')
            outside = Path(temporary) / 'outside'
            os.link(inside, outside)
            self.assertEqual(relative_beneath_existing(inside, root), Path('nested/data'))
            with self.assertRaisesRegex(ValueError, 'outside'):
                relative_beneath_existing(outside, root)
            self.assertEqual(relative_beneath_existing(root, root), Path('.'))

    def test_ordinary_existing_file_rejects_an_ancestor_link(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'root'
            root.mkdir()
            member = root / 'data'
            member.write_bytes(b'input')
            self.assertEqual(relative_beneath_existing(member, root, ordinary_file=True), Path('data'))
            alias = Path(temporary) / 'alias'
            try:
                alias.symlink_to(root, target_is_directory=True)
            except (OSError, NotImplementedError):
                self.skipTest('directory symlink creation unavailable')
            with self.assertRaisesRegex(ValueError, 'linked|reparse'):
                relative_beneath_existing(alias / 'data', root, ordinary_file=True)
            with self.assertRaisesRegex(ValueError, 'linked|reparse'):
                relative_beneath_existing(member, alias, ordinary_file=True)

    def test_case_twins_and_same_directory_alias_follow_filesystem_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'CaseRoot'; root.mkdir()
            twin = Path(temporary) / 'caseroot'
            try:
                twin.mkdir()
            except FileExistsError:
                self.assertEqual(relative_beneath_existing(root, twin), Path('.'))
            else:
                (twin / 'data').write_bytes(b'other')
                with self.assertRaises(ValueError):
                    relative_beneath_existing(twin / 'data', root)
    def test_same_directory_alias_uses_existing_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'root'; root.mkdir()
            alias = Path(temporary) / 'alias'
            try:
                alias.symlink_to(root, target_is_directory=True)
            except (OSError, NotImplementedError):
                self.skipTest('directory symlink creation unavailable; Windows case alias is covered separately')
            self.assertEqual(relative_beneath_existing(alias, root), Path('.'))

    def test_parent_parts_are_refused_before_the_walk(self):
        # Through the root's own name, a .. step could reach a file outside it
        # and the name walk would pass the root on the way.
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'root'; (root / 'inner').mkdir(parents=True)
            outside = Path(temporary) / 'outside'; outside.mkdir()
            (outside / 'data').write_bytes(b'outside')
            for path, boundary in ((root / 'inner' / '..' / '..' / 'outside' / 'data', root),
                                   (root / 'inner' / '..', root),
                                   (root / 'inner', root / 'inner' / '..')):
                with self.subTest(path=str(path), root=str(boundary)):
                    with self.assertRaisesRegex(ValueError, 'parent'):
                        relative_beneath_existing(path, boundary)
            self.assertEqual(relative_beneath_existing(root / 'inner', root), Path('inner'))

    def test_missing_and_identity_errors_are_not_outside_or_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaises(FileNotFoundError):
                relative_beneath_existing(root / 'missing', root)
            with patch('scripts.file_boundary.os.path.samefile', side_effect=PermissionError('denied')):
                with self.assertRaises(PermissionError):
                    relative_beneath_existing(root, root)


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

    @unittest.skipIf(os.name == 'nt', 'POSIX directory permissions and no-follow handles')
    def test_readonly_module_directories_remove_without_chmod_following_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            outside = parent/'outside'; outside.mkdir()
            sentinel = outside/'keep'; sentinel.write_bytes(b'foreign owned bytes')
            sentinel.chmod(0o444)
            outside.chmod(0o555)
            root = parent/'owned'
            cache = root/'module/cache'; cache.mkdir(parents=True)
            (cache/'source.go').write_bytes(b'package fixture\n')
            (cache/'foreign').symlink_to(outside, target_is_directory=True)
            cache.chmod(0o555); cache.parent.chmod(0o555)
            modes = [path.stat().st_mode for path in (parent, outside, sentinel)]
            try:
                remove_tree(root)
                self.assertFalse(root.exists())
                self.assertEqual(sentinel.read_bytes(), b'foreign owned bytes')
                self.assertEqual([path.stat().st_mode for path in (parent, outside, sentinel)], modes)
            finally:
                outside.chmod(0o755)
                sentinel.chmod(0o644)
