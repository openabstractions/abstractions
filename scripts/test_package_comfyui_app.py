"""Small pinned-fixture checks for the release-side ComfyUI repack."""

import base64
import csv
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
import types
import unittest
from unittest import mock
import zipfile

from scripts import package_comfyui_app as app
from scripts.git_env import source_git_environment


class LocalNativeSnapshotTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.backend, self.control, self.work = [self.root/name for name in ('backend','control','fresh')]
        self.context = dict(source_commit='a'*40, native_source_commit='b'*40,
            native_library_sha256='c'*64, appmodel_library_sha256='d'*64, appmodel_wheel_sha256='e'*64)
        resources = {}
        for directory in (self.backend,self.control):
            directory.mkdir()
            (directory/'vendor').mkdir()
            (directory/'vendor/data.bin').write_bytes(b'inert vendor DATA')
            raw = json.dumps(dict(runtime_files={'vendor/data.bin':dict(zip(('bytes','sha256'),app._sha256(directory/'vendor/data.bin')))})).encode()
            (directory/'manifest.json').write_bytes(raw)
            resources['backend' if directory == self.backend else 'control'] = dict(bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())
        self.proof = dict(source_mode='private-local-preview',private_provenance=self.context,resources=resources)
        self.log, self.receipt = self.root/'sealed.log', self.root/'sealed.jsonl'
        self.record = dict(operation='private_bounded_source_check',state='COMPLETE',accepted=True,exit_code=0,
            inputs_unchanged=True,join=dict(quiescent=True),owned_handles=dict(handles_closed=True),log=str(self.log),
            argv=['python','-B','scripts/package_comfyui_app.py','refresh-local-native',
                  *[value for key,pin in self.context.items() for value in ('--'+key.replace('_','-'),pin)]])
        self.write_receipt()
        self.source_patch = mock.patch.object(app,'_require_private_source_revision')
        self.source = self.source_patch.start()
        self.addCleanup(self.source_patch.stop)
        self.enterContext(mock.patch.object(subprocess,'Popen',side_effect=AssertionError('physical process forbidden')))
        self.enterContext(mock.patch.object(app.shutil,'disk_usage',return_value=types.SimpleNamespace(free=100 << 30)))

    def write_receipt(self):
        self.log.write_text(json.dumps(self.proof)+'\n',encoding='utf-8')
        self.record.update(log_bytes=self.log.stat().st_size,log_sha256=app._sha256(self.log)[1])
        self.receipt.write_text(json.dumps(self.record)+'\n',encoding='utf-8')
        self.options = dict(receipt_bytes=self.receipt.stat().st_size,receipt_sha256=app._sha256(self.receipt)[1],controller_source='f'*40,reserve_bytes=20 << 30)

    def receive(self):
        return app.snapshot_local_native_runtime(self.backend,self.control,self.work,self.receipt,**self.options)

    def test_complete_data_copy_uses_real_snapshot_and_cli(self):
        argv=['package_comfyui_app.py','snapshot-local-native','--backend',str(self.backend),'--control',str(self.control),
              '--work',str(self.work),'--sealed-receipt',str(self.receipt),
              *[value for key,pin in self.options.items() for value in ('--'+key.replace('_','-'),str(pin))]]
        with mock.patch.object(sys,'argv',argv), mock.patch.object(sys,'stdout',io.StringIO()) as output:
            self.assertEqual(app.main(),0)
        result=json.loads(output.getvalue())
        self.assertEqual(result['files'],4)
        self.assertEqual(result['retained_provenance'],self.context)
        self.source.assert_called_once_with(Path(app.__file__).resolve().parents[1],'f'*40)
        for original,target in ((self.backend,self.work/'extract/backend'),(self.control,self.work/'extract/control/oa-control')):
            for name in ('manifest.json','vendor/data.bin'):
                self.assertEqual((target/name).read_bytes(),(original/name).read_bytes())
                self.assertNotEqual((target/name).stat().st_ino,(original/name).stat().st_ino)
                self.assertEqual((target/name).stat().st_nlink,1)
            # The next existing refresh owner admits these exact root names.
            app._require_owned_stage(target,self.work)
            self.assertEqual(app._normalize_runtime_directories(target.parent,target.name,16 << 30,selected_root=True),[])

    def test_help_never_admits_or_copies(self):
        with mock.patch.object(sys,'argv',['package_comfyui_app.py','snapshot-local-native','--help']), \
                mock.patch.object(sys,'stdout',io.StringIO()) as output, self.assertRaises(SystemExit) as stopped:
            app.main()
        self.assertEqual(stopped.exception.code,0)
        self.assertIn('--sealed-receipt',output.getvalue())
        self.source.assert_not_called()
        self.assertFalse(self.work.exists())

    def test_data_roster_exceeds_code_pin_count_without_omitting_members(self):
        rows={}
        for index in range(1025):
            path=self.backend/f'vendor/{index}.bin'
            path.write_bytes(str(index).encode())
            rows[f'vendor/{index}.bin']=dict(zip(('bytes','sha256'),app._sha256(path)))
        rows['vendor/data.bin']=dict(zip(('bytes','sha256'),app._sha256(self.backend/'vendor/data.bin')))
        raw=json.dumps(dict(runtime_files=rows)).encode()
        (self.backend/'manifest.json').write_bytes(raw)
        self.proof['resources']['backend']=dict(bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())
        self.write_receipt()
        result=self.receive()
        self.assertEqual(result['files'],1029)
        for name in rows:
            self.assertEqual((self.work/'extract/backend'/name).read_bytes(),(self.backend/name).read_bytes())

    def test_pin_closed_proof_and_provenance_refuse_before_copy(self):
        changes=[('accepted',False),('inputs_unchanged',False),('join',dict(quiescent=False)),
                 ('owned_handles',dict(handles_closed=False)),('state','FAILED'),('exit_code',1)]
        for key,value in changes:
            previous=self.record[key];self.record[key]=value;self.write_receipt()
            with self.subTest(key=key), mock.patch.object(app,'_snapshot_verified_source',side_effect=AssertionError('copy forbidden')):
                with self.assertRaisesRegex(ValueError,'successful closed'):self.receive()
            self.record[key]=previous
        self.write_receipt()
        self.options['receipt_sha256']='0'*64
        with self.assertRaisesRegex(ValueError,'selected pin'):self.receive()
        self.write_receipt()
        self.log.write_bytes(b'changed log')
        with self.assertRaisesRegex(ValueError,'selected pin'):self.receive()
        self.record['argv'][-1]='0'*64;self.write_receipt()
        with self.assertRaisesRegex(ValueError,'provenance'):self.receive()
        self.assertFalse(self.work.exists())

    def test_roster_missing_extra_size_links_and_existing_work_refuse_before_copy(self):
        file=self.backend/'vendor/data.bin'
        raw=file.read_bytes()
        for change in ('missing','extra','size','hardlink'):
            with self.subTest(change=change), mock.patch.object(app,'_snapshot_verified_source',side_effect=AssertionError('copy forbidden')):
                if change=='missing':file.unlink()
                elif change=='extra':(self.backend/'extra').write_bytes(b'extra')
                elif change=='size':file.write_bytes(b'short')
                else:os.link(file,self.root/'alias')
                with self.assertRaisesRegex(ValueError,'roster|size|hard-linked'):self.receive()
                if change=='missing':file.write_bytes(raw)
                elif change=='extra':(self.backend/'extra').unlink()
                elif change=='size':file.write_bytes(raw)
                else:(self.root/'alias').unlink()
        self.work.mkdir()
        with self.assertRaisesRegex(ValueError,'fresh owned'):self.receive()

    def test_space_floor_and_source_identity_refuse_before_effects(self):
        with mock.patch.object(app.shutil,'disk_usage',return_value=types.SimpleNamespace(free=40 << 30)):
            with self.assertRaisesRegex(ValueError,'space'):self.receive()
        self.assertFalse(self.work.exists())
        self.source.side_effect=ValueError('private developer source differs')
        with self.assertRaisesRegex(ValueError,'source differs'):self.receive()
        self.assertFalse(self.work.exists())

    def test_changed_bytes_and_mutation_do_not_publish_success(self):
        file=self.backend/'vendor/data.bin'
        raw=file.read_bytes();file.write_bytes(b'x'*len(raw))
        with self.assertRaisesRegex(ValueError,'SHA-256 mismatch'):self.receive()
        self.assertFalse((self.work/'extract/control/oa-control').exists())

    def test_source_mutated_after_copy_refuses(self):
        snapshot=app._snapshot_verified_source
        def copy_then_change(source,target,*pins):
            snapshot(source,target,*pins)
            if source.name=='data.bin':source.write_bytes(b'changed after snapshot')
        with mock.patch.object(app,'_snapshot_verified_source',side_effect=copy_then_change):
            with self.assertRaisesRegex(ValueError,'source changed'):self.receive()

    def test_existing_clean_source_owner_refuses_changed_source_without_dispatch(self):
        self.source_patch.stop()
        # Exercise the existing predicate; only its Git reads are inert fixtures.
        with mock.patch.object(app,'_git_head',return_value='f'*40), \
                mock.patch.object(app,'_integration_packager_owner') as owner:
            owner.return_value.untracked_source_files.return_value=[]
            owner.return_value.matches_revision.return_value=False
            with self.assertRaisesRegex(ValueError,'selected clean revision'):
                app.snapshot_local_native_runtime(self.backend,self.control,self.work,self.receipt,**self.options)
        self.assertFalse(self.work.exists())

    def test_symlink_member_refuses_before_stage(self):
        # Exercise both OS link reports without requiring Windows link privileges.
        lstat=Path.lstat
        file=app._extended(self.backend/'vendor/data.bin')
        for mode,attributes in ((stat.S_IFLNK,0),(stat.S_IFREG,getattr(stat,'FILE_ATTRIBUTE_REPARSE_POINT',1024))):
            def observed(path,*args,**kwargs):
                if path==file:return types.SimpleNamespace(st_mode=mode,st_file_attributes=attributes)
                return lstat(path,*args,**kwargs)
            with self.subTest(mode=mode), mock.patch.object(Path,'lstat',observed):
                with self.assertRaisesRegex(ValueError,'link/reparse'):self.receive()
        self.assertFalse(self.work.exists())


class LocalOASiteReplacementTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.old, self.new = self.root/'old-site', self.root/'new-site'
        self.record_name = 'abstraction_test-0.8.0.dist-info'
        for site, body in ((self.old, b'old'), (self.new, b'current')):
            (site/'abstraction').mkdir(parents=True)
            (site/'abstraction/test.py').write_bytes(body)
            info = site/self.record_name
            info.mkdir()
            (info/'METADATA').write_text('Name: abstraction-test\nVersion: 0.8.0\n', encoding='utf-8')
            names = ['abstraction/test.py', self.record_name+'/METADATA', self.record_name+'/RECORD']
            (info/'RECORD').write_text(''.join(name+',,\n' for name in names), encoding='utf-8')
        self.pins = {path.relative_to(self.old).as_posix():dict(zip(('bytes','sha256'),app._sha256(path)))
                     for path in self.old.rglob('*') if path.is_file()}
        self.patch = mock.patch.object(app, '_integration_distributions', return_value={'abstraction_test'})
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def replace(self):
        return app.replace_local_oa_site(self.old, self.new, self.pins)

    def test_current_oa_replaces_only_rostered_files_and_preserves_third_party(self):
        (self.old/'torch').mkdir()
        vendor = self.old/'torch/unchanged.dll'
        vendor.write_bytes(b'exact retained third party')
        before = app._sha256(vendor)
        removed = self.replace()
        self.assertIn('abstraction/test.py', removed)
        self.assertEqual((self.old/'abstraction/test.py').read_bytes(), b'current')
        self.assertEqual(app._sha256(vendor), before)

    def test_traversal_foreign_or_duplicate_claim_refuses_before_deleting(self):
        record = self.old/self.record_name/'RECORD'
        original = record.read_bytes()
        for name in ('../outside.py', 'torch/foreign.py', 'abstraction/test.py'):
            with self.subTest(name=name):
                record.write_bytes(original+(name+',,\n').encode())
                with self.assertRaises(ValueError): self.replace()
                self.assertEqual((self.old/'abstraction/test.py').read_bytes(), b'old')

    def test_unrecorded_stale_oa_or_changed_retained_pin_refuses(self):
        extra = self.old/'abstraction/stale.py'
        extra.write_bytes(b'stale')
        with self.assertRaises(ValueError): self.replace()
        extra.unlink()
        (self.old/'abstraction/test.py').write_bytes(b'altered')
        with self.assertRaises(ValueError): self.replace()
        self.assertEqual((self.old/'abstraction/test.py').read_bytes(), b'altered')

    def test_replacement_foreign_file_refuses(self):
        (self.new/'foreign.py').write_bytes(b'foreign')
        with self.assertRaises(ValueError): self.replace()
        self.assertEqual((self.old/'abstraction/test.py').read_bytes(), b'old')

    def test_recovery_requires_exact_already_selected_oa_bytes_before_replacement(self):
        with self.assertRaisesRegex(ValueError,'overlay'):
            app.replace_local_oa_site(self.old,self.new,self.pins,require_current=True)
        self.assertEqual((self.old/'abstraction/test.py').read_bytes(),b'old')

    def test_known_historical_optional_component_can_be_added_but_new_target_stays_complete(self):
        with mock.patch.object(app,'_integration_distributions',return_value={'abstraction_test','abstraction_facade_native'}):
            with self.assertRaisesRegex(ValueError,'incomplete'):self.replace()
            info=self.new/'abstraction_facade_native-0.8.0.dist-info';info.mkdir()
            package=self.new/'abstraction_facade_native';package.mkdir()
            component=package/'__init__.py'
            component.write_bytes(b'current component')
            (info/'METADATA').write_text('Name: abstraction-facade-native\nVersion: 0.8.0\n',encoding='utf-8')
            names=['abstraction_facade_native/__init__.py',info.name+'/METADATA',info.name+'/RECORD']
            (info/'RECORD').write_text(''.join(name+',,\n' for name in names),encoding='utf-8')
            self.replace()
            self.assertEqual((self.old/component.relative_to(self.new)).read_bytes(),b'current component')

    def test_missing_historical_base_distribution_is_not_an_optional_upgrade(self):
        with mock.patch.object(app,'_integration_distributions',return_value={'abstraction_test','abstraction_facade_native','abstraction_config_protocol'}):
            with self.assertRaisesRegex(ValueError,'incomplete'):self.replace()
        self.assertEqual((self.old/'abstraction/test.py').read_bytes(),b'old')


@unittest.skipUnless(os.name=='nt','selected Windows build interpreter owner')
class LocalRuntimeRefreshTests(LocalOASiteReplacementTests):
    def _refresh_fixture(self, mode='valid'):
        work = self.root/'work'
        backend, control = work/'extract/backend', work/'extract/control/oa-control'
        for destination in (backend/'standalone-env/Lib/site-packages', control/'Lib/site-packages'):
            shutil.copytree(self.old, destination)
        node = backend/'ComfyUI/custom_nodes/abstraction_downloads'
        node.mkdir(parents=True)
        (node/'old.py').write_bytes(b'old OA node')
        (backend/'vendor.dll').write_bytes(b'unchanged third party')
        (backend/'ComfyUI'/app.LAUNCHER_NAME).write_bytes(b'old launcher')
        (backend/'ComfyUI/oa_interpreter.py').write_bytes(b'old interpreter')
        config = backend/app.HELPER_CONFIG_NAME
        config.parent.mkdir(parents=True)
        config.write_bytes(b'old helper config')
        (backend/'OA-LICENSES').mkdir()
        for name in ('OpenAbstractions-LICENSE', 'OpenAbstractions-NOTICE'):
            (backend/'OA-LICENSES'/name).write_bytes(b'old OA notice')
        (control/'python.exe').write_bytes(b'inert Python fixture')
        (control/'oa_native_host.py').write_bytes(b'old adapter')
        if mode=='installers-present':
            installer=backend/'standalone-env/Lib/site-packages/pip/__init__.py'
            installer.parent.mkdir();installer.write_bytes(b'bootstrap installer fixture')
        for directory in (backend, control):
            rows = {path.relative_to(directory).as_posix():dict(zip(('bytes','sha256'),app._sha256(path)))
                    for path in directory.rglob('*') if path.is_file()}
            value = dict(runtime_files=rows)
            if directory == backend:
                value['oa_launcher'] = dict(name='ComfyUI/'+app.LAUNCHER_NAME,
                    interpreter=dict(name='ComfyUI/oa_interpreter.py'))
            (directory/'manifest.json').write_text(json.dumps(value), encoding='utf-8')
        if mode == 'old-tampered': (backend/'vendor.dll').write_bytes(b'tampered')
        original_manifests = {role:(directory/'manifest.json').read_bytes()
                              for role,directory in (('backend',backend),('control',control))}
        integration = self.root/'integration.zip'
        integration.write_bytes(b'authenticated integration fixture')
        current = {app.LAUNCHER_NAME:b'current launcher', 'oa_interpreter.py':b'current interpreter'}
        def apply(*args, **kwargs):
            self.assertFalse(kwargs['install_wheels'])
            self.assertTrue(kwargs['require_wrapper'])
            node.mkdir()
            (node/'current.py').write_bytes(b'current OA node')
            return dict(license=b'current licence', notice=b'current notice')
        def pip(args, **kwargs):
            self.assertEqual(args[1:5], ['-B','-m','pip','install'])
            self.assertIn('--no-index', args)
            self.assertIn('--no-deps', args)
            shutil.copytree(self.new, work/'current-oa-site')
            return subprocess.CompletedProcess(args, 0)
        def launcher(folder):
            for name,raw in current.items(): (folder/name).write_bytes(raw)
            return current
        def helper(directory):
            path = directory/app.HELPER_CONFIG_NAME
            path.parent.mkdir()
            path.write_bytes(b'current helper config')
            return {'fixture':True}
        def seal(portable, selected_work, **kwargs):
            app._require_owned_stage(portable, selected_work)
            self.assertFalse(kwargs['backend_bootstrap'])
            if mode == 'vendor-changed' and portable == app._extended(backend):
                (backend/'vendor.dll').write_bytes(b'foreign change')
            if mode == 'python-changed' and portable != app._extended(backend):
                (portable/'python_embeded/python.exe').write_bytes(b'changed interpreter')
            return {'fixture':True}
        def probe(python,selected,selected_work):
            from scripts import file_boundary
            self.assertTrue(file_boundary._selected_python_spawn('subprocess.Popen',None,[str(python)]))
            self.assertEqual(app._sha256(python),tuple(original_python_pin[key] for key in ('bytes','sha256')))
            return {'library_sha256':('e' if mode=='wrong-abi' else 'f')*64}
        original_python_pin=dict(zip(('bytes','sha256'),app._sha256(control/'python.exe')))
        context = dict(source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64,
            appmodel_library_sha256='f'*64,appmodel_wheel_sha256='d'*64)
        manifest = dict(wheels=[dict(filename='abstraction_facade_native-fixture.whl',sha256='d'*64,native_library_sha256='f'*64)])
        with mock.patch.object(app,'_freeze_backend_integration',return_value=(integration,{'private_context':context},manifest,0)), \
                mock.patch.object(app,'_apply_integration',side_effect=apply) as installer, \
                mock.patch.object(subprocess,'run',side_effect=pip), \
                mock.patch.object(app,'_fill_wheel_records',return_value=[]), \
                mock.patch.object(app,'_launcher_inputs',return_value=current), \
                mock.patch.object(app,'_stage_launcher',side_effect=launcher), \
                mock.patch.object(app,'_stage_backend_helper',side_effect=helper), \
                mock.patch.object(app,'_seal_private_python',side_effect=seal), \
                mock.patch.object(app,'_native_control_appmodel_check',side_effect=probe) as abi:
            if mode == 'interrupted-recovery':
                with mock.patch.object(app,'_normalize_runtime_directories',side_effect=InterruptedError('owner stop')):
                    with self.assertRaises(InterruptedError):
                        app.refresh_local_native_runtime(backend,control,integration,work,integration_bytes=1,
                            integration_sha256='1'*64,private_context=context)
                self.assertTrue(control.is_dir())
                self.assertFalse((control/'manifest.json').exists())
                with mock.patch.object(app,'_require_local_refresh_recovery'):
                    with mock.patch.object(app,'_verify_local_refresh_wrapper'):
                        result = app.refresh_local_native_runtime(backend,control,integration,work,integration_bytes=1,
                            integration_sha256='1'*64,private_context=context,
                            recovery=dict(original_manifests,controller_source='e'*40))
                self.assertEqual(result['recovery_controller_source'],'e'*40)
                self.assertEqual(abi.call_count,2)
            elif mode != 'valid':
                with self.assertRaises(ValueError):
                    app.refresh_local_native_runtime(backend,control,integration,work,integration_bytes=1,
                        integration_sha256='1'*64,private_context=context)
                if mode in ('old-tampered','installers-present'): installer.assert_not_called()
                if mode == 'python-changed': abi.assert_not_called()
                return
            else:
                result = app.refresh_local_native_runtime(backend,control,integration,work,integration_bytes=1,
                    integration_sha256='1'*64,private_context=context)
        self.assertEqual(result['source_mode'],'private-local-preview')
        self.assertEqual((backend/'vendor.dll').read_bytes(),b'unchanged third party')
        self.assertFalse((node/'old.py').exists())
        for directory,prefix in ((backend,'standalone-env/'),(control,'')):
            self.assertEqual((directory/prefix/'Lib/site-packages/abstraction/test.py').read_bytes(),b'current')
            rows = json.loads((directory/'manifest.json').read_bytes())['runtime_files']
            for name,pin in rows.items(): self.assertEqual(app._sha256(directory/name),(pin['bytes'],pin['sha256']))

    def test_refresh_current_oa_preserves_vendor_and_remeasures_complete_rosters(self):
        self._refresh_fixture()

    def test_stop_before_final_roster_replays_oa_and_reuses_vendor_bytes(self):
        self._refresh_fixture('interrupted-recovery')

    def test_tampered_old_vendor_refuses_before_replacement(self):
        self._refresh_fixture('old-tampered')

    def test_changed_vendor_after_overlay_and_wrong_abi_refuse(self):
        self._refresh_fixture('vendor-changed')

    def test_wrong_loaded_component_refuses(self):
        self._refresh_fixture('wrong-abi')

    def test_changed_interpreter_refuses_before_abi_execution(self):
        self._refresh_fixture('python-changed')

    def test_bootstrap_installer_refuses_before_oa_replacement(self):
        self._refresh_fixture('installers-present')

    def test_existing_sealed_owner_does_not_require_removed_bootstrap_pip(self):
        work=self.root/'sealed-work'
        for role in ('backend','control-portable'):
            with self.subTest(role=role):
                portable=work/'extract'/role
                site=portable/'python_embeded/Lib/site-packages'
                package=site/'retained_vendor';package.mkdir(parents=True)
                (package/'__init__.py').write_bytes(b'# retained sealed dependency\n')
                info=site/'retained_vendor-1.0.dist-info';info.mkdir()
                (info/'METADATA').write_bytes(b'Name: retained-vendor\nVersion: 1.0\n')
                record=info/'RECORD'
                with record.open('w',encoding='utf-8',newline='') as stream:
                    writer=csv.writer(stream)
                    for path in (package/'__init__.py',info/'METADATA'):
                        raw=path.read_bytes()
                        digest=base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip('=')
                        writer.writerow((path.relative_to(site).as_posix(),'sha256='+digest,len(raw)))
                    writer.writerow((record.relative_to(site).as_posix(),'',''))
                before={path.relative_to(site).as_posix():path.read_bytes() for path in site.rglob('*') if path.is_file()}
                with self.assertRaisesRegex(ValueError,'bootstrap pip'):
                    app._seal_private_python(portable,work,embedded=False,backend_bootstrap=True)
                # Exercise the complete existing seal and launch-check owners,
                # with the same pip-free state as both retained interpreters.
                result=app._seal_private_python(portable,work,embedded=False,backend_bootstrap=False)
                self.assertEqual(result['removed_installers'],[])
                self.assertEqual(result['launch_check'],{'files':3,'hashed':2})
                self.assertEqual(before,{path.relative_to(site).as_posix():path.read_bytes() for path in site.rglob('*') if path.is_file()})


class LocalNativePreviewTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.host = self.root/'host.exe'
        self.host.write_bytes(b'inert native fixture')
        self.backend = self.root/'oa-backend'
        self.control = self.root/'oa-control'
        self.resources = {}
        for directory in (self.backend, self.control):
            directory.mkdir()
            (directory/'ordinary.py').write_bytes(b'fixture data')
            row = dict(zip(('bytes', 'sha256'), app._sha256(directory/'ordinary.py')))
            (directory/'manifest.json').write_text(json.dumps(dict(runtime_files={'ordinary.py':row})), encoding='utf-8')
            self.resources[directory.name.removeprefix('oa-')] = dict(zip(('bytes', 'sha256'), app._sha256(directory/'manifest.json')))
        def patched(owner,name,**kwargs):
            selected=mock.patch.object(owner,name,**kwargs)
            result=selected.start()
            self.addCleanup(selected.stop)
            return result
        patched(subprocess,'Popen',side_effect=AssertionError('physical process forbidden'))
        patched(subprocess,'run',side_effect=AssertionError('physical process forbidden'))
        patched(app,'_require_private_source_revision',return_value=None)
        patched(shutil,'disk_usage',return_value=types.SimpleNamespace(free=64<<30))
        sdk_path=self.root/'sdk.json'
        sdk_receipt=dict(receipt_path=str(sdk_path),source_identity={'fixture':'selected SDK identity'})
        sdk_path.write_text(json.dumps(sdk_receipt),encoding='utf-8')
        compilers={name:self.root/(name+'.exe') for name in ('rc','cvtres')}
        for path in compilers.values():path.write_bytes(b'inert compiler fixture')
        original_spec=importlib.util.spec_from_file_location
        source_owner=types.SimpleNamespace(_require_private_source_revision=lambda root,source:self.assertEqual(source,'a'*40))
        sdk_owner=types.SimpleNamespace(validate_sdk_receipt=lambda value:self.assertEqual(value,sdk_receipt))
        def spec(name,path):
            selected={'comfy_private_source_revision_owner':source_owner,'comfy_local_windows_go_sdk':sdk_owner}.get(name)
            if selected is None:return original_spec(name,path)
            loader=types.SimpleNamespace(create_module=lambda value:None,
                exec_module=lambda module:module.__dict__.update(vars(selected)))
            return importlib.util.spec_from_loader(name,loader)
        patched(importlib.util,'spec_from_file_location',side_effect=spec)
        self.identity=app._local_native_source_identity(Path(app.__file__).resolve().parents[1],'a'*40,
            dict(sdk_receipt_path=str(sdk_path),resource_tool_paths={name:str(path) for name,path in compilers.items()}))
        self.receipt = dict(native=dict(zip(('bytes', 'sha256'), app._sha256(self.host))), resources=self.resources,
            source_mode='private-local-preview',private_source_identity=json.loads(json.dumps(self.identity)),
            **{name:self.identity[name] for name in ('source_files','graph_sources','resource_tools','resource_postprocess','compiler')})
        self.context = dict(source_commit='a'*40, native_source_commit='b'*40, native_library_sha256='c'*64,
            appmodel_library_sha256='f'*64,appmodel_wheel_sha256='d'*64)
        self.output = self.root/'preview.zip'

    def assemble(self):
        return app.assemble_local_native_preview(self.host, self.backend, self.control, self.output,
            host_receipt=self.receipt, private_context=self.context)

    def test_complete_local_archive_has_exact_members_and_no_public_proof(self):
        result = self.assemble()
        self.assertEqual(result['source_mode'], 'private-local-preview')
        self.assertEqual(result['signing'], 'unsigned')
        self.assertEqual(result['archive']['sha256'], app._sha256(self.output)[1])
        with zipfile.ZipFile(self.output) as archive:
            self.assertEqual(set(archive.namelist()), {'local-preview.json', 'Setup OA ComfyUI.cmd',
                app.app_pair_receipt.NATIVE_HOST, 'oa-backend/manifest.json', 'oa-backend/ordinary.py',
                'oa-control/manifest.json', 'oa-control/ordinary.py'})
            self.assertNotIn(app.PROVENANCE, archive.namelist())
            self.assertTrue(all(row.compress_type == zipfile.ZIP_STORED for row in archive.infolist()))
            setup = archive.read('Setup OA ComfyUI.cmd').decode()
            launcher = result['host']['name']
            self.assertIn(launcher, archive.namelist())
            # The extracted launcher only copies the preview into OA's private
            # package folder; the installed copy seals, registers and starts.
            self.assertEqual(
                [line for line in setup.splitlines() if '"%~dp0' in line],
                [f'for /f "usebackq delims=" %%i in (`"%~dp0{launcher}" {operation}`) do set "OA_COMFY=%%i"'
                 for operation in ('install-local-preview', 'installed-local-preview')],
            )
            self.assertEqual(
                [line for line in setup.splitlines() if line.startswith('"%OA_COMFY%"')],
                [f'"%OA_COMFY%" {operation}' for operation in (
                    'seal-native-install', 'check-native-install', 'register-self', 'owned-host')],
            )
            self.assertIn('Review and Keep', setup)
        self.assertEqual(self.receipt['native'], {key:result['host'][key] for key in ('bytes','sha256')})
        source = self.root/'source.zip'
        with zipfile.ZipFile(source,'w') as archive: archive.writestr('LICENSE',b'inert corresponding-source fixture')
        source_pin = dict(name=source.name, **dict(zip(('bytes','sha256'),app._sha256(source))))
        source_set = dict(source_pin,format='zip',parts=[source_pin])
        with self.assertRaises(KeyError):
            app.app_pair_receipt.inspect_desktop_set(self.output,self.root,source_set,desktop_context={})

    def test_extra_missing_changed_runtime_each_refuses(self):
        for mutation in ('extra', 'missing', 'changed'):
            with self.subTest(mutation=mutation):
                target = self.backend/'ordinary.py'
                target.write_bytes(b'fixture data')
                extra = self.backend/'extra.py'
                extra.unlink(missing_ok=True)
                if mutation == 'extra': extra.write_bytes(b'foreign')
                elif mutation == 'missing': target.unlink()
                else: target.write_bytes(b'changed')
                with self.assertRaises(ValueError): self.assemble()
                self.assertFalse(self.output.exists())

    def test_wrong_host_resource_or_source_refuses(self):
        for field in ('host', 'resource', 'source'):
            with self.subTest(field=field):
                original = json.loads(json.dumps(self.receipt))
                if field == 'host': self.receipt['native']['sha256'] = '0'*64
                elif field == 'resource': self.receipt['resources']['backend']['sha256'] = '0'*64
                else: self.context['source_commit'] = 'd'*40
                with self.assertRaises(ValueError): self.assemble()
                self.assertFalse(self.output.exists())
                self.receipt = original
                self.context['source_commit'] = 'a'*40

    def test_hard_link_refuses(self):
        original = self.backend/'ordinary.py'
        alias = self.root/'alias'
        os.link(original, alias)
        with self.assertRaises(ValueError): self.assemble()
        self.assertFalse(self.output.exists())

    def test_missing_stale_or_foreign_native_provenance_refuses(self):
        original=json.loads(json.dumps(self.receipt))
        for mode in ('missing','stale','foreign-source','foreign-graph','foreign-compiler','public'):
            with self.subTest(mode=mode):
                self.receipt=json.loads(json.dumps(original))
                if mode=='missing':self.receipt.pop('private_source_identity')
                elif mode=='stale':self.receipt['private_source_identity']['source_commit']='e'*40
                elif mode=='public':self.receipt['source_mode']='public-split'
                else:
                    key={'foreign-source':'source_files','foreign-graph':'graph_sources','foreign-compiler':'resource_tools'}[mode]
                    self.receipt[key]={'foreign':{}}
                with self.assertRaises(ValueError):self.assemble()
                self.assertFalse(self.output.exists())

    @unittest.skipUnless(os.name=='nt','actual Windows long-path spelling')
    def test_local_archive_reads_long_runtime_members_through_existing_path_owner(self):
        relative='/'.join(('a'*90,'b'*90,'c'*60,'payload.dat'))
        target=app._extended(self.backend).joinpath(*relative.split('/'))
        target.parent.mkdir(parents=True)
        target.write_bytes(b'long-path fixture')
        manifest=self.backend/'manifest.json'
        value=json.loads(manifest.read_bytes())
        value['runtime_files'][relative]=dict(zip(('bytes','sha256'),app._sha256(target)))
        manifest.write_text(json.dumps(value),encoding='utf-8')
        self.receipt['resources']['backend']=dict(zip(('bytes','sha256'),app._sha256(manifest)))
        try:
            self.assemble()
            with zipfile.ZipFile(self.output) as archive:
                self.assertEqual(archive.read('oa-backend/'+relative),b'long-path fixture')
        finally:
            app.remove_tree(app._extended(self.backend)/('a'*90))


