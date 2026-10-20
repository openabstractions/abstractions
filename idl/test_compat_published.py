import json
import os
import subprocess
import tempfile
import unittest
from unittest import mock
from pathlib import Path

import compat_published
from personfolders import setUpModule, tearDownModule  # noqa: F401  (processes inherit the redirected folders)

HEAD = '''encoding json {
  escape = "minimal"
  indent = "2"
  map_keys = "utf8-bytes"
  numbers = "integer-decimal"
  opaque = "verbatim"
  terminator = "newline"
  duplicate_keys = "refuse"
  depth_limit = "64"
}
refusal {
  1: malformed (stage = "grammar")
  2: bad_string (stage = "grammar")
  3: number_spelling (stage = "grammar")
  4: wrong_type (stage = "grammar")
  5: depth_exceeded (stage = "grammar")
  6: duplicate_key (stage = "grammar")
  7: duplicate_field (stage = "structure")
  8: unknown_field (stage = "structure")
  9: missing_field (stage = "structure")
  10: trailing_bytes (stage = "document")
}
'''
SHARED = HEAD + ('struct Origin {\n  1: required string rung\n} (unknown_fields = "refuse")\n'
                 'struct Note {\n  1: required string text\n} (document = "true", unknown_fields = "refuse")\n')
MAIN = 'include "../abstraction-shared/shared.thrift"\n' + HEAD + '''struct Answer {
  1: required string id
  2: required shared.Origin origin
} (document = "true", unknown_fields = "refuse")
service Lookup {
  Answer Read(1: string id)
} (wire_name = "example.test/reader@1")
'''


