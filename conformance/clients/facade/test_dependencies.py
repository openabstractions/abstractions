"""Executable local Go-module counterexamples for the facade dependency guard."""
import os
from pathlib import Path
import tempfile
import re
import subprocess
import unittest
from unittest.mock import patch

import dependencies as guard
from personfolders import setUpModule, tearDownModule  # noqa: E402,F401 (per-user folders in a stand-in)


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

    def test_only_existing_pure_digest_owner_is_allowed(self):
        storage = "github.com/openabstractions/abstraction-storage/go"
        self.write("go.mod", (self.root / "go.mod").read_text() +
                   "require " + storage + " v0.0.0\nreplace " + storage + " => ./storage\n")
        self.write("storage/go.mod", "module " + storage + "\n\ngo 1.22\n")
        for package in ("digest", "other"):
            self.write("storage/" + package + "/owner.go", "package " + package + "\n")
        self.write("storage/store.go", "package storage\n")
        for suffix, allowed in (("/digest", True), ("", False), ("/other", False)):
            with self.subTest(package=storage + suffix):
                self.write("client/client.go", 'package client\nimport _ "' + storage + suffix + '"\n')
                self.assertEqual(not allowed, bool(self.findings()))
        self.write("client/client.go", 'package client\nimport _ "' + storage + '/digest"\n')
        self.write("storage/digest/owner.go", 'package digest\nimport _ "' + storage + '"\n')
        self.assertTrue(any(f.endswith("/digest -> " + storage) for f in self.findings()))

    def test_each_asks_client_edge_stays_out_of_the_provider_root(self):
        asks = "github.com/openabstractions/abstraction-asks/go"
        self.write("go.mod", (self.root / "go.mod").read_text() +
                   "require " + asks + " v0.0.0\nreplace " + asks + " => ./asks\n")
        self.write("asks/go.mod", "module " + asks + "\n\ngo 1.22\n")
        self.write("asks/book.go", "package asks\n")
        self.write("client/client.go", 'package client\nimport _ "' + asks + '/client"\n')
        production = Path(__file__).resolve().parents[3] / "openabstractions-flat/abstraction-asks/go/client"
        for name in ("details", "choice", "content", "notification", "operator", "permission", "responder"):
            with self.subTest(edge=name):
                self.assertNotIn('"' + asks + '"', (production / (name + ".go")).read_text(encoding="utf-8"))
                self.write("asks/client/" + name + ".go", 'package client\nimport _ "' + asks + '"\n')
                self.assertTrue(any(f.endswith("/client -> " + asks) for f in self.findings()))
                (self.root / "asks/client" / (name + ".go")).unlink()
        for pure in ("catalogue", "typedpermission"):
            self.write("asks/" + pure + "/pure.go", "package " + pure + "\n")
            self.write("asks/client/client.go", 'package client\nimport _ "' + asks + '/' + pure + '"\n')
            self.assertEqual([], self.findings())
            self.write("asks/" + pure + "/pure.go", 'package ' + pure + '\nimport _ "' + asks + '"\n')
            self.assertTrue(any(f.endswith("/" + pure + " -> " + asks) for f in self.findings()))
            self.write("asks/" + pure + "/pure.go", "package " + pure + "\n")
        self.assertTrue(guard.forbidden(asks + "/other"))
        storage = "github.com/openabstractions/abstraction-storage/go"
        self.write("go.mod", (self.root / "go.mod").read_text() +
                   "require " + storage + " v0.0.0\nreplace " + storage + " => ./storage\n")
        self.write("storage/go.mod", "module " + storage + "\n\ngo 1.22\n")
        self.write("storage/store.go", "package storage\n")
        self.write("storage/digest/pure.go", "package digest\n")
        self.write("asks/typedpermission/pure.go", 'package typedpermission\nimport _ "' + storage + '/digest"\n')
        self.assertEqual([], self.findings())
        self.write("storage/digest/pure.go", 'package digest\nimport _ "' + storage + '"\n')
        self.assertTrue(any(f.endswith("/typedpermission -> " + storage + "/digest -> " + storage) for f in self.findings()))

    def test_native_presenters_use_the_explicit_presentation_owner(self):
        root = Path(__file__).resolve().parents[3]
        names = subprocess.check_output(["git", "ls-files", "--", "monitor", "serve",
                   "openabstractions-flat/abstraction-facade/go/grants"], cwd=root, text=True).splitlines()
        for name in names:
            if not name.endswith(".go") or name.endswith("_test.go"):
                continue
            text = (root / name).read_text(encoding="utf-8")
            imports = re.findall(r'(?m)^\s*(?:(\w+)\s+)?"github.com/openabstractions/abstraction-asks/go/client"', text)
            for alias in imports:
                with self.subTest(source=name):
                    self.assertIsNone(re.search(r'\b' + (alias or "client") +
                        r'\.(ReadAskerContext|ReadDetailsContext|ReadContentContext|ReadPermissionDetailsContext)\(', text))


if __name__ == "__main__":
    unittest.main()