def fixture_free_space():
    """Let tiny fixtures reach the intended check without a production disk budget."""
    def disk_usage(_path):
        return types.SimpleNamespace(free=100 << 30)

    return mock.patch.object(app.shutil, 'disk_usage', new=disk_usage)


class LocalBackendReceivingTests(unittest.TestCase):
    def test_interrupted_layout_refuses_before_archive_or_tree_work(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve();output=root/'backend';output.mkdir()
            (output/'python_embeded').mkdir()
            raw=json.dumps(dict(runtime_files={'standalone-env/python.exe':dict(bytes=3,sha256='a'*64)})).encode()
            (output/'manifest.json').write_bytes(raw)
            receipt=root/'producer.json'
            receipt.write_text(json.dumps(dict(schema=3,source_mode='private-diagnostic',private_provenance=dict(
                source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64),
                input_descriptor={},archive={},source_pair={})),encoding='utf-8')
            with mock.patch.object(app,'backend_archive_metadata',side_effect=AssertionError('archive must not run')), \
                    mock.patch.object(app,'_scan_tree',side_effect=AssertionError('tree must not scan')), \
                    mock.patch.object(subprocess,'Popen',side_effect=AssertionError('physical process forbidden')):
                with self.assertRaisesRegex(ValueError,'interrupted refresh layout.*verification not performed'):
                    app.receive_local_backend(root,receipt,output,root/'7zr.exe',verify_existing=True)
            self.assertEqual((output/'manifest.json').read_bytes(),raw)
            self.assertTrue((output/'python_embeded').is_dir())
            self.assertFalse((output/'standalone-env').exists())

    @fixture_free_space()
    def test_existing_archive_owner_and_complete_roster_bind_fresh_extraction(self):
        for mode in ('valid','missing','extra','changed'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temporary:
                root=Path(temporary).resolve()
                part=root/'backend.7z';part.write_bytes(b'opaque inert archive DATA')
                pin=dict(name=part.name,**dict(zip(('bytes','sha256'),app._sha256(part))))
                logical=dict(pin,format='7z',parts=[pin])
                vendor=b'unchanged vendor DATA'
                manifest=json.dumps(dict(runtime_files={'vendor.dll':dict(bytes=len(vendor),sha256=hashlib.sha256(vendor).hexdigest())})).encode()
                value=dict(schema=3,source_mode='private-diagnostic',private_provenance=dict(
                    source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64),
                    input_descriptor={},archive=logical,source_pair={})
                receipt=root/'producer.json';receipt.write_text(json.dumps(value),encoding='utf-8')
                output=root/'backend'
                tool=root/'selected-7zr.exe'
                def extract(args,**kwargs):
                    self.assertEqual(args[0],str(tool))
                    self.assertIn('-o'+str(output),args)
                    self.assertEqual(args[-1],str(part))
                    (output/'manifest.json').write_bytes(manifest)
                    if mode!='missing':(output/'vendor.dll').write_bytes(b'changed' if mode=='changed' else vendor)
                    if mode=='extra':(output/'foreign').write_bytes(b'extra')
                    return subprocess.CompletedProcess(args,0)
                with mock.patch.object(app,'backend_archive_metadata',return_value=({'manifest.json','vendor.dll'},manifest)) as metadata, \
                        mock.patch.object(subprocess,'run',side_effect=extract), \
                        mock.patch.object(subprocess,'Popen',side_effect=AssertionError('physical process forbidden')):
                    if mode!='valid':
                        with self.assertRaisesRegex(ValueError,'complete roster') as refused:
                            app.receive_local_backend(root,receipt,output,tool)
                        details=json.loads(str(refused.exception).split('complete roster: ',1)[1])
                        if mode=='changed':
                            self.assertEqual(details,{'file_pins':dict(changed_count=1,changed=['vendor.dll'])})
                        else:
                            key='missing' if mode=='missing' else 'extra'
                            name='vendor.dll' if mode=='missing' else 'foreign'
                            for category in ('archive_members','file_roster'):
                                self.assertEqual(details[category][key+'_count'],1)
                                self.assertEqual(details[category][key],[name])
                    else:
                        result=app.receive_local_backend(root,receipt,output,tool)
                        self.assertEqual(result['source_mode'],'private-local-input')
                        self.assertEqual(result['retained_provenance'],value['private_provenance'])
                        self.assertEqual((output/'vendor.dll').read_bytes(),vendor)
                    metadata.assert_called_once_with(root,logical,tool)

    def test_foreign_context_duplicate_metadata_and_negative_pin_refuse(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve();receipt=root/'producer.json'
            receipt.write_text(json.dumps(dict(schema=3,source_mode='public-split')),encoding='utf-8')
            with mock.patch.object(app,'backend_archive_metadata',side_effect=AssertionError('archive owner not reached')), \
                    mock.patch.object(subprocess,'Popen',side_effect=AssertionError('physical process forbidden')):
                with self.assertRaises(ValueError):app.receive_local_backend(root,receipt,root/'backend',root/'7zr.exe')
            with self.assertRaises(ValueError):app._local_runtime_json(b'{"runtime_files":{},"runtime_files":{}}')
            with self.assertRaises(ValueError):app._local_runtime_rows({'runtime_files':{'x':dict(bytes=-1,sha256='a'*64)}})

    def test_existing_roster_diagnostics_bound_names_and_separate_manifest_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve();output=root/'backend';output.mkdir()
            vendor=b'private fixture content never reported'
            (output/'vendor.dll').write_bytes(vendor)
            raw=json.dumps(dict(runtime_files={'vendor.dll':dict(bytes=len(vendor),sha256=hashlib.sha256(vendor).hexdigest())})).encode()
            (output/'manifest.json').write_bytes(raw+b' ')
            for i in range(20):(output/f'extra-{i:02d}').write_bytes(vendor)
            receipt=root/'producer.json'
            receipt.write_text(json.dumps(dict(schema=3,source_mode='private-diagnostic',private_provenance=dict(
                source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64),
                input_descriptor={},archive={},source_pair={})),encoding='utf-8')
            with mock.patch.object(app,'backend_archive_metadata',return_value=({'manifest.json','vendor.dll','empty-directory'},raw)), \
                    mock.patch.object(subprocess,'Popen',side_effect=AssertionError('physical process forbidden')):
                with self.assertRaisesRegex(ValueError,'complete roster') as refused:
                    app.receive_local_backend(root,receipt,output,root/'7zr.exe',verify_existing=True)
            message=str(refused.exception)
            details=json.loads(message.split('complete roster: ',1)[1])
            self.assertEqual(details['archive_members']['missing'],['empty-directory'])
            for category in ('archive_members','file_roster'):
                self.assertEqual(details[category]['extra_count'],20)
                self.assertEqual(details[category]['extra'],[f'extra-{i:02d}' for i in range(8)])
            self.assertEqual(details['manifest_bytes'],'changed')
            self.assertNotIn('file_pins',details)
            self.assertNotIn(vendor.decode(),message)
            self.assertNotIn(str(root),message)

    @unittest.skipUnless(os.name=='nt','actual Windows long-path spelling')
    def test_existing_long_path_tree_is_fully_verified_without_extraction(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve();output=root/'backend';output.mkdir()
            relative='/'.join(('a'*90,'b'*90,'c'*60,'payload.dat'))
            target=app._extended(output).joinpath(*relative.split('/'))
            target.parent.mkdir(parents=True);target.write_bytes(b'long-path fixture')
            raw=json.dumps(dict(runtime_files={relative:dict(zip(('bytes','sha256'),app._sha256(target)))})).encode()
            (output/'manifest.json').write_bytes(raw)
            part=root/'backend.7z';part.write_bytes(b'opaque fixture')
            pin=dict(name=part.name,**dict(zip(('bytes','sha256'),app._sha256(part))))
            logical=dict(pin,format='7z',parts=[pin])
            receipt=root/'producer.json'
            receipt.write_text(json.dumps(dict(schema=3,source_mode='private-diagnostic',private_provenance=dict(
                source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64),
                input_descriptor={},archive=logical,source_pair={})),encoding='utf-8')
            pieces=relative.split('/')
            members={'manifest.json',relative,*('/'.join(pieces[:i]) for i in range(1,len(pieces)))}
            try:
                with mock.patch.object(app,'backend_archive_metadata',return_value=(members,raw)), \
                        mock.patch.object(subprocess,'run',side_effect=AssertionError('existing tree must not be extracted')):
                    result=app.receive_local_backend(root,receipt,output,root/'7zr.exe',verify_existing=True)
                    self.assertEqual(result['extraction'],'verified-existing')
                    target.write_bytes(b'changed')
                    with self.assertRaisesRegex(ValueError,'complete roster'):
                        app.receive_local_backend(root,receipt,output,root/'7zr.exe',verify_existing=True)
            finally:
                app.remove_tree(app._extended(output))


class LocalNativeCLIContextTests(unittest.TestCase):
    def test_both_local_clis_forward_real_private_appmodel_admission_context(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve()
            wheel_names=['abstraction_ipc-0.8.0-py3-none-win_amd64.whl',
                         'abstraction_facade_native-0.8.0-py3-none-win_amd64.whl']
            wheels=[dict(filename=wheel_names[0],source_commit='b'*40,native_library_sha256='c'*64),
                    dict(filename=wheel_names[1],source_commit='a'*40,native_library_sha256='f'*64)]
            integration=root/'integration.zip'
            with zipfile.ZipFile(integration,'w') as archive:
                archive.writestr('manifest.json',json.dumps(dict(source_mode='private-diagnostic',source_commit='a'*40,wheels=wheels)))
                for name in wheel_names:archive.writestr('wheels/'+name,b'inert wheel fixture; wheel verifier is separately owned')
            receipt=root/'host.json';receipt.write_text('{}',encoding='utf-8')
            expected=dict(source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64,
                          appmodel_library_sha256='f'*64,appmodel_wheel_sha256='d'*64)
            common=['--source-commit','a'*40,'--native-source-commit','b'*40,'--native-library-sha256','c'*64,
                    '--appmodel-library-sha256','f'*64,'--appmodel-wheel-sha256','d'*64,
                    '--backend',str(root/'backend'),'--control',str(root/'control')]
            captured=[]
            def admit(*args,**kwargs):
                context=app._local_private_context(kwargs['private_context'])
                self.assertEqual(context,expected)
                work=root/('admission-'+str(len(captured)));work.mkdir()
                _,_,manifest,_=app._freeze_backend_integration(work,integration,{},integration.stat().st_size,
                    app._sha256(integration)[1],source=None,run_id=None,run_attempt=None,private_context=context)
                self.assertEqual(manifest['wheels'],wheels)
                captured.append(context)
                return {'admitted':True}
            owner=mock.Mock(NATIVE_SOURCES=['native-source'])
            owner.matches_revision.return_value=True
            with mock.patch.object(app,'_integration_packager_owner',return_value=owner), \
                    mock.patch.object(app,'_require_private_source_revision'), \
                    mock.patch.object(app,'_check_integration_consistency'), \
                    mock.patch.object(app,'_verify_private_integration_wheel'), \
                    mock.patch.object(app,'refresh_local_native_runtime',side_effect=admit), \
                    mock.patch.object(app,'assemble_local_native_preview',side_effect=admit), \
                    mock.patch.object(subprocess,'Popen',side_effect=AssertionError('physical process forbidden')), \
                    mock.patch.object(sys,'stdout',io.StringIO()):
                for verb,arguments in (
                    ('refresh-local-native',['--work',str(root),'--integration',str(integration),
                        '--integration-bytes',str(integration.stat().st_size),'--integration-sha256',app._sha256(integration)[1]]),
                    ('local-native-preview',['--host',str(root/'host.exe'),'--host-receipt',str(receipt),'--output',str(root/'preview.zip')])):
                    with mock.patch.object(sys,'argv',['package_comfyui_app.py',verb,*common,*arguments]):
                        self.assertEqual(app.main(),0)
            self.assertEqual(captured,[expected,expected])
            with self.assertRaisesRegex(ValueError,'AppModel'):
                app._local_private_context({key:value for key,value in expected.items() if not key.startswith('appmodel_')})


class LocalRefreshRecoveryAdmissionTests(unittest.TestCase):
    def test_vendor_roster_loads_immutable_launcher_inputs_once_for_many_members(self):
        rows = {'vendor/'+str(index)+'.bin':dict(bytes=1,sha256='a'*64) for index in range(4096)}
        manifest = dict(runtime_files=rows,oa_launcher=dict(name='ComfyUI/oa_comfyui.py',
                        interpreter=dict(name='ComfyUI/oa_interpreter.py')))
        with mock.patch.object(app,'_launcher_inputs',return_value={'oa_comfyui.py':b'inert'}) as launcher, \
                mock.patch.object(app,'_integration_distributions',return_value={'abstraction_test'}) as distributions:
            app._verify_local_refresh_vendor('backend',manifest,dict(rows))
            launcher.assert_called_once_with()
            distributions.assert_called_once_with(appmodel=True)

    def test_extra_cached_wrapper_bytecode_refuses_before_source_loader(self):
        with tempfile.TemporaryDirectory() as temporary:
            backend = Path(temporary)
            node = backend/'ComfyUI/custom_nodes/abstraction_downloads'
            home = backend/'standalone-env';home.mkdir()
            (home/'python.exe').write_bytes(b'inert interpreter')
            names = {'__init__.py','oa_connect.py','oa_interpreter.py','oa_helper.py','oa_relay.py',
                     'oa_nodes/__init__.py','oa_client/abstraction/ipc/__init__.py','web/oa_connect.js',
                     'oa_atomic/abstraction_cas.py'}
            expected = {}
            for name in names:
                path = node/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(b'inert wrapper')
                expected[name] = dict(zip(('bytes','sha256'),app._sha256(path)))
            helper = node/'helper';helper.mkdir()
            (helper/'python.exe').write_bytes(b'inert interpreter')
            (helper/'pyvenv.cfg').write_bytes(app.HELPER_CONFIG_TEMPLATE)
            (node/'application-manifest.json').write_bytes(b'{}')
            sibling = node.parent/'upstream_example.py'
            sibling.write_bytes(b'preserved upstream node')
            full_expected = dict(expected)
            for name in ('helper/python.exe','helper/pyvenv.cfg','application-manifest.json'):
                full_expected[name] = dict(zip(('bytes','sha256'),app._sha256(node/name)))
            app._verify_integration_wrapper(node,full_expected)
            self.assertEqual(sibling.read_bytes(),b'preserved upstream node')
            cache = node/'__pycache__/oa_connect.cpython-312.pyc'
            cache.parent.mkdir();cache.write_bytes(b'injected bytecode')
            with mock.patch.object(importlib.util,'module_from_spec',side_effect=AssertionError('loader reached')):
                with self.assertRaisesRegex(ValueError,'wrapper files'):
                    app._verify_local_refresh_wrapper(backend,dict(wrapper_files=expected))

    def test_altered_wrapper_refuses_before_loading_its_code(self):
        with tempfile.TemporaryDirectory() as temporary:
            backend = Path(temporary)
            node = backend/'ComfyUI/custom_nodes/abstraction_downloads'
            node.mkdir(parents=True)
            (backend/'standalone-env').mkdir()
            (node/'oa_connect.py').write_bytes(b'changed wrapper')
            with mock.patch.object(importlib.util,'module_from_spec',side_effect=AssertionError('unverified code loaded')):
                with self.assertRaisesRegex(ValueError,'wrapper'):
                    app._verify_local_refresh_wrapper(backend,dict(wrapper_files={
                        'oa_connect.py':dict(bytes=1,sha256='a'*64)}))

    def test_frozen_archive_product_source_controller_and_closed_job_are_required(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary).resolve()
            backend, control, integration = work/'backend', work/'control', work/'integration.zip'
            integration.write_bytes(b'frozen integration')
            size,digest = app._sha256(integration)
            root = Path(app.__file__).resolve().parents[1]
            context = dict(source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64,
                           appmodel_library_sha256='d'*64,appmodel_wheel_sha256='e'*64)
            raw = json.dumps(dict(runtime_files={'python.exe':dict(bytes=1,sha256='f'*64)})).encode()
            original_control = work/'original/manifest.json'
            args = ['python','-B','scripts/package_comfyui_app.py','refresh-local-native',
                    '--backend',str(backend),'--control',str(control),'--work',str(work),'--integration',str(integration),
                    '--integration-bytes',str(size),'--integration-sha256',digest]
            for key,value in context.items(): args.extend(['--'+key.replace('_','-'),value])
            source = b'original controller source'
            pin = dict(bytes=len(source),sha256=hashlib.sha256(source).hexdigest())
            inputs = [dict(path=str(root/name),**pin) for name in ('scripts/package_comfyui_app.py','scripts/package_comfyui_integration.py')]
            inputs.extend([dict(path=str(integration),bytes=size,sha256=digest),
                           dict(path=str(original_control),bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())])
            record = dict(operation='private_bounded_source_check',accepted=False,state='FAILED',inputs_unchanged=True,
                          join=dict(quiescent=True),owned_handles=dict(handles_closed=True),argv=args,cwd=str(root),inputs=inputs)
            recovery = dict(controller_source='1'*40,receipt=record,backend=raw,control=raw,control_path=original_control,
                            backend_pin=dict(bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest()))
            def git(argv,**kwargs): return '' if argv[1]=='diff' else source
            def admit():
                app._require_local_refresh_recovery(recovery,context,backend,control,integration,work,size,digest)
            with mock.patch.object(app,'_require_private_source_revision') as controller, \
                    mock.patch.object(subprocess,'check_output',side_effect=git), \
                    mock.patch.object(subprocess,'Popen',side_effect=AssertionError('physical process forbidden')):
                admit()
                controller.assert_called_with(root,'1'*40)
                integration.write_bytes(b'changed integration')
                with self.assertRaisesRegex(ValueError,'archive'): admit()
                integration.write_bytes(b'frozen integration')
                with mock.patch.object(subprocess,'check_output',return_value='adopters/comfyui/oa_connect.py\n'):
                    with self.assertRaisesRegex(ValueError,'product source'): admit()
                with mock.patch.object(app,'_require_private_source_revision',side_effect=ValueError('untrusted controller')):
                    with self.assertRaisesRegex(ValueError,'untrusted controller'): admit()
                record['join']['quiescent'] = False
                with self.assertRaisesRegex(ValueError,'closed stopped'): admit()

    def test_recovery_refuses_changed_removed_and_added_vendor_members(self):
        manifest = dict(runtime_files={'vendor.dll':dict(bytes=1,sha256='a'*64)},
                        oa_launcher=dict(name='ComfyUI/oa_comfyui.py',interpreter=dict(name='ComfyUI/oa_interpreter.py')))
        for actual in ({}, {'vendor.dll':dict(bytes=2,sha256='b'*64)},
                       dict(manifest['runtime_files'], foreign=dict(bytes=0,sha256='c'*64))):
            with self.subTest(actual=actual), self.assertRaisesRegex(ValueError,'third-party'):
                app._verify_local_refresh_vendor('backend',manifest,actual)


class MacosPythonHostTests(unittest.TestCase):
    def test_dependency_metadata_keeps_spaces_and_excludes_install_id(self):
        image = Path('/selected Python/libpython.dylib')
        outputs = {'-L': str(image) + ':\n\t/selected Python/libpython.dylib (compatibility version 3.13.0, current version 3.13.0)\n\t@rpath/lib crypto.dylib (compatibility version 1.0.0, current version 1.0.0)\n',
                   '-D': str(image) + ':\n/selected Python/libpython.dylib\n',
                   '-l': 'Load command 0\n cmd LC_RPATH\n cmdsize 40\n path @loader_path/dependencies (offset 12)\n'}
        def run(args, **kwargs):
            return subprocess.CompletedProcess(args, 0, stdout=outputs[args[1]])
        self.assertEqual(app._macos_dependencies(image, run), (['@rpath/lib crypto.dylib'], ['@loader_path/dependencies']))

    @unittest.skipUnless(sys.platform == 'darwin', 'actual macOS compiler and existing CPython required')
    def test_actual_app_local_host_and_standard_library_dependency_closure(self):
        from installer.posix.build import sign_development_code
        with tempfile.TemporaryDirectory(prefix='oa-comfy-python-') as temporary:
            output = Path(temporary) / 'helper'
            manifest = app.stage_macos_python_host(Path(sys.executable), output, sign_code=sign_development_code)
            self.assertTrue(manifest['runtime_files'])
            self.assertFalse(any('site-packages' in name for name in manifest['runtime_files']))
            query = '''import ctypes, json, ssl, sqlite3, sys, zlib, lzma
native=ctypes.CDLL('/usr/lib/libSystem.B.dylib')
native._dyld_image_count.restype=ctypes.c_uint32
native._dyld_get_image_name.argtypes=[ctypes.c_uint32]
native._dyld_get_image_name.restype=ctypes.c_char_p
print(json.dumps(dict(executable=sys.executable,prefix=sys.prefix,images=[native._dyld_get_image_name(i).decode() for i in range(native._dyld_image_count())])))'''
            result = subprocess.run([str(output/'python-host'), '-I', '-B', '-c', query], check=True, capture_output=True, text=True, timeout=30)
            actual = json.loads(result.stdout)
            self.assertEqual(Path(actual['executable']).resolve(), output/'python-host')
            self.assertEqual(Path(actual['prefix']).resolve(), output/'runtime')
            outside = [name for name in actual['images'] if not name.startswith(('/usr/lib/', '/System/Library/', str(output)+'/'))]
            self.assertEqual(outside, [], 'the host still loaded a build-account dependency')
            for name, pin in manifest['runtime_files'].items():
                self.assertEqual(app._sha256(output/name), (pin['bytes'], pin['sha256']))
                self.assertFalse((output/name).is_symlink())
                self.assertEqual((output/name).stat().st_nlink, 1)
            self.assertNotEqual(os.stat(output/'python-host').st_ino, os.stat(sys.executable).st_ino)
            print(json.dumps(dict(python=manifest['python_version'], files=len(manifest['runtime_files']), relocated_images=len(manifest['relocated_inputs']), host_sha256=app._sha256(output/'python-host')[1], actual=actual), sort_keys=True))




class NativeControlAssemblyTests(unittest.TestCase):
    def test_control_probe_requires_facade_abi_two(self):
        proof=dict(library_sha256='f'*64,**{key:True for key in
            ('describe','read','apply','replay','refusal','restore','closed')})
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve()
            for abi in (1,2):
                result=subprocess.CompletedProcess([],0,json.dumps(dict(proof,abi=abi)),'')
                with mock.patch.object(app.subprocess,'run',return_value=result) as run:
                    if abi==1:
                        with self.assertRaisesRegex(ValueError,'incomplete'):
                            app._native_control_appmodel_check(root/'python.exe',root,root)
                    else:self.assertEqual(app._native_control_appmodel_check(root/'python.exe',root,root)['abi'],2)
                    self.assertIn('assert registry._lib.abi_version() == 2',run.call_args.args[0][5])

    def test_graph_bridge_is_whole_and_pins_no_frontend(self):
        manifest=dict(wrapper_files={name:{} for name in app._GRAPH_FILES})
        app._verify_native_graph_wrapper(manifest)
        app._verify_native_graph_wrapper(dict(wrapper_files={}))
        for name in app._GRAPH_FILES:
            altered=dict(wrapper_files={key:{} for key in app._GRAPH_FILES-{name}})
            with self.subTest(missing=name),self.assertRaisesRegex(ValueError,'incomplete'):app._verify_native_graph_wrapper(altered)
        with self.assertRaisesRegex(ValueError,'retired'):
            app._verify_native_graph_wrapper(dict(manifest,native_graph_frontend=dict(version='1.55.18')))

    @fixture_free_space()
    def _assemble_control_fixture(self, mode='valid'):
        """Assemble the isolated adapter with inert Python and SDK members."""
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve();inputs=root/'inputs';inputs.mkdir()
            source=inputs/'public-adopter';(source/'.installer').mkdir(parents=True)
            adapter=source/'.installer/oa_native_host.py'
            shutil.copyfile(Path(__file__).resolve().parents[1]/'adopters/comfyui/node/oa_native_host.py', adapter)
            raw={'python/python.exe':b'PBS executable DATA','python/python313.dll':b'PBS DLL DATA',
                 'python/Lib/os.py':b'PBS stdlib DATA','python/Lib/site-packages/bootstrap.pth':b'',
                 'sources/python-LICENSE':b'Python licence DATA'}
            files={}
            for name,data in raw.items():
                path=inputs/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(data);files[name]=path
            value=dict(python_files=[name for name in raw if name.startswith('python/')],
                files=[dict(name=name,bytes=len(data),sha256=hashlib.sha256(data).hexdigest()) for name,data in raw.items()],
                runtime_provenance={'python':{'license':'sources/python-LICENSE'}})
            descriptor=inputs/'descriptor.json';descriptor.write_bytes(b'fixture admitted descriptor')
            integration=inputs/'integration.zip';integration.write_bytes(b'fixture authenticated integration')
            calls=[]
            def public(manifest,checkouts):calls.append('public-source');return []
            def install(portable,archive,work,**options):
                app._require_owned_stage(portable,work)
                self.assertEqual(calls,['public-source']);calls.append('SDK-install')
                self.assertTrue(options['require_public']);self.assertTrue(options['require_wrapper'])
                self.assertEqual(options['python_executable'],portable/'python_embeded/python.exe')
                site=portable/'python_embeded/Lib/site-packages';(site/'abstraction').mkdir()
                (site/'abstraction/__init__.py').write_bytes(b'fixture SDK')
                (portable/'python_embeded/empty/nested').mkdir(parents=True)
            def seal(portable,work,**options):
                app._require_owned_stage(portable,work)
                calls.append('seal');self.assertEqual(options,dict(embedded=False,backend_bootstrap=True));return {}
            def probe(python, selected, work):
                calls.append('AppModel-probe')
                self.assertTrue((selected/'oa_native_host.py').is_file())
                self.assertFalse(output.exists())
                if mode == 'bad-abi': raise ValueError('unsupported packaged AppModel ABI')
                if mode == 'valid':
                    self.assertFalse((selected/'oa_model_files.py').exists())
                    child_root = app._plain(selected)
                    subprocess.run([
                        sys.executable, '-I', '-S', '-B', '-c',
                        "import runpy, sys; "
                        "owner = runpy.run_path(sys.argv[1]); "
                        "assert owner['_held_walk'] and owner['output_reference']",
                        str(child_root/'oa_native_host.py'),
                    ], cwd=child_root, check=True, capture_output=True)
                return {'library_sha256':('e' if mode == 'wrong-library' else 'f')*64}
            output=root/'oa-control'
            manifest=dict(wheels=[] if mode == 'missing' else [dict(
                filename='abstraction_facade_native-0.8.0-py3-none-win_amd64.whl',
                sha256='d'*64,native_library_sha256='f'*64)])
            with mock.patch.object(app,'_native_control_python_inputs',return_value=(value,files)), \
                    mock.patch.object(app,'_freeze_backend_integration',return_value=(integration,{},manifest,0)), \
                    mock.patch.object(app,'_public_source_checkouts',side_effect=public), \
                    mock.patch.object(app,'_apply_integration',side_effect=install), \
                    mock.patch.object(app,'_fill_wheel_records',return_value=[]) as fill, \
                    mock.patch.object(app,'_seal_private_python',side_effect=seal), \
                    mock.patch.object(app,'_native_control_appmodel_check',side_effect=probe,create=True):
                if mode != 'valid':
                    with self.assertRaisesRegex(ValueError, {'missing':'companion','bad-abi':'ABI','wrong-library':'another AppModel'}[mode]):
                        app.build_native_control(descriptor,'1'*64,integration,integration.stat().st_size,
                            app._sha256(integration)[1],output,candidate_source='a'*40,run_id=1,run_attempt=1,
                            integration_options={},adapter=adapter,public_sources={'openabstractions/adopter-comfyui':source})
                    self.assertFalse(output.exists())
                    if mode == 'missing': self.assertEqual(calls, [])
                    return
                pin=app.build_native_control(descriptor,'1'*64,integration,integration.stat().st_size,
                    app._sha256(integration)[1],output,candidate_source='a'*40,run_id=1,run_attempt=1,
                    integration_options={},adapter=adapter,public_sources={'openabstractions/adopter-comfyui':source})
            self.assertEqual(calls,['public-source','SDK-install','seal','AppModel-probe'])
            self.assertEqual(list(fill.call_args.args[1].values()),['d'*64])
            self.assertEqual(app._sha256(output/'manifest.json'),(pin['bytes'],pin['sha256']))
            rows=json.loads((output/'manifest.json').read_bytes())['runtime_files']
            for name in value['python_files']:self.assertIn(name.removeprefix('python/'),rows)
            self.assertIn('oa_native_host.py',rows);self.assertIn('Lib/site-packages/abstraction/__init__.py',rows)
            self.assertFalse(any('torch' in name.casefold() or name.startswith('ComfyUI/') for name in rows))
            self.assertFalse((output/'empty').exists())
            for name,pin in rows.items():self.assertEqual(app._sha256(output/name),(pin['bytes'],pin['sha256']))

    def test_complete_selected_python_sdk_and_adapter_form_separate_control_root(self):
        self._assemble_control_fixture()

    def test_missing_component_refuses_before_install_and_commit(self):
        self._assemble_control_fixture('missing')

    def test_component_abi_failure_and_wrong_loaded_bytes_never_commit_control(self):
        for mode in ('bad-abi','wrong-library'):
            with self.subTest(mode=mode): self._assemble_control_fixture(mode)

    def test_normalize_only_empty_owned_runtime_directories_without_content_loss(self):
        with tempfile.TemporaryDirectory() as temporary:
            stage=Path(temporary).resolve();root=stage/'backend';(root/'code').mkdir(parents=True)
            leaf=root/'code/data.bin';leaf.write_bytes(b'kept exact bytes');before=app._sha256(leaf)
            (root/'empty/nested').mkdir(parents=True)
            removed=app._normalize_runtime_directories(stage,'backend',1<<20)
            self.assertEqual(set(removed),{'backend/empty/nested','backend/empty'})
            self.assertEqual(app._sha256(leaf),before);self.assertTrue((root/'code').is_dir())
            self.assertEqual(app._normalize_runtime_directories(stage,'backend',1<<20),[])
            with self.assertRaises(ValueError):app._normalize_runtime_directories(stage,'source',1<<20)

    def test_normalize_refuses_reparse_and_new_unlisted_content(self):
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as temporary:
            stage=Path(temporary).resolve();root=stage/'backend';root.mkdir();(root/'keep').write_bytes(b'kept')
            empty=root/'empty';empty.mkdir();original=Path.lstat
            def linked(path,*args,**kwargs):
                value=original(path,*args,**kwargs)
                if path==app._extended(empty):return SimpleNamespace(st_mode=value.st_mode,st_file_attributes=0x400)
                return value
            with mock.patch.object(Path,'lstat',linked),mock.patch.object(Path,'rmdir') as removal:
                with self.assertRaisesRegex(ValueError,'reparse'):app._normalize_runtime_directories(stage,'backend',1<<20)
                removal.assert_not_called()
            actual=app.relative_beneath_existing
            def changed(path,boundary):
                (empty/'late-code.py').write_bytes(b'new content');return actual(path,boundary)
            with mock.patch.object(app,'relative_beneath_existing',side_effect=changed):
                with self.assertRaisesRegex(ValueError,'unlisted content'):app._normalize_runtime_directories(stage,'backend',1<<20)
            self.assertEqual((empty/'late-code.py').read_bytes(),b'new content')


class BackendHelperIdentityTests(unittest.TestCase):
    def test_helper_copy_uses_canonical_identity_and_propagates_lookup_failure(self):
        for identity in (False, True, PermissionError('identity unavailable')):
            with self.subTest(identity=identity), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve(); portable = root/'portable'
                node = portable/'ComfyUI/custom_nodes/abstraction_downloads'
                node.mkdir(parents=True)
                home=portable/'standalone-env';home.mkdir()
                source=home/'python.exe';source.write_bytes(b'inert interpreter fixture')
                (home/'python313.dll').write_bytes(b'inert Python DLL fixture')
                for name in ('oa_connect.py','oa_interpreter.py'):
                    shutil.copyfile(app.LAUNCHER_SOURCE.parent/'node'/name,node/name)
                actual = app.same_existing_file
                def compare(left, right):
                    self.assertEqual(left.read_bytes(), right.read_bytes())
                    self.assertFalse(actual(left, right))
                    if isinstance(identity, Exception): raise identity
                    return identity
                with mock.patch.object(app, 'same_existing_file', side_effect=compare) as equality:
                    if identity is False:
                        app._stage_backend_helper(portable)
                    else:
                        with self.assertRaises(PermissionError if isinstance(identity, Exception) else ValueError):
                            app._stage_backend_helper(portable)
                    self.assertEqual(equality.call_args_list[0],mock.call(source,node/'helper/python.exe'))
                    self.assertEqual(equality.call_count,2 if identity is False else 1)


def completed_archive_process(command, raw):
    """Finite owned pipe DATA for a directly exited Git process stand-in."""
    stdout, write = os.pipe()
    os.write(write, raw); os.close(write)
    stderr, write = os.pipe(); os.close(write)
    return mock.Mock(args=command,stdout=os.fdopen(stdout,'rb',buffering=0),stderr=os.fdopen(stderr,'rb',buffering=0),
                     poll=mock.Mock(return_value=0),wait=mock.Mock(return_value=0))


def private_manifest(test, root):
    """Projection assertions run in the private organization workspace."""
    path = root / 'scripts/split.manifest'
    if not path.is_file():
        test.skipTest('split.manifest is private organization input; the private gate runs this projection check')
    return path.read_text(encoding='utf-8')


def archive_tool_fixture(root):
    """Owned DATA pins model the source-selected tool; never execute this file."""
    policy = json.loads(app.BACKEND_POLICY.read_bytes())
    tool_root = root / 'archive-tool-inputs'; tool_root.mkdir()
    selected = policy['archive_tool']
    records = [(selected, b'not an executable'), (selected['source'], b'fixture full tool source')]
    for notice in selected['notices']:
        path = tool_root / 'notices' / notice['name']; path.parent.mkdir(parents=True, exist_ok=True)
        raw = ('fixture full notice ' + notice['name']).encode(); path.write_bytes(raw)
        notice.update(bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest())
    for record, raw in records:
        (tool_root / record['name']).write_bytes(raw)
        record.update(bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest())
    policy_path = root / 'backend-amd-v1.json'
    policy_path.write_text(json.dumps(policy), encoding='utf-8')
    shutil.copyfile(app.BACKEND_POLICY.with_suffix('.patch'), policy_path.with_suffix('.patch'))
    return tool_root / '7zr.exe', policy_path


# Exact upstream build_local.py8ffa1109, MIT; source-only fixture, never main.
import zlib
BACKEND_UPSTREAM_FIXTURE = zlib.decompress(base64.b64decode(
    'eNqtW/9z2zay/11/BcpcxlQqUrHbSe45VWZc12ky18QZ2+l718SPQ5GUxZoiWX6RLfv0v7/PLgASpCTH6bzMXS0SiwWwu9j97AJ8'
    '8t24LovxNE7HUboU+aqaZ+kPA8uyfq7jJBRZGomy8tPQT/DTAU1cZOkiSivhF8E8XkYiyQI/SVYjcROjc12JvJ4mcTmP0yt3MDiL'
    '8iIL6yAqRTUnXlFeimwm3CtQ19PxTVZcz5LsphxPaUCnO5i7WiRilhXCH5Tgl0Ri6Rexj9GzlPnNsxIDJn4FosWhCLObNMn8UC3E'
    '6fMcDQL6I46zxWz16d1I5HHuxCkIkoT5LaM0xHDfawr8WvipfxUVooj+quMiorWXo8G0TkNMp16OsKYizvFnkV1HThWV1UhcRWlU'
    '+FVEneMZXrl/llk6EpiHyP3gGgxpuIGWIYmOx49u/aASL+9AVfiLqIqKUhy/E3UZlRDmBSgg4RxCTvwVyXrhV8Ecsr2Z+5Uoo2IZ'
    'eawPN18JCAwjl4eDgcC/n0D+evyTkh9+Vf7V63FAy6xjU+wNicMk7su7B/p31jcYfMgq0ruIS1HnpIgoxJpXN/OoiFzxryjKDSug'
    'l5ikKFdp0EhgoA0CS4pSEVcimPu0ECz/uMjKUupUDZJm0D5exlPowoYyoaUyS5YskShKSjYeGrKo0xSdBtpWhlBYBitKVtqkSilM'
    '4txY1uk5zDwSfhBEeRWFr0gRsEU9Ry+My5w6kTn6NNa1sNNMnB1AF0ERgafzWu8HteryOs6HospgIxX0X8UzaFzOE8KDnekZ0oo/'
    'lbCUQ5a/tGhRBjC2Su2WVtc3cer4i1A4jtZoEc3E8rn7o/ti/5H9sbfD2BfNvycwMjJncgAkX7UlyDkMBvEizwpyAlew1DLSz2wG'
    '6rdeiH4uGqpyXldx0jzVU/gIeIiyebNqftZFksRTlzYfpjKYFdlChJhWFS9gPJJGP48E/fcORizpoJo5Omuyj3gcyBa5+iyP0mDp'
    '+cXixY+aiFuEX3ZJpDHBm518PPXOTk8vxITZ2Z43i5PI84auMjx76EIg8BHqz2DwRLyPi0KZobacPbjDLHeSaBklgjwv+Rn4kVtX'
    'fMppPWiGgcAe3MHHf1+8Pf3g/X5ydv7u9ANGtn5w9/G/A2vw8edz7+zkt5Oj8xN6f/D84MXzg/19i0b9GBWO9pZS+d4S7iSGEWT4'
    'UcQhdglLgyYmRxd2UBc0bWyLFHJ8BT6Re+U29nUDyeRxmmJfw4RpEthokID2x3Ua0yB+Is5OjxfipXvg7qutOHQHvx+dvTv6cOF1'
    'V3SOqd+vB7qVH9lkrWlU+U5rmg5ryjoUlumLO41udVtZI9m77bijS584TqsocW7zeoOeW/rkkMcGId7RhPukwRaeeGeSJXFa3z5+'
    'vpJ8xxQ2Cb9haQs/cBZ5uUGI94oMulIa/Pjb0cWb07P3j9GZT8HuxY9OHlAzIjVYlstgu7Zu//nCe5i2s6KvkytJfZ0weAzHvrIU'
    'dZ1ep0Agjmy+Suut2noc8bb17exi6EyL2c/zJHJCv8D8pc7enp5feKesKlrpDwdEzq1qTHqWP/BG9Txk5tbahTey4ZfdJoCyj+mb'
    'gT+FbyEPIeHe8cdPEidWUVDViKQ5ApxySq8QMTnIcuj3wS2MZ7OIvA+8EQViOMFVTj6zhksu4WXYL2EiMtZG8RX0Cu7CgKWuXObR'
    '2fHbE8MqdxnfIWRxdPYeFjpqiKy1kuoOG+A+73+RfSRNv8umoqhTj3arpohObpk1K+2JRE0dmHR+9ObEOz/59f3Jhwtu6eK+sSgO'
    'jn1A2uzKBfxjRQCsgdU0AjqA1/c5NKLfFSP5WPpuZhOKT2e/MU6lVxqfUpBLgUfdQWfsCWK6C5Y5mu3C+t/PR84fvnP33Pmvy/an'
    '6zmXz/5hsb2cMLwFoKVOiJgcjQyo64jriEASrwUqv8Nki4goMU+/wlQwg5PfTz78AQ3/Svr9bDnVyzvShLN4PknuFv6BfLid/CB/'
    'zKaTH9S7cLL/YiF/lpMsVe2TbDazLgc0v98l/I/DUgNSgEwMP0No9BmtLmMJNhH9kOLkOQSGOHqNYF4KGyKL0VvmE2B3E9/5RSiA'
    '/oDwGGuShxnTbh1L06ChAEURQ2HGcw7JGA7gLYl8QE2ZZZRgVdYgIFiyzb3KMA5sWhDuzUTgp4SMgXlJiwt38PPJxZH38ezkzbv/'
    'IZzATAASBiGBRLkdvay01c+hRJxx0+ZiQUVVkkhsg5Wio38aaEz0r88wmC7tJRMDFNdF2jLOk7iyLQeq2B9+fn7Zm1MXt/Tmp1jt'
    'ABXssFSHkeg2DtUwcemV/izysDts/L/Ld5pliW2au8v5ARMOeYdYrmtxCkIbCBtMMpXAMa0X06jwSDE99oDP2ADRbVUw8KdtBuKo'
    'iANBKe9PH15D2TNY3CtKnWQ+BfgUUb4j0elzlyA48VrILVhGtFGxA4mB/SX8fvgPCJSGNBeEiGIv3Ksiq3N7H0uAfheS83M1dbXd'
    'Pb3dW/m108fEyYRcWIR7dWcZVmJakJhMZOCQA1jIIi1zLjNrS+Z5r3qvnXuMt77HUGurI1WdbnYmNhKKl4eN2j7g7yIGhTKecqSS'
    'e1D95dHqRpTSgwyBFvim1c7FvJe3Y6f7edTH73u/6iRfakV32eNET8oKEyhf9f1zCF1Thnp2gMWv4Ev0bsc0iliD8rhqtKxkdt/s'
    'NismIKFl0L5WS0UbiaV9zxPE292maRBrI5hiSujzGE1Zbfcn5N0Vi1JlFlIuKs6nkEperTiklOwa2W/KX7F0kwa3PMtrSkNDTkaL'
    'Jp//Na7e1tPGUQZZvhoJf0YkyyzBfnLYtVTYPUg8msXxqFjV50tjxYb5oMk0pk0aaVUGmTKzlrLrs1o97XJmRtcqg+DMnsp0P8sW'
    '67JPS9GoO1a3h2zf7OfXYZzt7sbNnV75tPSUrEFupJ0GTb3cyrFedji1m7BJLZi8uzUfpNd7Fv3MHfxZ0VrbdfuI/oq4w0CV/x7H'
    'QBF3GMhNRzk9yHWpwgU8tHW1wq2rYIgYW8zojW09/bfzdOE8DS+evj18+v7w6fkflrKStfKGiOx2sAhH4tmz6xu/uNLeKy/Iyc+s'
    '78X9nthz/8zi1AZfOxjyxgkoUqHfEDtWzJK6nE8uijrqRIm2GuM2owTzKLhmUmNENRVdcrXrIhkRFqp6c/lFEVAcuwfRtrHZNXRL'
    'PS4eqfpCfIeEfAAC85HgVyEXWa2bqcUts3kLRGRpySV/QHs9m/5py46zuZ6xqtR4srrlGfvdVlPHkBTj5lWVl4fjsZ/HqlxNYHcM'
    'QJqVY66GOafF1VjVxcZqf5Rjydj6ppW1K4BDxeAUd1yWK7UOGc1YaOLtYQ01QCP0gbdt78KP4Q7PV4hBi5NbwlZvfMiByzVq4bqs'
    'p0vc2oeCzyuA8bLsVhHVYFqdR3WVOWFECZ1yyhsVQs0RuQxHh459tUAJWEX5RJt+hnHRBWDqJfIZWzlVN7qNGHE0qexEZrAKZdAx'
    'hjrCsLS2ZxFAm/a9bQizKZKPmhjaq5ANzZqrp1NeKvr1Et7PioEEt6QbECFkqvOH+y7b9ff3hutcN816gLU+i/AoCmqIZZjkrLFJ'
    'wx597HA/ccr5eMepR2uZerOOO/MY39PE13IgDDql05AJYx0IX8ne4NeZWHf/q95DDRUJ426yUg2WSeUurqFsW9kKPA9ikV9winZ7'
    'N7P4mMXW7OntsXqn+g8vZVf93A5nUXGW4ayeRveQSdmn4u3WKVKzazUR5U6KRVVEUTOUMi0+S9L+QxkUNoy2Yl4D9ERrYFJeDPZO'
    'Xs3p5758nhZ+GtCLDuAQ2zS94XNcyZ7EoNemWiySh8pylE2ZEaxv/r34q5Ywi1PftIRODORGrggyrTo5mwi7LZKPN/hCE37oVRAk'
    '9nyQUVCYWHU1c/6ptCC53hRxFUmyNsVkVm7Bx2229SVVPejf9wLPTiFaV9xOlKbYadwW0SWRgWP7s5MtQ/MoZWJ6sAdNy/BY3ymP'
    '1Xps5dqC+SILbfUALpVNkMDDy0j8RzzP9vf3lUl2A/RnUr7sxtuC6xtRWtYIU3EuTazOrwo/jABLVCh/48NfGsbW/gv8nAp1njxm'
    'NOHBrrHUKMrKugOqZhr3JpyQjFS8l4dzE+BwLaCdnpWd/M76XbMGyRIRAlAsZNDDbyTw4Z8Efrac6jSqk2+l97SGf2PVaeYEPgTs'
    'wJFZ5lZ22I/t2D3EbIV9TDWqZ3JKHXnp/JcqQV697O5cnZSnS2Ob9myQKSibMzHN1vhRL/sgpokYVt+QJxuGXNNRWjAnF9CUUh/U'
    'rFlWajRs7Grjn4zvqoDaUjTBZ2bd0wrXWIJzryay7luLe8dK02Kql/yi5aaOAPGSoGNrWQTh1Ev3jzh/Q9XOPhPCcXezw87cZceZ'
    'y2CPKAm+MGVZBArKkuYkG2o0QG1YVocbktiCbpkViNtlbMytG9K6CMsYnFt1xXdCr51tJWqz8mMag1HtQc9dhfDt2KFR370af93V'
    'k+q0C1ZgvL8NKbaMsg1d8Abn0EPYOMfewi6e7FuXHZxAijkwEQiS35EW85SAqnynoMqWsR8FPzZVaPBWDqOkCKrumpSEo8nU2sru'
    '7m3cZfxbPGXGxM7R7DplqZSitLZjpDlYw1WSTTVmfzbudh1qj8bC9IyjGxZHtwxnltvOiV7UaRpR7PMLVUKS5TZk1tHS4RJ77q/4'
    'ClA19ysJ7OggI88xUcBw/DcKS+FzdZwzcV1l+7oPpYWAZLtkJXpBrMn9qoq43CpUOWUMqYyfuSSby1bORDvj4xvwkyJTXYfdjT/r'
    '2kS3XyFl/cz1LaPbli6ADeaU4jRIahmh5Yty7hf8GPizWXRgbaIDRejBo9YBfo99pILzqC7isoqDcehX2MJV2bLkLPhyIzdX1szS'
    'HNO8RiK+SjOAjojuSZQG6OCiBYVUmjpVCEnUes0esBc3eh6satcwTPHwCGBD/A3Wz1y8MwWKRy3SRVzSXTQvuzbYPBG/RNP6yilX'
    'i2mWSNvOqeJhH7+Dm+NSKBc3BXtCOgIayhMhUC5KPup6pTjRDaH2uJOvQWF705rY2GkX0P2q1F8is6e8290FMvmUQomDj5hsi/ub'
    'K+vBydkekEkorHtIY20Jh1NayKPM8IAoEagZaGcY0rLF/f16DRB+8HqMbThO6yTZ24oupXqiJFHFJAONGmF4M650N8QjpxyuaMd1'
    'Z337TVN9cLrKi9GtP48sfSsw++ZUYRvgDDpgUqEU3mKvVFlmj5/2RvKt6+mCg+cNX1lb+v4VLtqueKCe+PP1jshVS6pUgKplYLwk'
    'RsbjVxlK5Nuwko97I/V+d3dFf5QkIsgKfROsFKf/2htuphv/D+nFpl7owUx1LXW1ToZLNgvwkrfL4E6QkG+ieiyiyMo8Ciq9UJ2Z'
    'A8Ni0/8dK5LICszz66vmsMFI6nk1FMYeziMDKtNqNckCkVaTfOoox6x4Py6rhKEAOBgu1IAUBTKDELSuzPX5qBKZPzcGlBBDZ88V'
    '1vwwPrLkmiEyuhK7fWVKngzj9CkFKeQRE9s8fpOnMYcdCauXw+1HNFuJVdNw6+nM1h6yZdg5dAGhXHkjND5TH37evyS5qbbdwuue'
    'LJAr9V7e6Uq4DMBpGPOdRMYP8rbFyzufSlbW8eGXj0WGbH8hKC8qv7x0kCJ9eXnHKUUH7iCDJeWY4ahhjWBYCNsYaSbvV7YEblzy'
    'uTSsgef+ARbfiRk8QDdQ6MNmahlsr46/vOMaupwd9tjHo4u3wtZXwnkx2GX5SyRSw6amrMBfv4jGZ9Ebh8yQvlHb1kfrkMTDp+0K'
    'DFy0l9LN+gHzEfIOJyaLv537Okl8zdo6fudupicP1OeapKtft2Mu+pyLr930/M6orTyO+NCsPT63NoRy2btfsvXmQM/3NqlbwKmb'
    'FtpIPNPT2nD7ZCc9Lo1503UvKrgYt4kex5TOniAApVUSlbr6MuajDU2ick95CbhUjiW6BYTtAkelmUW2jGwjHdUzGcojNfPNpkJ1'
    'KOhLXfXsve84tR5zZd8LP061B+Dr3QWbq7zq7R4VVzUZ4UduocM4DnzwUhPPC7PA86QLAtKNSptu9QwNTq4fhgiKkoVtKelZBK6y'
    'GD57UiLgRKGtbwMPH+jcPaeiBc/8OqkmlrU7GgHO5RNtq0IW38d05mar3oe9Qy1VERtaD04EHL5pAmftoZsx8k/Gel47fGNkX310'
    'YD0UXxEIhKMuzfHdIfq4gb6d8FOV9iK9A6wRPpYDInIwD68H9i3LmHwHc0LOuF1eB/aEsOmvLvdUfrVSZFlFN3TSEgOFYrrq341p'
    'RUFsx1+ZJBn+7ll+4A9+Hp7XeVDw9xu0lduxuQQ87n0I80pcR3nFEREelzAbHRSqCdKJOPaImif/oZnC/Af95Ay5G82MAp7h/ojY'
    'bXwgEjhF/sDJbrdIYN2bLNb6DuBUXk9SH0xhbL5y+9CnMl1Ds/SXM83nHENXfCrb0sy2b2C2fcxiDR97pGquo4kUuoOrrgYDBqib'
    'd8Z938897pd/W3oINlf0+c7GCS2kWUlB6jvKcWn1eKHXxmzXGpnTdznsTEvXuHkgOL7vvpcwaG8GcFf6SXUg6x7Na+UpLIUZ6MSf'
    'tFGndLuRutGXUjcjlpi8I+WLBSw9dn79WRp7c0u4vSOsmHVuCnM9obtj5e3sIvoTG4JcDn3u5a9c84LC9muWj9HKBSZEdwi+K9b6'
    'cy9f8KI607KTiApnJfZ+jLwRf/dcJJF7Hv3H2Ru6fZv+md0+6UJd9N0b73FlkO+n3SJ6BVgKe/WR/ACGf4vFSgYMLXB1mkU2PxFy'
    'D9MDIwMCtB1HKQ+WKNr2rE1dohsaHxE1TsXVQGPSeexRQtA0rssAo7QfkvDM+m+aLrH0EzqNXUlYUtIBNMEQUuIsLkr4wXtiujZW'
    'uQXVKLPuITxMVwfwLfv54TsZJv1o1+25jr9U8HTHcbyU0O5D8N5425CvzB6xqP4xXGcaEpltL2vLtn6xqNtdgWy6T0j3es37A815'
    'xMYxPx8sLh0OOUT59uTol63Z+FdS3V7KrZQqaxKYzVcrFcq5tbfhmo8vjGt8yFX/9vUAPbPNy33EtS+X/nUFPmr9Vv76Ot8O/rsu'
    'EnzLOGt5j1vfkZ30Lzp3jZP9kb7l3L3d/DA8/Mrd5+5xVC9fMG9j8LW0sF7kpa2pRrCNEFwmB8OR2LzU0TtV7KbOW1a3MVHT7XWv'
    'on1J6SN18lP61FDY+qdxhYJq64jbK+y7Ye8yW+MKt0C7nvvTXc4p/MFHHva/qO3FRcchsCvuzekzCBjAWXu8Ns/jZNfzKN/yPJXx'
    'yuRr8H+BSGgd'
))

class SourceGitIsolationTests(unittest.TestCase):
    def test_public_launcher_bootstrap_uses_only_projected_canonical_inputs(self):
        canonical=Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve();source=root/'adopters/comfyui';source.mkdir(parents=True)
            (source/'oa_atomic').mkdir()
            script=source/app.LAUNCHER_NAME;script.write_bytes(app.LAUNCHER_SOURCE.read_bytes())
            (source/'oa_interpreter.py').write_bytes(app._launcher_inputs()['oa_interpreter.py'])
            atomic=source/'oa_atomic/abstraction_cas.py'
            atomic.write_bytes(app._launcher_inputs()['oa_atomic/abstraction_cas.py'])
            (source/'_oa_atomic_file.LICENSE').write_bytes((canonical/'LICENSE').read_bytes())
            with mock.patch.object(app,'__file__',str(root/'scripts/package_comfyui_app.py')),mock.patch.object(app,'LAUNCHER_SOURCE',script):
                inputs=app._launcher_inputs()
                self.assertEqual(inputs['oa_atomic/abstraction_cas.py'],atomic.read_bytes())
                atomic.unlink()
                with self.assertRaisesRegex(ValueError,'plain source'):app._launcher_inputs()
                atomic.write_bytes(b'foreign CAS')
                with self.assertRaisesRegex(ValueError,'selected CAS'):app._launcher_inputs()

    def test_helper_validation_binds_the_exact_consumed_read(self):
        original=Path.read_bytes
        helper=app.LAUNCHER_SOURCE.with_name('oa_interpreter.py')
        observed=[]
        def changed_read(path):
            if path==helper:
                observed.append(str(path))
                return b'changed only in the staging read'
            return original(path)
        # The real runtime owner validates canonical bytes with builtin open;
        # the independent Path.read_bytes staging read must be checked too.
        self.assertEqual(hashlib.sha256(app._launcher_inputs()['oa_interpreter.py']).hexdigest(),
                         app._launcher().INTERPRETER_SHA256)
        with mock.patch.object(Path,'read_bytes',changed_read):
            with self.assertRaisesRegex(ValueError,'consumed interpreter helper'):
                app._launcher_inputs()
        self.assertEqual(len(observed),1)

    def test_launcher_helper_stages_exact_bytes_and_refuses_missing_drift_and_collision(self):
        canonical=app.LAUNCHER_SOURCE.with_name('oa_interpreter.py')
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary).resolve();source=root/'source';source.mkdir();stage=root/'stage';stage.mkdir()
            script=source/app.LAUNCHER_NAME;script.write_bytes(app.LAUNCHER_SOURCE.read_bytes())
            helper=source/'oa_interpreter.py'
            with mock.patch.object(app,'LAUNCHER_SOURCE',script):
                with self.assertRaisesRegex(Exception,'unavailable'):app._stage_launcher(stage)
                shutil.copyfile(canonical,helper)
                inputs=app._stage_launcher(stage)
                self.assertEqual((stage/'oa_interpreter.py').read_bytes(),canonical.read_bytes())
                atomic = app._launcher_inputs()['oa_atomic/abstraction_cas.py']
                self.assertEqual((stage/'oa_atomic/abstraction_cas.py').read_bytes(),atomic)
                self.assertEqual((stage/'_oa_atomic_file.LICENSE').read_bytes(),(Path(__file__).resolve().parents[1]/'LICENSE').read_bytes())
                with self.assertRaisesRegex(ValueError,'collides'):app._stage_launcher(stage)
                (stage/'oa_interpreter.py').write_bytes(b'changed stage')
                with self.assertRaisesRegex(ValueError,'differs'):app._check_launcher(stage,inputs)
                (stage/'oa_interpreter.py').write_bytes(inputs['oa_interpreter.py'])
                alias=stage/'extra-helper-name';os.link(stage/'oa_interpreter.py',alias)
                with self.assertRaisesRegex(ValueError,'differs'):app._check_launcher(stage,inputs)
                alias.unlink()
                helper.write_bytes(b'changed source')
                with self.assertRaisesRegex(Exception,'differs'):app._check_launcher(stage,inputs)

    def test_private_projection_manifest_is_explicitly_unavailable_in_public_layout(self):
        with tempfile.TemporaryDirectory() as area:
            with self.assertRaisesRegex(unittest.SkipTest, 'private organization input'):
                private_manifest(self, Path(area).resolve())

    def assert_environment(self, kwargs):
        env=kwargs.get('env')
        self.assertIsInstance(env,dict)
        self.assertNotIn('GIT_DIR',env);self.assertNotIn('GIT_CONFIG_PARAMETERS',env)
        self.assertEqual(env['GIT_CONFIG_COUNT'],'2')
        self.assertEqual((env['GIT_CONFIG_KEY_0'],env['GIT_CONFIG_VALUE_0']),('core.fsmonitor','false'))
        self.assertEqual((env['GIT_CONFIG_KEY_1'],env['GIT_CONFIG_VALUE_1']),('core.hooksPath',os.devnull))

    def test_selected_checkout_head_uses_source_isolation(self):
        def git(command,**kwargs):self.assert_environment(kwargs);return 'a'*40
        with mock.patch.dict(os.environ,{'GIT_DIR':'foreign','GIT_CONFIG_PARAMETERS':'core.fsmonitor=foreign'}),mock.patch.object(app.subprocess,'check_output',side_effect=git):
            self.assertEqual(app._git_head(Path('selected-checkout')),'a'*40)

    def test_source_archive_never_inherits_repository_or_config_execution(self):
        with tempfile.TemporaryDirectory() as area:
            root=Path(area).resolve()
            def git(command,**kwargs):
                self.assert_environment(kwargs)
                self.assertEqual(command[:4],['git','-C',str(root),'archive'])
                raw=io.BytesIO()
                with zipfile.ZipFile(raw,'w') as archive:
                    archive.writestr('source/main.py',b'fixture source')
                return completed_archive_process(command,raw.getvalue())
            with mock.patch.dict(os.environ,{'GIT_DIR':'foreign','GIT_CONFIG_PARAMETERS':'core.fsmonitor=foreign'}),mock.patch.object(app.subprocess,'Popen',side_effect=git),zipfile.ZipFile(root/'result.zip','w') as combined:
                app._append_git_source(combined,root,'source','a'*40,root)
            with zipfile.ZipFile(root/'result.zip') as result:self.assertEqual(result.read('source/main.py'),b'fixture source')

    def test_public_source_closure_checks_and_archives_share_isolation(self):
        with tempfile.TemporaryDirectory() as area:
            root=Path(area).resolve();checkout=root/'selected';checkout.mkdir();calls=[]
            def read(command,**kwargs):
                self.assert_environment(kwargs);calls.append(command)
                return 'a'*40 if 'rev-parse' in command else ''
            def archive(command,**kwargs):
                self.assert_environment(kwargs);calls.append(command)
                prefix=next(x.split('=',1)[1] for x in command if x.startswith('--prefix='))
                raw=io.BytesIO()
                with zipfile.ZipFile(raw,'w') as component:
                    component.writestr(prefix+'LICENSE',b'fixture notice')
                return completed_archive_process(command,raw.getvalue())
            selected={'repository':'openabstractions/abstractions','commit':'a'*40}
            manifest={'source_mode':'public-split','adopter_source':selected,'packager_source':selected,'wheels':[]}
            with mock.patch.object(app.subprocess,'check_output',side_effect=read),mock.patch.object(app.subprocess,'Popen',side_effect=archive),zipfile.ZipFile(root/'source.zip','w') as combined:
                result=app._append_public_integration_sources(combined,manifest,{'openabstractions/abstractions':checkout},root)
            self.assertEqual(len(calls),3)
            self.assertEqual(result,['OA-Integration-abstractions-'+'a'*40+'/'])

    def test_both_projected_comfy_source_trees_receive_the_canonical_helper(self):
        root=Path(__file__).resolve().parents[1];manifest=private_manifest(self,root)
        sections={};repo=None
        for line in manifest.splitlines():
            if line.startswith('repo '):repo=line.split()[1]
            if line.strip()=='file scripts/git_env.py scripts/git_env.py':sections[repo]=True
        self.assertEqual(set(sections),{'abstractions','redist'})
        self.assertEqual(Path(app.source_git_environment.__code__.co_filename).resolve(),root/'scripts/git_env.py')


class SourceCapacityTests(unittest.TestCase):
    def setUp(self):
        self.area=tempfile.TemporaryDirectory(prefix='oa-source-capacity-')
        self.addCleanup(self.area.cleanup);self.root=Path(self.area.name).resolve()

    def row(self,name,data):
        return name,len(data),hashlib.sha256(data).hexdigest(),data

    def test_git_capture_uses_selected_older_commit_and_current_commit_bytes(self):
        repo = self.root/'selected-repo'; repo.mkdir()
        def git(*args):
            return subprocess.check_output(['git','-C',str(repo),*args],
                env=app.source_git_environment(), stderr=subprocess.STDOUT)
        git('init'); git('config','core.autocrlf','false')
        commits = []
        for content in (b'older native source', b'current application source'):
            (repo/'source.txt').write_bytes(content)
            git('add','source.txt')
            git('-c','user.name=fixture','-c','user.email=fixture@example.invalid',
                'commit','-m','selected source')
            commits.append((git('rev-parse','HEAD').decode().strip(), content))
        for index, (commit, content) in enumerate(commits):
            project = f'selected-{index}'
            component = app._capture_git_source(repo, project, commit, self.root)
            with zipfile.ZipFile(component) as archive:
                self.assertEqual(archive.namelist(), [f'{project}-{commit}/',
                                                     f'{project}-{commit}/source.txt'])
                self.assertEqual(archive.read(f'{project}-{commit}/source.txt'), content)

    def test_git_capture_rejects_revision_aliases_before_child_and_unknown_commit_cleans(self):
        for commit in (None, 1, 'HEAD', 'main', 'a'*39, 'a'*41, 'A'*40,
                       'a'*40+' ', '--output=foreign', 'a'*40+'^{tree}'):
            with self.subTest(commit=commit), mock.patch.object(app.subprocess,'Popen') as child:
                with self.assertRaisesRegex(ValueError, 'selected commit'):
                    app._capture_git_source(self.root,'invalid',commit,self.root)
                child.assert_not_called()
        repo = self.root/'unknown-repo'; repo.mkdir()
        subprocess.run(['git','-C',str(repo),'init'], check=True, capture_output=True,
                       env=app.source_git_environment())
        with self.assertRaisesRegex(ValueError, 'archive failed'):
            app._capture_git_source(repo,'unknown','f'*40,self.root)
        self.assertFalse((self.root/'unknown.zip').exists())

    def test_stored_plan_bounds_zip64_unicode_empty_and_incompressible_members(self):
        rows=[self.row('empty',b''),self.row('long/'+'é'*120,bytes(range(256))*1000)]
        plan=app._source_member_plan(rows)
        output=self.root/'source.zip';app._write_source_plan(output,plan)
        self.assertLessEqual(output.stat().st_size,plan['bound'])
        with zipfile.ZipFile(output) as archive:
            for name,_,_,data in rows:
                self.assertEqual(archive.read(name),data)
                self.assertEqual(archive.getinfo(name).compress_type,zipfile.ZIP_STORED)
                self.assertEqual(archive.getinfo(name).extra,b'')
        # force_zip64 writes the local ZIP64 size extra even for these tiny files.
        self.assertIn(b'\x01\x00\x10\x00',output.read_bytes()[:100])

    def test_names_caps_changed_inputs_and_output_overrun_refuse_without_replacement(self):
        for names in [('a','A'),('../bad',),('a\n',),('é'*32768,)]:
            with self.subTest(names=names),self.assertRaises(ValueError):
                app._source_member_plan([self.row(n,b'') for n in names])
        with self.assertRaisesRegex(ValueError,'logical archive'):
            app._source_member_plan([('large',8<<30,'a'*64,b'')])
        source=self.root/'input';source.write_bytes(b'old')
        plan=app._source_member_plan([('input',3,hashlib.sha256(b'old').hexdigest(),source)])
        source.write_bytes(b'new')
        with self.assertRaisesRegex(ValueError,'differs'):app._write_source_plan(self.root/'bad.zip',plan)
        self.assertFalse((self.root/'bad.zip').exists())
        plan=app._source_member_plan([self.row('ok',b'payload')]);plan['bound']=1
        with self.assertRaises(ValueError):app._write_source_plan(self.root/'over.zip',plan)
        self.assertFalse((self.root/'over.zip').exists())
        target=self.root/'existing';target.write_bytes(b'keep')
        with self.assertRaises(FileExistsError):app._write_source_plan(target,plan)
        self.assertEqual(target.read_bytes(),b'keep')

    def test_capacity_keeps_backend_reserve_and_counts_source_twice_and_all_git_temporaries(self):
        plan=app._source_member_plan([self.row('source',b'x')],[(Path('one'),(10,'a'*64)),(Path('two'),(20,'b'*64))])
        actual=app._backend_capacity(1,2,plan,3,4,5,20<<30)
        common=30+8+3+(8<<20)+5+(20<<30)
        phases=[1+2+(16<<30),(16<<30)+(8<<30),(8<<30)+plan['bound'],(16<<30)+2*plan['bound']]
        self.assertEqual(actual,max(phases)+common)
        for index,phase in enumerate(('prepare','pack','source','finalize')):
            self.assertEqual(app._backend_capacity(1,2,plan,3,4,5,20<<30,phase=phase),max(phases[index:])+common)
        self.assertEqual(app._backend_capacity(1,2,None,0,0,0,20<<30,phase='inputs'),3+(36<<30))
        with self.assertRaisesRegex(ValueError,'unknown backend capacity'):
            app._backend_capacity(1,2,plan,3,4,5,20<<30,phase='unknown')

    def test_owned_phase_cleanup_refuses_unknown_or_linked_root_and_preserves_siblings(self):
        (self.root/'inputs').mkdir();(self.root/'keep').write_bytes(b'keep')
        with self.assertRaisesRegex(ValueError,'unknown backend temporary'):
            app._release_backend_phase(self.root,('keep',))
        app._release_backend_phase(self.root,('inputs','wheels'))
        self.assertFalse((self.root/'inputs').exists());self.assertEqual((self.root/'keep').read_bytes(),b'keep')
        (self.root/'extract').write_bytes(b'not a directory')
        with self.assertRaisesRegex(ValueError,'directory'):app._release_backend_phase(self.root,('extract',))
        (self.root/'extract').unlink();(self.root/'extract').mkdir()
        original=Path.lstat
        linked=type('Linked',(),dict(st_mode=stat.S_IFLNK,st_file_attributes=0))()
        selected=app._extended(self.root/'extract')
        with mock.patch.object(Path,'lstat',lambda path:linked if app._extended(path)==selected else original(path)):
            with self.assertRaisesRegex(ValueError,'linked removal'):
                app._release_backend_phase(self.root,('extract',))

    def test_zip64_member_count_framing_is_within_the_shared_bound(self):
        plan=app._source_member_plan([self.row(str(n),b'') for n in range(65536)])
        target=self.root/'count.zip';app._write_source_plan(target,plan)
        self.assertLess(target.stat().st_size,plan['bound'])
        with zipfile.ZipFile(target) as archive:self.assertEqual(len(archive.infolist()),65536)

    def capture_fixture(self,code,*,limit=1024,timeout=2):
        import sys
        original=subprocess.Popen;children=[]
        def launch(command,**kwargs):
            self.assertEqual(command[0],'git');self.assertNotIn('-o',command)
            self.assertEqual(kwargs['env']['GIT_CONFIG_VALUE_0'],'false')
            child=original([sys.executable,'-I','-c',code],**kwargs);children.append(child);return child
        with mock.patch.object(app.subprocess,'Popen',side_effect=launch):
            try:return app._capture_git_source(self.root,'fixture','a'*40,self.root,limit=limit,timeout=timeout)
            finally:
                for child in children:
                    self.assertIsNotNone(child.poll());self.assertTrue(child.stdout.closed);self.assertTrue(child.stderr.closed)

    def test_capture_drains_stderr_while_copying_and_handles_success(self):
        path=self.capture_fixture("import sys;sys.stderr.buffer.write(b'e'*900000);sys.stderr.flush();sys.stdout.buffer.write(b'x'*1024)",timeout=10)
        self.assertEqual(path.read_bytes(),b'x'*1024)

    def test_capture_overrun_error_stderr_limit_timeout_and_existing_path(self):
        cases=[("import sys;sys.stdout.buffer.write(b'x'*1025)",ValueError,2),
               ("import sys;sys.stderr.buffer.write(b'e'*1100000)",ValueError,2),
               ("import sys;sys.exit(7)",ValueError,2),
               ("import time;time.sleep(10)",subprocess.TimeoutExpired,.05)]
        for code,error,timeout in cases:
            with self.subTest(code=code),self.assertRaises(error):self.capture_fixture(code,timeout=timeout)
            self.assertFalse((self.root/'fixture.zip').exists())
        (self.root/'fixture.zip').write_bytes(b'keep')
        with self.assertRaises(FileExistsError):self.capture_fixture('pass')
        self.assertEqual((self.root/'fixture.zip').read_bytes(),b'keep')

    def test_direct_child_exit_with_delayed_pipe_eof_still_obeys_deadline(self):
        import threading
        read,write=os.pipe()
        child=completed_archive_process(['git'],b'')
        child.stdout.close();child.stdout=os.fdopen(read,'rb',buffering=0)
        # This owned writer deliberately retains stdout after the direct child
        # has exited. No descendant process or reader thread is needed.
        release=threading.Timer(.2,lambda:os.close(write));release.start()
        try:
            with mock.patch.object(app.subprocess,'Popen',return_value=child), \
                 self.assertRaises(subprocess.TimeoutExpired):
                app._capture_git_source(self.root,'delayed','a'*40,self.root,timeout=.05)
            self.assertTrue(child.stdout.closed);self.assertTrue(child.stderr.closed)
            self.assertFalse((self.root/'delayed.zip').exists())
            child.kill.assert_not_called();child.wait.assert_called()
        finally:release.join()

    def test_actual_git_export_semantics_prepared_zip_reuse_and_tamper_refusal(self):
        repo=self.root/'repo';repo.mkdir()
        def git(*args):
            return subprocess.check_output(['git','-C',str(repo),*args],env=app.source_git_environment(),stderr=subprocess.STDOUT)
        git('init');git('config','core.autocrlf','false')
        (repo/'.gitattributes').write_text('version export-subst\nignored export-ignore\n',encoding='utf-8')
        (repo/'version').write_bytes(b'$Format:%H$');(repo/'ignored').write_bytes(b'omit')
        git('add','.');git('-c','user.name=fixture','-c','user.email=fixture@example.invalid','commit','-m','fixture')
        commit=git('rev-parse','HEAD').decode().strip()
        selected=dict(repository='openabstractions/source',commit=commit)
        manifest=dict(source_mode='public-split',adopter_source=selected,packager_source=selected,wheels=[])
        checkouts={'openabstractions/source':repo}
        self.assertEqual(app._public_source_checkouts(manifest,checkouts)[0][2],commit)
        with self.assertRaisesRegex(ValueError,'needs exact public source checkouts'):app._public_source_checkouts(manifest,{})
        with mock.patch.object(app,'_git_head',return_value='a'*40),self.assertRaisesRegex(ValueError,'checkout differs'):
            app._public_source_checkouts(manifest,checkouts)
        (repo/'ignored').write_bytes(b'dirty')
        with self.assertRaisesRegex(ValueError,'checkout differs'):app._public_source_checkouts(manifest,checkouts)
        (repo/'ignored').write_bytes(b'omit')
        component=app._capture_git_source(repo,'project',commit,self.root)
        rows=[]
        with zipfile.ZipFile(component) as archive:
            self.assertNotIn('project-'+commit+'/ignored',archive.namelist())
            self.assertEqual(archive.read('project-'+commit+'/version'),commit.encode())
            for entry in archive.infolist():
                raw=archive.read(entry);rows.append((entry.filename,len(raw),hashlib.sha256(raw).hexdigest(),(component,entry.filename)))
        plan=app._source_member_plan(rows,[(component,app._sha256(component))])
        with mock.patch.object(app.subprocess,'Popen',side_effect=AssertionError('must reuse prepared archive')):
            app._write_source_plan(self.root/'final.zip',plan)
        component.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError,'prepared Git source changed'):
            app._write_source_plan(self.root/'changed.zip',plan)


class BackendClosureTests(unittest.TestCase):
    def metadata(self, **changes):
        rows = [dict(name='kornia', version='0.8.3', requires_dist=['torch>=2.0', 'kornia-rs>=0.1'],
                     provides_extra=['x'], requires_python='>=3.9'),
                dict(name='kornia-rs', version='0.2.0', requires_dist=[
                     'sdk==1.0; extra == "all"'], provides_extra=['all'], requires_python=''),
                dict(name='sdk', version='1.0', requires_dist=[], provides_extra=[], requires_python='')]
        for key, value in changes.items():
            rows[0][key] = value
        return self.closure(rows)

    @staticmethod
    def closure(rows):
        return app._backend_closure('3.12.10', rows)

    def test_shared_component_provides_torch_and_its_closure_has_no_wheel_of_it(self):
        closure = self.metadata()
        self.assertEqual(closure['shared'], {'torch': '2.11.0+rocm7.13.0', 'torchvision': '0.26.0+rocm7.13.0'})
        app._validate_backend_closure(closure, ['torch', 'torchvision>=0.20', 'torchaudio', 'kornia',
                                                'torch[device-all]==2.11.0'])
        for text in ('torch>=2.12', 'torchvision<0.26'):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, 'shared PyTorch component version'):
                app._validate_backend_closure(closure, [text])
        for name in ('torch', 'torchvision', 'torchaudio'):
            rows = closure['wheels'] + [dict(name=name, version='2.0', requires_dist=[], provides_extra=[],
                                             requires_python='')]
            with self.subTest(wheel=name), self.assertRaisesRegex(ValueError, 'shared PyTorch engine component owns'):
                app._validate_backend_closure(self.closure(rows), [])

    def test_nested_extras_self_edge_and_all_selected_base_requirements(self):
        closure = self.metadata()
        app._validate_backend_closure(closure, ['kornia>=0.8', 'kornia-rs[all]'])
        for name in ('kornia-rs', 'sdk'):
            with self.subTest(missing=name), self.assertRaisesRegex(ValueError, 'outside frozen'):
                rows = [row for row in closure['wheels'] if row['name'] != name]
                app._validate_backend_closure(self.closure(rows), ['kornia-rs[all]'])
        closure['wheels'][-1]['requires_dist'] = ['unselected==1']
        with self.assertRaisesRegex(ValueError, 'unselected'):
            app._validate_backend_closure(closure, [])

    def test_target_markers_never_inherit_host_python_or_platform(self):
        app._validate_backend_closure(self.metadata(requires_dist=[
            'absent; python_version < "3.12" or sys_platform == "linux"',
            'sdk==1.0; python_full_version == "3.12.10" and platform_machine == "AMD64"']), [])
        for text in ('absent; python_version >= "3.12"', 'absent; sys_platform == "win32"'):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, 'absent'):
                app._validate_backend_closure(self.metadata(requires_dist=[text]), [])

    def test_constraints_urls_unsupported_markers_and_python_refuse(self):
        cases = [('kornia!=0.8.3', 'outside frozen'),
                 ('kornia[missing]', 'undeclared'),
                 ('kornia @ https://example.invalid/kornia.whl', 'URL'),
                 ('kornia; python_version < "1" and platform_release == "10"', 'unsupported target'),
                 ('kornia; platform_version == "10"', 'unsupported target'),
                 ('not valid !!!', '')]
        for text, diagnostic in cases:
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, diagnostic):
                app._validate_backend_closure(self.metadata(), [text])
        with self.assertRaisesRegex(ValueError, 'Requires-Python'):
            app._validate_backend_closure(self.metadata(requires_python='>=3.13'), [])
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            closure = self.metadata()
            app._validate_backend_closure(self.closure(closure['wheels'] + [closure['wheels'][0]]), [])

    def test_inactive_extra_is_ignored_and_late_extra_activation_is_checked(self):
        closure = self.metadata()
        closure['wheels'][-1].update(provides_extra=['later'], requires_dist=['absent; extra == "later"'])
        app._validate_backend_closure(closure, [])
        closure['wheels'][1]['requires_dist'] = ['sdk[later]']
        with self.assertRaisesRegex(ValueError, 'absent'):
            app._validate_backend_closure(closure, ['kornia-rs'])

    def test_torch_and_rocm_distributions_belong_to_the_shared_component(self):
        for name in ('torch', 'torchvision', 'torchaudio', 'amd-torch-device-gfx1151', 'amd_torchvision_device_gfx11',
                     'rocm', 'rocm-sdk-core', 'rocm_sdk_device_gfx1100', 'rocm-bootstrap', 'pytorch-triton-rocm'):
            self.assertTrue(app.shared_torch_distribution(name), name)
        for name in ('torchsde', 'kornia', 'comfy-kitchen', 'rocmish', 'spandrel'):
            self.assertFalse(app.shared_torch_distribution(name), name)
        self.assertTrue(app._shared_torch_absence('kornia 0.8.3 requires torch, which is not installed.'))
        self.assertFalse(app._shared_torch_absence('kornia 0.8.3 requires kornia-rs, which is not installed.'))
        self.assertFalse(app._shared_torch_absence('kornia 0.8.3 has requirement torch>=9, but you have torch 2.'))


