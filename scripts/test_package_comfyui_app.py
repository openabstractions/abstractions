"""Small pinned-fixture checks for the release-side ComfyUI repack."""

import base64
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile

from scripts import package_comfyui_app as app


# A tiny 7z made once from .build/comfy-repack-fixture/App/README.txt.
TINY_7Z = base64.b64decode(
    "N3q8ryccAASMcSTcpQAAAAAAAAAiAAAAAAAAAFWRCSsBAAt0aW55IGZpeHR1cmUAAACBMweuD87ysgwHyEN/QbH6/duIttdkiK3bp3H4FOvssaR7Cz9SXQbl799HM4peKavkn87pjivpBptb6HPSkZ9BYuuAbiGA8dg/fdVhlB901EBNhi3dBu6h7QougsT/sSsTcxXkfmT95XEwjZG7nVIIkJhMLY2PMGo/yB0TgWvO+8KGUhZI3urCAYqUsZJJRoZCNgAXBhABCYCVAAcLAQABIwMBAQVdABAAAAyA7goBS5uR4gAA"
)


class _Stage(unittest.TestCase):
    """Shared fixture. RepackTests and RepackSourceTests run repack() and need
    the build's free-disk floor (about 20 GB beyond the inputs); StageTests
    exercise the build steps on small staged trees."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source.7z"
        self.source.write_bytes(TINY_7Z)

    def build(self, name="app.zip", **overrides):
        values = dict(
            expected_size=len(TINY_7Z),
            expected_sha256=hashlib.sha256(TINY_7Z).hexdigest(),
            archive_root=".build",
            expected_commit=None,
            max_expanded_bytes=4096,
        )
        values.update(overrides)
        output = self.root / name
        result = app.repack(self.source, output, None, **values)
        return output, result

    def _integration_fixture(self) -> Path:
        package = self.root / "integration.zip"
        wheels = []
        with zipfile.ZipFile(package, "w") as archive:
            archive.writestr("node/abstraction_downloads/__init__.py", b"# node\n")
            archive.writestr("LICENSE", b"OA Apache fixture")
            archive.writestr("NOTICE", b"OA notice fixture")
            lines = []
            pure = sorted(app._integration_distributions() - {"abstraction_ipc"})
            for index in range(len(pure) + 1):
                filename = ("abstraction_ipc-0.0.0-py3-none-win_amd64.whl" if index == 0 else
                            f"{pure[index - 1]}-0.0.0-py3-none-any.whl")
                data = f"wheel {index}".encode()
                digest = hashlib.sha256(data).hexdigest()
                archive.writestr("wheels/" + filename, data)
                wheel = {"filename": filename, "size": len(data), "sha256": digest,
                         "source_commit": app.INTEGRATION_SOURCE_COMMIT}
                if index == 0:
                    wheel["source_commit"] = app.INTEGRATION_NATIVE_COMMIT
                    wheel["native_library_sha256"] = "0" * 64
                wheels.append(wheel)
                distribution = filename.split("-")[0].replace("_", "-")
                lines.append(f"{distribution}==0.0.0 --hash=sha256:{digest}")
            archive.writestr("requirements.lock", "\n".join(lines) + "\n")
            archive.writestr("manifest.json", json.dumps({"source_commit": app.INTEGRATION_SOURCE_COMMIT,
                                                           "wheels": wheels}))
        return package


class RepackTests(_Stage):
    def test_disposable_runner_floor_keeps_same_expansion_and_asset_caps(self):
        workstation = app.required_free_bytes()
        runner = app.required_free_bytes(minimum_free_after_build_bytes=1 << 30)
        self.assertEqual(workstation - runner, 19 << 30)
        self.assertGreater(runner, 16 << 30)
        with self.assertRaisesRegex(ValueError, "at least one GiB"):
            app.required_free_bytes(minimum_free_after_build_bytes=0)

    @unittest.skipUnless(shutil.which("tar.exe"), "bsdtar.exe is required for the tiny 7z fixture")
    def test_success_is_deterministic_and_preserves_payload(self):
        first, manifest = self.build()
        second, _ = self.build("again.zip")
        self.assertEqual(first.read_bytes(), second.read_bytes())
        self.assertEqual(manifest["file_count"], 1)
        with zipfile.ZipFile(first) as archive:
            self.assertEqual(archive.read(".build/comfy-repack-fixture/App/README.txt"), b"tiny fixture")
            self.assertIn(app.PROVENANCE, archive.namelist())
            self.assertNotIn(".oa-install.json", archive.namelist())

    def test_rejects_size_and_hash_before_extraction(self):
        with self.assertRaisesRegex(ValueError, "size or SHA-256"):
            self.build(expected_size=len(TINY_7Z) + 1, tar="missing-tar")
        with self.assertRaisesRegex(ValueError, "size or SHA-256"):
            self.build(expected_sha256="0" * 64, tar="missing-tar")
        self.assertFalse((self.root / "app.zip").exists())

    def test_preserves_existing_output(self):
        output = self.root / "app.zip"
        output.write_bytes(b"existing app")
        with self.assertRaises(FileExistsError):
            self.build()
        self.assertEqual(output.read_bytes(), b"existing app")

    def test_free_space_floor_refuses_before_extraction(self):
        usage = mock.Mock(free=app.MIN_FREE_AFTER_BUILD_BYTES)
        with mock.patch.object(app.shutil, "disk_usage", return_value=usage):
            with self.assertRaisesRegex(OSError, "insufficient free disk"):
                self.build(tar="missing-tar")
        self.assertFalse((self.root / "app.zip").exists())

    @unittest.skipUnless(shutil.which("tar.exe"), "bsdtar.exe is required for the tiny 7z fixture")
    def test_release_asset_limit_refuses_before_publication(self):
        with self.assertRaisesRegex(ValueError, "release asset size limit"):
            self.build(max_release_asset_bytes=1)
        self.assertFalse((self.root / "app.zip").exists())

    @unittest.skipUnless(shutil.which("tar.exe"), "bsdtar.exe is required for the tiny 7z fixture")
    def test_expanded_limit_and_failed_extraction_clean_stage(self):
        with self.assertRaisesRegex(ValueError, "expanded-byte ceiling"):
            self.build(max_expanded_bytes=1)
        self.assertFalse(list(self.root.glob("oa-comfy-repack-*")))
        with self.assertRaises(FileNotFoundError):
            self.build(tar="this-tar-does-not-exist")
        self.assertFalse(list(self.root.glob("oa-comfy-repack-*")))
        self.assertFalse((self.root / "app.zip").exists())


class StageTests(_Stage):
    def test_public_integration_requires_exact_candidate_and_wheel_attempt(self):
        private = self._integration_fixture()
        public = self.root / "public-integration.zip"
        with zipfile.ZipFile(private) as archive:
            members = {name: archive.read(name) for name in archive.namelist()}
        manifest = json.loads(members["manifest.json"])
        manifest.pop("source_commit")
        manifest.update(source_mode="public-split",
                        adopter_source={"repository": "openabstractions/adopter-comfyui", "commit": "b" * 40},
                        packager_source={"repository": "openabstractions/abstractions", "commit": "d" * 40})
        for wheel in manifest["wheels"]:
            wheel.pop("source_commit")
            wheel.update(repository="openabstractions/abstraction-identity", commit="c" * 40)
        members["manifest.json"] = json.dumps(manifest).encode()
        with zipfile.ZipFile(public, "w") as archive:
            for name, data in members.items():
                archive.writestr(name, data)
        size, digest = app._sha256(public)
        wheel_receipt = self.root / "wheel-inputs.json"
        wheel_receipt.write_text('{"selected":"fixture"}\n', encoding="utf-8")
        producer_receipt = self.root / "producer-inputs.json"
        producer_receipt.write_text(json.dumps({"schema": 1, "candidate_source": "a" * 40,
                                                "run_id": 12, "run_attempt": 3,
                                                "wheel_receipt_sha256": app._sha256(wheel_receipt)[1],
                                                "integration": {"name": public.name, "bytes": size, "sha256": digest},
                                                "adopter_source": manifest["adopter_source"],
                                                "packager_source": manifest["packager_source"],
                                                "wheels": manifest["wheels"]}), encoding="utf-8")
        portable = self.root / "portable-public"
        (portable / "ComfyUI" / "custom_nodes").mkdir(parents=True)
        (self.root / "work-wrong").mkdir()
        (self.root / "work-right").mkdir()
        with self.assertRaisesRegex(ValueError, "candidate source or Windows wheel job"):
            app._apply_integration(portable, public, self.root / "work-wrong", install_wheels=False,
                                   archive_size=size, archive_sha256=digest,
                                   producer_receipt_path=producer_receipt, wheel_receipt_path=wheel_receipt,
                                   expected_redist_source_commit="a" * 40, expected_run_id=12,
                                   expected_run_attempt=2)
        result = app._apply_integration(portable, public, self.root / "work-right", install_wheels=False,
                                        archive_size=size, archive_sha256=digest,
                                        producer_receipt_path=producer_receipt, wheel_receipt_path=wheel_receipt,
                                        expected_redist_source_commit="a" * 40, expected_run_id=12,
                                        expected_run_attempt=3)
        self.assertEqual(result["native_source_commit"], "c" * 40)
        self.assertEqual(result["source_commit"], "b" * 40)

    def _rewrite(self, package: Path, change) -> Path:
        """A copy of the integration ZIP with change(members) applied to its members."""
        with zipfile.ZipFile(package) as archive:
            members = {name: archive.read(name) for name in archive.namelist()}
        change(members)
        changed = self.root / "changed-integration.zip"
        with zipfile.ZipFile(changed, "w") as archive:
            for name, data in members.items():
                archive.writestr(name, data)
        return changed

    def test_the_integration_zip_must_agree_with_itself(self):
        package = self._integration_fixture()
        self.assertEqual(len(app._check_integration_consistency(package)), len(app._integration_distributions()))

        def drop_lock_line(members):
            members["requirements.lock"] = b"".join(members["requirements.lock"].splitlines(keepends=True)[1:])

        def extra_wheel(members):
            members["wheels/abstraction_unlisted_protocol-0.0.0-py3-none-any.whl"] = b"unlisted"

        def stale_packager_set(members):
            # A ZIP built before abstraction-logging joined the integration, consistent with itself.
            manifest = json.loads(members["manifest.json"])
            gone = next(w for w in manifest["wheels"] if w["filename"].startswith("abstraction_logging_protocol-"))
            manifest["wheels"].remove(gone)
            members.pop("wheels/" + gone["filename"])
            members["manifest.json"] = json.dumps(manifest).encode()
            members["requirements.lock"] = b"".join(
                line for line in members["requirements.lock"].splitlines(keepends=True)
                if not line.startswith(b"abstraction-logging-protocol=="))

        def second_native(members):
            manifest = json.loads(members["manifest.json"])
            manifest["wheels"][1]["native_library_sha256"] = "0" * 64
            members["manifest.json"] = json.dumps(manifest).encode()

        def changed_bytes(members):
            name = next(n for n in members if n.startswith("wheels/") and "abstraction_ipc" not in n)
            members[name] = b"other bytes"

        for change in (drop_lock_line, extra_wheel, second_native, changed_bytes, stale_packager_set):
            with self.subTest(change=change.__name__):
                with self.assertRaisesRegex(ValueError, "inconsistent.*package_comfyui_integration.py.*"
                                                        "INTEGRATION_NATIVE_COMMIT"):
                    app._check_integration_consistency(self._rewrite(package, change))
        with self.assertRaisesRegex(ValueError, "lacks abstraction_logging_protocol and carries nothing"):
            app._check_integration_consistency(self._rewrite(package, stale_packager_set))

    def test_the_pin_check_runs_before_the_consistency_check(self):
        package = self._integration_fixture()
        data = package.read_bytes()
        app._check_integration_consistency(package)
        usage = mock.Mock(free=1 << 60)
        consistency = mock.patch.object(app, "_check_integration_consistency",
                                        side_effect=AssertionError("consistency checked before the pin"))
        with mock.patch.object(app.shutil, "disk_usage", return_value=usage), consistency as checked:
            with self.assertRaisesRegex(ValueError, "size or SHA-256"):
                self.build(integration_archive=package, expected_integration_size=len(data),
                           expected_integration_sha256="0" * 64, tar="missing-tar")
        checked.assert_not_called()
        self.assertFalse((self.root / "app.zip").exists())

    def test_an_inconsistent_integration_zip_is_refused_before_extraction(self):
        package = self._rewrite(self._integration_fixture(), lambda members: members.pop("requirements.lock"))
        data = package.read_bytes()
        usage = mock.Mock(free=1 << 60)
        extraction = mock.patch.object(app, "_listed_members", side_effect=AssertionError("extracted"))
        with mock.patch.object(app.shutil, "disk_usage", return_value=usage), extraction:
            with self.assertRaisesRegex(ValueError, "inconsistent"):
                self.build(integration_archive=package, expected_integration_size=len(data),
                           expected_integration_sha256=hashlib.sha256(data).hexdigest(), tar="missing-tar")
        self.assertFalse((self.root / "app.zip").exists())
        self.assertFalse(list(self.root.glob("oa-comfy-repack-*")))

    def test_tree_validation_rejects_case_collision_links_and_escape_names(self):
        seen = set()
        app._register_name("Portable/a.txt", seen)
        with self.assertRaisesRegex(ValueError, "case-insensitive duplicate"):
            app._register_name("portable/A.TXT", seen)
        root = self.root / "tree"
        payload = root / "Portable"
        payload.mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "unsafe"):
            app._safe_name("bad:member")
        with self.assertRaisesRegex(ValueError, "unsafe"):
            app._safe_name("..")
        try:
            (payload / "link").symlink_to(self.source)
        except (OSError, NotImplementedError):
            pass
        else:
            with self.assertRaisesRegex(ValueError, "link/reparse"):
                app._scan_tree(root, "Portable", 100)

    def test_gpu_record_ownership_and_cpu_entrypoints(self):
        work = self.root / "work"
        portable = work / "extract" / "Portable"
        site = portable / "python_embeded" / "Lib" / "site-packages"
        site.mkdir(parents=True)
        for index, dist_name in enumerate(app.GPU_DISTS):
            (site / f"gpu{index}.bin").write_bytes(bytes([index]))
            dist = site / dist_name
            dist.mkdir()
            (dist / "RECORD").write_text(f"gpu{index}.bin,,\n{dist_name}/RECORD,,\n", encoding="utf-8")
        retained = site / "retained-1.0.dist-info"
        retained.mkdir()
        (retained / "RECORD").write_text("gpu0.bin,,\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "shared with retained"):
            app._recorded_gpu_files(portable)
        (retained / "RECORD").write_text("retained-1.0.dist-info/RECORD,,\n", encoding="utf-8")
        receipt = app._remove_gpu_files(portable)
        self.assertEqual(receipt["files"], 12)
        self.assertFalse((site / "gpu0.bin").exists())
        self.assertTrue((retained / "RECORD").exists())

        (portable / "README_VERY_IMPORTANT.txt").write_text("upstream AMD")
        for name in ("run_amd_gpu.bat", "run_amd_gpu_enable_dynamic_vram.bat"):
            (portable / name).write_text("amd")
        (portable / "update").mkdir()
        (portable / "update" / "update_comfyui.bat").write_text("update")
        app._rewrite_cpu_entrypoints(portable, work)
        self.assertFalse((portable / "run_amd_gpu.bat").exists())
        self.assertFalse((portable / "update").exists())
        self.assertEqual((portable / app.LAUNCHER_NAME).read_bytes(), app.LAUNCHER_SOURCE.read_bytes())
        self.assertEqual((portable / "OA-DOCS" / "UPSTREAM_AMD_README.txt").read_text(), "upstream AMD")

        records = "ab" * 32
        app._write_cpu_launch(portable, records)
        bat = (portable / "run_openabstractions_cpu.bat").read_text()
        self.assertIn("--cpu", bat)
        # ComfyUI's own switch for api.comfy.org: no API nodes, and a CSP that
        # keeps the frontend's release notices off (VISION 2026-10-01).
        self.assertIn("--disable-api-nodes", bat)
        self.assertIn("%LOCALAPPDATA%", bat)
        # The manual launch runs the same launch check as the app activation,
        # pinned to the RECORD set the build sealed.
        self.assertIn(f'python.exe" -B -s "%~dp0oa_comfyui_launch.py" --records {records} --state "%OA_DATA%\\launch" -- '
                      '"%~dp0ComfyUI\\main.py" --cpu', bat)
        with self.assertRaisesRegex(ValueError, "64 lowercase hex"):
            app._write_cpu_launch(portable, "AB" * 32)

    def _private_python(self, work: Path) -> Path:
        """A staged private Python with pip, ensurepip and pip-style unhashed bytecode rows."""
        portable = work / "extract" / "Portable"
        python_root = portable / "python_embeded"
        site = python_root / "Lib" / "site-packages"

        def install(name, files, unhashed=()):
            info = site / f"{name}-1.0.dist-info"
            info.mkdir(parents=True)
            rows = []
            for relative, data in files.items():
                path = site / Path(*relative.split("/"))
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
                digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip("=")
                rows.append(f"{relative},," if relative in unhashed else f"{relative},sha256={digest},{len(data)}")
            rows.append(f"{info.name}/RECORD,,")
            (info / "RECORD").write_text("\n".join(rows) + "\n", encoding="utf-8")

        install("pip", {"pip/__init__.py": b"# pip\n", "pip/__pycache__/__init__.cpython-312.pyc": b"pip bytecode",
                        "../../Scripts/pip.exe": b"MZ pip launcher"},
                unhashed=("pip/__pycache__/__init__.cpython-312.pyc",))
        install("kept", {"kept/__init__.py": b"x = 1\n", "kept/__pycache__/__init__.cpython-312.pyc": b"kept bytecode",
                         "kept/LICENSE": b"licence"},
                unhashed=("kept/__pycache__/__init__.cpython-312.pyc", "kept/LICENSE"))
        (python_root / "Lib" / "ensurepip").mkdir(parents=True)
        (python_root / "Lib" / "ensurepip" / "__init__.py").write_bytes(b"# ensurepip\n")
        with zipfile.ZipFile(python_root / "python312.zip", "w") as stdlib:
            stdlib.writestr("os.pyc", b"stdlib")
        (python_root / "python312._pth").write_bytes(
            b"python312.zip\r\n.\r\n../ComfyUI\r\n# Uncomment to run site.main() automatically\r\nimport site\r\n")
        return portable

    def test_private_python_loses_installers_and_passes_the_launch_check(self):
        work = self.root / "work"
        portable = self._private_python(work)
        launcher = app._launcher()
        python_root = portable / "python_embeded"
        site = python_root / "Lib" / "site-packages"
        # As extracted, the launch check refuses this Python for its installer.
        with self.assertRaisesRegex(launcher.Refusal, "installer"):
            launcher.check_environment(str(site), str(python_root))
        receipt = app._seal_private_python(portable, work)
        self.assertEqual(receipt["removed_installers"], ["pip-1.0.dist-info (4 recorded files)", "Lib/ensurepip"])
        self.assertEqual(receipt["sealed_bytecode_rows"], 1)
        for gone in (site / "pip", site / "pip-1.0.dist-info", python_root / "Scripts", python_root / "Lib" / "ensurepip"):
            self.assertFalse(gone.exists(), gone)
        record = (site / "kept-1.0.dist-info" / "RECORD").read_text(encoding="utf-8")
        digest = base64.urlsafe_b64encode(hashlib.sha256(b"kept bytecode").digest()).decode().rstrip("=")
        self.assertIn(f"kept/__pycache__/__init__.cpython-312.pyc,sha256={digest},13\n", record)
        self.assertIn("kept/LICENSE,,\n", record)
        self.assertEqual(launcher.check_environment(str(site), str(python_root))["unchecked_bytecode"], 0)
        # The embedded Python starts without site; the launcher adds site-packages after its check.
        self.assertEqual((python_root / "python312._pth").read_bytes(),
                         b"python312.zip\r\n.\r\n../ComfyUI\r\n# Uncomment to run site.main() automatically\r\n")
        self.assertIn("removed 'import site'", receipt["entrypoint_change"])
        # The RECORD set is pinned after the bytecode rows are sealed.
        self.assertEqual(receipt["records_sha256"], launcher.records_digest(str(site)))
        (site / "evil-1.0.dist-info").mkdir()
        (site / "evil-1.0.dist-info" / "RECORD").write_text("evil-1.0.dist-info/RECORD,,\n", encoding="utf-8")
        with self.assertRaisesRegex(launcher.Refusal, "differs from this package's pin"):
            launcher.check_environment(str(site), str(python_root), records=receipt["records_sha256"])

    def test_private_python_refuses_a_pth_layout_it_does_not_know(self):
        for content in (b"python312.zip\r\n.\r\n", b"import site\r\nimport site\r\n"):
            with self.subTest(content=content):
                work = self.root / ("work-" + hashlib.sha256(content).hexdigest()[:8])
                portable = self._private_python(work)
                (portable / "python_embeded" / "python312._pth").write_bytes(content)
                with self.assertRaisesRegex(ValueError, "exactly one 'import site' line"):
                    app._seal_private_python(portable, work)

    def test_private_python_seal_refuses_what_it_cannot_remove(self):
        work = self.root / "work"
        portable = self._private_python(work)
        python_root = portable / "python_embeded"
        with zipfile.ZipFile(python_root / "python312.zip", "a") as stdlib:
            stdlib.writestr("ensurepip/__init__.py", b"")
        with self.assertRaisesRegex(ValueError, "python312.zip carries an installer"):
            app._seal_private_python(portable, work)

        work = self.root / "work-planted"
        portable = self._private_python(work)
        (portable / "python_embeded" / "Lib" / "site-packages" / "planted.py").write_bytes(b"import os\n")
        with self.assertRaisesRegex(ValueError, "fails the OA launch check: Lib/site-packages/planted.py is not listed"):
            app._seal_private_python(portable, work)

        outside = self.root / "outside" / "extract" / "Portable"
        outside.mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "owned extraction stage"):
            app._seal_private_python(portable, self.root / "outside")

    @staticmethod
    def _pins(*wheels: Path) -> dict[Path, str]:
        return {wheel: hashlib.sha256(wheel.read_bytes()).hexdigest() for wheel in wheels}

    @staticmethod
    def _backslash_wheel(site: Path, wheels: Path, name: str, installed: bytes | None = None) -> Path:
        """Install a wheel whose RECORD names its files with backslashes, as pip leaves torchvision 0.24.1+cpu.

        pip keeps the backslash rows and lists each installed file again under
        its forward-slash path with no hash.
        """
        files = {f"{name}/_C.pyd": b"MZ " + name.encode() + b" native", f"{name}/__init__.py": b"x = 1\n",
                 f"{name}/LICENSE": b"licence"}

        def digest(data):
            return "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip("=")
        wheel_record = "".join(f"{path.replace('/', chr(92))},{digest(data)},{len(data)}\n" for path, data in files.items())
        wheel_record += f"{name}-1.0.dist-info\\RECORD,,\n"
        wheels.mkdir(parents=True, exist_ok=True)
        wheel = wheels / f"{name}-1.0-cp312-cp312-win_amd64.whl"
        with zipfile.ZipFile(wheel, "w") as archive:
            for path, data in files.items():
                archive.writestr(path, data)
            archive.writestr(f"{name}-1.0.dist-info/RECORD", wheel_record)
        for path, data in files.items():
            target = site / Path(*path.split("/"))
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(installed if installed is not None and path.endswith("_C.pyd") else data)
        info = site / f"{name}-1.0.dist-info"
        info.mkdir()
        (info / "RECORD").write_text(wheel_record + "".join(f"{path},,\n" for path in files),
                                     encoding="utf-8", newline="")
        return wheel

    def _backslash_record_install(self, work: Path, installed: bytes | None = None) -> tuple[Path, Path]:
        """A private Python holding one verified backslash-RECORD wheel, vision."""
        portable = self._private_python(work)
        site = portable / "python_embeded" / "Lib" / "site-packages"
        return portable, self._backslash_wheel(site, work / "verified-wheels", "vision", installed)

    def test_a_backslash_record_is_refused_until_its_hashes_come_from_the_verified_wheel(self):
        before, _ = self._backslash_record_install(self.root / "before")
        with self.assertRaisesRegex(ValueError, r"fails the OA launch check: vision-1\.0\.dist-info/RECORD lists "
                                                r"Lib/site-packages/vision/_C\.pyd without a hash"):
            app._seal_private_python(before, self.root / "before")

        work = self.root / "after"
        portable, wheel = self._backslash_record_install(work)
        site = portable / "python_embeded" / "Lib" / "site-packages"
        self.assertEqual(app._fill_wheel_records(site, self._pins(wheel)),
                         ["vision-1.0.dist-info/vision/_C.pyd", "vision-1.0.dist-info/vision/__init__.py"])
        record = (site / "vision-1.0.dist-info" / "RECORD").read_text(encoding="utf-8")
        self.assertIn("vision/LICENSE,,\n", record)
        receipt = app._seal_private_python(portable, work)
        # kept/__init__.py, its sealed bytecode, and the three vision files.
        self.assertEqual(receipt["launch_check"]["hashed"], 5)
        self.assertEqual(app._fill_wheel_records(site, self._pins(wheel)), [])

    def test_installed_bytes_that_differ_from_the_verified_wheel_stay_refused(self):
        work = self.root / "work"
        portable, wheel = self._backslash_record_install(work, installed=b"MZ replaced")
        site = portable / "python_embeded" / "Lib" / "site-packages"
        record = site / "vision-1.0.dist-info" / "RECORD"
        before = record.read_bytes()
        with self.assertRaisesRegex(ValueError, r"vision-1\.0\.dist-info/RECORD: vision/_C\.pyd differs from "
                                                r"vision-1\.0-cp312-cp312-win_amd64\.whl"):
            app._fill_wheel_records(site, self._pins(wheel))
        self.assertEqual(record.read_bytes(), before)
        with self.assertRaisesRegex(ValueError, r"vision/_C\.pyd without a hash"):
            app._seal_private_python(portable, work)

    def test_a_refusal_in_a_later_record_leaves_an_earlier_fillable_record_unchanged(self):
        work = self.root / "work"
        portable = self._private_python(work)
        site = portable / "python_embeded" / "Lib" / "site-packages"
        wheels = work / "verified-wheels"
        first = self._backslash_wheel(site, wheels, "alpha")
        second = self._backslash_wheel(site, wheels, "vision", installed=b"MZ replaced")
        records = {name: (site / f"{name}-1.0.dist-info" / "RECORD").read_bytes() for name in ("alpha", "vision")}
        with self.assertRaisesRegex(ValueError, r"vision/_C\.pyd differs from"):
            app._fill_wheel_records(site, self._pins(first, second))
        for name, before in records.items():
            with self.subTest(record=name):
                self.assertEqual((site / f"{name}-1.0.dist-info" / "RECORD").read_bytes(), before)

    def test_a_hashless_code_row_no_verified_wheel_installed_is_refused(self):
        work = self.root / "work"
        portable, wheel = self._backslash_record_install(work)
        site = portable / "python_embeded" / "Lib" / "site-packages"
        record = site / "vision-1.0.dist-info" / "RECORD"
        original = record.read_text(encoding="utf-8")
        # Membership is checked before the installed file is read: rows outside
        # the wheel, missing or escaping site-packages, refuse as ValueError.
        for row, message in (("vision/extra.py", r"vision/extra\.py is not in vision-1\.0-cp312-cp312-win_amd64\.whl"),
                             ("vision/absent.py", r"vision/absent\.py is not in"),
                             ("../../outside.py", r"\.\./\.\./outside\.py is not in"),
                             ("../../Scripts/vision.exe", r"Scripts/vision\.exe is not in")):
            with self.subTest(row=row):
                if row == "vision/extra.py":
                    (site / "vision" / "extra.py").write_bytes(b"planted\n")
                record.write_text(original + f"{row},,\n", encoding="utf-8", newline="")
                with self.assertRaisesRegex(ValueError, message):
                    app._fill_wheel_records(site, self._pins(wheel))
        record.write_text(original, encoding="utf-8", newline="")
        (site / "vision" / "__init__.py").unlink()
        with self.assertRaisesRegex(ValueError, r"vision/__init__\.py, which is not installed"):
            app._fill_wheel_records(site, self._pins(wheel))
        other = self.root / "other" / "verified-wheels"
        other.mkdir(parents=True)
        with zipfile.ZipFile(other / "unrelated-1.0-py3-none-any.whl", "w") as archive:
            archive.writestr("unrelated-1.0.dist-info/RECORD", "")
        with self.assertRaisesRegex(ValueError, r"vision-1\.0\.dist-info/RECORD lists vision/_C\.pyd without a hash "
                                                r"and no verified wheel installed it"):
            app._fill_wheel_records(site, self._pins(other / "unrelated-1.0-py3-none-any.whl"))

    def test_the_fill_rehashes_each_wheel_against_its_pin_and_names_its_trust_mode(self):
        work = self.root / "work"
        portable, wheel = self._backslash_record_install(work)
        site = portable / "python_embeded" / "Lib" / "site-packages"
        record = site / "vision-1.0.dist-info" / "RECORD"
        before = record.read_bytes()
        with self.assertRaisesRegex(ValueError, r"vision-1\.0-cp312-cp312-win_amd64\.whl differs from its pinned SHA-256"):
            app._fill_wheel_records(site, {wheel: "0" * 64})
        self.assertEqual(record.read_bytes(), before)
        rule = app._launcher()._hashless_allowed
        for arguments in ({}, {"wheels": self._pins(wheel), "trust_installed": True}):
            with self.subTest(arguments=sorted(arguments)):
                with self.assertRaisesRegex(ValueError, "either verified wheels or trust_installed=True"):
                    app.wheel_records.fill(site, rule, **arguments)
        record.write_text(before.decode("utf-8") + "../../outside.py,,\n", encoding="utf-8", newline="")
        with self.assertRaisesRegex(ValueError, r"names a path outside site-packages: \.\./\.\./outside\.py"):
            app.wheel_records.fill(site, rule, trust_installed=True)

    def test_cpu_conversion_fills_records_from_its_verified_wheels_right_after_pip(self):
        work = self.root / "work"
        portable = work / "extract" / "Portable"
        (portable / "python_embeded").mkdir(parents=True)
        (portable / "python_embeded" / "python.exe").write_bytes(b"fixture")
        wheels = self.root / "wheels"
        wheels.mkdir()
        (wheels / "vision-fixture.whl").write_bytes(b"wheel")
        calls = []

        def run(argv, **kwargs):
            calls.append("pip install" if "install" in argv else "smoke")
        patches = (mock.patch.object(app, "_snapshot_verified_source",
                                     side_effect=lambda source, target, size, digest: shutil.copyfile(source, target)),
                   mock.patch.object(app, "_pip_check", return_value=set()),
                   mock.patch.object(app, "_remove_gpu_files", return_value={}),
                   mock.patch.object(app.subprocess, "run", side_effect=run),
                   mock.patch.object(app, "_fill_wheel_records",
                                     side_effect=lambda site, verified: calls.append(
                                         ("fill", site, sorted((path.name, pin) for path, pin in verified.items()))) or []),
                   mock.patch.object(app, "_discard_relocated_torch_tools", return_value=[]),
                   mock.patch.object(app, "_cpu_wheel_receipts", return_value=[]))
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)
        receipt = app._convert_cpu(portable, wheels, work, {"vision-fixture.whl": (5, "0" * 64, "vision==0.0.0")})
        site = portable / "python_embeded" / "Lib" / "site-packages"
        self.assertEqual(calls, ["pip install", ("fill", site, [("vision-fixture.whl", "0" * 64)]), "smoke"])
        self.assertEqual(receipt["record_hashes_filled_from_verified_wheels"], [])

    def test_cpu_wheel_pin_refusal_has_no_gpu_removal(self):
        work = self.root / "work"
        portable = work / "extract" / "Portable"
        portable.mkdir(parents=True)
        wheels = self.root / "wheels"
        wheels.mkdir()
        (wheels / "torch-fixture.whl").write_bytes(b"wheel")
        with self.assertRaisesRegex(ValueError, "size or SHA-256"):
            app._convert_cpu(portable, wheels, work,
                             {"torch-fixture.whl": (5, "0" * 64, "torch==0.0.0")})
        self.assertTrue(portable.exists())

    def test_cpu_wheel_receipt_requires_installed_license_and_notice(self):
        portable = self.root / "Portable"
        site = portable / "python_embeded" / "Lib" / "site-packages"
        for distribution, (_, _, _, _, license_paths) in app.CPU_WHEEL_PROVENANCE.items():
            version = next(spec[2].split("==", 1)[1] for spec in app.CPU_WHEELS.values()
                           if spec[2].startswith(distribution + "=="))
            for relative in license_paths:
                target = site / f"{distribution}-{version}.dist-info" / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text("license fixture")
        receipts = app._cpu_wheel_receipts(portable, app.CPU_WHEELS)
        self.assertEqual(len(receipts), 3)
        self.assertTrue(all(item["wheel_url"].startswith("https://download.pytorch.org/whl/cpu/")
                            for item in receipts))
        self.assertTrue(all(item["upstream_release_source_url"].startswith("https://github.com/pytorch/")
                            and item["build_commit_verified"] is False for item in receipts))
        self.assertTrue(all(item["installed_license_paths"] for item in receipts))
        (site / "torch-2.9.1+cpu.dist-info" / "NOTICE").unlink()
        with self.assertRaisesRegex(ValueError, "installed license/notice"):
            app._cpu_wheel_receipts(portable, app.CPU_WHEELS)

    def test_generated_torch_launchers_do_not_retain_build_path(self):
        work = self.root / "oa-comfy-repack-fixture"
        portable = work / "extract" / "Portable"
        site = portable / "python_embeded" / "Lib" / "site-packages"
        scripts = portable / "python_embeded" / "Scripts"
        scripts.mkdir(parents=True)
        for distribution in app.CPU_WHEEL_PROVENANCE:
            version = next(spec[2].split("==", 1)[1] for spec in app.CPU_WHEELS.values()
                           if spec[2].startswith(distribution + "=="))
            info = site / f"{distribution}-{version}.dist-info"
            info.mkdir(parents=True)
            lines = []
            if distribution == "torch":
                for name in ("torchfrtrace.exe", "torchrun.exe"):
                    data = b"MZ fixture " + str(work).encode() + b"/extract/Portable/python_embeded/python.exe"
                    (scripts / name).write_bytes(data)
                    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip("=")
                    lines.append(f"../../Scripts/{name},sha256={digest},{len(data)}\n")
            lines.append(f"{info.name}/RECORD,,\n")
            (info / "RECORD").write_text("".join(lines), encoding="utf-8")
        self.assertEqual(app._discard_relocated_torch_tools(portable, work),
                         ["torchfrtrace.exe", "torchrun.exe"])
        self.assertFalse(list(scripts.iterdir()))
        torch_record = site / "torch-2.9.1+cpu.dist-info" / "RECORD"
        self.assertNotIn("../../Scripts/", torch_record.read_text())


class RepackSourceTests(_Stage):
    def _git_fixture(self, root: Path, files: dict[str, bytes]) -> str:
        root.mkdir(parents=True)
        subprocess.run(["git", "-C", str(root), "init", "-q"], check=True)
        for name, data in files.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        subprocess.run(["git", "-C", str(root), "add", "-A"], check=True)
        subprocess.run(["git", "-C", str(root), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"], check=True)
        return subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()

    def test_public_integration_source_closes_every_pinned_repository(self):
        adopter = self.root / "adopter-comfyui"
        identity = self.root / "abstraction-identity"
        charter = self.root / "abstractions"
        adopter_head = self._git_fixture(adopter, {"LICENSE": b"Apache fixture", "__init__.py": b"# node\n"})
        identity_head = self._git_fixture(identity, {"LICENSE": b"Apache fixture", "py/pyproject.toml": b"# wheel\n"})
        charter_head = self._git_fixture(charter, {"LICENSE": b"Apache fixture", "scripts/package_comfyui_app.py": b"# packager\n"})
        manifest = {"source_mode": "public-split",
                    "adopter_source": {"repository": "openabstractions/adopter-comfyui", "commit": adopter_head},
                    "packager_source": {"repository": "openabstractions/abstractions", "commit": charter_head},
                    "wheels": [{"repository": "openabstractions/abstraction-identity", "commit": identity_head}]}
        source_zip = self.root / "public-source.zip"
        checkouts = {"openabstractions/adopter-comfyui": adopter,
                     "openabstractions/abstractions": charter,
                     "openabstractions/abstraction-identity": identity}
        with zipfile.ZipFile(source_zip, "w") as archive:
            paths = app._append_public_integration_sources(archive, manifest, checkouts, self.root)
        with zipfile.ZipFile(source_zip) as archive:
            self.assertEqual(set(paths), {f"OA-Integration-adopter-comfyui-{adopter_head}/",
                                          f"OA-Integration-abstraction-identity-{identity_head}/",
                                          f"OA-Integration-abstractions-{charter_head}/"})
            self.assertIn(f"OA-Integration-adopter-comfyui-{adopter_head}/__init__.py", archive.namelist())
            self.assertIn(f"OA-Integration-abstraction-identity-{identity_head}/py/pyproject.toml", archive.namelist())
        (identity / "py" / "pyproject.toml").write_text("# dirty\n", encoding="utf-8")
        with zipfile.ZipFile(self.root / "dirty-source.zip", "w") as archive:
            with self.assertRaisesRegex(ValueError, "source checkout differs"):
                app._append_public_integration_sources(archive, manifest, checkouts, self.root)

    @unittest.skipUnless(shutil.which("tar.exe") and shutil.which("git"), "bsdtar and Git required")
    def test_combined_corresponding_source_and_frontend_license(self):
        portable = self.root / "payload" / "Portable"
        core = portable / "ComfyUI"
        core_head = self._git_fixture(core, {"LICENSE": b"core GPL fixture", "main.py": b"pass\n"})
        (portable / "README.txt").write_bytes(b"portable")
        private_python = portable / "python_embeded" / "python.exe"
        private_python.parent.mkdir()
        private_python.write_bytes(b"fixture executable")
        frontend = self.root / "frontend"
        frontend_head = self._git_fixture(frontend, {"LICENSE": b"frontend GPL fixture", "package.json": b'{"version":"1.51.10"}', "src/main.ts": b"export {};\n"})
        source_roots = {}
        source_pins = {}
        for project in ("embedded-docs", "pygit2", "libgit2"):
            checkout = self.root / project
            head = self._git_fixture(checkout, {"LICENSE": (project + " license").encode(),
                                                "source.txt": (project + " source").encode()})
            source_roots[project] = checkout
            source_pins[project] = (head, f"https://example.invalid/{project}/{head}.tar.gz")
        integration = self._integration_fixture()
        integration_data = integration.read_bytes()
        integration_args = dict(integration_archive=integration, install_wheels=False,
                                expected_integration_size=len(integration_data),
                                expected_integration_sha256=hashlib.sha256(integration_data).hexdigest(),
                                additional_source_checkouts=source_roots,
                                additional_source_pins=source_pins)
        with tarfile.open(self.source, "w") as archive:
            archive.add(portable, arcname="Portable")
        data = self.source.read_bytes()
        output = self.root / "app.zip"
        source_output = self.root / "source.zip"
        manifest = app.repack(
            self.source, output, source_output,
            expected_size=len(data), expected_sha256=hashlib.sha256(data).hexdigest(),
            archive_root="Portable", expected_commit=core_head,
            frontend_checkout=frontend, expected_frontend_commit=frontend_head,
            max_expanded_bytes=1 << 20, **integration_args,
        )
        with zipfile.ZipFile(source_output) as sources:
            self.assertIn(f"ComfyUI-{core_head}/main.py", sources.namelist())
            self.assertIn(f"ComfyUI_frontend-{frontend_head}/src/main.ts", sources.namelist())
            for project, (head, _) in source_pins.items():
                self.assertIn(f"{project}-{head}/source.txt", sources.namelist())
            self.assertEqual(sources.read("OA-Integration-Source/node/abstraction_downloads/__init__.py"), b"# node\n")
            self.assertEqual(sources.read("OA-Integration-Source/LICENSE"), b"OA Apache fixture")
            self.assertEqual(json.loads(sources.read("OA-Integration-Source/manifest.json"))["source_commit"],
                             app.INTEGRATION_SOURCE_COMMIT)
        with zipfile.ZipFile(output) as packed:
            self.assertEqual(packed.read("OA-LICENSES/ComfyUI_frontend-LICENSE"), b"frontend GPL fixture")
            self.assertEqual(packed.read("OA-LICENSES/OpenAbstractions-LICENSE"), b"OA Apache fixture")
            self.assertEqual(packed.read("OA-LICENSES/OpenAbstractions-NOTICE"), b"OA notice fixture")
            self.assertEqual(packed.read("Portable/ComfyUI/custom_nodes/abstraction_downloads/__init__.py"), b"# node\n")
            self.assertIn(app.PROVENANCE, packed.namelist())
        self.assertEqual(manifest["corresponding_comfyui_source_zip_sha256"], hashlib.sha256(source_output.read_bytes()).hexdigest())
        self.assertEqual(manifest["oa_integration"]["sha256"], integration_args["expected_integration_sha256"])
        self.assertIn("OA-Integration-Source/node/abstraction_downloads/__init__.py",
                      manifest["oa_integration_corresponding_source_paths"])
        self.assertEqual({key: value["commit"] for key, value in manifest["additional_corresponding_sources"].items()},
                         {key: value[0] for key, value in source_pins.items()})
        self.assertEqual(set(manifest["added_notice_paths"]), {"OA-LICENSES/ComfyUI_frontend-LICENSE",
                                                                "OA-LICENSES/OpenAbstractions-LICENSE",
                                                                "OA-LICENSES/OpenAbstractions-NOTICE"})

        # The same source must refuse a different frontend commit before output.
        with self.assertRaisesRegex(ValueError, "pinned v1.51.10 commit"):
            app.repack(self.source, self.root / "wrong.zip", self.root / "wrong-source.zip",
                       expected_size=len(data), expected_sha256=hashlib.sha256(data).hexdigest(),
                       archive_root="Portable", expected_commit=core_head,
                       frontend_checkout=frontend, expected_frontend_commit="0" * 40,
                       max_expanded_bytes=1 << 20, **integration_args)
        self.assertFalse((self.root / "wrong.zip").exists())
        with self.assertRaisesRegex(ValueError, "frontend source version"):
            app.repack(self.source, self.root / "version.zip", self.root / "version-source.zip",
                       expected_size=len(data), expected_sha256=hashlib.sha256(data).hexdigest(),
                       archive_root="Portable", expected_commit=core_head,
                       frontend_checkout=frontend, expected_frontend_commit=frontend_head,
                       expected_frontend_version="0.0.0", max_expanded_bytes=1 << 20,
                       **integration_args)
        self.assertFalse((self.root / "version.zip").exists())
        with self.assertRaisesRegex(ValueError, "size or SHA-256"):
            app.repack(self.source, self.root / "pin.zip", self.root / "pin-source.zip",
                       expected_size=len(data), expected_sha256=hashlib.sha256(data).hexdigest(),
                       archive_root="Portable", expected_commit=core_head,
                       frontend_checkout=frontend, expected_frontend_commit=frontend_head,
                       max_expanded_bytes=1 << 20,
                       **{**integration_args, "expected_integration_sha256": "0" * 64})
        self.assertFalse((self.root / "pin.zip").exists())
        with self.assertRaisesRegex(ValueError, "pygit2 source checkout differs"):
            app.repack(self.source, self.root / "source-pin.zip", self.root / "source-pin-source.zip",
                       expected_size=len(data), expected_sha256=hashlib.sha256(data).hexdigest(),
                       archive_root="Portable", expected_commit=core_head,
                       frontend_checkout=frontend, expected_frontend_commit=frontend_head,
                       max_expanded_bytes=1 << 20,
                       **{**integration_args,
                          "additional_source_pins": {**source_pins, "pygit2": ("0" * 40, "https://example.invalid/wrong")}})
        self.assertFalse((self.root / "source-pin.zip").exists())
        original_run = subprocess.run

        def reject_pip(command, *args, **kwargs):
            if len(command) >= 5 and command[1:5] == ["-B", "-s", "-m", "pip"]:
                self.assertIn("--no-index", command)
                self.assertIn("--require-hashes", command)
                self.assertEqual(kwargs["env"]["PYTHONNOUSERSITE"], "1")
                self.assertEqual(kwargs["env"]["PIP_CONFIG_FILE"], os.devnull)
                self.assertNotIn("PYTHONPATH", kwargs["env"])
                self.assertNotIn("PIP_TARGET", kwargs["env"])
                raise subprocess.CalledProcessError(1, command)
            return original_run(command, *args, **kwargs)

        with mock.patch.object(app.subprocess, "run", side_effect=reject_pip):
            with self.assertRaises(subprocess.CalledProcessError):
                app.repack(self.source, self.root / "pip-fail.zip", self.root / "pip-fail-source.zip",
                           expected_size=len(data), expected_sha256=hashlib.sha256(data).hexdigest(),
                           archive_root="Portable", expected_commit=core_head,
                           frontend_checkout=frontend, expected_frontend_commit=frontend_head,
                           max_expanded_bytes=1 << 20,
                           **{**integration_args, "install_wheels": True})
        self.assertFalse((self.root / "pip-fail.zip").exists())
        self.assertFalse(list(self.root.glob("oa-comfy-repack-*")))

    @unittest.skipUnless(shutil.which("tar.exe"), "bsdtar.exe is required for extraction boundary test")
    def test_bsdtar_refuses_traversal_and_link_escape(self):
        outside = self.root / "outside.exe"
        traversal = self.root / "traversal.tar"
        with tarfile.open(traversal, "w") as archive:
            entry = tarfile.TarInfo("Portable/../../outside.exe")
            entry.size = 1
            archive.addfile(entry, io.BytesIO(b"x"))
        with self.assertRaisesRegex(ValueError, "unsafe archive member"):
            app._listed_members(traversal, "Portable", "tar.exe")
        target = self.root / "stage"
        target.mkdir()
        result = subprocess.run(["tar.exe", "-xf", str(traversal), "-C", str(target)], capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(outside.exists())

        linked = self.root / "linked.tar"
        with tarfile.open(linked, "w") as archive:
            link = tarfile.TarInfo("Portable/shortcut")
            link.type = tarfile.SYMTYPE
            link.linkname = str(self.root)
            archive.addfile(link)
            payload = tarfile.TarInfo("Portable/shortcut/outside.exe")
            payload.size = 1
            archive.addfile(payload, io.BytesIO(b"x"))
        result = subprocess.run(["tar.exe", "-xf", str(linked), "-C", str(target)], capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(outside.exists())


if __name__ == "__main__":
    unittest.main()
