import subprocess
import tempfile
import unittest
import base64
import csv
import hashlib
import io
import json
import zipfile
from pathlib import Path
from unittest import mock
import importlib.util
import sys
import types
import shutil
import os

from package_comfyui_integration import (copy_sdk_source, matches_revision,
                                         compose_wrapper, extension_payload_owner, stage_wrapper,
                                         copy_tracked_checkout, load_public_wheel_receipt, read_pypi_pins,
                                         require_native_only_wheelhouse, untracked_source_files,
                                         GRAPH_FILES)


class NativeGraphRosterTests(unittest.TestCase):
    def test_current_wrapper_excludes_retired_shared_folder_provider(self):
        root = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as owned:
            destination = Path(owned) / "wrapper"
            pins = stage_wrapper(root, root / "openabstractions-flat/adopter-comfyui", destination)
            self.assertIn("oa_nodes/__init__.py", pins)
            self.assertIn("oa_nodes/service.py", pins)
            self.assertFalse(any("_vendor" in Path(name).parts for name in pins))
            for name in ("oa_nodes/__init__.py", "oa_nodes/service.py"):
                raw = (destination / name).read_bytes()
                self.assertNotIn(b"ABSTRACTION_COMFYUI_PROVIDER", raw)
                self.assertEqual(pins[name], {"bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()})
            # This receives the real selected source payload, without wheels,
            # native ABI execution or a claim of a qualified product ZIP.

    def test_public_repository_layout_stages_no_go_sources(self):
        # The public adopter-comfyui repository holds connector/ (Go) at its root beside the nodes.
        root = Path(__file__).resolve().parents[1]
        owner = extension_payload_owner(root / "installer/apps/build_extension.py")
        with tempfile.TemporaryDirectory() as owned:
            public = Path(owned) / "adopter-comfyui"
            shutil.copytree(root / "openabstractions-flat/adopter-comfyui", public,
                            ignore=shutil.ignore_patterns("__pycache__", "build"))
            shutil.copytree(root / "adopters/comfyui/connector", public / "connector")
            included = owner.include_files([{"from": "adopter-comfyui", "to": "oa_nodes"}], [], root=Path(owned))
            names = {name.as_posix() for name in included}
            self.assertIn("oa_nodes/__init__.py", names)
            self.assertIn("oa_nodes/service.py", names)
            self.assertFalse([name for name in names if name.endswith(".go")
                              or name.rsplit("/", 1)[-1] in ("go.mod", "go.sum")])
            self.assertFalse([name for name in names if name.startswith("oa_nodes/connector/")])

    def test_selected_wrapper_loads_the_graph_bridge_with_no_frontend_pin(self):
        root = Path(__file__).resolve().parents[1]
        node = root / "adopters/comfyui/node"
        owner = extension_payload_owner(root / "installer/apps/build_extension.py")
        files = {path.as_posix() for path in owner.payload_files(node)}
        self.assertTrue(GRAPH_FILES <= files)
        self.assertNotIn("web/oa_appmodel_frontend.json", files)
        self.assertFalse(any(name.endswith(".test.mjs") for name in files))
        self.assertIn("oa_appmodel_bridge.install()", (node / "__init__.py").read_text(encoding="utf-8"))


class AppModelCompanionTests(unittest.TestCase):
    """Real wheel/source admission over inert bytes; native PE owner is a stand-in."""
    def fixture(self, root, mode='valid'):
        import package_comfyui_integration as owner
        package=root/'abstraction_facade_native';package.mkdir()
        (package/'__init__.py').write_bytes(b'# selected companion source\n')
        license_file=root/'LICENSE';license_file.write_bytes(b'selected source licence')
        library=b'MZ inert source-admission DATA; never loaded'
        dist='abstraction_facade_native-0.8.0.dist-info/'
        members={'abstraction_facade_native/__init__.py':(package/'__init__.py').read_bytes(),
            owner.FACADE_NATIVE_MEMBER:library,dist+'LICENSE':license_file.read_bytes(),
            dist+'METADATA':b'Name: abstraction-facade-native\nVersion: 0.8.0\n',
            dist+'WHEEL':b'Wheel-Version: 1.0\nTag: py3-none-win_amd64\n'}
        if mode=='source':members['abstraction_facade_native/__init__.py']=b'# another source\n'
        if mode=='license':members[dist+'LICENSE']=b'other licence'
        if mode=='library':members[owner.FACADE_NATIVE_MEMBER]=b'another library'
        if mode=='extra':members['../escape.py']=b'not selected'
        record=io.StringIO(newline='');writer=csv.writer(record,lineterminator='\n')
        for name,raw in members.items():writer.writerow((name,'sha256='+base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip('='),str(len(raw))))
        writer.writerow((dist+'RECORD','',''));members[dist+'RECORD']=record.getvalue().encode()
        wheel=root/'abstraction_facade_native-0.8.0-py3-none-win_amd64.whl'
        with zipfile.ZipFile(wheel,'w') as archive:
            for name,raw in members.items():
                entry=zipfile.ZipInfo(name);entry.external_attr=(0o120777 if mode=='link' and name==owner.FACADE_NATIVE_MEMBER else 0o100644)<<16
                archive.writestr(entry,raw)
            if mode=='duplicate':archive.writestr('abstraction_facade_native/__init__.py',members['abstraction_facade_native/__init__.py'])
        return owner,wheel,package,license_file,hashlib.sha256(library).hexdigest()

    def test_selected_companion_binds_actual_python_library_license_and_platform(self):
        with tempfile.TemporaryDirectory() as owned:
            owner,wheel,package,license_file,pin=self.fixture(Path(owned))
            with mock.patch('py_wheels.check',return_value=('0.8.0','win_amd64',[])) as native:
                self.assertEqual(owner.verify_appmodel_wheel(wheel,package,license_file,'0.8.0',pin),wheel)
                native.assert_called_once_with(wheel)

    def test_changed_source_hash_license_and_unsafe_members_refuse_before_native_owner(self):
        for mode in ('source','library','license','extra','link','duplicate'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as owned:
                owner,wheel,package,license_file,pin=self.fixture(Path(owned),mode)
                with mock.patch('py_wheels.check') as native, self.assertRaises(ValueError):
                    owner.verify_appmodel_wheel(wheel,package,license_file,'0.8.0',pin)
                native.assert_not_called()

    def test_companion_selection_adds_only_the_appmodel_extra(self):
        import package_comfyui_integration as owner
        entry=dict(filename='abstraction_facade_client-0.0.0-py3-none-any.whl',sha256='a'*64)
        self.assertIn('abstraction-facade-client[appmodel]==',owner.wheel_requirement(entry,appmodel=True))
        self.assertNotIn('[appmodel]',owner.wheel_requirement(entry))
        base=owner.public_distributions('0.8.0')
        self.assertEqual(set(owner.public_distributions('0.8.0',appmodel=True))-set(base),{'abstraction-facade-native'})

    def test_public_companion_pin_must_share_exact_facade_source(self):
        import package_comfyui_integration as owner
        for mode in ('valid','commit','repository','directory'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as owned:
                rows=['package\trepository\tcommit\tdirectory']
                for name in owner.public_distributions('0.8.0'):
                    repo='abstraction-facade' if name=='abstraction-facade-client' else name
                    rows.append(f'{name}\topenabstractions/{repo}\t'+ 'a'*40+'\tpy')
                repo='another' if mode=='repository' else 'abstraction-facade'
                commit=('b' if mode=='commit' else 'a')*40
                directory='py' if mode=='directory' else 'py-native'
                rows.append(f'abstraction-facade-native\topenabstractions/{repo}\t{commit}\t{directory}')
                pins=Path(owned)/'pypi.tsv';pins.write_text('\n'.join(rows)+'\n',encoding='utf-8')
                if mode=='valid':self.assertIn('abstraction-facade-native',owner.read_pypi_pins(pins))
                else:
                    with self.assertRaisesRegex(ValueError,'same|share'):owner.read_pypi_pins(pins)






class WheelTemporaryPathTests(unittest.TestCase):
    def run_private_build(self, fail_during_build):
        import package_comfyui_integration as packager

        with tempfile.TemporaryDirectory(prefix="oa-wheel-path-test-") as owned:
            owned = Path(owned).resolve()
            checkout = owned / ("long-checkout-" + "x" * 100)
            wheelhouse = owned / "native"
            wheelhouse.mkdir()
            (wheelhouse / "abstraction_ipc-0.4.0-py3-none-win_amd64.whl").write_bytes(b"inert native fixture")
            copied_paths = []
            wheel_calls = []

            def copy_source(source, destination):
                copied_paths.append(destination)
                self.assertNotIn(checkout, destination.parents)
                self.assertEqual(destination.parents[1].parent, Path(tempfile.gettempdir()))
                self.assertLess(len(str(destination)), len(str(checkout)))
                destination.mkdir(parents=True)

            def run(command, **kwargs):
                if command[0] == "git":
                    return subprocess.CompletedProcess(command, 0, stdout="1\n")
                self.assertEqual(command[1:4], ["-m", "pip", "wheel"])
                wheel_calls.append(command)
                if fail_during_build:
                    raise subprocess.CalledProcessError(1, command)
                output = Path(command[command.index("--wheel-dir") + 1])
                for name in packager.PURE_WHEELS:
                    (output / name).write_bytes(b"inert wheel fixture")
                return subprocess.CompletedProcess(command, 0)

            argv = ["package_comfyui_integration.py", "--wheelhouse", str(wheelhouse),
                    "--output", str(owned / "result.zip"), "--source-commit", "a" * 40,
                    "--native-source-commit", "b" * 40, "--native-library-sha256", "c" * 64]
            with mock.patch.object(packager, "__file__", str(checkout / "scripts" / "package_comfyui_integration.py")), \
                    mock.patch.object(packager.sys, "argv", argv), \
                    mock.patch.object(packager.sys, "path", list(packager.sys.path)), \
                    mock.patch.object(packager, "untracked_source_files", return_value=[]), \
                    mock.patch.object(packager, "matches_revision", return_value=True), \
                    mock.patch.object(packager, "copy_sdk_source", side_effect=copy_source), \
                    mock.patch.object(packager.subprocess, "run", side_effect=run):
                with self.assertRaises(subprocess.CalledProcessError if fail_during_build else zipfile.BadZipFile):
                    packager.main()
            self.assertEqual(len(wheel_calls), 1 if fail_during_build else len(packager.PURE_SOURCES))
            self.assertTrue(copied_paths)
            for copied in copied_paths:
                self.assertFalse(copied.parents[1].exists(), "owned build root must be cleaned on failure")
            self.assertFalse((checkout / ".build").exists())

    def test_long_checkout_uses_short_owned_build_path_and_cleans_failed_build(self):
        self.run_private_build(fail_during_build=True)

    def test_short_build_path_survives_until_native_verification_then_cleans(self):
        self.run_private_build(fail_during_build=False)


class PackageSourceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="oa-package-source-")
        self.root = Path(self.temporary.name).resolve()
        (self.root / "node").mkdir()
        (self.root / "node" / "committed.py").write_text("value = 1\n", encoding="utf-8")
        self.git("init", "-q")
        # The fixture's committed bytes must also match intentionally inherited
        # Git calls in the foreign-environment negative below.
        self.git("config", "core.autocrlf", "false")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Package Test")
        self.git("add", "node/committed.py")
        self.git("commit", "-qm", "base")

    def tearDown(self):
        self.temporary.cleanup()

    def git(self, *arguments):
        from git_env import source_git_environment
        subprocess.run(["git", "-C", str(self.root), *arguments], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       env=source_git_environment())

    def git_output(self, *arguments):
        from git_env import source_git_environment
        return subprocess.run(["git", "-C", str(self.root), *arguments], check=True,
                              capture_output=True, text=True,
                              env=source_git_environment()).stdout.strip()

    def test_untracked_source_is_rejected(self):
        (self.root / "node" / "untracked.py").write_text("unsafe = True\n", encoding="utf-8")
        self.assertEqual(untracked_source_files(self.root, ["node"]), ["node/untracked.py"])

    def test_redist_comfy_inputs_remain_clean_after_windows_checkout(self):
        import package_comfyui_integration as owner
        from git_env import source_git_environment
        repository = Path(__file__).resolve().parents[1]
        files = {
            '.gitattributes': (repository / 'research/org-edits/redist/.gitattributes').read_bytes(),
            'pypi.tsv': (repository / 'research/org-edits/redist/pypi.tsv').read_bytes(),
            'windows/apps/build_app.py': (repository / 'installer/apps/build_app.py').read_bytes(),
            'windows/apps/bundle-actions/actions.cpp': (repository / 'installer/apps/bundle-actions/actions.cpp').read_bytes(),
        }
        for name, raw in files.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(raw)
        self.git('add', *files)
        self.git('commit', '-qm', 'selected Redist Comfy inputs')
        revision = self.git_output('rev-parse', 'HEAD')
        self.git('config', '--unset', 'core.autocrlf')
        # Actions checkout inherits Windows' global autocrlf. The source reader
        # independently suppresses global config, so checkout attributes must
        # keep its authenticated readset equal to the committed source bytes.
        global_config = self.root / 'fixture-global.gitconfig'
        global_config.write_bytes(b'[core]\n\tautocrlf = true\n')
        checkout_environment = source_git_environment()
        checkout_environment['GIT_CONFIG_GLOBAL'] = str(global_config)
        received = [name for name in files if name != '.gitattributes']
        for name in received:
            (self.root / name).unlink()
        subprocess.run(['git', '-C', str(self.root), 'checkout-index', '--index', '--force', '--', *received],
                       check=True, capture_output=True, env=checkout_environment, timeout=30)
        ordinary_status = subprocess.run(['git', '-C', str(self.root), 'status', '--porcelain=v1',
                                         '--untracked-files=all', '--', *received], check=True,
                                        capture_output=True, env=checkout_environment, timeout=30)
        self.assertEqual(ordinary_status.stdout, b'')
        # Recheck content rather than relying on checkout's cached stat data.
        for name in received:
            path = self.root / name
            information = path.stat()
            os.utime(path, ns=(information.st_atime_ns, information.st_mtime_ns + 2_000_000_000))
        for directory in ('pypi.tsv', 'windows/apps'):
            owner.verify_public_checkout(self.root, revision, directory)
        for name, raw in files.items():
            self.assertEqual((self.root / name).read_bytes(), raw)
        (self.root / 'pypi.tsv').write_bytes(files['pypi.tsv'] + b'changed source\n')
        with self.assertRaisesRegex(ValueError, 'dirty'):
            owner.verify_public_checkout(self.root, revision, 'pypi.tsv')
        (self.root / 'windows/apps/untracked.py').write_bytes(b'not in the selected commit\n')
        with self.assertRaisesRegex(ValueError, 'dirty'):
            owner.verify_public_checkout(self.root, revision, 'windows/apps')

    def test_foreign_git_environment_cannot_spoof_private_or_public_source(self):
        import package_comfyui_integration as owner
        revision=self.git_output('rev-parse','HEAD')
        foreign=self.root/'foreign';foreign.mkdir()
        shutil.copytree(self.root/'.git',foreign/'.git')
        (foreign/'node').mkdir();(foreign/'node/committed.py').write_text('value = 1\n',encoding='utf-8')
        redirected={'GIT_DIR':str(foreign/'.git'),'GIT_WORK_TREE':str(foreign),
                    'GIT_INDEX_FILE':str(foreign/'.git/index'),
                    'GIT_CONFIG_COUNT':'1','GIT_CONFIG_KEY_0':'core.fsmonitor','GIT_CONFIG_VALUE_0':'false'}
        (self.root/'node/committed.py').write_text('value = 2\n',encoding='utf-8')
        (self.root/'node/untracked.py').write_text('untracked = True\n',encoding='utf-8')
        with mock.patch.dict(os.environ,redirected):
            # Original inherited-env commands see the clean foreign tree.
            old=subprocess.run(['git','-C',str(self.root),'diff','--quiet',revision,'--','node'],check=False,timeout=30)
            self.assertEqual(old.returncode,0)
            old_status=subprocess.run(['git','-C',str(self.root),'status','--porcelain=v1','--untracked-files=all','--','node'],check=True,capture_output=True,text=True,timeout=30)
            self.assertEqual(old_status.stdout,'')
            self.assertFalse(owner.matches_revision(self.root,revision,['node']))
            self.assertEqual(owner.untracked_source_files(self.root,['node']),['node/untracked.py'])
            with self.assertRaisesRegex(ValueError,'dirty'):
                owner.verify_public_checkout(self.root,revision,'node')
        (self.root/'node/untracked.py').unlink()
        (self.root/'node/committed.py').write_text('value = 1\n',encoding='utf-8')
        with mock.patch.dict(os.environ,redirected):
            self.assertTrue(owner.matches_revision(self.root,revision,['node']))
            self.assertEqual(owner.untracked_source_files(self.root,['node']),[])
            owner.verify_public_checkout(self.root,revision,'node')
            self.assertEqual(owner.source_epoch(self.root,revision),int(self.git_output_clean('show','-s','--format=%ct',revision)))
        (self.root/'node/extra.py').write_text('extra = True\n',encoding='utf-8')
        self.git('add','node/extra.py');self.git('commit','-qm','extra source')
        with mock.patch.dict(os.environ,redirected):
            destination=self.root/'tracked-copy'
            owner.copy_tracked_checkout(self.root,destination)
            self.assertEqual((destination/'node/extra.py').read_text(), 'extra = True\n')

    def git_output_clean(self,*arguments):
        from git_env import source_git_environment
        return subprocess.run(['git','-C',str(self.root),*arguments],check=True,capture_output=True,text=True,
                              env=source_git_environment(),timeout=30).stdout.strip()

    def test_node_and_native_sources_can_have_distinct_pins(self):
        (self.root / "native").mkdir()
        (self.root / "native" / "ipc.cpp").write_text("int version = 1;\n", encoding="utf-8")
        self.git("add", "native/ipc.cpp")
        self.git("commit", "-qm", "native base")
        node_commit = self.git_output("rev-parse", "HEAD")
        (self.root / "native" / "ipc.cpp").write_text("int version = 2;\n", encoding="utf-8")
        self.git("commit", "-qam", "native update")
        native_commit = self.git_output("rev-parse", "HEAD")
        self.assertTrue(matches_revision(self.root, node_commit, ["node"]))
        self.assertTrue(matches_revision(self.root, native_commit, ["native"]))
        self.assertFalse(matches_revision(self.root, node_commit, ["native"]))

    def test_ignored_build_output_is_not_treated_as_package_source(self):
        pure = self.root / "openabstractions-flat" / "abstraction-facade" / "py"
        source_file = pure / "abstraction" / "facade" / "committed.py"
        source_file.parent.mkdir(parents=True)
        source_file.write_text("committed = True\n", encoding="utf-8")
        self.git("add", "openabstractions-flat/abstraction-facade/py")
        self.git("commit", "-qm", "pure source")
        build_file = pure / "build" / "lib" / "abstraction" / "facade" / "generated.py"
        build_file.parent.mkdir(parents=True)
        build_file.write_text("generated = True\n", encoding="utf-8")
        self.assertEqual(untracked_source_files(self.root, ["openabstractions-flat/abstraction-facade/py"]), [])
        destination = self.root / "copy"
        copy_sdk_source(pure, destination)
        self.assertTrue((destination / "abstraction" / "facade" / "committed.py").is_file())
        self.assertFalse((destination / "build" / "lib" / "abstraction" / "facade" / "generated.py").exists())

    def test_wheelhouse_rejects_arbitrary_same_name_pure_wheel(self):
        native = self.root / "abstraction_ipc-0.4.0-py3-none-win_amd64.whl"
        native.touch()
        supplied = self.root / "abstraction_asks_protocol-0.0.0-py3-none-any.whl"
        supplied.touch()
        with self.assertRaisesRegex(ValueError, "only the pinned native IPC wheel"):
            require_native_only_wheelhouse([native, supplied], native.name)

    def test_wheelhouse_requires_native_wheel(self):
        with self.assertRaisesRegex(ValueError, "missing Windows x64 native wheel"):
            require_native_only_wheelhouse([], "abstraction_ipc-0.4.0-py3-none-win_amd64.whl")

    def test_public_pins_fail_closed_until_every_wheel_is_pinned(self):
        pins = self.root / "pypi.tsv"
        pins.write_text(
            "package\trepository\tcommit\tdirectory\n"
            "abstraction-ipc\topenabstractions/abstraction-identity\t" + "a" * 40 + "\tpy\n",
            encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "missing required Comfy wheels"):
            read_pypi_pins(pins)

    def test_public_copy_excludes_ignored_untracked_files(self):
        (self.root / ".gitignore").write_text("node/ignored-source.py\n", encoding="utf-8")
        self.git("add", ".gitignore")
        self.git("commit", "-qm", "ignore generated source")
        ignored = self.root / "node" / "ignored-source.py"
        ignored.write_text("unpublished = True\n", encoding="utf-8")
        destination = self.root / "package-node"
        copy_tracked_checkout(self.root, destination)
        self.assertTrue((destination / "node" / "committed.py").is_file())
        self.assertFalse((destination / "node" / "ignored-source.py").exists())

    def test_public_receipt_is_bound_to_every_pin_and_exact_wheel_bytes(self):
        from git_env import source_git_environment
        from package_comfyui_integration import LEGACY_PUBLIC_DISTRIBUTIONS as PUBLIC_DISTRIBUTIONS

        source_root = self.root / "public"
        wheel_root = self.root / "wheels"
        wheel_root.mkdir()
        pin_path = self.root / "pypi.tsv"
        pin_lines = ["package\trepository\tcommit\tdirectory"]
        pins = {}
        entries = []
        for index, (distribution, wheel_prefix) in enumerate(PUBLIC_DISTRIBUTIONS.items()):
            repo = f"openabstractions/{distribution}"
            checkout = source_root / distribution
            source_dir = checkout / "py"
            source_dir.mkdir(parents=True)
            (source_dir / "source.py").write_text("# pinned source\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(checkout), "init", "-q"], check=True, env=source_git_environment())
            subprocess.run(["git", "-C", str(checkout), "config", "core.autocrlf", "false"], check=True, env=source_git_environment())
            subprocess.run(["git", "-C", str(checkout), "config", "user.email", "test@example.invalid"], check=True, env=source_git_environment())
            subprocess.run(["git", "-C", str(checkout), "config", "user.name", "Package Test"], check=True, env=source_git_environment())
            subprocess.run(["git", "-C", str(checkout), "add", "py/source.py"], check=True, env=source_git_environment())
            subprocess.run(["git", "-C", str(checkout), "commit", "-qm", "pinned source"], check=True, env=source_git_environment())
            commit = subprocess.run(["git", "-C", str(checkout), "rev-parse", "HEAD"], check=True,
                                    capture_output=True, text=True, env=source_git_environment()).stdout.strip()
            pins[distribution] = {"package": distribution, "repository": repo, "commit": commit, "directory": "py"}
            pin_lines.append(f"{distribution}\t{repo}\t{commit}\tpy")
            platform_tag = "win_amd64" if distribution == "abstraction-ipc" else "any"
            package_version = "0.4.0" if distribution == "abstraction-ipc" else "0.0.0"
            filename = f"{wheel_prefix}-{package_version}-py3-none-{platform_tag}.whl"
            wheel = wheel_root / filename
            wheel_dist_info = f"{wheel_prefix}-{package_version}.dist-info"
            members = {
                f"{wheel_dist_info}/METADATA": f"Metadata-Version: 2.1\nName: {distribution}\nVersion: {package_version}\n\n".encode(),
                f"{wheel_dist_info}/WHEEL": f"Wheel-Version: 1.0\nGenerator: test\nRoot-Is-Purelib: true\nTag: py3-none-{platform_tag}\n".encode(),
                "package.py": f"wheel-{index}".encode(),
            }
            record = io.StringIO(newline="")
            writer = csv.writer(record, lineterminator="\n")
            for member_name, contents in sorted(members.items()):
                digest = base64.urlsafe_b64encode(hashlib.sha256(contents).digest()).decode().rstrip("=")
                writer.writerow((member_name, f"sha256={digest}", str(len(contents))))
            writer.writerow((f"{wheel_dist_info}/RECORD", "", ""))
            members[f"{wheel_dist_info}/RECORD"] = record.getvalue().encode()
            with zipfile.ZipFile(wheel, "w") as archive:
                for member_name, contents in members.items():
                    archive.writestr(member_name, contents)
            data = wheel.read_bytes()
            entry = {"distribution": distribution, "repository": repo, "commit": commit,
                     "directory": "py", "filename": filename, "bytes": len(data),
                     "sha256": hashlib.sha256(data).hexdigest()}
            if distribution == "abstraction-ipc":
                entry["native_library_sha256"] = "b" * 64
            entries.append(entry)
        pin_path.write_text("\n".join(pin_lines) + "\n", encoding="utf-8")
        receipt_path = self.root / "wheel-inputs.json"
        receipt_path.write_text(json.dumps({"schema": 1, "candidate_source": "c" * 40,
                                            "candidate_version": "0.4.0",
                                            "run_id": 123, "run_attempt": 2,
                                            "platform": "win32-x64", "wheels": entries}), encoding="utf-8")
        loaded_pins = read_pypi_pins(pin_path,candidate_version='0.4.0')
        native = next(entry for entry in entries if entry["distribution"] == "abstraction-ipc")
        with mock.patch("py_wheels.verify_native_receipt_wheel",
                        return_value=(wheel_root / native["filename"], b"verified DLL", native)) as verify:
            wheels, receipt, native_bytes = load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                                                       "c" * 40, source_root, "0.4.0", 123, 2)
            verify.assert_called_once_with(receipt, wheel_root.resolve(),
                                           repository=native["repository"], commit=native["commit"],
                                           directory="py")
        self.assertEqual(len(wheels), len(PUBLIC_DISTRIBUTIONS))
        self.assertEqual(receipt["run_attempt"], 2)
        self.assertEqual(native_bytes, b"verified DLL")
        with self.assertRaisesRegex(ValueError, "selected candidate run"):
            load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                      "c" * 40, source_root, "0.4.1", 123, 2)
        with self.assertRaisesRegex(ValueError, "selected candidate run"):
            load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                      "c" * 40, source_root, "0.4.0", 123, 1)
        pure_entry = next(entry for entry in entries if entry["distribution"] != "abstraction-ipc")
        pure_entry["sha256"] = "d" * 64
        receipt_path.write_text(json.dumps({"schema": 1, "candidate_source": "c" * 40,
                                            "candidate_version": "0.4.0",
                                            "run_id": 123, "run_attempt": 2,
                                            "platform": "win32-x64", "wheels": entries}), encoding="utf-8")
        with mock.patch("py_wheels.verify_native_receipt_wheel",
                        return_value=(wheel_root / native["filename"], b"verified DLL", native)):
            with self.assertRaisesRegex(ValueError, "wheel bytes do not match receipt"):
                load_public_wheel_receipt(receipt_path, wheel_root, loaded_pins,
                                          "c" * 40, source_root, "0.4.0", 123, 2)




if __name__ == "__main__":
    unittest.main()