class RuntimeManifestHashTests(unittest.TestCase):
    def setUp(self):
        area = tempfile.TemporaryDirectory()
        self.addCleanup(area.cleanup)
        self.root = Path(area.name).resolve()
        self.files = []
        for i in range(24):
            path = self.root / f'{i:02}.bin'
            path.write_bytes(bytes([i]) * (i + 1))
            self.files.append((f'backend/{path.name}', path))

    def test_exact_manifest_bytes_and_four_read_bound(self):
        expected = {n.removeprefix('backend/'): dict(bytes=p.stat().st_size,
                    sha256=app._sha256(p)[1]) for n, p in self.files}
        original = app._sha256
        lock = threading.Lock()
        barrier = threading.Barrier(4, timeout=5)
        active = peak = calls = 0
        def hashed(path):
            nonlocal active, peak, calls
            with lock:
                active += 1
                peak = max(peak, active)
                calls += 1
                initial = calls <= 4
            try:
                if initial: barrier.wait()
                return original(path)
            finally:
                with lock: active -= 1
        with mock.patch.object(app, '_sha256', side_effect=hashed):
            actual = app._runtime_file_hashes(list(reversed(self.files)))
        self.assertEqual(peak, 4)
        self.assertEqual(active, 0)
        self.assertEqual(calls, len(self.files))
        self.assertEqual(list(actual), sorted(expected))
        self.assertEqual(app._backend_manifest_bytes({'runtime_files': actual}),
                         app._backend_manifest_bytes({'runtime_files': expected}))

    def test_first_and_later_failures_wait_running_and_stop_submission(self):
        for fail_index in (0, 4):
            with self.subTest(fail_index=fail_index):
                release = threading.Event()
                failed = threading.Event()
                finished = threading.Event()
                lock = threading.Lock()
                seen = []
                active = 0
                outcome = []
                def hashed(path):
                    nonlocal active
                    index = int(path.stem)
                    with lock:
                        seen.append(index)
                        active += 1
                    try:
                        if index == fail_index:
                            failed.set()
                            raise OSError('selected hash failure')
                        if index >= fail_index:
                            if not release.wait(5): raise TimeoutError('test release missing')
                        return path.stat().st_size, hashlib.sha256(path.read_bytes()).hexdigest()
                    finally:
                        with lock: active -= 1
                def run():
                    try: outcome.append(app._runtime_file_hashes(self.files))
                    except BaseException as error: outcome.append(error)
                    finally: finished.set()
                with mock.patch.object(app, '_sha256', side_effect=hashed):
                    thread = threading.Thread(target=run)
                    thread.start()
                    try:
                        self.assertTrue(failed.wait(5))
                        # Any already-running readers must finish before refusal returns.
                        with lock: running = active
                        if running: self.assertFalse(finished.is_set())
                    finally:
                        release.set()
                        thread.join(5)
                self.assertFalse(thread.is_alive())
                self.assertEqual(active, 0)
                self.assertEqual(len(outcome), 1)
                self.assertIsInstance(outcome[0], OSError)
                self.assertIn('selected hash failure', str(outcome[0]))
                self.assertLessEqual(max(seen), fail_index + 3)

    def test_missing_input_refuses_and_changed_bytes_are_hashed(self):
        self.files[5][1].unlink()
        with self.assertRaises(FileNotFoundError): app._runtime_file_hashes(self.files)
        self.files[5][1].write_bytes(b'changed')
        result = app._runtime_file_hashes(self.files)
        self.assertEqual(result['05.bin'], dict(bytes=7, sha256=hashlib.sha256(b'changed').hexdigest()))
        # Inventory/link/topology admission remains at the existing scan owner.
        stage = self.root / 'stage'
        (stage / 'backend').mkdir(parents=True)
        from types import SimpleNamespace
        with self.assertRaisesRegex(ValueError, 'link|reparse'):
            with mock.patch.object(Path, 'lstat', return_value=SimpleNamespace(
                    st_mode=0o040700, st_file_attributes=0x400)):
                app._scan_tree(stage, 'backend', 1 << 20)


