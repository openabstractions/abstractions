"""Executable local Go-module counterexamples for the facade dependency guard."""
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import dependencies as guard


class DependencyBoundary(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.write("go.mod", "module " + guard.FACADE + "\n\ngo 1.22\n"
                   "require github.com/openabstractions/abstraction-job/go v0.0.0\n"
                   "replace github.com/openabstractions/abstraction-job/go => ./provider\n")
        self.write("facade.go", 'package facade\nimport _ "' + guard.FACADE + '/client"\n')
        self.write("client/client.go", "// Package client preserves Unicode docs: “typed”.\npackage client\n")
        self.write("provider/go.mod", "module github.com/openabstractions/abstraction-job/go\n\ngo 1.22\n")
        self.write("provider/store.go", "package job\n")
        self.write("provider/acceptanceprovider/provider.go", "package acceptanceprovider\n")
        self.write("provider/abstraction/job/acceptance/rec.go", "package acceptance\n")
        # Explicit legacy adoption is present in the module but outside both
        # production entrypoint closures. A directory/string scan would reject it.
        self.write("legacy/local.go", 'package legacy\nimport _ "github.com/openabstractions/abstraction-job/go"\n')
        self.environment = patch.dict(os.environ, GOWORK="off", GOPROXY="off", GOSUMDB="off", GOFLAGS="")
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def findings(self):
        return guard.violations(guard.packages(self.root))

    def test_protocol_and_explicit_legacy_are_allowed(self):
        self.write("client/client.go", 'package client\nimport _ "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"\n')
        self.assertEqual([], self.findings())
        graph = guard.packages(self.root, [guard.FACADE + "/legacy"])
        self.assertIn("github.com/openabstractions/abstraction-job/go", graph)

    def test_primary_direct_provider_is_red(self):
        self.write("facade.go", 'package facade\nimport _ "github.com/openabstractions/abstraction-job/go"\n')
        self.assertTrue(any(guard.FACADE + " -> github.com/openabstractions/abstraction-job/go" == f for f in self.findings()))

    def test_transitive_provider_is_red(self):
        self.write("client/client.go", 'package client\nimport _ "' + guard.FACADE + '/bridge"\n')
        self.write("bridge/bridge.go", 'package bridge\nimport _ "github.com/openabstractions/abstraction-job/go/acceptanceprovider"\n')
        self.assertTrue(any("/client -> " + guard.FACADE + "/bridge -> " in f and f.endswith("/acceptanceprovider") for f in self.findings()))

    def test_runtime_and_legacy_reentry_are_red(self):
        for package in ("runtime", "legacy", "service", "serve"):
            with self.subTest(package=package):
                if package != "legacy":
                    self.write(package + "/host.go", "package " + package + "\n")
                self.write("client/client.go", 'package client\nimport _ "' + guard.FACADE + '/' + package + '"\n')
                self.assertTrue(any(f.endswith("/" + package) for f in self.findings()))

    def test_missing_dependency_is_failure(self):
        self.write("client/client.go", 'package client\nimport _ "example.invalid/missing"\n')
        with self.assertRaisesRegex(RuntimeError, "go list failed"):
            self.findings()


if __name__ == "__main__":
    unittest.main()