class LocalGitReads(unittest.TestCase):
    def test_client_inventory_keeps_names_kinds_and_receivers(self):
        before = compat_published.client_inventory([
            'type Item wire.Item', 'var KnownResources', 'const Ready',
            'func Read(word string) (Item, error)', 'func (Client) Close() error',
            '  Field string'])
        self.assertEqual(before, {'Item': 'type', 'KnownResources': 'var', 'Ready': 'const',
                                 'Read': 'func', 'Client.Close': 'func'})

    def test_long_client_roster_preserves_every_distinct_path_in_bounded_calls(self):
        files = [Path('shared path') / ('a' * 160) / f'client-{i}.go' for i in range(220)]
        inventory = Path('inventory tool.exe')
        expected = {str(path): {'lines': ['var Known'], 'problems': []} for path in files}

        def run(argv, **kwargs):
            self.assertEqual(argv[0], str(inventory))
            units = len(subprocess.list2cmdline(argv).encode('utf-16-le')) // 2 + 1
            self.assertLessEqual(units, 32767)
            return subprocess.CompletedProcess(argv, 0, stdout=json.dumps({p: expected[p] for p in argv[1:]}))

        run = mock.Mock(side_effect=run)
        self.assertEqual(compat_published.read_client_surfaces(inventory, [*files, files[0]], run=run), expected)
        self.assertGreater(run.call_count, 1)
        self.assertEqual([p for call in run.call_args_list for p in call.args[0][1:]], list(expected))

    def test_failed_later_client_batch_refuses_instead_of_returning_partial_inventory(self):
        files = [Path('shared path') / ('b' * 160) / f'client-{i}.go' for i in range(440)]

        def complete(argv, **kwargs):
            return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(dict.fromkeys(argv[1:], {})))

        run = mock.Mock(side_effect=[
            mock.DEFAULT, subprocess.CompletedProcess([], 1, stdout='', stderr='inventory child failed')
        ], wraps=complete)
        with self.assertRaisesRegex(ValueError, 'Go client inventory failed: inventory child failed'):
            compat_published.read_client_surfaces(Path('inventory tool.exe'), files, run=run)
        self.assertEqual(run.call_count, 2)

    def test_failed_git_process_refuses_at_both_historical_read_boundaries(self):
        with mock.patch.object(compat_published, 'source_git_environment', return_value={}), \
             mock.patch.object(compat_published.subprocess, 'run', side_effect=OSError('fixture Git failure')):
            with self.assertRaisesRegex(OSError, 'fixture Git failure'):
                compat_published.present(Path('fixture'), 'a' * 40)
            with self.assertRaisesRegex(OSError, 'fixture Git failure'):
                compat_published.show(Path('fixture'), 'a' * 40, 'main.thrift')

    def test_missing_included_blob_refuses_instead_of_skipping_the_closure(self):
        with mock.patch.object(compat_published, 'source_git_environment', return_value={}), \
             mock.patch.object(compat_published.subprocess, 'run', side_effect=[
                 subprocess.CompletedProcess([], 0, stdout=b'include "child.thrift"\n'),
                 subprocess.CompletedProcess([], 1, stdout=b''),
                 subprocess.CompletedProcess([], 0, stdout=b'100644 blob ' + b'b' * 40 + b'\tchild.thrift\0')]), \
             tempfile.TemporaryDirectory() as scratch:
            with self.assertRaisesRegex(ValueError, 'cannot read historical source child.thrift'):
                compat_published.materialize(Path('fixture'), 'a' * 40, 'main.thrift', Path(scratch))

    def test_unreadable_historical_blob_or_tree_is_not_a_new_definition(self):
        for entry, status in ((b'100644 blob ' + b'b' * 40 + b'\tmissing.thrift\0', 0), (b'', 1)):
            with self.subTest(tree_read_status=status), \
                 mock.patch.object(compat_published, 'source_git_environment', return_value={}), \
                 mock.patch.object(compat_published.subprocess, 'run', side_effect=[
                     subprocess.CompletedProcess([], 1, stdout=b''),
                     subprocess.CompletedProcess([], status, stdout=entry)]), \
                 tempfile.TemporaryDirectory() as scratch:
                with self.assertRaisesRegex(ValueError, 'cannot read historical source'):
                    compat_published.materialize(Path('fixture'), 'a' * 40, 'missing.thrift', Path(scratch))

    def test_historical_reads_are_fixed_source_noninteractive_and_never_lazy_fetch(self):
        fixed = {'FIXTURE_SOURCE': 'owned', 'GIT_NO_LAZY_FETCH': '0', 'GIT_TERMINAL_PROMPT': '1'}
        with mock.patch.object(compat_published, 'source_git_environment', return_value=fixed), \
             mock.patch.object(compat_published.subprocess, 'run',
                               side_effect=[subprocess.CompletedProcess([], 1, stdout=b''),
                                            subprocess.CompletedProcess([], 1, stdout=b''),
                                            subprocess.CompletedProcess([], 0, stdout=b'')]) as run:
            self.assertFalse(compat_published.present(Path('fixture'), 'a' * 40))
            self.assertIsNone(compat_published.show(Path('fixture'), 'a' * 40, 'missing.thrift'))
        self.assertEqual(run.call_count, 3)
        self.assertEqual(run.call_args_list[0].args[0],
                         ['git', '-C', 'fixture', 'cat-file', '-e', 'a' * 40 + '^{commit}'])
        self.assertEqual(run.call_args_list[1].args[0],
                         ['git', '-C', 'fixture', 'show', 'a' * 40 + ':missing.thrift'])
        self.assertEqual(run.call_args_list[2].args[0],
                         ['git', '-C', 'fixture', 'ls-tree', '-z', '--full-tree', 'a' * 40, '--', 'missing.thrift'])
        for call in run.call_args_list:
            environment = call.kwargs['env']
            self.assertTrue(environment.get('FIXTURE_SOURCE') == 'owned')
            self.assertTrue(environment.get('GIT_NO_LAZY_FETCH') == '1')
            self.assertTrue(environment.get('GIT_TERMINAL_PROMPT') == '0')
        self.assertTrue(fixed['GIT_NO_LAZY_FETCH'] == '0')


class CompatPublishedTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.built = tempfile.TemporaryDirectory()
        cls.gen = Path(cls.built.name)/'gen.exe'
        subprocess.run(['go', '-C', str(compat_published.ROOT/'idl'/'gen'), 'build', '-o', str(cls.gen), '.'],
                       env={**os.environ, 'GOWORK': 'off'}, check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        cls.built.cleanup()

    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        git = lambda *a: subprocess.run(['git', '-C', str(self.root), *a], check=True, capture_output=True, text=True).stdout.strip()
        git('init', '-q')
        git('config', 'user.email', 'test@example.invalid')
        git('config', 'user.name', 'test')
        self.git = git
        self.write('scripts/split.manifest', 'repo abstraction-main\ntree . openabstractions-flat/abstraction-main\n'
                   'repo abstraction-shared\ntree . openabstractions-flat/abstraction-shared\n')
        self.write('openabstractions-flat/abstraction-shared/shared.thrift', SHARED)
        self.write('openabstractions-flat/abstraction-main/main.thrift', MAIN)
        git('add', '.')
        git('commit', '-q', '-m', 'published')
        revision = git('rev-parse', 'HEAD')
        self.revision = revision
        self.write('published.tsv', f'> header\nabstraction-main\t{"0" * 40}\t{revision}\nabstraction-shared\tnone\t-\n')
        self.write('scripts/gosum_published.tsv', 'module\tversion\th1\tgomod_h1\ttag_commit\treceipt\n')

    def write(self, relative, text):
        path = self.root/relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding='utf-8', newline='\n')

    def test_an_unchanged_definition_is_compatible(self):
        code, lines = compat_published.check(self.root, self.gen)
        self.assertEqual((code, lines), (0, [lines[0]]))
        self.assertTrue(lines[0].startswith('ok   openabstractions-flat/abstraction-main/main.thrift'))

    def test_published_client_inventory_refuses_removed_value_and_changed_kind(self):
        self.write('scripts/idiom_surface.go', (compat_published.ROOT/'scripts'/'idiom_surface.go').read_text(encoding='utf-8'))
        path = 'openabstractions-flat/abstraction-main/go/client/client.go'
        original = 'package client\nvar KnownResources = []string{"memory"}\nfunc Read(word string) string { return word }\n'
        self.write(path, original)
        self.git('add', '.')
        self.git('commit', '-q', '-m', 'published client')
        revision = self.git('rev-parse', 'HEAD')
        with mock.patch.object(compat_published, 'published_baselines', return_value=[('abstraction-main', revision)]):
            self.assertEqual(compat_published.check_clients(self.root)[0], 0)
            self.write(path, 'package client\nvar Read = 1\n')
            code, lines = compat_published.check_clients(self.root)
            self.assertEqual(code, 1)
            self.assertIn('KnownResources, Read', lines[0])
            self.write(path, 'package client\nfunc broken(\n')
            with self.assertRaisesRegex(ValueError, 'cannot read Go client inventory'):
                compat_published.check_clients(self.root)
            (self.root/path).unlink()
            (self.root/path).parent.rmdir()
            code, lines = compat_published.check_clients(self.root)
            self.assertEqual(code, 1)
            self.assertIn('KnownResources, Read', lines[0])

    def test_a_change_to_an_included_closed_record_is_refused(self):
        # The published include closure is the published revision's, so a member
        # added to the included closed struct is seen.
        self.write('openabstractions-flat/abstraction-shared/shared.thrift',
                   SHARED.replace('  1: required string rung\n', '  1: required string rung\n  2: optional string path(omit = "absent")\n'))
        code, lines = compat_published.check(self.root, self.gen)
        self.assertEqual(code, 1)
        self.assertIn('closed struct shared.Origin gained member path', lines[0])

    def test_a_definition_the_revision_lacks_is_new(self):
        self.write('openabstractions-flat/abstraction-main/extra.thrift', HEAD + 'struct Doc {\n  1: required string id\n} (document = "true", unknown_fields = "refuse")\n')
        code, lines = compat_published.check(self.root, self.gen)
        self.assertEqual(code, 0)
        self.assertIn('new  openabstractions-flat/abstraction-main/extra.thrift', lines[0])

    def test_a_revision_the_clone_lacks_fails(self):
        # A shallow or fresh clone may lack the published revision; every
        # definition would then read "new" and the check would pass with nothing compared.
        missing = '1' * 40
        self.write('published.tsv', f'> header\nabstraction-main\t{"0" * 40}\t{missing}\n')
        with self.assertRaisesRegex(ValueError, 'abstraction-main: published revision 111111111111 is not in this clone'):
            compat_published.check(self.root, self.gen)
        root = compat_published.ROOT
        compat_published.ROOT = self.root
        try:
            self.assertEqual(compat_published.main(['--gen', str(self.gen)]), 2)
        finally:
            compat_published.ROOT = root

    def component(self, revision=None, receipt='fixture', module='go', repository='abstraction-main'):
        revision = revision or self.revision
        path = self.root/'scripts/gosum_published.tsv'
        text = path.read_text() if path.exists() else ''
        self.write('scripts/gosum_published.tsv', text +
                   f'# fixture: release job {receipt}, terminal receipts JSON sha256\n'
                   f'# {"a" * 64}, source {revision}.\n' +
                   f'github.com/openabstractions/{repository}/{module}\tv0.1.0\t'
                   f'h1:{"A" * 43}=\th1:{"B" * 43}=\t{"c" * 40}\t{receipt}\n')

    def test_older_component_catches_a_service_absent_from_latest_repository(self):
        self.component()
        self.write('openabstractions-flat/abstraction-main/main.thrift', MAIN.split('service Lookup')[0])
        self.git('add', '.')
        self.git('commit', '-qm', 'latest without service')
        latest = self.git('rev-parse', 'HEAD')
        self.write('published.tsv', f'abstraction-main\t{"0" * 40}\t{latest}\n')
        self.write('openabstractions-flat/abstraction-main/main.thrift', MAIN)
        self.write('openabstractions-flat/abstraction-shared/shared.thrift',
                   SHARED.replace('  1: required string rung\n',
                                  '  1: required string rung\n  2: optional string path(omit = "absent")\n'))
        code, lines = compat_published.check(self.root, self.gen)
        self.assertEqual(code, 1)
        self.assertTrue(any('closed struct shared.Origin gained member path' in line for line in lines))
        self.assertTrue(any(line.startswith('ok ') and latest[:12] in line for line in lines), lines)
        self.assertEqual(len(lines), 2)

    def test_component_tags_deduplicate_exact_pairs_but_keep_distinct_sources(self):
        self.component(module='go')
        self.component(module='go-core', receipt='same-source')
        self.assertEqual(compat_published.published_baselines(self.root), [('abstraction-main', self.revision)])
        self.git('commit', '--allow-empty', '-qm', 'other source')
        other = self.git('rev-parse', 'HEAD')
        self.component(other, 'other', 'other')
        self.assertEqual(len(compat_published.published_baselines(self.root)), 2)
        code, lines = compat_published.check(self.root, self.gen)
        self.assertEqual(code, 0)
        self.assertEqual(len(lines), 2)

    def test_known_non_defining_component_stays_in_the_accounted_inventory(self):
        self.component(repository='abstraction-helper')
        self.assertEqual(compat_published.published_baselines(self.root),
                         [('abstraction-helper', self.revision), ('abstraction-main', self.revision)])
        code, lines = compat_published.check(self.root, self.gen)
        self.assertEqual(code, 0)
        self.assertEqual(len(lines), 1)

    def test_unbound_non_defining_component_does_not_invent_a_source(self):
        self.write('scripts/gosum_published.tsv',
                   f'github.com/openabstractions/websocket\tv0.1.0\th1:{"A" * 43}=\t'
                   f'h1:{"B" * 43}=\t{"c" * 40}\tunbound\n')
        self.assertEqual(compat_published.published_baselines(self.root),
                         [('abstraction-main', self.revision)])

    def test_component_missing_source_provenance_refuses(self):
        self.component()
        path = self.root/'scripts/gosum_published.tsv'
        self.write('scripts/gosum_published.tsv', '\n'.join(
            line for line in path.read_text().splitlines() if not line.startswith('#')))
        with self.assertRaisesRegex(ValueError, 'lacks private source provenance'):
            compat_published.check(self.root, self.gen)

    def test_missing_publication_ledger_refuses_instead_of_dropping_all_components(self):
        (self.root/'scripts/gosum_published.tsv').unlink()
        with self.assertRaisesRegex(ValueError, 'published module ledger is missing'):
            compat_published.check(self.root, self.gen)

    def test_component_conflicting_source_provenance_refuses(self):
        self.component()
        self.component('1' * 40, module='go-core')
        with self.assertRaisesRegex(ValueError, 'conflicting release source provenance'):
            compat_published.check(self.root, self.gen)

    def test_component_missing_commit_refuses(self):
        self.component('1' * 40)
        with self.assertRaisesRegex(ValueError, 'not in this clone'):
            compat_published.check(self.root, self.gen)

    def test_missing_historical_include_refuses(self):
        self.git('rm', 'openabstractions-flat/abstraction-shared/shared.thrift')
        self.git('commit', '-qm', 'broken closure')
        self.component(self.git('rev-parse', 'HEAD'))
        with self.assertRaisesRegex(ValueError, 'which that revision lacks'):
            compat_published.check(self.root, self.gen)

    def test_historical_split_mapping_follows_moved_private_inputs(self):
        self.git('mv', 'openabstractions-flat/abstraction-main', 'old-main')
        self.write('scripts/split.manifest', 'repo abstraction-main\ntree . old-main\n'
                   'repo abstraction-shared\ntree . openabstractions-flat/abstraction-shared\n')
        self.git('add', '.')
        self.git('commit', '-qm', 'historical private location')
        self.component(self.git('rev-parse', 'HEAD'))
        self.write('openabstractions-flat/abstraction-main/main.thrift', MAIN)
        code, lines = compat_published.check(self.root, self.gen)
        self.assertEqual(code, 0)
        self.assertEqual(len(lines), 2)

    def test_missing_split_manifest_refuses(self):
        self.git('rm', 'scripts/split.manifest')
        self.git('commit', '-qm', 'no projection')
        self.component(self.git('rev-parse', 'HEAD'))
        with self.assertRaisesRegex(ValueError, 'lacks scripts/split.manifest'):
            compat_published.check(self.root, self.gen)


if __name__ == '__main__':
    unittest.main()