class BackendRecipeTests(unittest.TestCase):
    """Owned tiny files only; no Python/package lifecycle or native library runs."""
    def setUp(self):
        self.area = tempfile.TemporaryDirectory()
        self.addCleanup(self.area.cleanup)
        self.root = Path(self.area.name).resolve()
        self.descriptor = self.root / 'backend-inputs.json'
        self.files = {}
        self.wheels = []
        self.source_names = []
        for name, raw in {
            'builder/scripts/build_local.py': BACKEND_UPSTREAM_FIXTURE,
            'python/python.exe': b'not executed', 'python/python312.dll': b'not executed',
            'python/Lib/site-packages/pip-26.2.2.dist-info/METADATA': b'Name: pip\nVersion: 26.2.2\n',
            'ComfyUI/main.py': b'# source, not executed\n',
            'ComfyUI/requirements.txt': b'torch\n',
            'ComfyUI/manager_requirements.txt': b'comfyui-manager\n',
            'tools/uv.exe': b'not executed',
            'sources/source.txt': b'fixture corresponding source', 'sources/LICENSE': b'fixture licence',
            'sources/provenance.json': b'{"fixture":true}',
        }.items(): self.put(name, raw)
        # This recipe fixture models the source-selected bootstrap without
        # executing pip; the full selected RECORD receives separately.
        self.put('python/Lib/site-packages/pip/__init__.py',b'# not imported\n')
        self.put('python/Lib/site-packages/pip-26.2.2.dist-info/entry_points.txt',
                 b'[console_scripts]\npip=pip._internal.cli.main:main\npip3=pip._internal.cli.main:main\n')
        rows=[]
        for name,path in self.files.items():
            if name.startswith('python/Lib/site-packages/'):
                raw=path.read_bytes();rows.append([name.removeprefix('python/Lib/site-packages/'),
                    'sha256='+base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip('='),str(len(raw))])
        rows.extend([name,*pin] for name,pin in app.BACKEND_BOOTSTRAP_LAUNCHERS.items())
        rows.append(['pip-26.2.2.dist-info/RECORD','',''])
        record=io.StringIO();csv.writer(record,lineterminator='\n').writerows(rows)
        raw=record.getvalue().encode();self.put('python/Lib/site-packages/pip-26.2.2.dist-info/RECORD',raw)
        bootstrap=mock.patch.object(app,'BACKEND_BOOTSTRAP_RECORD_SHA256',hashlib.sha256(raw).hexdigest())
        bootstrap.start();self.addCleanup(bootstrap.stop)
        self.source_names = ['sources/source.txt', 'sources/LICENSE', 'sources/provenance.json']
        # ComfyUI-side wheels only: torch comes from the shared PyTorch component.
        for dist, version, requires in (('kornia', '0.8.3', 'Requires-Dist: torch>=2.0\nRequires-Dist: kornia-rs>=0.1\n'),
                                        ('kornia-rs', '0.2.0', '')):
            wheel_name = dist + '-fixture.whl'
            info = dist.replace('-', '_') + '-' + version + '.dist-info/'
            members = {info + 'METADATA': f'Name: {dist}\nVersion: {version}\n{requires}'.encode(),
                       dist + '/__init__.py': b'# not imported\n'}
            record = info + 'RECORD'
            rows = []
            for name, raw in members.items():
                digest = base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip('=')
                rows.append(f'{name},sha256={digest},{len(raw)}')
            members[record] = ('\n'.join(rows) + f'\n{record},,\n').encode()
            buffer = io.BytesIO()
            with zipfile.ZipFile(buffer, 'w') as archive:
                for name, raw in members.items(): archive.writestr(name, raw)
            name = 'wheels/' + wheel_name
            self.put(name, buffer.getvalue())
            self.wheels.append({'file': name, 'distribution': dist, 'version': version,
                'source': self.source_names[0], 'license': self.source_names[1], 'provenance': self.source_names[2]})
        self.value = {'schema': 1, 'profile': 'win-amd-manager-free-v1',
            'builder_commit': app.BACKEND_BUILDER_COMMIT, 'comfyui_commit': app.BACKEND_COMFY_COMMIT,
            'files': [], 'wheels': self.wheels, 'python_files': [name for name in self.files if name.startswith('python/')],
            'comfyui_files': [name for name in self.files if name.startswith('ComfyUI/')],
            'source_files': self.source_names, 'uv': 'tools/uv.exe',
            'runtime_provenance': {kind: {'source': self.source_names[0], 'license': self.source_names[1],
                'provenance': self.source_names[2], 'version': version} for kind, version in
                {'python': '3.12.10', 'uv': '0.13.4', 'builder': app.BACKEND_BUILDER_COMMIT,
                 'comfyui': app.BACKEND_COMFY_COMMIT}.items()}}
        self.pin()

    def put(self, name, raw):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
        self.files[name] = path

    def pin(self):
        self.value['files'] = [{'name': name, 'bytes': path.stat().st_size,
            'sha256': hashlib.sha256(path.read_bytes()).hexdigest()} for name, path in self.files.items()]
        self.descriptor.write_text(json.dumps(self.value), encoding='utf-8')
        self.digest = hashlib.sha256(self.descriptor.read_bytes()).hexdigest()

    def test_exact_upstream_patch_uses_only_frozen_offline_amd_lock(self):
        builder = app._backend_builder(self.files['builder/scripts/build_local.py'], self.root)
        portable = self.root / 'recipe'
        (portable / 'ComfyUI').mkdir(parents=True)
        calls = []
        builder['install_requirements'].__globals__['run'] = lambda command, **options: calls.append((command, options))
        builder['install_requirements'](portable, 'win-amd', 'requirements-amd-win.txt', self.root/'lock', self.root/'wheels', self.root/'python', {})
        self.assertEqual(len(calls),2)
        command = calls[1][0]
        for option in ('--no-index', '--require-hashes', '--only-binary=:all:'): self.assertIn(option, command)
        self.assertIn('--no-deps',command)
        self.assertNotIn('manager_requirements.txt', str(calls))
        (portable/'ComfyUI/manager_requirements.txt').write_text('must not resolve')
        with self.assertRaisesRegex(ValueError, 'before dependency'):
            builder['install_requirements'](portable, 'win-amd', '', self.root/'lock', self.root/'wheels', self.root/'python', {})
        self.assertEqual(len(calls), 2)
        # Run only the actual pure PEP508 validation body in-process; no pip install.
        import sys
        requirements=portable/'ComfyUI/requirements.txt';selection=self.root/'lock.json'
        selection.write_text(json.dumps(BackendClosureTests().metadata()))
        requirements.write_text('torch>=2.0\ntorchaudio\nkornia\n')
        import platform, inspect
        self.assertTrue(calls[0][0][4].startswith(inspect.getsource(app._validate_backend_closure)))
        # Initialize the trusted host parser before simulating target runtime metadata.
        # Its first import must observe the real host's available standard library.
        from pip._vendor.packaging import requirements as _requirements_parser
        from pip._vendor.packaging import markers as _markers_parser, version as _version_parser
        from pip._vendor.packaging import specifiers as _specifiers_parser, utils as _utils_parser
        with mock.patch.object(sys,'argv',['verify',str(requirements),str(selection)]), \
             mock.patch.object(sys,'platform','win32'), \
             mock.patch.object(sys,'version_info',(3,12,10)), \
             mock.patch.object(platform,'python_implementation',return_value='CPython'):
            exec(compile(calls[0][0][4],'<frozen-requirements-validation>','exec'),{})
            requirements.write_text('comfyui-frontend-package==1.55.18\n')
            with self.assertRaisesRegex(ValueError,'outside frozen wheel closure'):
                exec(compile(calls[0][0][4],'<frozen-requirements-validation>','exec'),{})
        with mock.patch.object(sys,'argv',['verify',str(requirements),str(selection)]), \
             mock.patch.object(sys,'version_info',(3,13,17)), \
             self.assertRaisesRegex(ValueError,'Windows CPython 3.12.10'):
            exec(compile(calls[0][0][4],'<frozen-requirements-validation>','exec'),{})

    def test_control_receives_only_python_and_its_provenance_without_wheel_reads(self):
        expected=set(self.value['python_files']) | set(self.source_names) | {'builder/scripts/build_local.py'}
        actual=app._backend_file;received=[]
        def read(root,name):
            received.append(name)
            self.assertFalse(name.startswith(('wheels/','ComfyUI/','tools/')))
            return actual(root,name)
        with mock.patch.object(app,'_backend_file',side_effect=read), \
                mock.patch.object(app.zipfile,'ZipFile',side_effect=AssertionError('control opened a wheel')):
            value,files=app._native_control_python_inputs(self.descriptor,self.digest)
        self.assertEqual(value,self.value)
        self.assertEqual(set(files),expected)
        self.assertEqual(set(received),expected | {self.descriptor.name})

    def test_control_subset_does_not_admit_corrupt_or_missing_full_backend_material(self):
        wheel=self.files[self.wheels[0]['file']]
        original=wheel.read_bytes()
        for missing in (False,True):
            if missing:wheel.unlink()
            else:wheel.write_bytes(b'corrupt unconsumed wheel')
            app._native_control_python_inputs(self.descriptor,self.digest)
            with self.subTest(missing=missing),self.assertRaises((ValueError,FileNotFoundError)):
                app.backend_inputs(self.descriptor,self.digest)
            wheel.write_bytes(original)
        with zipfile.ZipFile(wheel,'a') as archive:archive.writestr('unrecorded.py',b'changed payload')
        self.pin()
        app._native_control_python_inputs(self.descriptor,self.digest)
        with self.assertRaisesRegex(ValueError,'RECORD is not complete'):
            app.backend_inputs(self.descriptor,self.digest)

    def test_control_selected_bytes_and_complete_descriptor_remain_mandatory(self):
        for name in ['python/python.exe',*self.source_names,'builder/scripts/build_local.py']:
            path=self.files[name];raw=path.read_bytes();path.write_bytes(raw+b'changed')
            with self.subTest(name=name),self.assertRaisesRegex(ValueError,'differs from frozen closure'):
                app._native_control_python_inputs(self.descriptor,self.digest)
            path.write_bytes(raw)
        original=json.loads(json.dumps(self.value))
        for mutation in ('roster','duplicate','unsafe','provenance','version','bounds'):
            self.value=json.loads(json.dumps(original))
            if mutation=='roster':self.value['comfyui_files'].append('ComfyUI/missing.py')
            if mutation=='duplicate':self.value['files'].append(dict(self.value['files'][0]))
            if mutation=='unsafe':self.value['files'][0]['name']='builder/../escape'
            if mutation=='provenance':self.value['runtime_provenance']['python']['source']='sources/missing'
            if mutation=='version':self.value['wheels'][0]['distribution']='rocm-sdk-core'
            if mutation=='bounds':self.value['files'][0]['bytes']=app.backend_limits()['expanded_bytes']+1
            self.descriptor.write_text(json.dumps(self.value),encoding='utf-8')
            digest=hashlib.sha256(self.descriptor.read_bytes()).hexdigest()
            with self.subTest(mutation=mutation),mock.patch.object(app,'_receive_backend_files') as receive, self.assertRaises(ValueError):
                app._native_control_python_inputs(self.descriptor,digest)
            receive.assert_not_called()
        self.value=original;self.pin()

    def test_env_build_refuses_torch_and_rocm_wheels(self):
        original = json.loads(json.dumps(self.value))
        with mock.patch.object(app.subprocess, 'run') as child:
            for dist in ('torch', 'torchvision', 'torchaudio', 'amd-torch-device-gfx1151',
                         'rocm', 'rocm-sdk-core', 'rocm_sdk_device_gfx1100'):
                self.value = json.loads(json.dumps(original))
                self.value['wheels'][1]['distribution'] = dist
                self.descriptor.write_text(json.dumps(self.value), encoding='utf-8')
                digest = hashlib.sha256(self.descriptor.read_bytes()).hexdigest()
                for receive in (app.backend_inputs, app._native_control_python_inputs):
                    with self.subTest(dist=dist, receive=receive.__name__), \
                            self.assertRaisesRegex(ValueError, 'shared PyTorch engine component provides torch'):
                        receive(self.descriptor, digest)
            child.assert_not_called()
        self.value = original
        self.pin()
        app.backend_inputs(self.descriptor, self.digest)

    def test_missing_actual_torch_rocm_dependency_refuses_before_children(self):
        wheel = self.files[next(row['file'] for row in self.wheels if row['distribution']=='kornia')]
        with zipfile.ZipFile(wheel) as archive:
            members = {name: archive.read(name) for name in archive.namelist()}
        metadata = next(name for name in members if name.endswith('.dist-info/METADATA'))
        record = next(name for name in members if name.endswith('.dist-info/RECORD'))
        members[metadata] += b'Requires-Dist: rocm[libraries]==7.14.0\n'
        rows = []
        for name, raw in members.items():
            if name != record:
                digest = base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip('=')
                rows.append(f'{name},sha256={digest},{len(raw)}')
        members[record] = ('\n'.join(rows) + f'\n{record},,\n').encode()
        with zipfile.ZipFile(wheel,'w') as archive:
            for name, raw in members.items(): archive.writestr(name,raw)
        self.pin()
        with mock.patch.object(app.subprocess,'run') as child, self.assertRaisesRegex(ValueError,'rocm'):
            app.backend_inputs(self.descriptor,self.digest)
        child.assert_not_called()

    def test_source_selected_complete_closure_and_record_are_required_before_children(self):
        value, files = app.backend_inputs(self.descriptor, self.digest)
        self.assertEqual(value['wheels'], self.wheels)
        self.assertEqual(set(files), set(self.files))
        with mock.patch.object(app.subprocess, 'run') as child:
            for mutate in ('pin', 'manager', 'source', 'cpu', 'unknown'):
                original = json.loads(json.dumps(self.value))
                if mutate == 'pin': self.value['files'][0]['sha256'] = '0'*64
                if mutate == 'manager': self.value['wheels'][0]['distribution'] = 'comfyui-manager'
                if mutate == 'source': self.value['wheels'][0]['source'] = 'missing'
                if mutate == 'cpu': self.value['wheels'][0]['version'] = '2.12.0+cpu'
                if mutate == 'unknown': self.value['ready'] = True
                self.descriptor.write_text(json.dumps(self.value), encoding='utf-8')
                digest = hashlib.sha256(self.descriptor.read_bytes()).hexdigest()
                with self.subTest(mutate=mutate), self.assertRaises((ValueError, KeyError)):
                    app.backend_inputs(self.descriptor, digest)
                self.value = original
            child.assert_not_called()
        self.pin()
        wheel = self.files[self.wheels[0]['file']]
        with zipfile.ZipFile(wheel, 'a') as archive: archive.writestr('unrecorded.py', 'bad')
        self.pin()
        with self.assertRaisesRegex(ValueError, 'RECORD is not complete'):
            app.backend_inputs(self.descriptor, self.digest)

    def test_source_selected_python_and_bootstrap_pip_correction_refuse_old_cohorts(self):
        metadata=self.files['python/Lib/site-packages/pip-26.2.2.dist-info/METADATA']
        original=metadata.read_bytes()
        with mock.patch.object(app.subprocess,'run') as child:
            app.backend_inputs(self.descriptor,self.digest)
            for version in ('26.0.1','26.2.1','26.2.3'):
                metadata.write_bytes(f'Name: pip\nVersion: {version}\n'.encode());self.pin()
                with self.subTest(pip=version),self.assertRaisesRegex(ValueError,'source-selected security correction'):
                    app.backend_inputs(self.descriptor,self.digest)
            metadata.write_bytes(original);self.pin()
            self.value['runtime_provenance']['python']['version']='3.13.17';self.pin()
            with self.assertRaisesRegex(ValueError,'runtime versions differ'):
                app.backend_inputs(self.descriptor,self.digest)
            child.assert_not_called()

    def test_actual_recipe_stages_standalone_layout_gpu_integration_seal_and_external_receipt(self):
        self._actual_recipe_receiving(private=False)

    def test_private_developer_recipe_reaches_actual_finalizer_without_public_identity(self):
        self._actual_recipe_receiving(private=True)

    @unittest.skipUnless(os.name == 'nt', 'actual Windows deep-path filesystem')
    def test_private_recipe_uses_extended_local_io_and_plain_native_arguments(self):
        import contextlib
        owner = app.owned_temporary_tree
        @contextlib.contextmanager
        def deep_work(*, prefix, parent):
            with owner(prefix=prefix, parent=parent) as base:
                deep = app._extended(base) / ('deep-stage-'+'a'*90) / ('payload-'+'b'*90)
                deep.mkdir(parents=True)
                self.assertGreater(len(str(app._plain(deep))), 260)
                yield app._plain(deep)
        with mock.patch.object(app, 'owned_temporary_tree', side_effect=deep_work):
            self._actual_recipe_receiving(private=True)

    def _actual_recipe_receiving(self, *, private, reuse_wheels=True, guard_phase=None, mutate_source=False,
                                 minimum_free_after_build_bytes=20<<30):
        out = self.root/'output'
        integration = self.root/'integration.zip'
        with zipfile.ZipFile(integration,'w') as archive:
            archive.writestr('fixture.txt',b'fixture integration')
            archive.writestr('manifest.json',json.dumps(dict(source_mode='public-split')))
        integration_size,integration_sha=app._sha256(integration)
        wheel_receipt=self.root/'integration-wheels.json';wheel_receipt.write_bytes(b'{}')
        producer_receipt=self.root/'integration-producer.json'
        producer_receipt.write_text(json.dumps(dict(schema=1,candidate_source='a'*40,run_id=4,run_attempt=2,
            integration=dict(name=integration.name,bytes=integration_size,sha256=integration_sha),
            wheel_receipt_sha256=app._sha256(wheel_receipt)[1])))
        integration_options=dict(producer_receipt_path=producer_receipt,wheel_receipt_path=wheel_receipt,
            expected_redist_source_commit='a'*40,expected_run_id=4,expected_run_attempt=2)
        context=dict(source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64)
        actual_freeze=app._freeze_backend_integration
        def freeze(*args,**kwargs):
            if not private:return actual_freeze(*args,**kwargs)
            self.assertEqual(kwargs['private_context'],context)
            self.assertEqual((kwargs['source'],kwargs['run_id'],kwargs['run_attempt']),(None,None,None))
            self.assertEqual(args[2],{})
            return integration,{'private_context':context},{'source_mode':'private-diagnostic'},8
        def capture(checkout,project,commit,work,**kwargs):
            self.assertIn(commit,(context['source_commit'],context['native_source_commit']))
            path=work/(project+'.zip')
            with zipfile.ZipFile(path,'w') as archive:archive.writestr(project+'/source.commit',commit)
            return path
        launcher_source=self.root/'source-owned-launcher.py'
        launcher_source.write_bytes(app.LAUNCHER_SOURCE.read_bytes())
        shutil.copyfile(app.LAUNCHER_SOURCE.with_name('oa_interpreter.py'),launcher_source.with_name('oa_interpreter.py'))
        observed = {}
        events=[]
        actual_release=app._release_backend_phase
        actual_source=app._write_source_plan
        actual_capacity=app._backend_capacity
        actual_launcher=app._check_launcher
        def release(work,names):
            events.append(names)
            actual_release(work,names)
        def source(path,plan):
            self.assertFalse((path.parent/'extract').exists())
            self.assertFalse((path.parent/'inputs').exists())
            self.assertTrue(all(not isinstance(row[3],Path) or row[3].is_file() for row in plan['members']))
            events.append('source')
            return actual_source(path,plan)
        def capacity(*args,**kwargs):
            if kwargs.get('phase')==guard_phase and guard_phase is not None:return 200<<30
            return actual_capacity(*args,**kwargs)
        def launcher(path,inputs):
            self.assertTrue(path.is_dir())
            if 'pack' in events:
                self.assertFalse((path.parent.parent.parent/'inputs').exists())
                events.append('launcher')
            else:
                self.assertTrue((path.parent.parent.parent/'inputs').exists())
                events.append('launcher_stage')
            return actual_launcher(path,inputs)
        original_copy = shutil.copyfile
        wheel_copies = []
        def copy(source, target, *args, **kwargs):
            if Path(source).suffix == '.whl': wheel_copies.append((Path(source), Path(target)))
            return original_copy(source, target, *args, **kwargs)
        tool, tool_policy = archive_tool_fixture(self.root)
        original_builder = app._backend_builder
        def builder(source, work):
            result = original_builder(source, work)
            def install(stage, variant, vendor, lock, wheels, python, env):
                events.append('install')
                self.assertEqual(stage, app._plain(stage))
                self.assertEqual(lock, app._plain(lock))
                self.assertEqual(wheels, app._plain(wheels))
                self.assertNotEqual(python, app._plain(python))
                self.assertTrue(app.same_existing_file(python, stage/'python_embeded/python.exe'))
                self.assertTrue(all(value == str(app._plain(value)) for value in env.values()))
                self.assertFalse((app._extended(stage)/'ComfyUI/manager_requirements.txt').exists())
                self.assertIn('kornia==0.8.3',app._extended(lock).read_text())
                self.assertNotIn('torch',app._extended(lock).read_text().replace('kornia',''))
                expected = {Path(wheel['file']).name: next(row['sha256'] for row in self.value['files']
                            if row['name'] == wheel['file']) for wheel in self.value['wheels']}
                self.assertEqual({path.name: app._sha256(path)[1] for path in app._extended(wheels).glob('*.whl')}, expected)
                self.assertEqual(wheels.parent.name == 'inputs', reuse_wheels)
                self.assertEqual({line.rsplit(':',1)[1] for line in app._extended(lock).read_text().splitlines()}, set(expected.values()))
                observed['install'] = True
            result['install_requirements'] = install
            return result
        def integration_apply(stage, *args, **kwargs):
            self.assertNotEqual(kwargs['python_executable'], app._plain(kwargs['python_executable']))
            self.assertTrue(app.same_existing_file(kwargs['python_executable'],stage/'python_embeded/python.exe'))
            from scripts import package_comfyui_integration as integration_owner
            source_root=Path(__file__).resolve().parents[1]
            owner=integration_owner.extension_payload_owner(source_root/'installer/apps/build_extension.py')
            repositories={path.name:path for path in (source_root/'openabstractions-flat').iterdir() if path.is_dir()}
            node=stage/'ComfyUI/custom_nodes/abstraction_downloads'
            pins=integration_owner.compose_wrapper(owner,source_root/'adopters/comfyui/extension.json',repositories,node)
            self.assertTrue(kwargs['require_wrapper'])
            self.assertEqual(kwargs['require_public'],not private)
            if private:self.assertEqual(kwargs['private_context'],context)
            app._verify_integration_wrapper(node,pins)
            return {'manifest': {'source_mode': 'fixture'},'license':b'fixture licence','notice':b'fixture notice'}
        def pack(stage, archive, selected_tool):
            self.assertFalse((stage.parent.parent/'inputs').exists())
            self.assertFalse((stage.parent.parent/'wheels').exists())
            events.append('pack')
            app._archive_tool_inputs(selected_tool)
            observed['tree'] = {path.relative_to(stage).as_posix(): path.read_bytes()
                                for path in stage.rglob('*') if path.is_file()}
            archive.write_bytes(b'7z\xbc\xaf\x27\x1c opaque stub, no real archive claim')
            if mutate_source:self.files[self.source_names[0]].write_bytes(b'changed canonical source')
        def record_consumer(*args,**kwargs):
            self.assertTrue(all(path.is_file() for path in args[-1]))
            events.append('records')
        with mock.patch.object(app.shutil, 'copyfile', side_effect=copy), \
             mock.patch.object(app,'_release_backend_phase',side_effect=release), \
             mock.patch.object(app,'_write_source_plan',side_effect=source), \
             mock.patch.object(app,'_backend_capacity',side_effect=capacity), \
             mock.patch.object(app,'_check_launcher',side_effect=launcher), \
             mock.patch.object(app, '_backend_builder', side_effect=builder), \
             mock.patch.object(app, 'BACKEND_POLICY', tool_policy), \
             mock.patch.object(app, '_pack_backend_archive', side_effect=pack), \
             mock.patch.object(app, 'LAUNCHER_SOURCE', launcher_source), \
             mock.patch.object(app, '_pip_check', return_value=set()), \
             mock.patch.object(app, '_apply_integration', side_effect=integration_apply), \
             mock.patch.object(app, '_discard_backend_entrypoints', return_value=[]), \
             mock.patch.object(app, '_fill_wheel_records', side_effect=record_consumer), \
             mock.patch.object(app, '_seal_private_python', return_value={'records_sha256': 'f'*64,
                 'removed_installers':[], 'sealed_bytecode_rows':0, 'entrypoint_change':None,
                 'launch_check':{'files':0,'hashed':0},
                 'required_startup':'-S; OA launch check before site-packages activation'}), \
             mock.patch.object(app, '_public_source_checkouts', return_value=[]), \
             mock.patch.object(app, '_freeze_backend_integration', side_effect=freeze), \
             mock.patch.object(app, '_capture_git_source', side_effect=capture), \
             mock.patch.object(app.shutil, 'disk_usage', return_value=shutil._ntuple_diskusage(100<<30,0,100<<30)), \
             mock.patch.object(app, '_convert_cpu') as cpu, mock.patch.object(app.subprocess, 'run') as child:
            result=app.build_backend(self.descriptor,self.digest,out/'backend.7z',out/'backend-source.zip',out/'backend-producer.json',
                candidate_source=None if private else 'a'*40,run_id=None if private else 4,run_attempt=None if private else 2,
                integration_archive=integration,integration_options={} if private else integration_options,
                expected_integration_size=integration_size,expected_integration_sha256=integration_sha,archive_tool=tool,
                minimum_free_after_build_bytes=minimum_free_after_build_bytes,
                **({'private_context':context} if private else {}))
            child.assert_not_called();cpu.assert_not_called()
        self.assertEqual(events,['install','records','launcher_stage',('inputs','wheels'),'pack','launcher',('extract',),'source'])
        self.assertEqual(len(wheel_copies), len(self.wheels) * (1 if reuse_wheels else 2))
        self.assertTrue(observed['install'])
        self.assertEqual(result['input_descriptor']['sha256'],self.digest)
        if private:
            self.assertEqual(result['source_mode'],'private-diagnostic')
            self.assertEqual(result['private_provenance'],context)
            self.assertFalse({'candidate_source','run_id','run_attempt'} & set(result))
        else:self.assertEqual(result['candidate_source'],'a'*40)
        tree = observed['tree']
        self.assertIn('standalone-env/python.exe', tree)
        self.assertIn('ComfyUI/custom_nodes/abstraction_downloads/__init__.py', tree)
        self.assertNotIn('ComfyUI/manager_requirements.txt', tree)
        self.assertNotIn('standalone-env/python312._pth', tree)
        self.assertNotIn('backend-producer.json', tree)
        self.assertFalse(json.loads(tree['manifest.json'])['manager'])
        manifest = json.loads(tree['manifest.json'])
        self.assertEqual(manifest['python_version'],'3.12.10')
        self.assertEqual(manifest['pbs_release'],'20250409')
        self.assertEqual((manifest['torch_version'],manifest['torchvision_version'],manifest['torchaudio_version']),
                         ('2.11.0+rocm7.13.0','0.26.0+rocm7.13.0',''))
        self.assertEqual(tree[app.SHARED_TORCH_PTH],app.SHARED_TORCH_PTH_TEMPLATE)
        self.assertEqual(manifest['pytorch_component_config'],dict(name=app.SHARED_TORCH_PTH,
            release='torch-2.11.0+rocm7.13.0 cpython-3.12.10 gfx1151',target_family='gfx1151',
            template=dict(bytes=len(app.SHARED_TORCH_PTH_TEMPLATE),sha256=hashlib.sha256(app.SHARED_TORCH_PTH_TEMPLATE).hexdigest())))
        if private:
            self.assertNotIn('private_provenance',json.loads(tree['manifest.json']))
            self.assertNotIn('source_mode',json.loads(tree['manifest.json']))
        runtime=json.loads(tree['manifest.json'])['runtime_files']
        self.assertEqual(set(runtime),set(tree)-{'manifest.json'})
        for name,record in runtime.items():
            self.assertEqual(record,dict(bytes=len(tree[name]),sha256=hashlib.sha256(tree[name]).hexdigest()))
        helper=tree['ComfyUI/oa_interpreter.py']
        self.assertEqual(helper,launcher_source.with_name('oa_interpreter.py').read_bytes())
        helper_pin=json.loads(tree['manifest.json'])['oa_launcher']['interpreter']
        self.assertEqual(helper_pin,dict(name='ComfyUI/oa_interpreter.py',bytes=len(helper),sha256=hashlib.sha256(helper).hexdigest()))
        atomic=tree['ComfyUI/oa_atomic/abstraction_cas.py']
        self.assertEqual(atomic,app._launcher_inputs()['oa_atomic/abstraction_cas.py'])
        self.assertEqual(runtime['ComfyUI/oa_atomic/abstraction_cas.py'],
                         dict(bytes=len(atomic),sha256=hashlib.sha256(atomic).hexdigest()))
        self.assertEqual(set(json.loads(tree['manifest.json'])['oa_launcher']),{'name','bytes','sha256','interpreter'})
        with zipfile.ZipFile(out/'backend-source.zip') as archive:
            self.assertEqual(archive.read('OA-Backend-Source/oa_interpreter.py'),helper)
            self.assertEqual(archive.read('OA-Backend-Source/oa_atomic/abstraction_cas.py'),atomic)
            self.assertIn('OA-Backend-Source/archive-tool/7z2605-src.tar.xz', archive.namelist())
            self.assertIn('OA-Backend-Source/archive-tool/notices/DOC/License.txt', archive.namelist())
            self.assertNotIn('OA-Backend-Source/archive-tool/7zr.exe', archive.namelist())
            self.assertEqual(json.loads(archive.read('backend-source.json'))['schema'], 1)
        self.assertEqual(result['schema'], 3)
        for key in ('archive','source_pair'):
            path=out/result[key]['name']
            self.assertEqual(result[key]['sha256'],hashlib.sha256(path.read_bytes()).hexdigest())

        return tree["manifest.json"], result, tree

    def _phase_refusal(self,phase):
        with self.assertRaisesRegex(ValueError,'backend '+phase+' phase'):
            self._actual_recipe_receiving(private=False,guard_phase=phase)
        self.assertEqual(list((self.root/'output').iterdir()),[])

    def test_pack_phase_refresh_refuses_before_archive(self):self._phase_refusal('pack')
    def test_source_phase_refresh_refuses_before_source_write(self):self._phase_refusal('source')
    def test_finalize_phase_refresh_refuses_before_parts(self):self._phase_refusal('finalize')

    def test_canonical_source_mutation_after_snapshot_release_still_refuses(self):
        with self.assertRaisesRegex(ValueError,'source member differs'):
            self._actual_recipe_receiving(private=False,mutate_source=True)
        self.assertEqual(list((self.root/'output').iterdir()),[])

    def test_whole_python_drops_only_owned_wheel_declared_console_tools(self):
        work=self.root/'work';portable=work/'extract/backend'
        site=portable/'python_embeded/Lib/site-packages';info='torch-fixture.dist-info'
        (site/info).mkdir(parents=True);scripts=portable/'python_embeded/Scripts';scripts.mkdir()
        script=scripts/'torchrun.exe';raw=b'MZ fixture generated launcher';script.write_bytes(raw)
        digest='sha256='+base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip('=')
        record=site/info/'RECORD';record.write_text(f'../../Scripts/torchrun.exe,{digest},{len(raw)}\n{info}/RECORD,,\n')
        wheel=self.root/'entrypoints.whl'
        with zipfile.ZipFile(wheel,'w') as archive:
            archive.writestr(info+'/METADATA','Name: torch\nVersion: fixture\n')
            archive.writestr(info+'/entry_points.txt','[console_scripts]\ntorchrun = torch.distributed.run:main\n')
        self.assertEqual(app._discard_backend_entrypoints(portable,work,{wheel:hashlib.sha256(wheel.read_bytes()).hexdigest()}),['torchrun.exe'])
        self.assertFalse(script.exists());self.assertNotIn('../../Scripts',record.read_text())
        record.write_text('../../../unreviewed-data,,\n')
        with self.assertRaisesRegex(ValueError,'non-site runtime data'):
            app._discard_backend_entrypoints(portable,work,{wheel:hashlib.sha256(wheel.read_bytes()).hexdigest()})

    def test_public_source_projection_carries_actual_policy_and_patch_loader_paths(self):
        root=Path(app.__file__).resolve().parents[1]
        manifest=private_manifest(self,root)
        projected=self.root/'public'
        for source in (app.BACKEND_POLICY,app.BACKEND_POLICY.with_suffix('.patch')):
            relative=source.relative_to(root).as_posix()
            self.assertIn('\tfile '+relative+' '+relative,manifest.splitlines())
            target=projected/relative;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(source.read_bytes())
        public_script=projected/'scripts/package_comfyui_app.py'
        self.assertEqual(public_script.parent.parent/'adopters/comfyui/upstream/backend-amd-v1.json',projected/app.BACKEND_POLICY.relative_to(root))
        policy=json.loads((projected/app.BACKEND_POLICY.relative_to(root)).read_bytes())
        self.assertEqual(app.app_pair_receipt.validate_data_release_policy(policy.get('data_release')),
            {'repository':'openabstractions/redist','tag_prefix':'comfyui-data-win-amd-'})
        self.assertEqual(policy['patch_sha256'],hashlib.sha256((projected/app.BACKEND_POLICY.with_suffix('.patch').relative_to(root)).read_bytes()).hexdigest())

    def test_limits_links_and_unselected_input_pin_refuse_without_lifecycle(self):
        with mock.patch.object(app.subprocess,'run') as child:
            with self.assertRaisesRegex(ValueError,'selected source'):
                app.backend_inputs(self.descriptor,'0'*64)
            original=Path.lstat
            selected=self.files['tools/uv.exe']
            class Linked:
                st_mode=0o120777
                st_file_attributes=0
            with mock.patch.object(Path,'lstat',lambda path:Linked() if path==selected else original(path)):
                with self.assertRaisesRegex(ValueError,'linked backend'):
                    app.backend_inputs(self.descriptor,self.digest)
            with mock.patch.object(app,'backend_limits',return_value=dict(app.backend_limits(),expanded_bytes=1)):
                with self.assertRaisesRegex(ValueError,'expanded-byte'):
                    app.backend_inputs(self.descriptor,self.digest)
            child.assert_not_called()

    def test_outer_distribution_metadata_ignores_vendored_dist_info(self):
        archive_path=self.root/'nested.whl'
        with zipfile.ZipFile(archive_path,'w') as archive:
            archive.writestr('setuptools-77.0.3.dist-info/METADATA','Name: setuptools\nVersion: 77.0.3\n')
            archive.writestr('setuptools/_vendor/packaging-24.2.dist-info/METADATA','Name: packaging\nVersion: 24.2\n')
            archive.writestr('setuptools/_vendor/packaging-24.2.dist-info/RECORD','vendored record')
        with zipfile.ZipFile(archive_path) as archive:
            self.assertEqual(app._outer_wheel_metadata(archive),'setuptools-77.0.3.dist-info/METADATA')
        with zipfile.ZipFile(archive_path,'a') as archive:
            archive.writestr('foreign-1.dist-info/METADATA','Name: foreign\nVersion: 1\n')
        with zipfile.ZipFile(archive_path) as archive:
            with self.assertRaises(ValueError):app._outer_wheel_metadata(archive)

    def test_actual_input_receiver_uses_outer_metadata_and_record_with_vendored_distribution(self):
        selected=self.wheels[0];wheel=self.files[selected['file']]
        with zipfile.ZipFile(wheel) as archive:members={name:archive.read(name) for name in archive.namelist()}
        outer=next(name for name in members if name.endswith('.dist-info/RECORD'))
        members['torch/_vendor/packaging-24.2.dist-info/METADATA']=b'Name: packaging\nVersion: 24.2\n'
        members['torch/_vendor/packaging-24.2.dist-info/RECORD']=b'vendored metadata record\n'
        rows=[]
        for name,raw in members.items():
            if name==outer:continue
            encoded=base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip('=')
            rows.append(f'{name},sha256={encoded},{len(raw)}')
        members[outer]=('\n'.join(rows)+f'\n{outer},,\n').encode()
        with zipfile.ZipFile(wheel,'w') as archive:
            for name,raw in members.items():archive.writestr(name,raw)
        self.pin()
        with mock.patch.object(app.subprocess,'run') as child:
            observed,_=app.backend_inputs(self.descriptor,self.digest)
            self.assertEqual(observed['wheels'][0]['distribution'],selected['distribution'])
            child.assert_not_called()

    def test_actual_input_receiver_preserves_genuine_empty_python_module_bytes(self):
        name='python/Lib/email/mime/__init__.py';self.put(name,b'');self.value['python_files'].append(name);self.pin()
        with mock.patch.object(app.subprocess,'run') as child:
            observed,files=app.backend_inputs(self.descriptor,self.digest)
            self.assertIn(name,observed['python_files']);self.assertEqual(files[name].read_bytes(),b'')
            self.assertEqual(next(row for row in observed['files'] if row['name']==name)['sha256'],hashlib.sha256(b'').hexdigest())
            changed=json.loads(self.descriptor.read_bytes())
            next(row for row in changed['files'] if row['name']==name)['sha256']='0'*64
            self.descriptor.write_text(json.dumps(changed));digest=hashlib.sha256(self.descriptor.read_bytes()).hexdigest()
            with self.assertRaisesRegex(ValueError,'differs from frozen closure'):app.backend_inputs(self.descriptor,digest)
            child.assert_not_called()

    def _nest_wheels(self):
        for wheel in self.wheels:
            old = wheel['file']; new = 'wheels/nested/' + Path(old).name
            self.put(new, self.files.pop(old).read_bytes()); wheel['file'] = new
        self.pin()

    def test_nested_wheels_keep_separate_authenticated_wheelhouse(self):
        self._nest_wheels()
        self.assertFalse(app._backend_reuses_snapshot_wheels(*app.backend_inputs(self.descriptor,self.digest)))
        self._actual_recipe_receiving(private=True, reuse_wheels=False)

    def test_changed_private_snapshot_refuses_before_installer(self):
        receive = app.backend_inputs
        def changed(descriptor, digest):
            if descriptor.parent.name == 'inputs':
                wheel = descriptor.parent / self.wheels[0]['file']
                wheel.write_bytes(wheel.read_bytes() + b'changed')
            return receive(descriptor, digest)
        with mock.patch.object(app,'backend_inputs',side_effect=changed), \
             mock.patch.object(app.subprocess,'run') as child:
            with self.assertRaisesRegex(ValueError,'differs from frozen closure'):
                self._actual_recipe_receiving(private=True)
            child.assert_not_called()

    def test_snapshot_wheel_reuse_requires_complete_canonical_roster(self):
        self.assertTrue(app._backend_reuses_snapshot_wheels(*app.backend_inputs(self.descriptor,self.digest)))
        self.assertFalse(app._backend_reuses_snapshot_wheels(self.value,dict(self.files, **{'wheels/extra.whl':self.descriptor})))
        self.assertFalse(app._backend_reuses_snapshot_wheels(dict(self.value,wheels=[]),self.files))

    def test_recipe_capacity_is_independent_of_generic_legacy_limit_and_refuses_policy_drift(self):
        self.assertEqual(app.MAX_EXPANDED_BYTES,8<<30)
        self.assertEqual(app.backend_limits()['expanded_bytes'],16<<30)
        raw=json.loads(app.BACKEND_POLICY.read_bytes());raw['limits']['expanded_bytes']+=1
        source=self.root/'unreviewed-policy.json';source.write_text(json.dumps(raw))
        with mock.patch.object(app,'BACKEND_POLICY',source):
            with self.assertRaises(ValueError):app.backend_limits()

    def test_integration_preflight_preserves_reserve_before_any_work(self):
        integration=self.root/'budget-integration.zip'
        with zipfile.ZipFile(integration,'w') as archive:archive.writestr('fixture',b'x')
        tool,policy=archive_tool_fixture(self.root)
        with mock.patch.object(app,'BACKEND_POLICY',policy), \
             mock.patch.object(app.shutil,'disk_usage',return_value=shutil._ntuple_diskusage(20<<30,0,20<<30)), \
             mock.patch.object(app,'owned_temporary_tree') as work, mock.patch.object(app.subprocess,'run') as child:
            with self.assertRaisesRegex(ValueError,'insufficient free space'):
                app.build_backend(self.descriptor,self.digest,self.root/'budget/backend.7z',self.root/'budget/backend-source.zip',
                    self.root/'budget/backend-producer.json',candidate_source='a'*40,run_id=1,run_attempt=1,
                    integration_archive=integration,integration_options={},archive_tool=tool,
                    expected_integration_size=integration.stat().st_size,expected_integration_sha256=app._sha256(integration)[1])
            work.assert_not_called();child.assert_not_called()

    def test_public_backend_disposable_floor_preserves_installation_reserve(self):
        self._actual_recipe_receiving(private=False,minimum_free_after_build_bytes=1<<30)
        self.assertEqual(app.backend_limits()['reserve_bytes'],20<<30)
        with zipfile.ZipFile(self.root/'output/backend-source.zip') as source:
            self.assertEqual(json.loads(source.read('backend-amd-v1.json'))['limits']['reserve_bytes'],20<<30)

    def test_private_backend_cannot_claim_disposable_floor_from_actions_environment(self):
        with mock.patch.dict(os.environ,{'GITHUB_ACTIONS':'true','GITHUB_RUN_ID':'4','GITHUB_RUN_ATTEMPT':'2'}):
            with self.assertRaisesRegex(ValueError,'20 GiB'):
                self._actual_recipe_receiving(private=True,minimum_free_after_build_bytes=1<<30)

    def test_disposable_floor_requires_complete_explicit_source_run_context(self):
        with mock.patch.dict(os.environ,{'GITHUB_ACTIONS':'true','GITHUB_RUN_ID':'4','GITHUB_RUN_ATTEMPT':'2'}):
            self.assertEqual(app.backend_producer_floor(),20<<30)
            self.assertEqual(app.backend_producer_floor('a'*40,4,2),1<<30)
            for source,run,attempt in [('a'*40,None,2),(None,4,2),('a'*40,True,2),('a'*40,4,0),('bad',4,2)]:
                with self.subTest(source=source,run=run,attempt=attempt),self.assertRaisesRegex(ValueError,'actual source/run/attempt'):
                    app.backend_producer_floor(source,run,attempt)

    def test_full_source_plan_boundary_refuses_before_heavy_copies_and_accepts_exact_remaining_budget(self):
        self._capacity_boundary()

    def test_nested_wheel_capacity_keeps_full_additional_copy_budget(self):
        self._nest_wheels()
        self._capacity_boundary()

    def _capacity_boundary(self):
        integration=self.root/'integration-budget.zip';integration.write_bytes(b'zip')
        receipt=self.root/'budget-receipt.json';receipt.write_bytes(b'{}')
        tool,policy=archive_tool_fixture(self.root)
        options=dict(producer_receipt_path=receipt,wheel_receipt_path=receipt)
        plan=dict(bound=10000,temporary_bytes=17)
        input_bytes=sum(p.stat().st_size for p in self.files.values())+self.descriptor.stat().st_size
        wheel_bytes=(0 if app._backend_reuses_snapshot_wheels(self.value,self.files) else
                     sum(self.files[w['file']].stat().st_size for w in self.value['wheels']))
        with mock.patch.object(app,'BACKEND_POLICY',policy):
            required=app._backend_capacity(input_bytes,wheel_bytes,plan,3,8,
                sum(p.stat().st_size for p in app._archive_tool_inputs(tool).values()),20<<30)
            remaining=required-17-3-4
            for delta in (-1,0):
                free=iter([100<<30,remaining+delta])
                with self.subTest(delta=delta), \
                     mock.patch.object(app,'_freeze_backend_integration',return_value=(integration,options,{},8)), \
                     mock.patch.object(app,'_backend_source_plan',return_value=plan), \
                     mock.patch.object(app.shutil,'disk_usage',side_effect=lambda p:shutil._ntuple_diskusage(100<<30,0,next(free))), \
                     mock.patch.object(app.shutil,'copyfile',side_effect=RuntimeError('admitted heavy copy boundary')) as copy, \
                     mock.patch.object(app,'_backend_builder') as builder:
                    error,reason=(ValueError,'insufficient free space') if delta<0 else (RuntimeError,'admitted heavy copy boundary')
                    with self.assertRaisesRegex(error,reason):
                        app.build_backend(self.descriptor,self.digest,self.root/'boundary/backend.7z',
                            self.root/'boundary/source.zip',self.root/'boundary/producer.json',candidate_source='a'*40,
                            run_id=1,run_attempt=1,integration_archive=integration,integration_options=options,
                            archive_tool=tool,expected_integration_size=3,expected_integration_sha256='b'*64)
                    self.assertEqual(copy.call_count,0 if delta<0 else 1);builder.assert_not_called()

    def test_source_plan_prepares_each_public_zip_once_and_includes_every_metadata_member(self):
        tool,policy=archive_tool_fixture(self.root)
        def capture(checkout,project,commit,work,**kwargs):
            path=work/(project+'.zip')
            with zipfile.ZipFile(path,'w') as archive:archive.writestr(project+'/LICENSE',b'public source')
            return path
        selected=[('one',self.root,'a'*40),('two',self.root,'b'*40)]
        usage=shutil._ntuple_diskusage(100<<30,0,100<<30)
        with mock.patch.object(app,'BACKEND_POLICY',policy), \
             mock.patch.object(app,'_public_source_checkouts',return_value=selected), \
             mock.patch.object(app,'_capture_git_source',side_effect=capture) as child, \
             mock.patch.object(app.shutil,'disk_usage',return_value=usage):
            plan=app._backend_source_plan(self.value,self.files,self.descriptor,app._archive_tool_inputs(tool),
                b'fixed lock\n',{},None,self.root,20<<30,self.digest)
            self.assertEqual(child.call_count,2)
            self.assertEqual(plan['temporary_bytes'],sum(p.stat().st_size for p,_ in plan['archives']))
            app._write_source_plan(self.root/'complete-source.zip',plan)
            with zipfile.ZipFile(self.root/'complete-source.zip') as archive:
                self.assertEqual(set(archive.namelist()),{row[0] for row in plan['members']})
                self.assertEqual(archive.read('OA-Backend-Source/requirements.lock'),b'fixed lock\n')
                self.assertIn('one/LICENSE',archive.namelist());self.assertIn('two/LICENSE',archive.namelist())
                self.assertEqual(json.loads(archive.read('backend-source.json'))['builder_commit'],app.BACKEND_BUILDER_COMMIT)
            child.assert_called()
        with mock.patch.object(app,'BACKEND_POLICY',policy), \
             mock.patch.object(app,'_public_source_checkouts',return_value=selected), \
             mock.patch.object(app,'_capture_git_source') as child, \
             mock.patch.object(app.shutil,'disk_usage',return_value=shutil._ntuple_diskusage(1,0,(28<<30)-1)):
            with self.assertRaisesRegex(ValueError,'Git source preflight'):
                app._backend_source_plan(self.value,self.files,self.descriptor,app._archive_tool_inputs(tool),
                    b'fixed lock\n',{},None,self.root,20<<30,self.digest)
            child.assert_not_called()
        self.descriptor.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError,'descriptor changed'):
            app._backend_source_plan(self.value,self.files,self.descriptor,{},b'lock',{},None,self.root,20<<30,self.digest)

    def test_actual_cli_backend_mode_does_not_enter_legacy_repack(self):
        import sys
        argv=['package_comfyui_app.py','--backend-inputs',str(self.descriptor),
            '--backend-inputs-sha256',self.digest,'--backend-receipt-output',str(self.root/'receipt.json'),
            '--output',str(self.root/'backend.7z'),'--source-output',str(self.root/'backend-source.zip'),
            '--backend-archive-tool',str(self.root/'7zr.exe'),
            '--integration-archive',str(self.root/'integration.zip'),'--integration-redist-source-commit','a'*40,
            '--integration-run-id','4','--integration-run-attempt','2',
            '--integration-producer-receipt',str(self.root/'producer.json'),
            '--integration-wheel-receipt',str(self.root/'wheels.json'),
            '--integration-bytes','7','--integration-sha256','1'*64]
        with mock.patch.object(sys,'argv',argv),mock.patch.object(app,'build_backend',return_value={'schema':2}) as build, \
             mock.patch.object(app,'repack') as legacy,mock.patch('builtins.print'):
            self.assertEqual(app.main(),0)
            self.assertEqual(build.call_args.args[1],self.digest)
            self.assertEqual(build.call_args.kwargs['run_attempt'],2)
            self.assertEqual(build.call_args.kwargs['archive_tool'], self.root/'7zr.exe')
            self.assertEqual(build.call_args.kwargs['expected_integration_size'],7)
            self.assertEqual(build.call_args.kwargs['expected_integration_sha256'],'1'*64)
            legacy.assert_not_called()
        with mock.patch.object(sys,'argv',argv+['--cpu-wheelhouse','never']),mock.patch.object(app,'build_backend') as build:
            with self.assertRaises(SystemExit):app.main()
            build.assert_not_called()
        old_single_zip = list(argv)
        index = old_single_zip.index('--backend-archive-tool'); del old_single_zip[index:index+2]
        with mock.patch.object(sys,'argv',old_single_zip),mock.patch.object(app,'build_backend') as build, \
             mock.patch.object(app,'repack') as legacy:
            with self.assertRaises(SystemExit): app.main()
            build.assert_not_called(); legacy.assert_not_called()


