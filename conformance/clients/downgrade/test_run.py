"""Offline checks of the downgrade runner and the shared Go template helpers."""
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parent))
import run  # noqa: E402
import workspace  # noqa: E402


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding='utf-8')


class ReplaceGeneration(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        write(self.root / 'go.work', 'go 1.26.0\n\nuse (\n\t./a/go // job\n\t./b\n\t"./c"\n)\nuse ./d\n')
        write(self.root / 'a/go/go.mod', 'module example.com/a/go\n\ngo 1.26.0\n\nrequire (\n\texample.com/b v0.1.0\n'
                                         '\tgolang.org/x/sys v0.47.0 // indirect\n)\n\nreplace example.com/z => ../z\n')
        write(self.root / 'b/go.mod', 'module example.com/b\n\nrequire example.com/c v0.0.0\n')
        write(self.root / 'c/go.mod', 'module example.com/c\n')
        write(self.root / 'd/go.mod', 'module example.com/d\n\nrequire example.com/a/go v1.0.0\n')

    def tearDown(self):
        self.temp.cleanup()

    def test_workspace_modules_read_every_use_form(self):
        modules = workspace.go_workspace_modules(self.root)
        self.assertEqual(sorted(modules), ['example.com/a/go', 'example.com/b', 'example.com/c', 'example.com/d'])
        with mock.patch.object(workspace, 'ROOT', self.root):
            self.assertEqual(sorted(workspace.go_workspace_modules()), sorted(modules))

    def test_requirements_read_blocks_single_lines_and_skip_replace(self):
        self.assertEqual(workspace.go_requirements(self.root / 'a/go/go.mod'), ['example.com/b', 'golang.org/x/sys'])
        self.assertEqual(workspace.go_requirements(self.root / 'b/go.mod'), ['example.com/c'])

    def test_closure_follows_workspace_requirements_only(self):
        modules = workspace.go_workspace_modules(self.root)
        self.assertEqual(workspace.go_replace_closure(['example.com/a/go'], modules),
                         ['example.com/a/go', 'example.com/b', 'example.com/c'])
        with self.assertRaises(SystemExit):
            workspace.go_replace_closure(['example.com/missing'], modules)


class TemplateBuilds(unittest.TestCase):
    def test_constraints_above_the_package_clause_are_stripped(self):
        source = '//go:build ignore\n\n// Doc comment.\npackage main\n\n//go:build is only a comment here\nfunc main() {}\n'
        stripped = workspace.strip_go_build_constraints(source)
        self.assertEqual(stripped, '// Doc comment.\npackage main\n\n//go:build is only a comment here\nfunc main() {}\n')
        self.assertEqual(workspace.strip_go_build_constraints('package main\n'), 'package main\n')

    def test_the_real_templates_carry_a_constraint_that_strips(self):
        for template in ('writer.go', 'previous.go'):
            text = (HERE / template).read_text(encoding='utf-8')
            self.assertTrue(text.startswith('//go:build ignore\n'), template)
            self.assertNotIn('//go:build', workspace.strip_go_build_constraints(text).split('package main', 1)[0])

    def test_generated_module_and_binary_paths_differ(self):
        calls = []

        def fake_run(args, cwd, **kwargs):
            calls.append((args, cwd, kwargs['env']))
            Path(args[3]).write_text('binary', encoding='utf-8')
            return mock.Mock(returncode=0, stdout='', stderr='')

        with tempfile.TemporaryDirectory() as temp:
            template = Path(temp) / 'tool.go'
            write(template, '//go:build ignore\n\npackage main\n\nfunc main() {}\n')
            with mock.patch('subprocess.run', fake_run):
                binary = workspace.go_template_binary(template, Path(temp) / 'work', 'writer',
                                                      requires=[('example.com/a', 'v1.2.3')],
                                                      replaces=[('example.com/a', Path(temp) / 'a')],
                                                      env={'GOPROXY': 'off', 'PATH': ''})
            module = Path(temp) / 'work' / 'writer'
            self.assertNotEqual(binary, module)
            self.assertTrue(binary.name.startswith('writer-bin'))
            self.assertEqual((module / 'main.go').read_text(encoding='utf-8'), 'package main\n\nfunc main() {}\n')
            gomod = (module / 'go.mod').read_text(encoding='utf-8')
            self.assertIn('\texample.com/a v1.2.3\n', gomod)
            self.assertIn('replace example.com/a => "', gomod)
            (args, cwd, env), = calls
            self.assertEqual(Path(cwd), module)
            self.assertEqual((env['GOWORK'], env['GOFLAGS']), ('off', '-mod=mod'))
            self.assertNotIn('GOPROXY', env)


class Verdicts(unittest.TestCase):
    def test_refusal_by_name(self):
        feature = 'abstraction.job/journal-attempts@1'
        self.assertTrue(run.names('unsupported storage feature "%s"' % feature, [feature]))
        self.assertTrue(run.names('unsupported owner field "Features"', [feature]))
        self.assertFalse(run.names('owner/version/execution profile mismatch', [feature]))

    def test_published_pins_skip_comments_and_unpublished(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            write(root / 'published.tsv', '> comment\nabstraction-job\tabc\tdef\nabstraction-x\tnone\t-\n')
            with mock.patch.object(run, 'ROOT', root):
                self.assertEqual(run.published_pins(), {'abstraction-job': 'abc', 'abstraction-x': 'none'})


if __name__ == '__main__':
    unittest.main()
