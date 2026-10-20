"""Inert pipe/process doubles; this suite dispatches no physical process."""
import ast
import io
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
from workspace import ReadinessOutput


class OwnedProcess:
    def __init__(self, stdout='READY\n', stderr='', code=None, fail_cleanup=False):
        self.stdout = io.StringIO(stdout)
        self.stderr = io.StringIO(stderr)
        self.code = code
        self.fail_cleanup = fail_cleanup
        self.terminated = self.killed = 0

    def poll(self):
        return self.code

    def terminate(self):
        self.terminated += 1
        if self.fail_cleanup:
            raise OSError('owned fixture cleanup failure')
        self.code = -15

    def kill(self):
        self.killed += 1
        self.code = -9

    def wait(self, timeout):
        if self.code is None:
            raise subprocess.TimeoutExpired('owned-inert', timeout)
        return self.code


class ReadinessTests(unittest.TestCase):
    def setUp(self):
        self.block = patch.multiple(subprocess, Popen=self.no_dispatch, run=self.no_dispatch)
        self.block.start()
        self.addCleanup(self.block.stop)

    @staticmethod
    def no_dispatch(*args, **kwargs):
        raise AssertionError('physical dispatch forbidden in inert readiness suite')

    def test_wrong_first_line_retains_actual_line_stderr_and_exit(self):
        output = ReadinessOutput(OwnedProcess('wrong\nREADY\n', 'exact startup refusal', 7))
        with self.assertRaises(RuntimeError) as caught:
            try:
                output.expect('READY')
            finally:
                output.finish()
        text = str(caught.exception) + repr(caught.exception.__notes__)
        self.assertIn('wrong', text)
        self.assertIn('exact startup refusal', text)
        self.assertIn('pre_cleanup_exit=7', text)

    def test_eof_retains_failure_and_cleanup_secondary(self):
        output = ReadinessOutput(OwnedProcess('', 'failed before listener', fail_cleanup=True))
        with self.assertRaises(RuntimeError) as caught:
            try:
                output.expect('READY')
            finally:
                output.finish()
        self.assertIn('EOF', str(caught.exception))
        self.assertIn('cleanup failed', repr(caught.exception.__notes__))

    def test_stderr_and_stdout_are_bounded_and_drained(self):
        output = ReadinessOutput(OwnedProcess('READY\n' + 'x' * 200000, 'y' * 200000))
        output.expect('READY')
        stdout, stderr = output.finish()
        self.assertEqual(len(stdout), output.LIMIT)
        self.assertEqual(len(stderr), output.LIMIT)

    def test_oversize_first_line_is_refused(self):
        output = ReadinessOutput(OwnedProcess('x' * 70000 + '\n'))
        with self.assertRaisesRegex(RuntimeError, 'oversize'):
            try:
                output.expect('x', prefix=True)
            finally:
                output.finish()

    def test_no_stdout_before_timeout_is_bounded(self):
        output = ReadinessOutput(OwnedProcess())
        output.first.get(timeout=1)
        with self.assertRaisesRegex(RuntimeError, 'timeout'):
            try:
                output.line(timeout=0.001)
            finally:
                output.finish()

    def test_owned_cleanup_escalates_and_reaps(self):
        process = OwnedProcess()
        output = ReadinessOutput(process)
        output.expect('READY')
        output.finish(stop=False, timeout=0.001)
        self.assertEqual(process.killed, 1)
        self.assertEqual(process.poll(), -9)

    def test_terminate_failure_still_kills_and_reaps(self):
        process = OwnedProcess(fail_cleanup=True)
        output = ReadinessOutput(process)
        with self.assertRaisesRegex(RuntimeError, 'cleanup failed'):
            output.finish()
        self.assertEqual(process.killed, 1)
        self.assertEqual(process.code, -9)

    def test_unreaped_failure_is_explicit_and_primary_survives(self):
        process = OwnedProcess(fail_cleanup=True)
        process.kill = lambda: None
        output = ReadinessOutput(process)
        with self.assertRaisesRegex(ValueError, 'primary') as caught:
            try:
                raise ValueError('primary')
            finally:
                output.finish(timeout=0.001)
        self.assertIn('remains unreaped', repr(caught.exception.__notes__))
        self.assertTrue(process.stdout.closed)
        self.assertTrue(process.stderr.closed)

    def test_poll_and_close_failures_do_not_skip_other_cleanup(self):
        process = OwnedProcess()
        process.poll = lambda: (_ for _ in ()).throw(OSError('poll failure'))
        class BadClose(io.StringIO):
            def close(self): raise OSError('close failure')
        process.stdin = BadClose()
        output = ReadinessOutput(process)
        with self.assertRaisesRegex(RuntimeError, 'poll failure') as caught:
            output.finish()
        self.assertIn('close failure', str(caught.exception))
        self.assertIsNotNone(process.code)
        self.assertTrue(process.stdout.closed)
        self.assertTrue(process.stderr.closed)

    def test_blocked_drains_and_unreaped_input_are_never_closed(self):
        process = OwnedProcess(fail_cleanup=True)
        process.kill = lambda: None
        output = ReadinessOutput(process)
        # Real drain threads finish on these inert streams before substituting
        # reader owners that model a blocked TextIOWrapper read lock.
        for thread in output.threads:
            thread.join(timeout=1)
            self.assertFalse(thread.is_alive())
        class BlockedReader:
            def join(self, timeout): pass
            def is_alive(self): return True
        class MustNotClose:
            def close(self): raise AssertionError('synchronous blocked stream close')
        output.threads = [BlockedReader(), BlockedReader()]
        process.stdin = process.stdout = process.stderr = MustNotClose()
        with self.assertRaisesRegex(RuntimeError, 'remains unreaped') as caught:
            output.finish(timeout=0.001)
        self.assertIn('pipe drain did not finish', str(caught.exception))
        self.assertIn('pipe reader remains active', str(caught.exception))
        self.assertIn('owned input left open', str(caught.exception))

    def test_python39_exception_access_and_annotation(self):
        class OldError(Exception):
            add_note = None
        error = OldError('primary')
        with patch.object(sys, 'exception', None, create=True):
            ReadinessOutput.annotate(error, 'retained diagnostic')
            output = ReadinessOutput(OwnedProcess())
            output.finish()
        self.assertEqual(error.__notes__, ['retained diagnostic'])
        source = (Path(__file__).parent / 'workspace.py').read_text(encoding='utf-8')
        ast.parse(source, feature_version=(3, 9))
        self.assertNotIn('sys.exception(', source)

    def test_facade_attempts_later_peers_after_first_failure(self):
        source = (Path(__file__).parent / 'facade/run.py').read_text(encoding='utf-8')
        function = next(node for node in ast.parse(source).body
                        if isinstance(node, ast.FunctionDef) and node.name == 'finish_owned')
        namespace = {'sys': sys, 'ReadinessOutput': ReadinessOutput}
        exec(compile(ast.Module(body=[function], type_ignores=[]), 'facade cleanup', 'exec'), namespace)
        visited = []
        class Peer:
            def __init__(self, name): self.name = name
            def finish(self):
                visited.append(self.name)
                if self.name == 'first': raise OSError('first cleanup')
        with self.assertRaisesRegex(OSError, 'first cleanup'):
            namespace['finish_owned']([Peer('first'), Peer('second'), Peer('third')])
        self.assertEqual(visited, ['first', 'second', 'third'])
        with self.assertRaisesRegex(ValueError, 'original') as caught:
            try:
                raise ValueError('original')
            finally:
                namespace['finish_owned']([Peer('first'), Peer('second')])
        self.assertIn('first cleanup', repr(caught.exception.__notes__))

    def test_config_central_preserves_native_profile_metadata_and_private_argv(self):
        source = (Path(__file__).parent / 'config/run.py').read_text(encoding='utf-8')
        function = next(node for node in ast.parse(source).body
                        if isinstance(node, ast.FunctionDef) and node.name == 'config_host_invocation')
        namespace = {}
        exec(compile(ast.Module(body=[function], type_ignores=[]), 'config invocation', 'exec'), namespace)
        selected = {'HOME': 'native-home', 'USERPROFILE': 'native-profile',
                    'APPDATA': 'native-roaming', 'LOCALAPPDATA': 'native-local',
                    'PROGRAMDATA': 'native-machine', 'ABSTRACTION_STORE': 'ambient-override'}
        home, case = Path('absent-private-provider'), Path('owned-case')
        command, env = namespace['config_host_invocation'](
            'owned-host.exe', selected, home, case, 'private-config', False, 'fixture-name')
        for key in ('HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'PROGRAMDATA'):
            self.assertEqual(env[key], selected[key])
        self.assertEqual(env['ABSTRACTION_STORE'], 'host-environment-must-not-leak')
        self.assertEqual(command, ['owned-host.exe', 'serve', 'runtime', '--isolated', 'fixture-name',
                         '--state-dir', str(home.resolve()), '--config-endpoint', 'private-config',
                         '--without-jobs', '--without-models', '--supervised'])
        command, env = namespace['config_host_invocation'](
            'owned-host.exe', selected, home, case, 'private-config', True, 'unused')
        self.assertEqual(command, ['owned-host.exe', '--endpoint', 'private-config'])
        for key in ('HOME', 'USERPROFILE', 'APPDATA'):
            self.assertEqual(env[key], str(home))
        self.assertEqual(env['PROGRAMDATA'], str(case / 'machine'))
        self.assertEqual(env['LOCALAPPDATA'], selected['LOCALAPPDATA'])
        self.assertEqual(selected['ABSTRACTION_STORE'], 'ambient-override')
        self.assertIn('arguments,host_env=config_host_invocation(', source)

    def test_legacy_fixtures_and_central_runtime_are_distinct(self):
        here = Path(__file__).resolve().parent
        config = (here / 'config/run.py').read_text(encoding='utf-8')
        logging = (here / 'logging/run.py').read_text(encoding='utf-8')
        self.assertIn("'serve','runtime','--isolated'", config)
        self.assertIn("'serve', 'runtime'", logging)
        for name in ('config', 'logging', 'router', 'facade', 'fixture/livevoice'):
            source = (here / name / 'run.py').read_text(encoding='utf-8')
            self.assertIn('ReadinessOutput', source)
            self.assertNotIn('stderr.read()', source)


if __name__ == '__main__':
    unittest.main()