# A tiny 7z made once from .build/comfy-repack-fixture/App/README.txt.
TINY_7Z = base64.b64decode(
    "N3q8ryccAASMcSTcpQAAAAAAAAAiAAAAAAAAAFWRCSsBAAt0aW55IGZpeHR1cmUAAACBMweuD87ysgwHyEN/QbH6/duIttdkiK3bp3H4FOvssaR7Cz9SXQbl799HM4peKavkn87pjivpBptb6HPSkZ9BYuuAbiGA8dg/fdVhlB901EBNhi3dBu6h7QougsT/sSsTcxXkfmT95XEwjZG7nVIIkJhMLY2PMGo/yB0TgWvO+8KGUhZI3urCAYqUsZJJRoZCNgAXBhABCYCVAAcLAQABIwMBAQVdABAAAAyA7goBS5uR4gAA"
)


class _Stage(unittest.TestCase):
    """Shared tiny archive and staged-tree fixtures."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
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


class BackendIntegrationSnapshotTests(unittest.TestCase):
    def test_consumed_integration_and_receipts_are_frozen_before_later_reads(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve(); work = root / 'work'; work.mkdir()
            archive = root / 'selected.zip'
            with zipfile.ZipFile(archive, 'w') as content:
                content.writestr('manifest.json', json.dumps({'source_mode':'public-split'}))
            original = archive.read_bytes(); size, digest = app._sha256(archive)
            wheels = root / 'wheels.json'; wheels.write_text('{}', encoding='utf-8')
            receipt = root / 'producer.json'
            receipt.write_text(json.dumps({'schema':1,'candidate_source':'a'*40,'run_id':4,'run_attempt':2,
                'integration':{'name':archive.name,'bytes':size,'sha256':digest},
                'wheel_receipt_sha256':app._sha256(wheels)[1]}), encoding='utf-8')
            options = dict(producer_receipt_path=receipt,wheel_receipt_path=wheels,
                expected_redist_source_commit='a'*40,expected_run_id=4,expected_run_attempt=2)
            snapshot, frozen, manifest, _ = app._freeze_backend_integration(
                work,archive,options,size,digest,source='a'*40,run_id=4,run_attempt=2)
            archive.write_bytes(b'x'*size); receipt.write_text('{}',encoding='utf-8'); wheels.write_text('[]',encoding='utf-8')
            self.assertEqual(snapshot.read_bytes(), original)
            self.assertEqual(manifest, {'source_mode':'public-split'})
            self.assertEqual(json.loads(frozen['producer_receipt_path'].read_bytes())['run_id'],4)
            self.assertEqual(frozen['wheel_receipt_path'].read_bytes(),b'{}')

    def test_changed_archive_and_private_diagnostic_refuse_before_integration_import(self):
        for mode, changed in (('public-split',True),('private-diagnostic',False)):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root=Path(temporary).resolve();work=root/'work';work.mkdir();archive=root/'selected.zip'
                with zipfile.ZipFile(archive,'w') as content:
                    content.writestr('manifest.json',json.dumps({'source_mode':mode}))
                size,digest=app._sha256(archive);wheels=root/'wheels.json';wheels.write_bytes(b'{}')
                receipt=root/'producer.json';receipt.write_text(json.dumps({'schema':1,
                    'candidate_source':'a'*40,'run_id':4,'run_attempt':2,
                    'integration':{'name':archive.name,'bytes':size,'sha256':digest},
                    'wheel_receipt_sha256':app._sha256(wheels)[1]}),encoding='utf-8')
                options=dict(producer_receipt_path=receipt,wheel_receipt_path=wheels,
                    expected_redist_source_commit='a'*40,expected_run_id=4,expected_run_attempt=2)
                if changed: archive.write_bytes(b'x'*size)
                with mock.patch.object(app,'_stage_backend_helper') as importer:
                    with self.assertRaises(ValueError):
                        app._freeze_backend_integration(work,archive,options,size,digest,
                            source='a'*40,run_id=4,run_attempt=2)
                    importer.assert_not_called()


class PrivateDeveloperBackendTests(_Stage):
    """Actual private receivers over inert wheel/ZIP data; no package activation."""
    def private_fixture(self, *, with_appmodel=False):
        owner=app._integration_packager_owner()
        source=Path(app.__file__).resolve().parents[1]
        node=self.root/'wrapper'
        pins=owner.stage_wrapper(source,source/'openabstractions-flat/adopter-comfyui',node)
        library=b'MZ inert native DLL fixture, never loaded'
        context=dict(source_commit='a'*40,native_source_commit='b'*40,
                     native_library_sha256=hashlib.sha256(library).hexdigest())
        members={f'node/abstraction_downloads/{name}':(node/name).read_bytes() for name in pins}
        members.update(LICENSE=b'OA fixture licence',NOTICE=b'OA fixture notice')
        wheels=[];lock=[]
        for distribution in sorted(app._integration_distributions(appmodel=with_appmodel)):
            native=distribution=='abstraction_ipc';component=distribution=='abstraction_facade_native'
            tag='win_amd64' if native or component else 'any'
            filename=f'{distribution}-0.0.0-py3-none-{tag}.whl';info=f'{distribution}-0.0.0.dist-info/'
            contents={info+'METADATA':f'Name: {distribution}\nVersion: 0.0.0\n'.encode(),
                      info+'WHEEL':f'Wheel-Version: 1.0\nTag: py3-none-{tag}\n'.encode(),
                      ('abstraction/ipc/_native/abstraction_ipc.dll' if native else
                       'abstraction_facade_native/_native/abstraction_facade_appmodel.dll' if component else
                       distribution+'/__init__.py'):library if native or component else b'# inert source\n'}
            rows=[f'{name},sha256={base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip("=")},{len(raw)}' for name,raw in contents.items()]
            contents[info+'RECORD']=('\n'.join(rows)+f'\n{info}RECORD,,\n').encode()
            buffer=io.BytesIO()
            with zipfile.ZipFile(buffer,'w') as wheel:
                for name,raw in contents.items():wheel.writestr(name,raw)
            raw=buffer.getvalue();members['wheels/'+filename]=raw;sha=hashlib.sha256(raw).hexdigest()
            entry=dict(filename=filename,size=len(raw),sha256=sha,
                       source_commit=context['native_source_commit'] if native else context['source_commit'])
            if native or component:entry['native_library_sha256']=context['native_library_sha256']
            if component:
                context.update(appmodel_library_sha256=context['native_library_sha256'],appmodel_wheel_sha256=sha)
            wheels.append(entry);lock.append(owner.wheel_requirement(entry,appmodel=with_appmodel))
        manifest=dict(source_mode='private-diagnostic',source_commit=context['source_commit'],wheels=wheels,wrapper_files=pins)
        members['manifest.json']=json.dumps(manifest).encode()
        members['requirements.lock']=('\n'.join(lock)+'\n').encode()
        archive=self.root/'private-integration.zip'
        with zipfile.ZipFile(archive,'w') as zipped:
            for name,raw in members.items():zipped.writestr(name,raw)
        return archive,context,owner








    def test_selected_appmodel_companion_uses_native_platform_and_exact_private_pins(self):
        archive,context,owner=self.private_fixture(with_appmodel=True)
        self.assertEqual(len(app._check_integration_consistency(archive)),14)
        target=self.root/'selected-component';target.mkdir()
        with zipfile.ZipFile(archive) as packaged:
            manifest=json.loads(packaged.read('manifest.json'))
            entry=next(row for row in manifest['wheels'] if row['filename'].startswith('abstraction_facade_native-'))
            wheel=target/entry['filename'];wheel.write_bytes(packaged.read('wheels/'+entry['filename']))
        with mock.patch('scripts.windows_native.validate_portable_windows_dll',return_value=()):
            app._verify_private_integration_wheel(wheel,entry,context)
            for changed in (dict(context,appmodel_library_sha256='f'*64),
                            dict(context,appmodel_wheel_sha256='e'*64),
                            dict(context,source_commit='d'*40)):
                with self.subTest(changed=changed),self.assertRaises(ValueError):
                    app._verify_private_integration_wheel(wheel,entry,changed)

    def test_private_appmodel_selection_requires_declared_dependency_closure(self):
        archive,context,owner=self.private_fixture(with_appmodel=True)
        with zipfile.ZipFile(archive) as packaged: wheels=json.loads(packaged.read('manifest.json'))['wheels']
        app._private_appmodel_selected(wheels,context)
        for selected,inputs in (([row for row in wheels if not row['filename'].startswith('abstraction_facade_native-')],context),
                                (wheels,{key:value for key,value in context.items() if not key.startswith('appmodel_')})):
            with self.assertRaisesRegex(ValueError,'closure'):app._private_appmodel_selected(selected,inputs)
        with self.assertRaises(ValueError):app._private_integration_context(dict(context,appmodel_wheel_sha256='bad'))

    def test_current_private_snapshot_and_apply_reuse_real_wheel_wrapper_receivers(self):
        archive,context,owner=self.private_fixture();size,sha=app._sha256(archive)
        work=self.root/'private-snapshot';work.mkdir()
        with mock.patch.object(owner,'matches_revision',return_value=True), \
             mock.patch.object(owner,'untracked_source_files',return_value=[]), \
             mock.patch.object(app,'_git_head',return_value=context['source_commit']), \
             mock.patch('scripts.windows_native.validate_portable_windows_dll',return_value=[]), \
             mock.patch.object(app,'_integration_packager_owner',return_value=owner):
            snapshot,options,manifest,_=app._freeze_backend_integration(work,archive,{},size,sha,
                source=None,run_id=None,run_attempt=None,private_context=context)
        archive.write_bytes(b'x'*size)
        self.assertEqual(app._sha256(snapshot),(size,sha))
        portable=self.root/'private-portable';(portable/'ComfyUI/custom_nodes').mkdir(parents=True)
        apply_work=self.root/'private-apply';apply_work.mkdir()
        with mock.patch('scripts.windows_native.validate_portable_windows_dll',return_value=[]), \
             mock.patch.object(app.subprocess,'run') as child:
            observed=app._apply_integration(portable,snapshot,apply_work,install_wheels=False,
                archive_size=size,archive_sha256=sha,require_wrapper=True,**options)
            child.assert_not_called()
        self.assertEqual(observed['source_commit'],context['source_commit'])
        self.assertEqual(observed['native_source_commit'],context['native_source_commit'])
        app._verify_integration_wrapper(portable/'ComfyUI/custom_nodes/abstraction_downloads',manifest['wrapper_files'])

    def test_private_pins_clean_source_and_public_identity_are_mandatory(self):
        archive,context,owner=self.private_fixture();size,sha=app._sha256(archive)
        for mode in ('source','native','dll','dirty','run','archive'):
            with self.subTest(mode=mode):
                work=self.root/mode;work.mkdir();selected=dict(context)
                if mode in ('source','native','dll'):
                    key={'source':'source_commit','native':'native_source_commit','dll':'native_library_sha256'}[mode]
                    selected[key]='c'*len(selected[key])
                with mock.patch.object(owner,'matches_revision',return_value=mode!='dirty'), \
                     mock.patch.object(owner,'untracked_source_files',return_value=[]), \
                     mock.patch.object(app,'_git_head',return_value=selected['source_commit']), \
                     mock.patch.object(app,'_integration_packager_owner',return_value=owner), \
                     mock.patch.object(app,'_stage_backend_helper') as importer:
                    with self.assertRaises(ValueError):
                        app._freeze_backend_integration(work,archive,{},size,'0'*64 if mode=='archive' else sha,
                            source=None,run_id=1 if mode=='run' else None,run_attempt=None,private_context=selected)
                    importer.assert_not_called()
        work=self.root/'legacy';work.mkdir();portable=self.root/'legacy-portable';portable.mkdir()
        with self.assertRaisesRegex(ValueError,'pinned private source'):
            app._apply_integration(portable,archive,work,install_wheels=False,archive_size=size,archive_sha256=sha)
        work=self.root/'public';work.mkdir()
        with self.assertRaisesRegex(ValueError,'selected public-split'):
            app._apply_integration(portable,archive,work,install_wheels=False,archive_size=size,archive_sha256=sha,require_public=True)

    def test_private_native_pin_checks_consumed_dll_bytes_not_manifest_agreement(self):
        archive,context,_=self.private_fixture()
        with zipfile.ZipFile(archive) as content:
            manifest=json.loads(content.read('manifest.json'))
            entry=next(row for row in manifest['wheels'] if row['filename'].startswith('abstraction_ipc-'))
            wheel=self.root/entry['filename'];wheel.write_bytes(content.read('wheels/'+entry['filename']))
        changed=dict(context,native_library_sha256='f'*64)
        changed_entry=dict(entry,native_library_sha256=changed['native_library_sha256'])
        with mock.patch('scripts.windows_native.validate_portable_windows_dll') as pe:
            with self.assertRaisesRegex(ValueError,'DLL differs'):
                app._verify_private_integration_wheel(wheel,changed_entry,changed)
            pe.assert_not_called()
        wheel.write_bytes(b'changed consumed wheel')
        with self.assertRaisesRegex(ValueError,'bytes differ'):
            app._verify_private_integration_wheel(wheel,entry,context)

    def test_private_finalizer_output_is_rejected_by_actual_public_intake(self):
        context=dict(source_commit='a'*40,native_source_commit='b'*40,native_library_sha256='c'*64)
        archive=self.root/'backend.7z';archive.write_bytes(b'opaque inert archive fixture')
        source=self.root/'backend-source.zip'
        with zipfile.ZipFile(source,'w') as zipped:zipped.writestr('LICENSE',b'fixture')
        out=self.root/'private-output';out.mkdir();pin=dict(name='backend-inputs.json',bytes=2,sha256='d'*64)
        result=app.finalize_backend_outputs(archive,source,out,'backend-developer.json',input_descriptor=pin,
            candidate_source=None,run_id=None,run_attempt=None,private_context=context)
        self.assertEqual(result['source_mode'],'private-diagnostic');self.assertEqual(result['private_provenance'],context)
        self.assertFalse({'candidate_source','run_id','run_attempt'} & set(result))
        receipt=out/'backend-developer.json';size,sha=app._sha256(receipt)
        with self.assertRaisesRegex(ValueError,'provenance differs'):
            app.app_pair_receipt.read_backend_producer(receipt,
                expected_receipt=dict(name=receipt.name,bytes=size,sha256=sha),expected_input=pin,
                candidate_source='a'*40,run_id=1,run_attempt=1)

    def test_explicit_cli_private_context_cannot_leak_into_public_default(self):
        import contextlib
        import sys
        base=['package_comfyui_app.py','--backend-inputs','input.json','--backend-inputs-sha256','a'*64,
              '--backend-receipt-output','developer.json','--backend-archive-tool',str(self.root/'7zr.exe'),
              '--output','backend.7z','--source-output','source.zip','--integration-archive','integration.zip',
              '--integration-bytes','123','--integration-sha256','b'*64]
        private=['--source-mode','private','--integration-source-commit','c'*40,
                 '--integration-native-source-commit','d'*40,'--integration-native-library-sha256','e'*64]
        with mock.patch.object(sys,'argv',base+private),mock.patch.object(app,'build_backend',return_value={}) as build,contextlib.redirect_stdout(io.StringIO()):
            app.main()
            kwargs=build.call_args.kwargs
            self.assertIsNone(kwargs['candidate_source']);self.assertIsNone(kwargs['run_id'])
            self.assertEqual(kwargs['integration_options'],{})
            self.assertEqual(kwargs['private_context']['native_library_sha256'],'e'*64)
        for argv in (base,base+private+['--integration-run-id','1'],base+private[2:]):
            with mock.patch.object(sys,'argv',argv),mock.patch.object(app,'build_backend') as build,contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):app.main()
                build.assert_not_called()


class RepackTests(_Stage):
    def test_disposable_runner_floor_keeps_same_expansion_and_asset_caps(self):
        workstation = app.required_free_bytes()
        runner = app.required_free_bytes(minimum_free_after_build_bytes=1 << 30)
        self.assertEqual(workstation - runner, 19 << 30)
        self.assertGreater(runner, 16 << 30)
        with self.assertRaisesRegex(ValueError, "at least one GiB"):
            app.required_free_bytes(minimum_free_after_build_bytes=0)

    @unittest.skipUnless(shutil.which("tar.exe"), "bsdtar.exe is required for the tiny 7z fixture")
    @fixture_free_space()
    def test_success_is_deterministic_and_preserves_payload(self):
        first, manifest = self.build()
        second, _ = self.build("again.zip")
        self.assertEqual(first.read_bytes(), second.read_bytes())
        self.assertEqual(manifest["file_count"], 1)
        with zipfile.ZipFile(first) as archive:
            self.assertEqual(archive.read(".build/comfy-repack-fixture/App/README.txt"), b"tiny fixture")
            self.assertIn(app.PROVENANCE, archive.namelist())
            self.assertNotIn(".oa-install.json", archive.namelist())

    @fixture_free_space()
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
    @fixture_free_space()
    def test_release_asset_limit_refuses_before_publication(self):
        with self.assertRaisesRegex(ValueError, "release asset size limit"):
            self.build(max_release_asset_bytes=1)
        self.assertFalse((self.root / "app.zip").exists())

    @unittest.skipUnless(shutil.which("tar.exe"), "bsdtar.exe is required for the tiny 7z fixture")
    @fixture_free_space()
    def test_expanded_limit_and_failed_extraction_clean_stage(self):
        with self.assertRaisesRegex(ValueError, "expanded-byte ceiling"):
            self.build(max_expanded_bytes=1)
        self.assertFalse(list(self.root.glob("oa-comfy-repack-*")))
        with self.assertRaises(FileNotFoundError):
            self.build(tar="this-tar-does-not-exist")
        self.assertFalse(list(self.root.glob("oa-comfy-repack-*")))
        self.assertFalse((self.root / "app.zip").exists())


class StageTests(_Stage):
    def test_private_python_routes_use_one_explicit_environment_before_each_child(self):
        self._private_python_routes_receiving(selected=False)

    @unittest.skipUnless(os.name == 'nt', 'selected Windows build Python role')
    def test_selected_python_pip_check_install_and_abi_keep_only_executable_extended(self):
        self._private_python_routes_receiving(selected=True)

    def _private_python_routes_receiving(self, *, selected):
        import contextlib
        from scripts.file_boundary import spawn_refusal
        integration=self._integration_fixture();size,digest=app._sha256(integration)
        portable=self.root/'private-runtime';(portable/'ComfyUI/custom_nodes').mkdir(parents=True)
        python=portable/'python_embeded/python.exe';python.parent.mkdir();python.write_bytes(b'owned inert Python')
        work=self.root/'private-work';work.mkdir()
        expected={'SystemRoot':'C:/ActualWindows','WINDIR':'C:/ActualWindows',
            'PATH':'C:/ActualWindows/System32','TEMP':str(work),'TMP':str(work)}
        injected={name:'foreign' for name in ('PATH','SystemRoot','WINDIR','PythonHome','PYTHONPATH',
            'PYTHONINSPECT','PYTHONSTARTUP','PYTHONWARNINGS','PIP_CONFIG_FILE','PIP_TARGET',
            'LD_PRELOAD','LD_LIBRARY_PATH','DYLD_INSERT_LIBRARIES','DYLD_LIBRARY_PATH')}
        calls=[];parent=dict(os.environ)
        def execute(argv,**options):
            calls.append(argv)
            self.assertEqual(argv[0],str(selected_python))
            self.assertTrue(all(arg == str(app._plain(arg)) for arg in argv[1:]))
            self.assertEqual(Path(options['cwd']),work)
            self.assertEqual(options['env'],dict(expected,PYTHONDONTWRITEBYTECODE='1',PYTHONNOUSERSITE='1',
                PIP_NO_CACHE_DIR='1',PIP_CONFIG_FILE=os.devnull,PIP_DISABLE_PIP_VERSION_CHECK='1'))
            self.assertNotIn('foreign',options['env'].values())
            self.assertIsNone(spawn_refusal('subprocess.Popen',
                (argv[0],argv,str(options['cwd']),options['env'])))
            return subprocess.CompletedProcess(argv,0,stdout='No broken requirements found.\n',stderr='')
        role = app._selected_python_executable(python,app._sha256(python),app._sha256) if selected else contextlib.nullcontext(python)
        with role as selected_python, mock.patch.dict(os.environ,injected), \
             mock.patch.object(app,'_windows_archive_environment',return_value=expected) as native, \
             mock.patch.object(app.subprocess,'run',side_effect=execute):
            env=app._private_python_env(work)
            self.assertEqual(app._pip_check(selected_python,env),set())
            app._apply_integration(portable,integration,work,install_wheels=True,archive_size=size,archive_sha256=digest,
                                   python_executable=selected_python if selected else None)
            self.assertEqual(native.call_args_list,[mock.call(work),mock.call(work)])
        self.assertEqual(len(calls),3)
        self.assertEqual(calls[0][1:],['-B','-s','-m','pip','check'])
        self.assertIn('--require-hashes',calls[1]);self.assertIn('--no-index',calls[1])
        self.assertIn('from abstraction.ipc import Cancellation, Library;',calls[2][-1])
        self.assertEqual(dict(os.environ),parent)

    def test_private_python_missing_temp_or_windows_query_refuses_before_child(self):
        with mock.patch.object(app.subprocess,'run') as child:
            with self.assertRaisesRegex(ValueError,'ordinary and absolute'):
                app._private_python_env(self.root/'absent')
            with mock.patch.object(app,'_windows_archive_environment',side_effect=ValueError('native query unavailable')):
                with self.assertRaisesRegex(ValueError,'native query unavailable'):
                    app._private_python_env(self.root)
            child.assert_not_called()

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

    def _sympy_data(self, work):
        portable = self._private_python(work)
        site = portable / "python_embeded/Lib/site-packages"
        raw = b".TH ISYMPY 1\nSymPy manual\n"
        relative = "../../share/man/man1/isympy.1"
        member = "sympy-1.14.0.data/data/share/man/man1/isympy.1"
        info = "sympy-1.14.0.dist-info"
        encoded = "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode().rstrip("=")
        target = site / relative
        target.parent.mkdir(parents=True)
        target.write_bytes(raw)
        (site / info).mkdir()
        record = site / info / "RECORD"
        record.write_text(f"{relative},{encoded},{len(raw)}\n{info}/RECORD,,\n")
        wheel = work / "sympy-1.14.0-py3-none-any.whl"
        with zipfile.ZipFile(wheel, "w") as archive:
            archive.writestr(member, raw)
            archive.writestr(info + "/METADATA", "Name: sympy\nVersion: 1.14.0\n")
            archive.writestr(info + "/RECORD", f"{member},{encoded},{len(raw)}\n{info}/RECORD,,\n")
        return portable, site, target, record, wheel

    def test_declared_sympy_data_survives_discard_fill_seal_and_whole_root_relocation(self):
        work = self.root / "sympy"
        portable, site, target, record, wheel = self._sympy_data(work)
        before = record.read_bytes()
        pins = {wheel: app._sha256(wheel)[1]}
        self.assertEqual(app._discard_backend_entrypoints(app._extended(portable), app._extended(work), pins), [])
        self.assertEqual(record.read_bytes(), before)
        app._fill_wheel_records(site, pins)
        app._seal_private_python(portable, work)
        raw = target.read_bytes()
        relocated = self.root / "moved/extract/relocated"
        relocated.parent.mkdir(parents=True)
        portable.rename(relocated)
        # The real inventory/check owner resolves site-relative RECORD rows
        # against the relocated complete Python root, without running Python.
        launcher = app._launcher()
        moved_root = relocated / "python_embeded"
        receipt = {"launch_check": launcher.check_environment(str(moved_root / "Lib/site-packages"), str(moved_root),
            records=launcher.records_digest(str(moved_root / "Lib/site-packages")))}
        self.assertGreater(receipt["launch_check"]["hashed"], 0)
        self.assertEqual((relocated / "python_embeded/share/man/man1/isympy.1").read_bytes(), raw)

    def test_sympy_data_tamper_link_escape_unknown_and_duplicate_refuse(self):
        for damage in ("tamper", "hash", "hardlink", "reparse", "escape", "unknown", "duplicate"):
            with self.subTest(damage=damage):
                work = self.root / damage
                portable, site, target, record, wheel = self._sympy_data(work)
                pins = {wheel: app._sha256(wheel)[1]}
                if damage == "reparse":
                    import stat, types
                    original = Path.lstat
                    def tagged(path, *args, **kwargs):
                        actual = original(path, *args, **kwargs)
                        return types.SimpleNamespace(st_mode=stat.S_IFDIR,st_file_attributes=stat.FILE_ATTRIBUTE_REPARSE_POINT) if path.name == "man1" else actual
                    with mock.patch.object(Path, "lstat", new=tagged), self.assertRaisesRegex(ValueError, "linked backend data"):
                        app._discard_backend_entrypoints(portable, work, pins)
                    continue
                if damage == "tamper":
                    target.write_bytes(b"foreign")
                elif damage == "hash":
                    record.write_text(record.read_text().replace("sha256=", "sha256=0"))
                elif damage == "hardlink":
                    os.link(target, work / "alias")
                elif damage in ("escape", "unknown"):
                    record.write_text(record.read_text().replace("../../share/man/man1/isympy.1",
                        "../../../../outside" if damage == "escape" else "../../share/unknown"))
                else:
                    second = work / "duplicate.whl"
                    shutil.copyfile(wheel, second)
                    pins[second] = app._sha256(second)[1]
                with self.assertRaises(ValueError):
                    app._discard_backend_entrypoints(portable, work, pins)

    def test_wheel_data_preflight_refuses_before_bulk_copy_and_target_dispatch(self):
        portable, site, target, record, wheel = self._sympy_data(self.root / "preflight")
        value = {"wheels": [{"file": "selected.whl"}]}
        files = {"selected.whl": wheel}
        app._backend_finalization_preflight(value, files)
        with zipfile.ZipFile(wheel, "a") as archive:
            archive.writestr("sympy-1.14.0.data/data/bin/unknown.exe", b"MZ")
        with mock.patch.object(app.shutil, "copyfile") as copy, mock.patch.object(app.subprocess, "run") as child:
            with self.assertRaisesRegex(ValueError, "selected.whl.*unknown.exe"):
                app._backend_finalization_preflight(value, files)
            copy.assert_not_called()
            child.assert_not_called()

    def test_runtime_manifest_cohort_budget_and_entry_cap(self):
        # Cohort-size model from 1377 metadata: 61,240 rows. Long names make
        # this >13 MiB; this is test DATA, never a final installed-tree receipt.
        files = {f"standalone-env/Lib/site-packages/cohort/{i:05d}/" + "x" * 95:
                 dict(bytes=0, sha256="0" * 64) for i in range(61240)}
        raw = app._backend_manifest_bytes({"runtime_files": files})
        self.assertGreater(len(raw), 13 << 20)
        self.assertLessEqual(len(raw), 16 << 20)
        with self.assertRaisesRegex(ValueError, "metadata size"):
            app._backend_manifest_bytes({"runtime_files": {"x": dict(bytes=0, sha256="0"*64)},
                                         "padding": "x" * (16 << 20)})
        with self.assertRaisesRegex(ValueError, "entry bound"):
            app._backend_manifest_bytes({"runtime_files": {str(i): {} for i in range(65537)}})

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

    def _bootstrap_python(self, work, *, present=False):
        """Small profile model; actual selected RECORD receiving is separate below."""
        portable=self._private_python(work)
        root=portable/'python_embeded';site=root/'Lib/site-packages'
        shutil.rmtree(site/'pip');shutil.rmtree(site/'pip-1.0.dist-info')
        shutil.rmtree(root/'Scripts')
        info=site/'pip-26.2.2.dist-info';info.mkdir()
        values={'pip/__init__.py':b'# bootstrap pip\n',
                info.name+'/METADATA':b'Name: pip\nVersion: 26.2.2\n',
                info.name+'/entry_points.txt':b'[console_scripts]\npip=pip._internal.cli.main:main\npip3=pip._internal.cli.main:main\n'}
        rows=[]
        for name,data in values.items():
            target=site/name;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(data)
            rows.append([name,'sha256='+base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip('='),str(len(data))])
        tools={}
        for name in app.BACKEND_BOOTSTRAP_LAUNCHERS:
            data=b'MZ owned bootstrap launcher model'
            pin=['sha256='+base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip('='),str(len(data))]
            tools[name]=pin;rows.append([name,*pin])
            if present:
                target=root/'Scripts'/name.rsplit('/',1)[1];target.parent.mkdir(exist_ok=True);target.write_bytes(data)
        rows.append([info.name+'/RECORD','',''])
        stream=io.StringIO();csv.writer(stream,lineterminator='\n').writerows(rows)
        raw=stream.getvalue().encode();(info/'RECORD').write_bytes(raw)
        return portable,tools,hashlib.sha256(raw).hexdigest()

    def test_selected_bootstrap_omissions_are_scoped_and_present_tools_are_removed(self):
        for present in (False,True):
            with self.subTest(present=present):
                work=self.root/('bootstrap-'+str(present));portable,tools,pin=self._bootstrap_python(work,present=present)
                if not present:
                    with self.assertRaisesRegex(ValueError,'missing or linked'):
                        app._seal_private_python(portable,work,embedded=False)
                with mock.patch.object(app,'BACKEND_BOOTSTRAP_RECORD_SHA256',pin),mock.patch.object(app,'BACKEND_BOOTSTRAP_LAUNCHERS',tools):
                    proof=app._seal_private_python(portable,work,embedded=False,backend_bootstrap=True)
                root=portable/'python_embeded';site=root/'Lib/site-packages'
                self.assertFalse(app._launcher().installer_presence(str(site),str(root)))
                self.assertTrue(proof['removed_installers']);self.assertFalse((root/'Scripts').exists())

    def test_bootstrap_refusals_collect_missing_siblings_and_preserve_files(self):
        work=self.root/'missing-siblings';portable,tools,pin=self._bootstrap_python(work)
        root=portable/'python_embeded';info=root/'Lib/site-packages/pip-26.2.2.dist-info'
        (root/'Lib/site-packages/pip/__init__.py').unlink();(info/'entry_points.txt').unlink()
        # The common removal reader names every missing leaf before any removal.
        with self.assertRaises(ValueError) as caught:
            app._remove_installers(portable,work)
        self.assertIn('pip/__init__.py',str(caught.exception));self.assertIn('entry_points.txt',str(caught.exception))
        self.assertTrue((info/'RECORD').exists())

    def test_bootstrap_pin_hash_links_and_shared_omissions_refuse(self):
        for damage in ('record','version','entrypoint','hash','hardlink','shared','escape','present-tool-hash'):
            with self.subTest(damage=damage):
                work=self.root/('damaged-'+damage);portable,tools,pin=self._bootstrap_python(work)
                root=portable/'python_embeded';site=root/'Lib/site-packages';info=site/'pip-26.2.2.dist-info'
                if damage=='record':(info/'RECORD').write_bytes((info/'RECORD').read_bytes()+b'unknown,,\n')
                elif damage=='version':(info/'METADATA').write_text('Name: pip\nVersion: 1\n')
                elif damage=='entrypoint':(info/'entry_points.txt').write_text('[console_scripts]\npip=other:main\n')
                elif damage=='hash':(site/'pip/__init__.py').write_bytes(b'changed')
                elif damage=='hardlink':os.link(site/'pip/__init__.py',work/'foreign-alias')
                elif damage=='shared':
                    other=site/'retained-1.0.dist-info';other.mkdir();(other/'RECORD').write_text('../../Scripts/pip.exe,,\n')
                elif damage=='present-tool-hash':
                    (root/'Scripts').mkdir();(root/'Scripts/pip.exe').write_bytes(b'changed launcher')
                else:
                    (info/'RECORD').write_text((info/'RECORD').read_text().replace('../../Scripts/pip.exe','../../../../outside.exe'))
                with mock.patch.object(app,'BACKEND_BOOTSTRAP_RECORD_SHA256',pin),mock.patch.object(app,'BACKEND_BOOTSTRAP_LAUNCHERS',tools):
                    with self.assertRaises(ValueError):app._seal_private_python(portable,work,embedded=False,backend_bootstrap=True)
                self.assertTrue((info/'RECORD').exists())

    def test_bootstrap_linked_script_parent_refuses_even_when_tools_are_absent(self):
        work=self.root/'linked-scripts';portable,tools,pin=self._bootstrap_python(work)
        outside=work/'foreign-scripts';outside.mkdir()
        try:os.symlink(outside,portable/'python_embeded/Scripts',target_is_directory=True)
        except (OSError,NotImplementedError):
            if os.name!='nt':raise
            # The existing Windows fixture pattern needs no symbolic-link privilege.
            subprocess.run([str(Path(os.environ['SystemRoot'])/'System32/cmd.exe'),'/d','/c','mklink','/J',
                str(portable/'python_embeded/Scripts'),str(outside)],check=True,capture_output=True,timeout=10)
        with mock.patch.object(app,'BACKEND_BOOTSTRAP_RECORD_SHA256',pin),mock.patch.object(app,'BACKEND_BOOTSTRAP_LAUNCHERS',tools):
            with self.assertRaisesRegex(ValueError,'linked bootstrap Scripts'):
                app._seal_private_python(portable,work,embedded=False,backend_bootstrap=True)

    def test_actual_selected_bootstrap_record_passes_complete_finalization(self):
        selected=os.environ.get('OA_COMFY_BOOTSTRAP_FIXTURE_TEST')
        if not selected:self.skipTest('private selected bootstrap DATA fixture not supplied')
        source=Path(selected)
        admitted={'python/'+p.relative_to(source).as_posix() for p in source.rglob('*') if p.is_file()}
        self.assertEqual(len(app._backend_bootstrap_omissions(source,admitted=admitted)),3)
        work=self.root/'actual-bootstrap';portable=work/'extract/Portable';root=portable/'python_embeded'
        shutil.copytree(app._extended(source),app._extended(root))
        site=root/'Lib/site-packages';kept=site/'kept-1.0.dist-info';kept.mkdir()
        (site/'kept.py').write_bytes(b'x = 1\n')
        digest=base64.urlsafe_b64encode(hashlib.sha256(b'x = 1\n').digest()).decode().rstrip('=')
        (kept/'RECORD').write_text(f'kept.py,sha256={digest},6\nkept-1.0.dist-info/RECORD,,\n')
        with self.assertRaisesRegex(ValueError,'missing or linked'):
            app._seal_private_python(portable,work,embedded=False)
        proof=app._seal_private_python(portable,work,embedded=False,backend_bootstrap=True)
        self.assertFalse(app._launcher().installer_presence(str(site),str(root)))
        self.assertTrue(proof['removed_installers']);self.assertEqual(proof['launch_check']['hashed'],1)

    def test_actual_bootstrap_preflight_collects_unknown_missing_before_copy(self):
        selected=os.environ.get('OA_COMFY_BOOTSTRAP_FIXTURE_TEST')
        if not selected:self.skipTest('private selected bootstrap DATA fixture not supplied')
        root=self.root/'source-python';shutil.copytree(selected,root)
        admitted={'python/'+p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file()}
        missing=['pip/__init__.py','pip/__main__.py']
        for name in missing:(root/'Lib/site-packages'/name).unlink()
        with mock.patch.object(app.shutil,'copyfile') as copy,mock.patch.object(app.subprocess,'run') as child:
            with self.assertRaises(ValueError) as caught:
                app._backend_finalization_preflight({'python_files':sorted(admitted),'wheels':[]},
                    {'python/python.exe':root/'python.exe'})
            for name in missing:self.assertIn(name,str(caught.exception))
            copy.assert_not_called();child.assert_not_called()

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
        env = source_git_environment()
        root.mkdir(parents=True)
        subprocess.run(["git", "-C", str(root), "init", "-q"], check=True, env=env)
        for name, data in files.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        subprocess.run(["git", "-C", str(root), "add", "-A"], check=True, env=env)
        subprocess.run(["git", "-C", str(root), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"], check=True, env=env)
        return subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True, env=env).strip()


    def test_git_fixture_isolates_incomplete_config_and_preserves_caller_environment(self):
        foreign = self.root / 'foreign'
        foreign.mkdir()
        marker = foreign / 'caller-owned'
        marker.write_bytes(b'unchanged')
        caller = dict(os.environ)
        with mock.patch.dict(os.environ, {
                'GIT_CONFIG_COUNT': '1', 'GIT_CONFIG_KEY_0': 'core.fsmonitor',
                'GIT_DIR': str(foreign / '.git'), 'GIT_WORK_TREE': str(foreign),
                'GIT_INDEX_FILE': str(foreign / 'index')}):
            os.environ.pop('GIT_CONFIG_VALUE_0', None)
            before = dict(os.environ)
            root = self.root / 'isolated-source'
            head = self._git_fixture(root, {'fixture.txt': b'owned source\n'})
            self.assertTrue(dict(os.environ) == before, 'fixture changed the caller environment')
            self.assertEqual(subprocess.check_output(
                ['git', '-C', str(root), 'show', head + ':fixture.txt'],
                env=source_git_environment()), b'owned source\n')
            self.assertEqual(list(foreign.iterdir()), [marker])
            self.assertEqual(marker.read_bytes(), b'unchanged')
        self.assertTrue(dict(os.environ) == caller, 'caller environment was not restored')

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
    @fixture_free_space()
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


class ArchiveToolJoinTests(unittest.TestCase):
    def setUp(self):
        self.area = tempfile.TemporaryDirectory(); self.addCleanup(self.area.cleanup)
        self.root = Path(self.area.name).resolve()
        self.tool, policy = archive_tool_fixture(self.root)
        patch = mock.patch.object(app, 'BACKEND_POLICY', policy); patch.start(); self.addCleanup(patch.stop)

    def test_missing_relative_linked_and_changed_tool_refuse_before_backend_inputs(self):
        def build(tool):
            return app.build_backend(self.root/'never.json','a'*64,self.root/'backend.7z',
                self.root/'source.zip',self.root/'receipt.json',candidate_source='b'*40,
                run_id=1,run_attempt=1,integration_archive=self.root/'never.zip',integration_options={},archive_tool=tool)
        with mock.patch.object(app, 'backend_inputs') as inputs, mock.patch.object(app.subprocess, 'run') as child:
            for tool in (None, Path('7zr.exe'), self.root/'missing/7zr.exe'):
                with self.subTest(tool=tool), self.assertRaises((ValueError, FileNotFoundError)):
                    build(tool)
            original = Path.lstat
            class Linked:
                st_mode = 0o120777
                st_file_attributes = 0
            with mock.patch.object(Path, 'lstat', lambda p: Linked() if app._plain(p) == self.tool else original(p)):
                with self.assertRaises(ValueError): build(self.tool)
            self.tool.write_bytes(b'changed same-name tool')
            with self.assertRaisesRegex(ValueError, 'selected policy'): build(self.tool)
            inputs.assert_not_called(); child.assert_not_called()

    @unittest.skipUnless(os.name == 'nt', 'actual Windows directory owner')
    def test_fixed_pack_command_uses_verified_absolute_tool_and_real_output_header(self):
        portable = self.root/'tree'; portable.mkdir()
        archive = self.root/'backend.7z'
        def execute(argv, **kwargs):
            self.assertEqual(argv, [str(self.tool),'a','-t7z','-m0=lzma2:c=4m','-mx=3','-mfb=32',
                '-md=4m','-ms=64m','-mf=off','-mmt=8',str(archive),'standalone-env','ComfyUI',
                'manifest.json','requirements-amd-win.txt','OA-LICENSES'])
            self.assertEqual(kwargs['cwd'], portable)
            self.assertEqual(kwargs['timeout'], 900)
            self.assertTrue(kwargs['check'])
            self.assertEqual(set(kwargs['env']), {'SystemRoot','WINDIR','TEMP','TMP','PATH'})
            archive.write_bytes(b'7z\xbc\xaf\x27\x1c opaque response from inert stub')
        with mock.patch.object(app.subprocess, 'run', side_effect=execute) as child:
            app._pack_backend_archive(portable, archive, self.tool)
            child.assert_called_once()
        self.assertEqual(app._sha256(self.tool)[1], hashlib.sha256(b'not an executable').hexdigest())

    @unittest.skipUnless(os.name == 'nt', 'actual Windows directory owner')
    def test_pack_timeout_and_tool_failure_propagate_without_success_output(self):
        for index, failure in enumerate((subprocess.TimeoutExpired('7zr', 900),
                                         subprocess.CalledProcessError(2, '7zr'))):
            with self.subTest(failure=type(failure).__name__):
                work = self.root / f'failure-{index}'; work.mkdir()
                portable = work / 'tree'; portable.mkdir()
                archive = work / 'backend.7z'
                with mock.patch.object(app.subprocess, 'run', side_effect=failure) as child:
                    with self.assertRaises(type(failure)) as caught:
                        app._pack_backend_archive(portable, archive, self.tool)
                self.assertIs(caught.exception, failure)
                self.assertEqual(child.call_args.kwargs['timeout'], 900)
                self.assertIn('-mmt=8', child.call_args.args[0])
                self.assertFalse(archive.exists())

    def test_existing_archive_refuses_before_tool_execution(self):
        archive = self.root / 'backend.7z'; archive.write_bytes(b'retained output')
        with mock.patch.object(app.subprocess, 'run') as child:
            with self.assertRaisesRegex(ValueError, 'already exists'):
                app._pack_backend_archive(self.root, archive, self.tool)
        child.assert_not_called()
        self.assertEqual(archive.read_bytes(), b'retained output')

    @unittest.skipUnless(os.name == 'nt', 'actual Windows directory owner')
    def test_foreign_and_mismatched_ambient_windows_roots_never_reach_child(self):
        (self.root/'expected-temp').mkdir()
        expected = app._windows_archive_environment(self.root/'expected-temp')
        for index, roots in enumerate(({'SystemRoot':str(self.root/'foreign'), 'WINDIR':str(self.root/'foreign')},
                                       {'SystemRoot':expected['SystemRoot'], 'WINDIR':str(self.root/'foreign')})):
            with self.subTest(roots=roots):
                work = self.root/f'case-{index}'; work.mkdir()
                portable = work/'tree'; portable.mkdir(); archive = work/'backend.7z'
                def execute(argv, **kwargs):
                    self.assertEqual(kwargs['env']['SystemRoot'], expected['SystemRoot'])
                    self.assertEqual(kwargs['env']['WINDIR'], expected['WINDIR'])
                    self.assertEqual(kwargs['env']['PATH'], expected['PATH'])
                    self.assertNotIn(str(self.root/'foreign'), kwargs['env'].values())
                    archive.write_bytes(b'7z\xbc\xaf\x27\x1c inert stub')
                with mock.patch.dict(os.environ, roots), mock.patch.object(app.subprocess, 'run', side_effect=execute) as child:
                    app._pack_backend_archive(portable, archive, self.tool)
                    child.assert_called_once()

    def test_tool_source_notice_drift_and_false_zip_output_refuse(self):
        source = self.tool.parent/'7z2605-src.tar.xz'
        original = source.read_bytes(); source.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'selected policy'): app._archive_tool_inputs(self.tool)
        source.write_bytes(original)
        notice = self.tool.parent/'notices/DOC/License.txt'; original_notice = notice.read_bytes(); notice.write_bytes(b'foreign')
        with self.assertRaisesRegex(ValueError, 'selected policy'): app._archive_tool_inputs(self.tool)
        notice.write_bytes(original_notice)
        if os.name == 'nt':
            portable = self.root/'tree'; portable.mkdir(); archive = self.root/'backend.7z'
            with mock.patch.object(app.subprocess, 'run', side_effect=lambda *a, **k: archive.write_bytes(b'PK fake ZIP')):
                with self.assertRaisesRegex(ValueError, '7z archive'):
                    app._pack_backend_archive(portable, archive, self.tool)


@unittest.skipUnless(os.name == 'nt' and os.environ.get('OA_COMFY_ARCHIVE_TOOL_TEST_INPUTS'),
                     'explicit admitted Windows archive-tool fixture required')
class ArchiveToolPhysicalReceivingTests(unittest.TestCase):
    """Opt-in tiny tool test; the external owner supplies bounded supervision."""
    def test_real_pack_finalizer_receiver_preserves_roots_and_notices(self):
        self.real_receiving(multipart=False)

    def test_real_multipart_pack_receiver_list_extract(self):
        self.real_receiving(multipart=True)

    def real_receiving(self, *, multipart):
        with tempfile.TemporaryDirectory(prefix='oa-real-archive-') as temporary:
            root = Path(temporary).resolve()
            tool = Path(os.environ['OA_COMFY_ARCHIVE_TOOL_TEST_INPUTS']) / '7zr.exe'
            app._archive_tool_inputs(tool)
            tree = root/'tree'; tree.mkdir()
            raw_input = b'{"fixture":"tiny real archive receiving"}'
            input_pin = dict(name='backend-inputs.json', bytes=len(raw_input), sha256=hashlib.sha256(raw_input).hexdigest())
            expected = {'standalone-env/python.exe': b'fixture bytes, never executed',
                'standalone-env/uv.exe': b'fixture bytes, never executed',
                'ComfyUI/main.py': b'# fixture, never imported\n',
                'ComfyUI/custom_nodes/abstraction_downloads/__init__.py': b'# fixture, never imported\n',
                'OA-LICENSES/OpenAbstractions-LICENSE': b'fixture OA licence\n',
                'OA-LICENSES/OpenAbstractions-NOTICE': b'fixture OA notice\n',
                'manifest.json': json.dumps({'id':'win-amd','manager':False,
                    'input_descriptor_sha256':input_pin['sha256'],
                    'private_python_seal':{'records_sha256':'f'*64}}).encode(),
                'requirements-amd-win.txt': b'# no package installation\n'}
            for name, raw in expected.items():
                path = tree/name; path.parent.mkdir(parents=True, exist_ok=True); path.write_bytes(raw)
            archive = root/'backend.7z'
            try:
                app._pack_backend_archive(tree, archive, tool)
            finally:
                if archive.is_file() and archive.stat().st_size <= 1 << 20:
                    self.archive_bytes = archive.read_bytes()  # retain primitive before later assertions
            source = root/'backend-source.zip'
            with zipfile.ZipFile(source, 'w') as zipped:
                zipped.writestr('OA-Backend-Source/backend-inputs.json',raw_input)
                source_files={'builder/scripts/build_local.py':b'# fixture source', 'ComfyUI/main.py':expected['ComfyUI/main.py']}
                zipped.writestr('backend-source.json',json.dumps({'schema':1,'input_descriptor_sha256':input_pin['sha256'],
                    'files':[dict(name=name,bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest()) for name,raw in source_files.items()]}))
                for name,raw in source_files.items():zipped.writestr(name,raw)
                zipped.writestr('LICENSE', expected['OA-LICENSES/OpenAbstractions-LICENSE'])
            out = root/'out'; out.mkdir()
            with mock.patch.object(app, 'BACKEND_VOLUME_BYTES', 128 if multipart else app.BACKEND_VOLUME_BYTES):
                produced = app.finalize_backend_outputs(archive, source, out, 'backend-producer.json',
                    input_descriptor=input_pin, candidate_source='a'*40, run_id=1, run_attempt=1)
            size, sha = app._sha256(out/'backend-producer.json')
            received = app.app_pair_receipt.read_backend_producer(out/'backend-producer.json',
                expected_receipt=dict(name='backend-producer.json',bytes=size,sha256=sha),
                expected_input=input_pin,candidate_source='a'*40,run_id=1,run_attempt=1)
            self.assertEqual(received, produced)
            first = out / produced['archive']['parts'][0]['name']
            if multipart:
                self.assertGreater(len(produced['archive']['parts']), 1)
                self.assertEqual(first.name, 'backend.7z.001')
            (root/'archive-temp').mkdir()
            env = app._windows_archive_environment(root/'archive-temp')
            listed = subprocess.run([str(tool),'l','-slt','-sccUTF-8',str(first)],cwd=tree,
                env=env,check=True,capture_output=True,text=True,encoding='utf-8',timeout=10).stdout
            self.assertIn('----------', listed)
            fields_by_name = {}
            for block in listed.split('----------',1)[1].replace('\r\n','\n').strip().split('\n\n'):
                fields = dict(line.split(' = ',1) for line in block.splitlines() if ' = ' in line)
                name = fields['Path'].replace('\\','/')
                self.assertNotIn(name, fields_by_name)
                fields_by_name[name] = fields
            self.actual_listing = fields_by_name  # retain actual protocol before roster assertions
            directories = {parent.as_posix() for name in expected for parent in Path(name).parents if parent != Path('.')}
            self.assertEqual(set(fields_by_name), set(expected) | directories)
            for name, raw in expected.items():
                self.assertEqual(int(fields_by_name[name]['Size']), len(raw))
            target = root/'extracted'
            subprocess.run([str(tool),'x','-y','-o'+str(target),str(first)],cwd=tree,
                env=env,check=True,capture_output=True,text=True,timeout=10)
            actual = {}
            for path in target.rglob('*'):
                self.assertFalse(path.is_symlink() or path.is_junction())
                if path.is_file(): actual[path.relative_to(target).as_posix()] = path.read_bytes()
            self.actual_hashes = {name:hashlib.sha256(raw).hexdigest() for name,raw in actual.items()}
            self.assertEqual(actual, expected)
            if multipart:
                owner_path=Path(__file__).resolve().parents[1]/'research/org-edits/redist/app_assets.py'
                spec=app.importlib.util.spec_from_file_location('actual_backend_api_receiver',owner_path)
                assets=app.importlib.util.module_from_spec(spec);spec.loader.exec_module(assets)
                artifact={'id':14,'name':'oa-backend-comfyui-1','sha256':'e'*64}
                intake={'schema':1,'kind':'windows-artifact-intake','source':'a'*40,'run_id':1,'run_attempt':1,
                    'artifact':artifact,'files':[dict(name=path.name,bytes=path.stat().st_size,
                        sha256=hashlib.sha256(path.read_bytes()).hexdigest()) for path in out.iterdir()]}
                metadata={}
                with mock.patch.dict('sys.modules',{'app_pair_receipt':app.app_pair_receipt}):
                    self.assertEqual(assets.receive_backend(out,intake,expected_artifact=artifact,
                        expected_input={key:input_pin[key] for key in ('bytes','sha256')},candidate_source='a'*40,
                        run_id=1,run_attempt=1,archive_owner=app,archive_tool=tool,metadata=metadata),produced)
                self.assertEqual(metadata,{'descriptor':raw_input,'manifest':expected['manifest.json']})
            self.assertTrue((root/'backend.7z').is_file())
            self.receiving_result = produced
            self.fixture_root = str(root)


class ArchiveMetadataTests(unittest.TestCase):
    def test_output_bound_polling_preserves_concurrent_child_file_positions(self):
        with tempfile.TemporaryDirectory() as area:
            root=Path(area).resolve();tool,policy=archive_tool_fixture(root)
            raw=b'opaque DATA';part=root/'backend.7z';part.write_bytes(raw)
            pin={'name':part.name,'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
            archive=dict(pin,format='7z',parts=[pin]);real_popen=subprocess.Popen
            listing=b'header\n----------\nPath = manifest.json\nSize = 2\n\nPath = data.bin\nSize = 0\n\n'
            code=('import os,time;data='+repr(listing)+';os.write(1,data);'
                'os.lseek(1,data.index(b"Size = 0")+7,0);'
                'os.write(2,b"ab");os.lseek(2,0,0);time.sleep(.2);'
                'assert os.lseek(2,0,1)==0,"stderr position changed";os.write(1,b"1")')
            def child(argv,**kwargs):
                program=code if argv[1]=='l' else 'import os;os.write(1,b"{}")'
                return real_popen([sys.executable,'-I','-S','-c',program],**kwargs)
            with mock.patch.object(app,'BACKEND_POLICY',policy),mock.patch.object(app.subprocess,'Popen',child):
                members,manifest=app.backend_archive_metadata(root,archive,tool)
            self.assertEqual(members,{'manifest.json','data.bin'})
            self.assertEqual(manifest,b'{}')

    def test_unicode_listing_uses_utf8_without_best_fit_filename_substitution(self):
        with tempfile.TemporaryDirectory() as area:
            root=Path(area).resolve();tool,policy=archive_tool_fixture(root)
            raw=b'opaque DATA';part=root/'backend.7z';part.write_bytes(raw)
            pin={'name':part.name,'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
            archive=dict(pin,format='7z',parts=[pin]);calls=[]
            name='models/FONLY__\uff0abf16.aks2'
            class Child:
                def __init__(self,argv,**kwargs):
                    calls.append(argv)
                    shown=name if '-sccUTF-8' in argv else name.replace('\uff0a','*')
                    listing=('header\n----------\nPath = manifest.json\nSize = 2\n\n'
                        f'Path = {shown}\nSize = 1\n\n').encode('utf-8')
                    kwargs['stdout'].write(listing if argv[1]=='l' else b'{}');kwargs['stdout'].flush()
                def poll(self):return 0
                def wait(self):return 0
                def kill(self):raise AssertionError('already terminal')
            with mock.patch.object(app,'BACKEND_POLICY',policy),mock.patch.object(app.subprocess,'Popen',Child):
                members,manifest=app.backend_archive_metadata(root,archive,tool)
            self.assertEqual(members,{'manifest.json',name})
            self.assertNotIn(name.replace('\uff0a','*'),members)
            self.assertEqual(manifest,b'{}')
            self.assertEqual(calls[0],[str(tool),'l','-slt','-sccUTF-8',str(part)])

    def test_listing_invalid_sizes_duplicate_fields_names_and_unsafe_paths_refuse(self):
        invalid=[b'Path = huge.bin\nSize = -1',b'Path = huge.bin\nSize = 1.0',
            b'Path = huge.bin\nSize = 17179869185',
            b'Path = a.bin\nSize = 1\nSize = 2',
            b'Path = manifest.json\nSize = 2',b'Path = ../foreign\nSize = 1',
            b'Path = bad\tname\nSize = 1']
        for block in invalid:
            with self.subTest(block=block),tempfile.TemporaryDirectory() as area:
                root=Path(area).resolve();tool,policy=archive_tool_fixture(root)
                raw=b'opaque DATA';part=root/'backend.7z';part.write_bytes(raw)
                pin={'name':part.name,'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
                archive=dict(pin,format='7z',parts=[pin]);calls=[]
                class Child:
                    def __init__(self,argv,**kwargs):
                        calls.append(argv)
                        kwargs['stdout'].write(b'header\n----------\nPath = manifest.json\nSize = 2\n\n'+block+b'\n\n')
                        kwargs['stdout'].flush()
                    def poll(self):return 0
                    def wait(self):return 0
                    def kill(self):raise AssertionError('already terminal')
                with mock.patch.object(app,'BACKEND_POLICY',policy),mock.patch.object(app.subprocess,'Popen',Child):
                    with self.assertRaises(ValueError):app.backend_archive_metadata(root,archive,tool)
                self.assertEqual(len(calls),1)

    def test_tool_mutation_after_either_child_and_aggregate_expansion_refuse(self):
        for variant in ('listing-tool','extract-tool','aggregate'):
            with self.subTest(variant=variant),tempfile.TemporaryDirectory() as area:
                root=Path(area).resolve();tool,policy=archive_tool_fixture(root)
                raw=b'opaque DATA';part=root/'backend.7z';part.write_bytes(raw)
                pin={'name':part.name,'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
                archive=dict(pin,format='7z',parts=[pin]);calls=[]
                class Child:
                    def __init__(self,argv,**kwargs):
                        calls.append(argv)
                        listing=b'header\n----------\nPath = manifest.json\nSize = 2\n\n'
                        if variant=='aggregate':listing+=b'Path = huge.bin\nSize = 17179869185\n\n'
                        kwargs['stdout'].write(listing if argv[1]=='l' else b'{}');kwargs['stdout'].flush()
                        if ((variant=='listing-tool' and argv[1]=='l')
                                or (variant=='extract-tool' and argv[1]=='x')):
                            tool.write_bytes(b'X'*tool.stat().st_size)
                    def poll(self):return 0
                    def wait(self):return 0
                    def kill(self):raise AssertionError('already terminal')
                with mock.patch.object(app,'BACKEND_POLICY',policy),mock.patch.object(app.subprocess,'Popen',Child):
                    with self.assertRaisesRegex(ValueError,'differs|expanded-byte'):
                        app.backend_archive_metadata(root,archive,tool)
                self.assertEqual(len(calls),2 if variant=='extract-tool' else 1)

    def test_fixed_metadata_commands_follow_complete_parts_and_bound_manifest(self):
        with tempfile.TemporaryDirectory() as area:
            root=Path(area).resolve();tool,policy=archive_tool_fixture(root)
            raw=b'opaque DATA';part=root/'backend.7z';part.write_bytes(raw)
            pin={'name':part.name,'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
            archive=dict(pin,format='7z',parts=[pin])
            manifest=b'{"fixture":true}'
            calls=[]
            class Child:
                def __init__(self,argv,**kwargs):
                    calls.append(argv)
                    self.value=0
                    output=(b'header\n----------\nPath = manifest.json\nSize = '+str(len(manifest)).encode()+b'\n\n'
                        if argv[1]=='l' else manifest)
                    kwargs['stdout'].write(output);kwargs['stdout'].flush()
                def poll(self):return self.value
                def wait(self):return self.value
                def kill(self):raise AssertionError('already terminal')
            with mock.patch.object(app,'BACKEND_POLICY',policy),mock.patch.object(app.subprocess,'Popen',Child):
                members,actual=app.backend_archive_metadata(root,archive,tool)
                self.assertEqual((members,actual),({'manifest.json'},manifest))
                self.assertEqual(calls,[[str(tool),'l','-slt','-sccUTF-8',str(part)],
                    [str(tool),'x','-so',str(part),'manifest.json']])
                part.write_bytes(b'X'*len(raw))
                with self.assertRaises(ValueError):app.backend_archive_metadata(root,archive,tool)
                self.assertEqual(len(calls),2)


class BackendMultipartOutputTests(unittest.TestCase):
    """Actual finalizer/receiver over inert data; no 7z or backend execution."""
    def setUp(self):
        self.area = tempfile.TemporaryDirectory(); self.addCleanup(self.area.cleanup)
        self.root = Path(self.area.name).resolve()
        self.out = self.root / 'out'; self.out.mkdir()
        self.backend = self.root / 'backend.7z'
        self.backend.write_bytes(b'opaque fixture bytes, not a 7z archive' * 3)
        self.source = self.root / 'backend-source.zip'
        with zipfile.ZipFile(self.source, 'w') as archive:
            archive.writestr('LICENSE', b'owned source licence fixture')
        self.input = {'name': 'backend-inputs.json', 'bytes': 7, 'sha256': 'a' * 64}
        self.context = dict(expected_input=self.input, candidate_source='b' * 40, run_id=19, run_attempt=2)

    def produce(self, schema=3):
        with mock.patch.object(app, 'BACKEND_VOLUME_BYTES', 64):
            result = app.finalize_backend_outputs(self.backend, self.source, self.out,
                'backend-producer.json', input_descriptor=self.input,
                **{key: self.context[key] for key in ('candidate_source', 'run_id', 'run_attempt')}, schema=schema)
        self.receipt = self.out / 'backend-producer.json'
        size, sha = app._sha256(self.receipt)
        self.receipt_pin = {'name': self.receipt.name, 'bytes': size, 'sha256': sha}
        return result

    def receive(self):
        return app.app_pair_receipt.read_backend_producer(self.receipt,
            expected_receipt=self.receipt_pin, **self.context)

    def test_actual_finalizer_to_receiver_binds_ordered_bytes_and_real_source_zip(self):
        produced = self.produce()
        self.assertEqual(self.receive(), produced)
        self.assertEqual(produced['schema'], 3)
        for key, source in (('archive', self.backend), ('source_pair', self.source)):
            value = produced[key]
            self.assertGreater(len(value['parts']), 1)
            raw = b''.join((self.out / p['name']).read_bytes() for p in value['parts'])
            self.assertEqual(raw, source.read_bytes())
            self.assertEqual(value['sha256'], hashlib.sha256(raw).hexdigest())
        with zipfile.ZipFile(io.BytesIO(b''.join((self.out / p['name']).read_bytes()
                for p in produced['source_pair']['parts']))) as source:
            self.assertEqual(source.read('LICENSE'), b'owned source licence fixture')

    def test_receiving_refuses_independent_provenance_and_coordinated_receipt_mutations(self):
        produced = self.produce()
        original = self.receipt.read_bytes()
        for key, bad in (('candidate_source', 'c' * 40), ('run_id', 20), ('run_attempt', 3),
                         ('input_descriptor', dict(self.input, sha256='d' * 64))):
            with self.subTest(key=key):
                self.receipt.write_text(json.dumps(dict(produced, **{key: bad})))
                # Model a separately received receipt pin: context remains independent.
                size, sha = app._sha256(self.receipt)
                with self.assertRaisesRegex(ValueError, 'provenance'):
                    app.app_pair_receipt.read_backend_producer(self.receipt,
                        expected_receipt={'name': self.receipt.name, 'bytes': size, 'sha256': sha}, **self.context)
        self.receipt.write_bytes(original + b' ')
        with self.assertRaisesRegex(ValueError, 'independent intake'):
            self.receive()

    def test_receiving_refuses_same_size_cached_substitution_and_missing_part(self):
        produced = self.produce()
        part = self.out / produced['archive']['parts'][0]['name']
        raw = part.read_bytes(); part.write_bytes(b'X' * len(raw))
        with self.assertRaisesRegex(ValueError, 'hash differs'):
            self.receive()
        part.unlink()
        with self.assertRaises(FileNotFoundError):
            self.receive()

    def test_actual_receiver_requires_strict_nested_pin_and_string_source_context(self):
        produced = self.produce()
        bad_pins = [dict(self.input, bytes=7.0), dict(self.input, bytes=True),
                    dict(self.input, extra=0), {key: value for key, value in self.input.items() if key != 'bytes'}]
        for pin in bad_pins:
            with self.subTest(pin=pin):
                self.receipt.write_text(json.dumps(dict(produced, input_descriptor=pin)))
                size, sha = app._sha256(self.receipt)
                with self.assertRaisesRegex(ValueError, 'archive pin'):
                    app.app_pair_receipt.read_backend_producer(self.receipt,
                        expected_receipt={'name': self.receipt.name, 'bytes': size, 'sha256': sha}, **self.context)
        numeric_source = int('1' * 40)
        with self.assertRaisesRegex(ValueError, 'independent backend context'):
            app.app_pair_receipt.read_backend_producer(self.receipt, expected_receipt=self.receipt_pin,
                **dict(self.context, candidate_source=numeric_source))
        with self.assertRaisesRegex(ValueError, 'backend output context'):
            app.finalize_backend_outputs(self.backend, self.source, self.out, 'another-receipt.json',
                input_descriptor=self.input, candidate_source=numeric_source, run_id=19, run_attempt=2)

    def test_legacy_schema2_exact_single_archive_reader(self):
        self.backend = self.root / 'backend.zip'
        with zipfile.ZipFile(self.backend, 'w') as archive:
            archive.writestr('fixture', b'legacy backend DATA')
        result = self.produce(schema=2)
        self.assertEqual(self.receive(), result)
        self.assertEqual(set(result['archive']), {'name', 'bytes', 'sha256'})
        self.assertEqual(sorted(p.name for p in self.out.iterdir()),
            ['backend-producer.json', 'backend-source.zip', 'backend.zip'])

    def test_finalizer_refuses_part_count_and_existing_output_before_writes(self):
        self.backend.write_bytes(b'x' * (64 * 16 + 1))
        with mock.patch.object(app, 'BACKEND_VOLUME_BYTES', 64), self.assertRaisesRegex(ValueError, 'part-count'):
            app.finalize_backend_outputs(self.backend, self.source, self.out, 'backend-producer.json',
                input_descriptor=self.input, candidate_source='b' * 40, run_id=19, run_attempt=2)
        self.assertEqual(list(self.out.iterdir()), [])
        self.backend.write_bytes(b'fixture')
        (self.out / self.backend.name).write_bytes(b'prior data')
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.produce()
        self.assertEqual((self.out / self.backend.name).read_bytes(), b'prior data')


if __name__ == "__main__":
    unittest.main()
