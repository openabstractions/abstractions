"""Inert archive boundary checks for the app-owned installer assembly.

Guard-message tests format through a real MSI session and the NULL-handle
consumer used by Burn, including plain and property-looking path data.
"""
import hashlib
import importlib.util
import io
import json
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import shutil
import zipfile
import os
import subprocess
import xml.etree.ElementTree as ET

sys.path.insert(0, str(Path(__file__).resolve().parent))
from personfolders import setUpModule, tearDownModule  # noqa: F401
import build_installer as build


def archive(entries):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode='w:gz') as target:
        for name, data, kind in entries:
            entry = tarfile.TarInfo(name)
            entry.type = kind
            entry.size = len(data)
            target.addfile(entry, io.BytesIO(data))
    return buffer.getvalue()


def identity_fixture(shared):
    """Complete the selected public shared owner without any private fallback."""
    actual=Path(build.app_builder().__file__).resolve().parent
    shutil.copyfile(actual/'build_app.py',shared/'build_app.py')
    shutil.copyfile(actual/'build_extension.py',shared/'build_extension.py')
    source=actual.parents[1]/'openabstractions-flat/abstraction-identity/fileid/native_windows.h'
    shutil.copyfile(source,shared/'native_windows.h')


def host_inputs_fixture(out):
    """The producer's host_inputs.h when OA_OPENCODE_TEST_HOST_INPUTS names
    one, otherwise a fixture with the real registration manifest digest, so
    the native tests run by default."""
    selected=os.environ.get('OA_OPENCODE_TEST_HOST_INPUTS')
    if selected:
        inputs=Path(selected)
        if inputs.name!='host_inputs.h':raise ValueError('OA_OPENCODE_TEST_HOST_INPUTS must name a host_inputs.h')
        shutil.copyfile(inputs,out/'host_inputs.h');return out/'host_inputs.h'
    manifest=hashlib.sha256(build.registration_manifest()).hexdigest()
    (out/'host_inputs.h').write_text('\n'.join(['#pragma once','typedef struct { const wchar_t *relative; const char *sha256; } OA_TRUSTED_FILE;',
        '#define OA_PAYLOAD_MAX_RELATIVE 120','#define OA_PLUGIN_RELATIVE L"plugin\\\\0.8.0"',
        '#define OA_HELPER_RELATIVE L"plugin\\\\0.8.0\\\\installer\\\\config-lifecycle.js"',
        '#define OA_BOOTSTRAP_INVENTORY_SHA256 L"'+('0'*64)+'"',
        '#define OA_PLUGIN_INVENTORY_SHA256 L"'+('0'*64)+'"',
        'static const OA_TRUSTED_FILE OA_TRUSTED[] = {',
        '  {L"desktop-fix\\\\watcher.node", "'+build.DESKTOP_FIX['files']['watcher.node']['sha256']+'"},',
        '  {L"installer\\\\empty-bunfig.toml", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},','};',
        '#define OA_TRUSTED_COUNT (sizeof(OA_TRUSTED)/sizeof(OA_TRUSTED[0]))',
        '#define OA_REGISTRATION_MANIFEST_NAME L"'+build.REGISTRATION_MANIFEST+'"','#define OA_REGISTRATION_MANIFEST_SHA256 "'+manifest+'"'])+'\n',encoding='utf-8')
    return out/'host_inputs.h'


def portable_executable(debug_path):
    """A minimal PE32+ image whose one CodeView record names debug_path."""
    import struct
    image=bytearray(0x400)
    image[0:2]=b'MZ';struct.pack_into('<I',image,0x3c,0x40)
    image[0x40:0x44]=b'PE\0\0'
    struct.pack_into('<HH',image,0x44,0x8664,1);struct.pack_into('<H',image,0x54,240)
    optional=0x58;struct.pack_into('<H',image,optional,0x20b)
    struct.pack_into('<I',image,optional+108,16)
    record=b'RSDS'+bytes(16)+struct.pack('<I',1)+debug_path.encode()+b'\0'
    struct.pack_into('<II',image,optional+112+6*8,0x1000,28)
    table=optional+240
    struct.pack_into('<IIII',image,table+8,0x200,0x1000,0x200,0x200)
    struct.pack_into('<IIHHIIII',image,0x200,0,0,0,0,2,len(record),0x101c,0x21c)
    image[0x21c:0x21c+len(record)]=record
    return bytes(image)


def desktop_fix_fixture(directory=None):
    """desktop_fix_payload's shape for fixtures that patch out every pinned read."""
    return ({build.DESKTOP_FIX_PREFIX+'watcher.node':portable_executable('watcher.pdb'),build.DESKTOP_FIX_PREFIX+'LICENSE':b'MIT fixture'},
            {'source':build.DESKTOP_FIX['source'],'live':build.DESKTOP_FIX['live'],'files':build.DESKTOP_FIX['files'],
             'stock':sorted({row['watcher_stock']['sha256'] for row in build.DESKTOP_RELEASES}),'earlier':list(build.DESKTOP_FIX['earlier'])})


def bootstrap_metadata():
    raw,_=build.bootstrap_lock()
    return {'version':build.UPSTREAM['version'],'lock_sha256':hashlib.sha256(raw).hexdigest(),
            'packages':[dict(row,license='MIT',license_files=[]) for row in build.bootstrap_inputs()], 'ripgrep':build.RIPGREP}


class PayloadBoundary(unittest.TestCase):
    def journey_fixture(self, out):
        """Actual prepare owner emits originals; no compiler or package process."""
        build.app_builder()
        cli=io.BytesIO()
        with zipfile.ZipFile(cli,'w') as target:target.writestr('opencode.exe',b'official CLI fixture')
        files={name:('fixture '+name).encode() for name in
               ('package.json','index.js','server.js','npm-shrinkwrap.json','extra/non-pe-client.js',
                'node_modules/@openabstractions/ipc-win32-x64/abstraction_ipc.dll',
                'node_modules/@openabstractions/ipc-win32-x64/oa_ipc_node.node')}
        with patch.object(build,'pinned',side_effect=lambda path,*args:cli.getvalue() if path=='cli' else b'inert input'),\
             patch.object(build,'plugin_files',return_value=({'version':'0.8.0'},files)),\
             patch.object(build,'package_files',return_value=({'name':'jsonc-parser','version':'3.3.1'},{'lib/main.js':b'parser fixture'})),\
             patch.object(build,'bootstrap_payload',return_value=({'bootstrap/files.json':b'{}'}, bootstrap_metadata())),\
             patch.object(build,'desktop_fix_payload',side_effect=desktop_fix_fixture):
            receipt=build.prepare('plugin','a'*64,17,'cli','parser',out)
        rows=[dict(value,id='ActualFile'+str(index),path=build.PAYLOAD_DIRECTORY+name)
              for index,(name,value) in enumerate(receipt['payload'].items())]
        rows.append(dict(receipt['registration_manifest'],id='application.manifest',
                         path=build.REGISTRATION_DIRECTORY+build.REGISTRATION_MANIFEST))
        rows[-1].pop('name')
        return receipt,{'schema':1,'files':rows,'binaries':[{'id':'OpenCodeInstallerHost','sha256':'b'*64,'bytes':77}]}

    def test_journey_roles_join_actual_prepared_full_plugin_and_source_config_tools(self):
        with tempfile.TemporaryDirectory() as directory,patch.dict(build.UPSTREAM,cli_exe_sha256=hashlib.sha256(b'official CLI fixture').hexdigest()):
            receipt,payload=self.journey_fixture(Path(directory)/'prepared')
            before=json.dumps([receipt,payload],sort_keys=True)
            roles=build.journey_payload_roles(receipt,payload)
            self.assertEqual(roles['official_cli']['path'],build.PAYLOAD_DIRECTORY+'cli/opencode.exe')
            self.assertEqual(set(roles['plugin_files']),{name for name in receipt['payload'] if name.startswith('plugin/0.8.0/')})
            self.assertIn('plugin/0.8.0/extra/non-pe-client.js',roles['plugin_files'])
            self.assertIn('plugin/0.8.0/installer/node_modules/jsonc-parser/lib/main.js',roles['plugin_files'])
            self.assertEqual(roles['config_tools']['configure']['path'],build.PAYLOAD_DIRECTORY+'plugin/0.8.0/configure.js')
            self.assertEqual(roles['config_tools']['lifecycle']['path'],build.PAYLOAD_DIRECTORY+'plugin/0.8.0/installer/config-lifecycle.js')
            self.assertEqual(set(roles['sdk_native']),set(build.SDK_NATIVE_MEMBERS))
            self.assertEqual(roles['original_plugin'],receipt['plugin'])
            self.assertEqual(roles['original_members'],receipt['payload'])
            inventory = build.PLUGIN_INVENTORY_MEMBER
            self.assertEqual(roles['members'][inventory],
                             dict(receipt['payload'][inventory],
                                  id=next(row['id'] for row in payload['files']
                                          if row['path'] == build.PAYLOAD_DIRECTORY + inventory),
                                  path=build.PAYLOAD_DIRECTORY + inventory))
            self.assertEqual(json.dumps([receipt,payload],sort_keys=True),before)
            roles['official_cli']['sha256']='0'*64
            roles['original_members']['cli/opencode.exe']['bytes']=0
            self.assertEqual(json.dumps([receipt,payload],sort_keys=True),before)

    def test_journey_roles_reject_missing_extra_duplicate_and_changed_members(self):
        with tempfile.TemporaryDirectory() as directory,patch.dict(build.UPSTREAM,cli_exe_sha256=hashlib.sha256(b'official CLI fixture').hexdigest()):
            original,expected=self.journey_fixture(Path(directory)/'prepared')
            for variant in ('missing','duplicate-id','duplicate-path','foreign-basename','extra','js-pin','sdk-pin',
                            'manifest-pin','manifest-folder','no-manifest','override','version','upstream',
                            'partial-plugin','runtime-config','bad-pin','bool-size','missing-receipt','foreign-original',
                            'plugin-inventory-pin','foreign-installer-member'):
                receipt,payload=json.loads(json.dumps([original,expected]))
                files=payload['files'];js=next(row for row in files if row['path'].endswith('extra/non-pe-client.js'))
                sdk=next(row for row in files if row['path'].endswith('oa_ipc_node.node'))
                if variant=='missing':files.remove(js)
                elif variant=='duplicate-id':js['id']=files[0]['id']
                elif variant=='duplicate-path':files.append(dict(js,id='Duplicate'))
                elif variant=='foreign-basename':js['path']='LocalAppDataFolder/foreign/non-pe-client.js'
                elif variant in ('extra','runtime-config'):files.append(dict(js,id='Foreign',path=build.PAYLOAD_DIRECTORY+'opencode.json'))
                elif variant=='js-pin':js['sha256']='c'*64
                elif variant=='sdk-pin':sdk['sha256']='c'*64
                elif variant=='manifest-pin':files[-1]['sha256']='c'*64
                elif variant=='manifest-folder':files[-1]['path']='LocalAppDataFolder/foreign/'+build.REGISTRATION_MANIFEST
                elif variant=='no-manifest':files.pop()
                elif variant=='override':receipt['app_owned_source_overrides']['configure.js']='c'*64
                elif variant=='version':receipt['plugin']['version']='0.9.0'
                elif variant=='upstream':receipt['upstream']['version']='foreign'
                elif variant=='partial-plugin':receipt['payload'].pop('plugin/0.8.0/configure.js')
                elif variant=='bad-pin':js['sha256']='claimed'
                elif variant=='bool-size':js['bytes']=True
                elif variant=='missing-receipt':receipt.pop('payload')
                elif variant=='foreign-original':receipt['payload']['config.json']={'sha256':'a'*64,'bytes':1}
                elif variant=='plugin-inventory-pin':
                    inventory = next(row for row in files
                                     if row['path'] == build.PAYLOAD_DIRECTORY + build.PLUGIN_INVENTORY_MEMBER)
                    inventory['sha256'] = 'c'*64
                elif variant=='foreign-installer-member':
                    member = PurePosixPath(build.PLUGIN_INVENTORY_MEMBER)
                    foreign = member.with_stem(member.stem + '-copy').as_posix()
                    item = receipt['payload'][build.PLUGIN_INVENTORY_MEMBER]
                    receipt['payload'][foreign] = item
                    files.append(dict(item, id='ForeignInventory', path=build.PAYLOAD_DIRECTORY + foreign))
                with self.subTest(variant=variant),self.assertRaises(ValueError):build.journey_payload_roles(receipt,payload)

    def test_journey_roles_existing_signing_changes_nonfile_native_and_container_only(self):
        # application_native_sign source_roles() signs Binary host + external
        # preflight. Already signed canonical SDK enters the prepared plugin;
        # package readback/source_roles require unchanged File member pins.
        with tempfile.TemporaryDirectory() as directory,patch.dict(build.UPSTREAM,cli_exe_sha256=hashlib.sha256(b'official CLI fixture').hexdigest()):
            receipt,payload=self.journey_fixture(Path(directory)/'prepared')
            original=build.journey_payload_roles(receipt,payload)
            payload.update(msi={'name':'signed.msi','sha256':'d'*64,'bytes':999},
                           binaries=[{'id':'OpenCodeInstallerHost','sha256':'e'*64,'bytes':123}],
                           own_pe=[{'table':'Binary','id':'OpenCodeInstallerHost','signature':{'untrusted_fixture':'not consumed'}}])
            self.assertEqual(build.journey_payload_roles(receipt,payload),original)
            payload['files'][0]['id']='RelocatedId'
            # Exact original path/pin selects the actual File ID, never its name.
            self.assertEqual(build.journey_payload_roles(receipt,payload)['members'][next(iter(receipt['payload']))]['id'],'RelocatedId')

    def test_journey_roles_accept_pre_inventory_payload_receipt(self):
        with tempfile.TemporaryDirectory() as directory,patch.dict(build.UPSTREAM,cli_exe_sha256=hashlib.sha256(b'official CLI fixture').hexdigest()):
            receipt,payload=self.journey_fixture(Path(directory)/'prepared')
            inventory = build.PLUGIN_INVENTORY_MEMBER
            receipt['payload'].pop(inventory)
            payload['files'] = [row for row in payload['files']
                                if row['path'] != build.PAYLOAD_DIRECTORY + inventory]
            self.assertNotIn(inventory, build.journey_payload_roles(receipt,payload)['members'])

    def test_source_owned_host_policy_is_fresh_and_used_by_packaging(self):
        policy=build.source_owned_host_policy()
        self.assertEqual(set(policy),{'name','sha256','bytes','arp_id','arp_version','arp_win64','download_url'})
        self.assertEqual(policy['sha256'],build.UPSTREAM['desktop']['sha256'])
        self.assertEqual(policy['bytes'],build.UPSTREAM['desktop']['bytes'])
        policy['name']='foreign.exe'
        self.assertEqual(build.source_owned_host_policy()['name'],'opencode-desktop-win-x64-1.18.34.exe')
        with tempfile.TemporaryDirectory() as directory:
            out=Path(directory)/'out';desktop,inputs=self.prepared_fixture(out)
            pin=build.file_pin(desktop)
            selected=dict(build.source_owned_host_policy(),name='selected-host.exe',arp_id='selected-registration',download_url='https://example.invalid/selected')
            def wix(args,**kwargs):Path(args[args.index('-o')+1]).write_bytes(b'inert MSI')
            with patch.dict(build.UPSTREAM,desktop=pin),patch.object(build,'source_owned_host_policy',return_value=selected) as getter,patch.object(build.subprocess,'run',side_effect=wix),patch.object(build.readback,'verify',return_value={'payload':{}}),patch.object(build,'build_native',side_effect=AssertionError('compiler')):
                receipt=json.loads((out/'payload-receipt.json').read_text());receipt['upstream']=build.UPSTREAM
                (out/'payload-receipt.json').write_text(json.dumps(receipt))
                build.build_msi(out,'0.8.0','unused','wix',desktop=desktop,native_inputs=inputs)
            getter.assert_called_once_with()
            host=json.loads((out/'bundle-app.json').read_text())['host']
            self.assertEqual({key:host[key] for key in selected if key!='name'},{key:value for key,value in selected.items() if key!='name'})
            self.assertEqual(Path(host['file']).name,selected['name'])
            self.assertEqual(Path(host['file']).read_bytes(),desktop.read_bytes())
            self.assertIn('preflight',host)

    def test_source_roles_selects_source_ids_and_exact_upstream_members(self):
        own, supplier, external = build.source_roles()
        self.assertEqual(own, (('Binary','OpenCodeInstallerHost','installer_host.dll'),))
        self.assertEqual((supplier,external),((),('desktop_preflight.exe',)))
        with tempfile.TemporaryDirectory() as directory:
            cli=Path(directory)/'cli.zip'
            with zipfile.ZipFile(cli,'w') as target:target.writestr('opencode.exe',b'MZ supplier')
            pin=build.file_pin(cli)
            native='plugin/0.8.0/node_modules/@openabstractions/ipc-win32-x64/'
            payload={native+'abstraction_ipc.dll':{'sha256':'a'*64,'bytes':11},
                     native+'oa_ipc_node.node':{'sha256':'b'*64,'bytes':12},
                     'cli/opencode.exe':{'sha256':hashlib.sha256(b'MZ supplier').hexdigest(),'bytes':11}}
            prefix='bootstrap/config/node_modules/@msgpackr-extract/msgpackr-extract-win32-x64/'
            payload.update({prefix+'node.abi115.node':{'sha256':'300b188fb8c997214cb65bf1b00729da293176c7d57035e40c2f432ea384dd89','bytes':112640},
                            prefix+'node.napi.node':{'sha256':'6958541c624784dfcebfc4f3c360c57f71cfeedd0530c2393845061729081a08','bytes':112640},
                            'bootstrap/cache/bin/rg.exe':build.RIPGREP['executable'],
                            'desktop-fix/watcher.node':build.DESKTOP_FIX['files']['watcher.node']})
            receipt={'plugin':{'version':'0.8.0'},'payload':payload}
            rows=[dict(id='actual'+str(index),path='LocalAppDataFolder/Programs/OpenAbstractions/apps/opencode/'+name,**value)
                  for index,(name,value) in enumerate(payload.items())]
            with patch.object(build,'check_prepared',return_value=receipt),patch.dict(build.UPSTREAM,cli=pin,cli_exe_sha256=payload['cli/opencode.exe']['sha256']):
                own,supplier,external=build.source_roles(Path(directory),cli,{'files':rows})
                self.assertEqual(own[1:],tuple(('File','actual'+str(i),'payload/'+name) for i,name in enumerate(list(payload)[:2])))
                self.assertEqual(supplier[0],('File','actual2',payload['cli/opencode.exe']))
                self.assertEqual(len(supplier),5)
                self.assertTrue(all(row[2]['source']['version']==build.UPSTREAM['version'] for row in supplier[1:4]))
                # OA's unsigned watcher build is a pinned content role with its source commits.
                self.assertEqual(supplier[4][1],'actual6')
                self.assertEqual(supplier[4][2]['source']['build_commit'],build.DESKTOP_FIX['source']['build_commit'])
                self.assertEqual({key:supplier[4][2][key] for key in ('sha256','bytes')},build.DESKTOP_FIX['files']['watcher.node'])
                facade='plugin/0.8.0/node_modules/@openabstractions/facade-native-win32-x64/'
                for name in build.FACADE_NATIVE_MEMBERS:
                    payload[facade+name]={'sha256':'e'*64,'bytes':13}
                plugin='plugin/0.8.0/node_modules/@msgpackr-extract/msgpackr-extract-win32-x64/'
                for name in ('node.abi115.node','node.napi.node'):
                    payload[plugin+name]=dict(payload[prefix+name])
                rows=[dict(id='actual'+str(index),path='LocalAppDataFolder/Programs/OpenAbstractions/apps/opencode/'+name,**value)
                      for index,(name,value) in enumerate(payload.items())]
                own,supplier,_=build.source_roles(Path(directory),cli,{'files':rows})
                self.assertEqual(len(payload),11)
                self.assertEqual(len(own),5) # Four OA native files and the installer Binary.
                self.assertEqual(len(supplier),7) # CLI plus six exact content occurrences.
                self.assertEqual({row[1] for row in own[1:]}|{row[1] for row in supplier}, {row['id'] for row in rows})
                for variant in ('missing','duplicate','path','pin','prepared-cli','archive'):
                    changed=[dict(row) for row in rows]
                    original=receipt['payload']['cli/opencode.exe']
                    if variant=='missing':changed.pop(0)
                    if variant=='duplicate':changed.append(dict(changed[0]))
                    if variant=='path':changed[0]['path']='INSTALLFOLDER/foreign.dll'
                    if variant=='pin':changed[0]['sha256']='c'*64
                    if variant=='prepared-cli':receipt['payload']['cli/opencode.exe']={'sha256':'d'*64,'bytes':11}
                    if variant=='archive':cli.write_bytes(b'foreign archive')
                    with self.subTest(variant=variant),self.assertRaises(ValueError):
                        build.source_roles(Path(directory),cli,{'files':changed})
                    receipt['payload']['cli/opencode.exe']=original

    def test_readback_exports_actual_closed_file_and_binary_identities(self):
        import types
        source=Path(__file__).with_name('readback.py')
        receiver=types.ModuleType('opencode_readback_fixture')
        exec(compile(source.read_bytes(),str(source),'exec'),receiver.__dict__)
        for variant in ('valid','icons','manifest_changed_bytes','manifest_wrong_folder','unanchored','duplicate_icon','foreign_icon','unsafe_icon','missing_icon','duplicate_id','duplicate_path','outside','extra_binary'):
            with self.subTest(variant=variant),tempfile.TemporaryDirectory() as temporary:
                root=Path(temporary);msi=root/'actual.msi';msi.write_bytes(b'inert container')
                host=root/'host.dll';host.write_bytes(b'inert action');manifest=root/'manifest.json';manifest.write_bytes(b'{}')
                pin=lambda data:{'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()}
                def decompile(*args,**kwargs):
                    dest=root/'readback';extracted=dest/'extracted'
                    (extracted/'File').mkdir(parents=True);(extracted/'Binary').mkdir()
                    for identifier,data in [('ActualPayloadId',b'payload'),('application.manifest',b'{}')]:
                        (extracted/'File'/identifier).write_bytes(data)
                    (extracted/'Binary/OpenCodeInstallerHost').write_bytes(host.read_bytes())
                    if variant=='manifest_changed_bytes':
                        manifest.write_bytes(b'changed staging bytes');(extracted/'File/application.manifest').write_bytes(b'changed staging bytes')
                    extra={'duplicate_id':'<File Id="ActualPayloadId" Name="other"/>',
                           'duplicate_path':'<File Id="OtherId" Name="client.node"/>'}.get(variant,'')
                    outside='<File Id="Outside" Name="outside"/>' if variant=='outside' else ''
                    binary='<Binary Id="Unlisted"/>' if variant=='extra_binary' else ''
                    icons=''
                    if variant in ('icons','duplicate_icon','foreign_icon','missing_icon'):
                        icons='<Icon Id="ActualIconId"/>'
                        if variant!='missing_icon':
                            (extracted/'Icon').mkdir();(extracted/'Icon/ActualIconId').write_bytes(b'inert icon')
                        if variant=='duplicate_icon':icons+='<Icon Id="ActualIconId"/>'
                        if variant=='foreign_icon':(extracted/'Icon/ForeignIcon').write_bytes(b'foreign')
                    if variant=='unsafe_icon':icons='<Icon Id="../outside"/>'
                    (dest/'package.wxs').write_text('<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs"><Package Version="0.8.0" Scope="perUser">'+outside+
                        '<StandardDirectory Id="LocalAppDataFolder"><Directory Id="Programs" Name="Programs"><Directory Id="OA" Name="OpenAbstractions"><Directory Id="Apps" Name="apps"><Directory Id="INSTALLFOLDER" Name="opencode"><File Id="ActualPayloadId" Name="client.node"/>'+extra+'<Component Id="ApplicationManifest"><File Id="application.manifest" Name="openabstractions-application.json"/></Component></Directory></Directory></Directory></Directory></StandardDirectory><Binary Id="OpenCodeInstallerHost"/>'+binary+icons+'</Package></Wix>')
                    if variant=='manifest_wrong_folder':
                        document=dest/'package.wxs';document.write_text(document.read_text().replace('<Component Id="ApplicationManifest">','<Directory Id="Foreign" Name="foreign"><Component Id="ApplicationManifest">').replace('</Component>','</Component></Directory>'))
                    if variant=='unanchored':
                        document=dest/'package.wxs'
                        document.write_text(document.read_text().replace('<StandardDirectory Id="LocalAppDataFolder">','').replace('</StandardDirectory>',''))
                install=['RollbackOpenCodeInstall','InstallOpenCodeIntegration','CommitOpenCodeInstall']
                remove=['RollbackOpenCodeRemove','RemoveOpenCodeIntegration','CommitOpenCodeRemove']
                positions={'CheckOpenCodeClosed':1,'InstallInitialize':2,'InstallFiles':3,
                    'RollbackOpenCodeInstall':4,'InstallOpenCodeIntegration':5,'CommitOpenCodeInstall':6,
                    'RollbackOpenCodeRemove':7,'RemoveOpenCodeIntegration':8,'RemoveFiles':9,'CommitOpenCodeRemove':10,'InstallFinalize':11}
                positions={name:position*10 for name,position in positions.items()};positions['RemoveExistingProducts']=25
                positions['RegisterApplication']=positions['InstallFinalize']+1
                def table(_msi,query,columns):
                    if columns==4:return [[name,str(kind),'OpenCodeInstallerHost',entry] for name,(entry,kind) in receiver.ACTIONS.items()]
                    conditions={name:'NOT REMOVE AND NOT UPGRADINGPRODUCTCODE' for name in install}
                    conditions.update({name:'REMOVE="ALL" AND NOT UPGRADINGPRODUCTCODE' for name in remove})
                    conditions['RegisterApplication']='OaPanel AND NOT REMOVE AND NOT UPGRADINGPRODUCTCODE AND NOT OAKEEPMAINTENANCE'
                    return [[name,conditions.get(name,'NOT UPGRADINGPRODUCTCODE'),str(position)] for name,position in positions.items()]
                with patch.object(receiver.subprocess,'run',side_effect=decompile),patch.object(receiver,'table',side_effect=table):
                    manifest_pin=dict(pin(b'{}'),name=build.REGISTRATION_MANIFEST)
                    if variant not in ('valid','icons'):
                        with self.assertRaises(ValueError):receiver.verify('inert-wix',msi,root,{'client.node':pin(b'payload')},host,manifest,'0.8.0',manifest_pin)
                        continue
                    result=receiver.verify('inert-wix',msi,root,{'client.node':pin(b'payload')},host,manifest,'0.8.0',manifest_pin)
                expected=result['expected_payload']
                self.assertEqual(expected['msi'],dict(pin(msi.read_bytes()),name=msi.name))
                prefix='LocalAppDataFolder/Programs/OpenAbstractions/apps/opencode/'
                self.assertEqual(expected['files'],[dict(pin(b'payload'),id='ActualPayloadId',path=prefix+'client.node'),dict(pin(b'{}'),id='application.manifest',path=prefix+build.REGISTRATION_MANIFEST)])
                self.assertEqual(expected['binaries'],[dict(pin(host.read_bytes()),id='OpenCodeInstallerHost')])
                self.assertEqual(expected['icons'],[dict(pin(b'inert icon'),id='ActualIconId')] if variant=='icons' else [])

    def test_default_entrypoint_keeps_legacy_prepare_then_msi_order(self):
        events=[]
        with patch.object(build,'prepare',side_effect=lambda *a:events.append('prepare')), \
             patch.object(build,'build_msi',side_effect=lambda *a:events.append('msi')):
            self.assertEqual(build.main(['--plugin','fixture.tgz','--plugin-sha256','a'*64,'--plugin-bytes','1',
                '--cli','cli.zip','--parser-tgz','parser.tgz','--bootstrap','bootstrap-inputs','--ripgrep','rg.zip','--out','unused','--version','0.4.956','--qa']),0)
        self.assertEqual(events,['prepare','msi'])

    def test_native_preparation_records_original_outputs_with_source_header_closure(self):
        with tempfile.TemporaryDirectory() as directory:
            out=Path(directory)/'out';_desktop,inputs=self.prepared_fixture(out)
            compiled={'host':Path(inputs['host']['path']),'preflight':Path(inputs['preflight']['path']),
                      'native_command':['compiler','original host'],'preflight_command':['compiler','original preflight']}
            with patch.object(build,'build_native',return_value=compiled) as compiler:
                build.prepare_native(out,'0.8.0','compiler')
            compiler.assert_called_once()
            receipt=json.loads((out/'payload-receipt.json').read_text())
            self.assertEqual(receipt['prepared_native']['host'],inputs['host'])
            self.assertEqual(receipt['prepared_native']['preflight'],inputs['preflight'])
            self.assertEqual(receipt['prepared_native']['version'],'0.8.0')
            self.assertEqual(receipt['native_source_hashes'],build.native_source_hashes())
            self.assertEqual(receipt['native_source_hashes']['shared/user_token.h'],hashlib.sha256(build.shared_native_header().read_bytes()).hexdigest())

    def test_prepare_binds_registration_manifest_before_native_signing(self):
        build.app_builder()
        with tempfile.TemporaryDirectory() as directory:
            out=Path(directory)/'prepared';cli=io.BytesIO()
            with zipfile.ZipFile(cli,'w') as target:target.writestr('opencode.exe',b'official CLI fixture')
            def pinned(path,*args):return cli.getvalue() if path=='cli' else b'inert input'
            with patch.object(build,'pinned',side_effect=pinned),patch.object(build,'plugin_files',return_value=({'version':'0.8.0'},{'package.json':b'{}'})),patch.object(build,'package_files',return_value=({'name':'jsonc-parser','version':'3.3.1'},{})),patch.object(build,'bootstrap_payload',return_value=({'bootstrap/files.json':b'{}','bootstrap/config/package.json':b'fixture leaf'}, bootstrap_metadata())),patch.object(build,'desktop_fix_payload',side_effect=desktop_fix_fixture),patch.dict(build.UPSTREAM,cli_exe_sha256=hashlib.sha256(b'official CLI fixture').hexdigest()):
                receipt=build.prepare('plugin','0'*64,1,'cli','parser',out)
            manifest=build.registration_manifest();header=(out/'host_inputs.h').read_text()
            self.assertEqual(receipt['registration_manifest'],{'name':build.REGISTRATION_MANIFEST,'sha256':hashlib.sha256(manifest).hexdigest(),'bytes':len(manifest)})
            self.assertIn('#define OA_REGISTRATION_MANIFEST_NAME L"'+build.REGISTRATION_MANIFEST+'"',header)
            self.assertIn(receipt['registration_manifest']['sha256'],header)
            self.assertIn('#define OA_BOOTSTRAP_INVENTORY_SHA256 L"'+receipt['payload']['bootstrap/files.json']['sha256']+'"',header)
            self.assertIn(r'bootstrap\\files.json',header)
            self.assertNotIn(r'bootstrap\\config\\package.json',header)
            self.assertIn('bootstrap/config/package.json',receipt['payload'])
            self.assertIn('#define OA_PLUGIN_INVENTORY_SHA256 L"'+receipt['payload']['installer/plugin-files.json']['sha256']+'"',header)
            with zipfile.ZipFile(out/'integration-payload.zip') as prepared:
                plugin_index=json.loads(prepared.read('installer/plugin-files.json'))
            self.assertEqual(plugin_index,{name.removeprefix('plugin/0.8.0/'):pin for name,pin in receipt['payload'].items()
                                           if name.startswith('plugin/0.8.0/')})
            # The Desktop fix enters the payload and the host's trusted table.
            self.assertEqual({name for name in receipt['payload'] if name.startswith('desktop-fix/')},set(build.DESKTOP_FIX['members']))
            self.assertIn('{L"desktop-fix\\\\watcher.node", "',header)
            self.assertIn('{L"desktop-fix\\\\LICENSE", "',header)
            self.assertEqual(receipt['desktop_fix']['source'],build.DESKTOP_FIX['source'])
            # The Desktop program OpenCode runs, which the guard verifies and
            # records (P-J1a ruling 2026-11-01); the private CLI only helps install.
            self.assertEqual(json.loads(manifest)['program'],'{local_app_data}/Programs/@opencode-aidesktop/OpenCode.exe')
            self.assertEqual(receipt['host_inputs_sha256'],hashlib.sha256((out/'host_inputs.h').read_bytes()).hexdigest())

    def test_selected_shared_token_header_is_required_compiled_and_source_bound(self):
        owner=build.app_builder()
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);shared=root/'redist/windows/apps';shared.mkdir(parents=True)
            selected=shared/'build_app.py';selected.write_bytes(b'owned builder identity fixture')
            header=shared/'user_token.h';out=root/'native';out.mkdir()
            identity_fixture(shared)
            with patch.object(owner,'__file__',str(selected)),patch.object(build.subprocess,'run') as process:
                with self.assertRaisesRegex(ValueError,'user_token.h'):build.build_native(out,'0.8.0','cl.exe')
                process.assert_not_called()
                header.write_bytes(b'raw selected shared token header\r\n')
                original=Path.is_symlink
                with patch.object(Path,'is_symlink',lambda path:True if path==header else original(path)):
                    with self.assertRaisesRegex(ValueError,'user_token.h'):build.native_source_hashes()
                compiled=build.build_native(out,'0.8.0','cl.exe')
                for command in (compiled['native_command'],compiled['preflight_command']):
                    self.assertIn('/I'+str(shared),command)
                    self.assertLess(command.index('/I'+str(shared)),command.index('/I'+str(out)))
                self.assertEqual(build.native_source_hashes()['shared/user_token.h'],hashlib.sha256(header.read_bytes()).hexdigest())
                prepared=root/'prepared';self.prepared_fixture(prepared)
                header.write_bytes(b'changed after native preparation')
                process.reset_mock()
                with self.assertRaisesRegex(ValueError,'source or official host closure'):build.check_prepared(prepared)
                process.assert_not_called()

    def test_selected_header_wins_source_and_output_shadows_and_detects_compile_drift(self):
        owner=build.app_builder();original=Path(build.__file__).parent
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);shared=root/'redist/windows/apps';shared.mkdir(parents=True);(shared/'build_app.py').write_bytes(b'owner')
            identity_fixture(shared)
            header=shared/'user_token.h';header.write_bytes(b'selected exact token header')
            source=root/'source';source.mkdir();(source/'build_installer.py').write_bytes(b'fixture identity')
            for name in build.NATIVE_SOURCES:
                (source/name).parent.mkdir(parents=True,exist_ok=True)
                shutil.copyfile(original/name,source/name)
            (source/'user_token.h').write_bytes(b'foreign source sibling')
            self.assertIn('#include <user_token.h>',(source/'installer_host.c').read_text())
            for variant in ('shadow','drift'):
                out=root/variant;out.mkdir();(out/'user_token.h').write_bytes(b'foreign output header')
                (out/'host_inputs.h').write_bytes(b'normal SDK-derived prepared header')
                selected=[]
                def compiler(args,**kwargs):
                    if args[0]=='cl.exe':
                        include=[Path(arg[2:]) for arg in args if arg.startswith('/I')]
                        resolved=next(path/'user_token.h' for path in include if (path/'user_token.h').is_file())
                        selected.append(resolved)
                        self.assertEqual(resolved,header)
                        if variant=='drift':header.write_bytes(b'changed selected token header')
                with patch.object(owner,'__file__',str(shared/'build_app.py')),patch.object(build,'__file__',str(source/'build_installer.py')),patch.object(build.subprocess,'run',side_effect=compiler):
                    if variant=='drift':
                        with self.assertRaisesRegex(ValueError,'changed during compilation'):build.build_native(out,'0.8.0','cl.exe')
                    else:build.build_native(out,'0.8.0','cl.exe')
                self.assertEqual(selected,[header,header])

    def test_generated_header_shadows_refuse_before_compiler(self):
        owner=build.app_builder()
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);shared=root/'redist/windows/apps';shared.mkdir(parents=True);(shared/'build_app.py').write_bytes(b'owner')
            identity_fixture(shared)
            (shared/'user_token.h').write_bytes(b'exact selected header')
            source=root/'source';source.mkdir();(source/'build_installer.py').write_bytes(b'identity')
            out=root/'out';out.mkdir();(out/'host_inputs.h').write_bytes(b'normal prepared header')
            for parent in (source,shared):
                for kind in ('file','directory'):
                    with self.subTest(parent=parent,kind=kind):
                        shadow=parent/'host_inputs.h'
                        if kind=='file':shadow.write_bytes(b'foreign shadow')
                        else:shadow.mkdir()
                        try:
                            with patch.object(owner,'__file__',str(shared/'build_app.py')),patch.object(build,'__file__',str(source/'build_installer.py')),patch.object(build.subprocess,'run') as process:
                                with self.assertRaisesRegex(ValueError,'conflicting host_inputs.h'):build.build_native(out,'0.8.0','cl.exe')
                                process.assert_not_called()
                        finally:
                            if shadow.is_dir():shadow.rmdir()
                            else:shadow.unlink()

    def prepared_fixture(self, out):
        out.mkdir()
        payload={'installer/empty-bunfig.toml':b''}
        with zipfile.ZipFile(out/'integration-payload.zip','w') as target:
            for name,data in payload.items():target.writestr(name,data)
        (out/'host_inputs.h').write_bytes(b'fixture final SDK-derived header')
        receipt={'upstream':build.UPSTREAM,'bootstrap':bootstrap_metadata(),'plugin':{'version':'0.8.0'},'app_owned_source_overrides':{},
                 'registration_manifest':{'name':build.REGISTRATION_MANIFEST,'bytes':len(build.registration_manifest()),'sha256':hashlib.sha256(build.registration_manifest()).hexdigest()},
                 'payload':{n:{'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()} for n,data in payload.items()},
                 'zip_sha256':hashlib.sha256((out/'integration-payload.zip').read_bytes()).hexdigest(),
                 'host_inputs_sha256':hashlib.sha256((out/'host_inputs.h').read_bytes()).hexdigest(),
                 'native_source_hashes':build.native_source_hashes(),
                 'prepared_native':{'version':'0.8.0','host':{'sha256':'a'*64},'preflight':{'sha256':'b'*64},
                                    'native_command':['cl.exe','original unsigned preparation'],
                                    'preflight_command':['cl.exe','original unsigned preparation']}}
        (out/'payload-receipt.json').write_text(json.dumps(receipt),encoding='utf-8')
        host=out/'signed-host.dll';host.write_bytes(b'MZ final signed action fixture')
        preflight=out/'signed-preflight.exe';preflight.write_bytes(b'MZ final signed preflight fixture')
        desktop=out/'desktop.exe';desktop.write_bytes(b'pinned official desktop fixture')
        inputs={name:build.file_pin(path) for name,path in [('host',host),('preflight',preflight)]}
        return desktop,inputs

    def test_package_entrypoint_consumes_exact_final_native_bytes_without_compiler(self):
        with tempfile.TemporaryDirectory() as directory:
            out=Path(directory)/'out';desktop,inputs=self.prepared_fixture(out);calls=[]
            def wix(args,**kwargs):
                self.assertEqual(args[0],'fixture-wix');calls.append(args)
                Path(args[args.index('-o')+1]).write_bytes(b'inert compiled container fixture')
            argv=['--phase','package','--out',str(out),'--version','0.8.0','--desktop',str(desktop),'--wix','fixture-wix']
            for flag,key in [('installer-host','host'),('preflight','preflight')]:
                pin=inputs[key];argv+=['--'+flag,pin['path'],'--'+flag+'-sha256',pin['sha256'],'--'+flag+'-bytes',str(pin['bytes'])]
            desktop_pin=dict(bytes=desktop.stat().st_size,sha256=hashlib.sha256(desktop.read_bytes()).hexdigest())
            with patch.dict(build.UPSTREAM,desktop=desktop_pin),patch.object(build,'build_native',side_effect=AssertionError('compiler after signing')), \
                 patch.object(build.subprocess,'run',side_effect=wix),patch.object(build.readback,'verify',return_value={'payload':{}}) as receive:
                # The prepared closure records this exact fixture's official input.
                receipt=json.loads((out/'payload-receipt.json').read_text());receipt['upstream']=build.UPSTREAM
                (out/'payload-receipt.json').write_text(json.dumps(receipt))
                self.assertEqual(build.main(argv),0)
            self.assertEqual(len(calls),1)
            self.assertEqual(Path(receive.call_args.args[4]).read_bytes(),b'MZ final signed action fixture')
            received=json.loads((out/'installer-receipt.json').read_text())
            self.assertEqual(received['fixture_removal'],{'profile':'nsis-current-user','basename':'Uninstall OpenCode.exe'})
            self.assertNotIn('fixture_removal',json.loads((out/'bundle-app.json').read_text())['host'])
            self.assertEqual(received['consumed_native']['host'],inputs['host'])
            self.assertEqual(received['prepared_native']['host']['sha256'],'a'*64)
            self.assertEqual(received['bundle_app_sha256'],hashlib.sha256((out/'bundle-app.json').read_bytes()).hexdigest())

    def test_package_refuses_missing_wrong_native_and_changed_preparation_before_wix(self):
        for variant in ['missing','wrong-hash','wrong-size','header','source','payload','version','bootstrap-version','bootstrap-source','bootstrap-missing']:
            with self.subTest(variant=variant),tempfile.TemporaryDirectory() as directory:
                out=Path(directory)/'out';desktop,inputs=self.prepared_fixture(out)
                if variant=='missing':Path(inputs['host']['path']).unlink()
                elif variant=='wrong-hash':inputs['host']['sha256']='0'*64
                elif variant=='wrong-size':inputs['preflight']['bytes']+=1
                elif variant=='header':(out/'host_inputs.h').write_bytes(b'changed')
                elif variant=='payload':(out/'integration-payload.zip').write_bytes(b'changed')
                elif variant in ['source','version','bootstrap-version','bootstrap-source','bootstrap-missing']:
                    receipt=json.loads((out/'payload-receipt.json').read_text())
                    if variant=='source':receipt['native_source_hashes']['installer_host.c']='0'*64
                    elif variant=='version':receipt['prepared_native']['version']='0.8.1'
                    elif variant=='bootstrap-version':receipt['bootstrap']['version']='1.18.31'
                    elif variant=='bootstrap-source':receipt['bootstrap']['packages'][0]['sha512']='0'*128
                    else:receipt.pop('bootstrap')
                    (out/'payload-receipt.json').write_text(json.dumps(receipt))
                with patch.dict(build.UPSTREAM,desktop=dict(bytes=desktop.stat().st_size,sha256=hashlib.sha256(desktop.read_bytes()).hexdigest())), \
                     patch.object(build.subprocess,'run') as process,patch.object(build,'build_native') as compiler:
                    receipt=json.loads((out/'payload-receipt.json').read_text());receipt['upstream']=build.UPSTREAM
                    (out/'payload-receipt.json').write_text(json.dumps(receipt))
                    with self.assertRaises((ValueError,FileNotFoundError)):
                        build.build_msi(out,'0.8.0','compiler-must-not-run','wix',desktop=desktop,native_inputs=inputs)
                process.assert_not_called();compiler.assert_not_called()

    def test_package_refuses_selected_header_change_during_wix(self):
        owner=build.app_builder();actual=Path(owner.__file__).parent
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);shared=root/'redist/windows/apps';shared.mkdir(parents=True)
            for name in ('build_app.py','app.wxs','user_token.h'):shutil.copyfile(actual/name,shared/name)
            # The redist tree carries the runtime-import owner beside apps/.
            shutil.copyfile(actual.parent/'runtime_imports.py',shared.parent/'runtime_imports.py')
            identity_fixture(shared)
            with patch.object(owner,'__file__',str(shared/'build_app.py')):
                out=root/'out';desktop,inputs=self.prepared_fixture(out)
                def wix(args,**kwargs):
                    Path(args[args.index('-o')+1]).write_bytes(b'inert MSI')
                    (shared/'user_token.h').write_bytes(b'changed after preparation')
                with patch.dict(build.UPSTREAM,desktop=dict(bytes=desktop.stat().st_size,sha256=hashlib.sha256(desktop.read_bytes()).hexdigest())),patch.object(build.subprocess,'run',side_effect=wix),patch.object(build,'build_native',side_effect=AssertionError('no compiler after signing')),patch.object(build.readback,'verify',side_effect=AssertionError('no readback after drift')):
                    receipt=json.loads((out/'payload-receipt.json').read_text());receipt['upstream']=build.UPSTREAM
                    (out/'payload-receipt.json').write_text(json.dumps(receipt))
                    with self.assertRaisesRegex(ValueError,'header changed during packaging'):
                        build.build_msi(out,'0.8.0','inert-cl','inert-wix',desktop=desktop,native_inputs=inputs)
                self.assertFalse((out/'installer-receipt.json').exists())

    def test_package_refuses_manifest_only_mutation_during_wix_or_readback(self):
        for phase in ('wix','readback'):
            with self.subTest(phase=phase),tempfile.TemporaryDirectory() as directory:
                out=Path(directory)/'out';desktop,inputs=self.prepared_fixture(out)
                before=build.native_source_hashes()
                def wix(args,**kwargs):
                    Path(args[args.index('-o')+1]).write_bytes(b'inert MSI')
                    if phase=='wix':(out/build.REGISTRATION_MANIFEST).write_bytes(b'changed materialized manifest only')
                def readback(*args):
                    if phase!='readback':raise AssertionError('no readback after manifest pin failure')
                    Path(args[5]).write_bytes(b'changed during readback only')
                    return {'payload':{}}
                with patch.dict(build.UPSTREAM,desktop=dict(bytes=desktop.stat().st_size,sha256=hashlib.sha256(desktop.read_bytes()).hexdigest())),patch.object(build.subprocess,'run',side_effect=wix),patch.object(build,'build_native',side_effect=AssertionError('no compiler after signing')),patch.object(build.readback,'verify',side_effect=readback):
                    receipt=json.loads((out/'payload-receipt.json').read_text());receipt['upstream']=build.UPSTREAM
                    (out/'payload-receipt.json').write_text(json.dumps(receipt))
                    with self.assertRaisesRegex(ValueError,'exact size/hash pin'):
                        build.build_msi(out,'0.8.0','inert-cl','inert-wix',desktop=desktop,native_inputs=inputs)
                self.assertEqual(build.native_source_hashes(),before)
                self.assertFalse((out/'installer-receipt.json').exists())

    def test_public_layout_entrypoint_loads_explicit_existing_shared_owner(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);projected=root/'abstractions/adopters/opencode/installer';projected.mkdir(parents=True)
            shared=root/'redist/windows/apps';shared.mkdir(parents=True)
            helpers=root/'abstractions/scripts';helpers.mkdir();shutil.copyfile(build.file_boundary_source(),helpers/'file_boundary.py')
            for name in ['build_installer.py','readback.py','integration.wxs']:
                shutil.copyfile(Path(__file__).with_name(name),projected/name)
            owner=build.app_builder()
            for name in ['build_app.py','app.wxs','user_token.h']:shutil.copyfile(Path(owner.__file__).with_name(name),shared/name)
            spec=importlib.util.spec_from_file_location('oa_projected_opencode_fixture',projected/'build_installer.py')
            module=importlib.util.module_from_spec(spec)
            with patch.object(sys,'dont_write_bytecode',False):
                exec(compile((projected/'build_installer.py').read_bytes(),str(projected/'build_installer.py'),'exec'),module.__dict__)
            events=[]
            with patch.object(module,'prepare',side_effect=lambda *args:events.append('prepare')), \
                 patch.object(module,'prepare_native',side_effect=lambda *args:events.append('native')):
                with patch.object(sys,'dont_write_bytecode',False):
                    self.assertEqual(module.main(['--phase','prepare-native','--app-builder',str(shared/'build_app.py'),
                        '--plugin','fixture.tgz','--plugin-sha256','a'*64,'--plugin-bytes','1','--cli','fixture.zip',
                        '--parser-tgz','parser.tgz','--bootstrap','bootstrap-inputs','--ripgrep','rg.zip','--out',str(root/'out'),'--version','0.8.0']),0)
            self.assertEqual(events,['prepare','native'])
            self.assertEqual(Path(module.build_app.__file__),shared/'build_app.py')
            self.assertEqual(module.shared_native_header(),shared/'user_token.h')
            self.assertEqual(module.file_boundary_source(),helpers/'file_boundary.py')
            (helpers/'file_boundary.py').unlink()
            with self.assertRaisesRegex(ValueError,'missing plain selected'):
                module.extract_payload(b'unused',root/'missing-helper-output')
            package=module.package_source(root)
            self.assertIn('CheckOpenCodeClosed',package.read_text())
            self.assertFalse((shared/'__pycache__').exists())
            self.assertFalse((projected/'__pycache__').exists())

    @unittest.skipUnless(sys.platform=='win32' and shutil.which('cl.exe'),'requires an x64 MSVC environment')
    def test_native_receivers_on_constrained_stack_and_allocation_failures(self):
        with tempfile.TemporaryDirectory(prefix='oa956-stack-') as directory:
            out=Path(directory);host_inputs_fixture(out)
            fixture=out/'empty';fixture.write_bytes(b'')
            command=['cl.exe','/nologo','/std:c17','/W4','/WX','/I'+str(build.native_identity_header().parent),'/I'+str(build.shared_native_header().parent),'/I'+str(out),'/Fo'+str(out/'stack.obj'),'/Fe'+str(out/'stack.exe'),
                     str(Path(__file__).with_name('test_stack_boundary.c')),'/link','msi.lib','shell32.lib','shlwapi.lib','ole32.lib','uuid.lib','bcrypt.lib','advapi32.lib','userenv.lib']
            compiled=subprocess.run(command,cwd=out,capture_output=True,text=True)
            self.assertEqual(compiled.returncode,0,compiled.stdout+compiled.stderr)
            received=subprocess.run([str(out/'stack.exe'),str(fixture),str(out)],cwd=out,capture_output=True,text=True,timeout=60)
            self.assertEqual(received.returncode,0,received.stdout+received.stderr)
            self.assertIn('subjects=7',received.stdout)
            self.assertIn('failures=0',received.stdout)
            # The closed-Desktop guard: an image this account cannot open is
            # skipped (5), the real process walk passes (6) and refuses once the
            # running test program stands in for the Desktop (7).
            for variant,result in ((5,1),(6,1),(7,0)):
                self.assertIn(f'process guard variant={variant} clean=1 result={result} expected={result}',received.stdout)
            self.assertIn('launcher context PASS:',received.stdout)
            request=json.loads(next(line.removeprefix('launcher_request=') for line in received.stdout.splitlines() if line.startswith('launcher_request=')))
            self.assertEqual(request,dict(operation='install',directory='directory',stage='stage',plugin='plugin',
                cli='cli',state='state',work='work',cache='cache',mcpLauncher=str(out/'selected OA tools/oa-opencode-integration.exe')))
            child_command=[*command]
            child_command[child_command.index('/Fo'+str(out/'stack.obj'))]='/Fo'+str(out/'child.obj')
            child_command[child_command.index('/Fe'+str(out/'stack.exe'))]='/Fe'+str(out/'child.exe')
            child_command[child_command.index(str(Path(__file__).with_name('test_stack_boundary.c')))]=str(Path(__file__).with_name('test_child_diagnostic.c'))
            compiled_child=subprocess.run(child_command,cwd=out,capture_output=True,text=True)
            self.assertEqual(compiled_child.returncode,0,compiled_child.stdout+compiled_child.stderr)
            child=subprocess.run([str(out/'child.exe')],cwd=out,capture_output=True,text=True,timeout=30)
            self.assertEqual(child.returncode,0,child.stdout+child.stderr)
            self.assertIn('child diagnostic PASS: success, nonzero, timeout, launch refusal',child.stdout)

    @unittest.skipUnless(sys.platform=='win32' and shutil.which('cl.exe') and shutil.which('wix'),'requires x64 MSVC and WiX')
    def test_an_upgrade_replaces_the_earlier_packages_own_manifest_only(self):
        # preview-1031m over 1031l: the guard accepted only this build's
        # manifest bytes, so every upgrade that changed the manifest was
        # refused. An earlier package's manifest, owned by it at this path with
        # the bytes its package recorded (MsiFileHash), is replaced; a file
        # nobody owns, or one edited after install, stays refused. The earlier
        # package's manifest component has another id, as after a change.
        import msilib
        with tempfile.TemporaryDirectory(prefix='oa-manifest-upgrade-') as directory:
            out=Path(directory);host_inputs_fixture(out)
            def package(name,component,guid,body):
                source=out/name;source.mkdir()
                (source/build.REGISTRATION_MANIFEST).write_bytes(body)
                wxs=source/'package.wxs'
                wxs.write_text('<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs"><Package Name="Manifest fixture" Manufacturer="Fixture" Version="0.8.0" '
                    'UpgradeCode="2B6F3D61-5E1C-4E1B-9C59-0F3D3A3C1E42" Scope="perUser"><MediaTemplate EmbedCab="yes"/><MajorUpgrade AllowSameVersionUpgrades="yes" DowngradeErrorMessage="newer"/>'
                    '<StandardDirectory Id="LocalAppDataFolder"><Directory Id="Root" Name="OaManifestFixture"><Component Id="%s" Guid="%s">'
                    '<File Source="%s" KeyPath="yes"/><RegistryValue Root="HKCU" Key="Software\\OaManifestFixture" Name="x" Value="1" Type="integer"/>'
                    '</Component></Directory></StandardDirectory><Feature Id="Main"><ComponentRef Id="%s"/></Feature></Package></Wix>'
                    %(component,guid,build.REGISTRATION_MANIFEST,component),encoding='utf-8')
                msi=out/(name+'.msi')
                built=subprocess.run(['wix','build','-o',str(msi),str(wxs)],cwd=source,capture_output=True,text=True)
                self.assertEqual(built.returncode,0,built.stdout+built.stderr)
                database=msilib.OpenDatabase(str(msi),msilib.MSIDBOPEN_READONLY)
                def one(query):
                    view=database.OpenView(query);view.Execute(None);return view.Fetch().GetString(1)
                return msi,one("SELECT `Value` FROM `Property` WHERE `Property`='ProductCode'"),one("SELECT `ComponentId` FROM `Component` WHERE `Component`='%s'"%component)
            earlier_bytes=b'{"name":"opencode","program":"{app}/cli/opencode.exe"}'
            current,current_product,current_component=package('current','ApplicationManifest','{6671D328-EE43-55E2-A7BF-BCD11C5D710D}',b'{"name":"opencode","program":"OpenCode.exe"}')
            earlier,earlier_product,earlier_component=package('earlier','EarlierManifest','{3C0F6B4E-1E2A-4D3B-8B67-6A1F2C9E0B11}',earlier_bytes)
            self.assertNotEqual(current_component,earlier_component)
            command=['cl.exe','/nologo','/std:c17','/W4','/WX','/I'+str(build.native_identity_header().parent),'/I'+str(build.shared_native_header().parent),'/I'+str(out),
                     '/I'+str(Path(__file__).parent),'/Fo'+str(out/'guard.obj'),'/Fe'+str(out/'guard.exe'),str(Path(__file__).with_name('test_manifest_upgrade.c')),
                     '/link','msi.lib','shell32.lib','shlwapi.lib','ole32.lib','uuid.lib','bcrypt.lib','advapi32.lib','userenv.lib']
            compiled=subprocess.run(command,cwd=out,capture_output=True,text=True)
            self.assertEqual(compiled.returncode,0,compiled.stdout+compiled.stderr)
            root=out/'integration';root.mkdir()
            manifest=root/build.REGISTRATION_MANIFEST
            def guard(mode,body,stage=None,error=0,directory=None):
                selected=root if directory is None else directory
                target=selected/build.REGISTRATION_MANIFEST
                if body is None:
                    target.unlink(missing_ok=True)
                else:
                    target.write_bytes(body)
                prior,prior_product,prior_component=(shared,shared_product,shared_component) if mode.startswith('retained-') else (earlier,earlier_product,earlier_component)
                command=[str(out/'guard.exe'),str(current),str(prior),str(selected),mode,current_product,current_component,prior_product,prior_component]
                if stage is not None:command.extend((stage,str(error)))
                ran=subprocess.run(command,
                                   cwd=out,capture_output=True,text=True,timeout=60)
                self.assertEqual(ran.returncode,0,ran.stdout+ran.stderr)
                return ran.stdout.strip()
            self.assertEqual(guard('absent',None),'manifest guard mode=absent allowed=1')
            self.assertEqual(guard('upgrade',earlier_bytes),'manifest guard mode=upgrade allowed=1')
            self.assertEqual(guard('edited',earlier_bytes+b' ',stage='manifest_related_size'),'manifest guard mode=edited allowed=0')
            self.assertEqual(guard('unowned',earlier_bytes),'manifest guard mode=unowned allowed=0')
            self.assertEqual(guard('current-edited',earlier_bytes),'manifest guard mode=current-edited allowed=0')
            # Actual recovery counterexample: the earlier product remains a
            # LOCAL client of the current shared component, but related-product
            # lookup has no match. No source-cache package
            # or family claim supplies authority for this fallback.
            current_bytes=build.registration_manifest()
            shared,shared_product,shared_component=package('shared','ApplicationManifest',current_component,current_bytes)
            self.assertEqual(shared_component,current_component)
            self.assertNotEqual(shared_product,current_product)
            (root/(build.REGISTRATION_MANIFEST+'.other')).write_bytes(current_bytes)
            self.assertEqual(guard('retained-local',current_bytes),'manifest guard mode=retained-local allowed=1')
            for mode in ('related-package-missing','related-product-missing'):
                with self.subTest(mode=mode):
                    code=1608 if mode=='related-package-missing' else 1605
                    self.assertEqual(guard(mode,current_bytes,stage='manifest_related_package_missing',error=code),'manifest guard mode='+mode+' allowed=1')
                    self.assertEqual(guard(mode,current_bytes+b' ',stage='manifest_component_bytes'),'manifest guard mode='+mode+' allowed=0')
            self.assertEqual(guard('recovery-plain',current_bytes),'manifest recovery dual-format PASS')
            literal=root/'{[N2_MISSING]} [[[]]]{{}} [ProductCode] [%TEMP] [N2_FORMAT_TEST] [#application.manifest] [$ApplicationManifest] [1]'
            literal.mkdir()
            self.assertEqual(guard('recovery-literal',current_bytes,directory=literal),'manifest recovery dual-format PASS')
            diagnostics={
                'retained-error':('manifest_client_enumeration',5),
                'current-error':('manifest_current_component installstate=-2',0),
                'related-error':('manifest_related_enumeration',5),
                'related-package-error':('manifest_related_package',5),
                'related-component-error':('manifest_related_component installstate=-2',0),
                'retained-current-broken':('manifest_current_component installstate=0',0),
                'related-broken':('manifest_related_component installstate=0',0),
                'retained-client-broken':('manifest_client_component installstate=0',0),
                'retained-reparse':('manifest_not_plain',0),
                'retained-wrong-context':('manifest_client_context',0),
                'retained-wrong-sid':('manifest_client_sid',0),
                'retained-edited':('manifest_component_bytes',0),
                'retained-unowned':('manifest_client_missing',0),
                'retained-limit':('manifest_client_limit',0)}
            for mode in ('retained-edited','retained-unowned','retained-wrong-component',
                         'retained-wrong-sid','retained-wrong-context','retained-wrong-path',
                         'retained-reparse','retained-error','retained-limit','current-error',
                         'related-error','related-package-error','related-component-error',
                         'retained-current-broken','related-broken','retained-client-broken'):
                body=current_bytes+b' ' if mode=='retained-edited' else current_bytes
                with self.subTest(mode=mode):
                    site,code=diagnostics.get(mode,(None,0))
                    self.assertEqual(guard(mode,body,stage=site,error=code),'manifest guard mode='+mode+' allowed=0')

    @unittest.skipUnless(sys.platform=='win32' and shutil.which('cl.exe'),'requires an x64 MSVC environment')
    def test_the_preflight_never_waits_on_a_box_without_ui(self):
        # Windows Sandbox, 2026-11-01: a quiet install run as administrator
        # waited 21 minutes on the preflight's refusal box and failed 0x643.
        # Without UI (--quiet) and for any unknown argument it returns its exit
        # code at once; Setup run as administrator is told so by name.
        with tempfile.TemporaryDirectory(prefix='oa-preflight-quiet-') as directory:
            out=Path(directory);host_inputs_fixture(out)
            common=['/nologo','/std:c17','/W4','/WX','/I'+str(build.native_identity_header().parent),'/I'+str(build.shared_native_header().parent),'/I'+str(out),'/I'+str(Path(__file__).parent)]
            libraries=['/link','msi.lib','shell32.lib','shlwapi.lib','ole32.lib','uuid.lib','bcrypt.lib','advapi32.lib','userenv.lib','user32.lib','version.lib','wintrust.lib','crypt32.lib']
            unit=subprocess.run(['cl.exe',*common,'/Fo'+str(out/'unit.obj'),'/Fe'+str(out/'unit.exe'),str(Path(__file__).with_name('test_preflight_quiet.c')),*libraries],cwd=out,capture_output=True,text=True)
            self.assertEqual(unit.returncode,0,unit.stdout+unit.stderr)
            ran=subprocess.run([str(out/'unit.exe')],cwd=out,capture_output=True,text=True,timeout=30)
            self.assertEqual(ran.returncode,0,ran.stdout+ran.stderr);self.assertIn('preflight quiet PASS',ran.stdout)
            program=subprocess.run(['cl.exe',*common,'/Fo'+str(out/'preflight.obj'),'/Fe'+str(out/'preflight.exe'),str(Path(__file__).with_name('desktop_preflight.c')),*libraries,'/SUBSYSTEM:WINDOWS'],cwd=out,capture_output=True,text=True)
            self.assertEqual(program.returncode,0,program.stdout+program.stderr)
            # The real program: an unknown argument refuses at once, with no box.
            refused=subprocess.run([str(out/'preflight.exe'),'--unknown'],cwd=out,capture_output=True,text=True,timeout=30)
            self.assertEqual(refused.returncode,1603)
            self.assertEqual(refused.stderr,'OpenCode preflight stage=arguments win32=87 hresult=0x80070057\n')
            self.assertEqual(refused.stdout,'')
            # Windows PowerShell 5 Start-Process appends a space to its argument
            # tail. Use the actual GUI parser in an inert native entrypoint;
            # successful parsing never reaches user/profile/desktop inspection.
            argument_fixture=subprocess.run(['cl.exe',*common,'/DOA_PREFLIGHT_ARGUMENT_FIXTURE','/Fo'+str(out/'arguments.obj'),'/Fe'+str(out/'arguments.exe'),str(Path(__file__).with_name('test_preflight_quiet.c')),*libraries,'/SUBSYSTEM:WINDOWS'],cwd=out,capture_output=True,text=True)
            self.assertEqual(argument_fixture.returncode,0,argument_fixture.stdout+argument_fixture.stderr)
            arguments_exe=str(out/'arguments.exe').replace("'","''")
            arguments_log=str(out/'arguments.stderr').replace("'","''")
            script=(f"$ErrorActionPreference='Stop';$p=Start-Process -FilePath '{arguments_exe}' -ArgumentList @('--quiet') -PassThru -WindowStyle Hidden -RedirectStandardError '{arguments_log}';"
                    "$null=$p.Handle;if(-not $p.WaitForExit(30000)){throw 'Owned argument fixture timed out'};$p.WaitForExit();"
                    "if($null -eq $p.ExitCode -or $p.ExitCode -ne 0){throw 'Owned argument fixture refused valid quiet mode'};$p.Dispose();"
                    f"Get-Content -LiteralPath '{arguments_log}'")
            powershell=Path(os.environ['SystemRoot'])/'System32/WindowsPowerShell/v1.0/powershell.exe'
            launched=subprocess.run([str(powershell),'-NoProfile','-NonInteractive','-Command',script],cwd=out,capture_output=True,text=True,timeout=40)
            self.assertEqual(launched.returncode,0,launched.stdout+launched.stderr)
            self.assertEqual(launched.stdout.strip(),'OpenCode preflight stage=arguments_accepted win32=0 hresult=0x00000000')
            # The existing registration policy fixture uses only owned paths
            # and a mock registry; it asserts each new diagnostic projection.
            policy=subprocess.run(['cl.exe',*common,'/Fo'+str(out/'policy.obj'),'/Fe'+str(out/'policy.exe'),str(Path(__file__).with_name('test_desktop_preflight.c')),*libraries],cwd=out,capture_output=True,text=True)
            self.assertEqual(policy.returncode,0,policy.stdout+policy.stderr)
            canonical=out/'canonical';canonical.mkdir()
            non_directory=out/'plain-file';non_directory.write_bytes(b'')
            left=out/'left';left.mkdir();right=out/'right';right.mkdir()
            received=subprocess.run([str(out/'policy.exe'),str(canonical),str(non_directory),str(left),str(right)],cwd=out,capture_output=True,text=True,timeout=30)
            self.assertEqual(received.returncode,0,received.stdout+received.stderr)
            self.assertIn('desktop preflight PASS',received.stdout)
        source=Path(__file__).with_name('desktop_preflight.c').read_text(encoding='utf-8')
        self.assertIn('if(!ok&&!quiet&&!busy&&!preflight_identity_indeterminate(&diagnostic))MessageBoxW(NULL,preflight_refusal(elevation)',source)
        self.assertIn('GetTokenInformation(primary,TokenElevationType',source)

    def test_the_manifest_names_the_desktop_program_the_guard_and_catalog_name(self):
        # The Panel accepts this program outside {app} only through OA's
        # compiled Catalog's shared official-program policy and the signer
        # it verifies itself; the guard checks the same path at install.
        manifest=json.loads(build.registration_manifest())
        self.assertEqual(manifest['program'],'{local_app_data}/Programs/@opencode-aidesktop/OpenCode.exe')
        host=Path(__file__).with_name('installer_host.c').read_text(encoding='utf-8')
        self.assertIn('combine(desktop,local,L"Programs\\\\@opencode-aidesktop\\\\OpenCode.exe")',host)
        catalog=(build.ROOT/'mcp-gateway'/'product'/'agent_connections.go').read_text(encoding='utf-8')
        self.assertIn('LocalAppData: "Programs/@opencode-aidesktop/OpenCode.exe"',catalog)

    def test_actual_package_composition_roots_the_native_fragment(self):
        with tempfile.TemporaryDirectory() as directory:
            package=ET.parse(build.package_source(Path(directory))).getroot()
            ns={'w':'http://wixtoolset.org/schemas/v4/wxs'}
            refs=package.findall('./w:Package/w:CustomActionRef',ns)
            self.assertEqual([item.get('Id') for item in refs],['CheckOpenCodeClosed'])
            self.assertEqual(package.find('./w:Package/w:MajorUpgrade',ns).get('Schedule'),'afterInstallInitialize')
            component=package.find(".//w:Component[@Id='ApplicationManifest']",ns)
            self.assertEqual(component.get('Directory'),'INSTALLFOLDER')
            self.assertEqual(component.find('w:File',ns).get('Name'),build.REGISTRATION_MANIFEST)
            self.assertIsNone(package.find(".//w:Directory[@Id='OfficialDesktopFolder']",ns))
            record=package.find(".//w:Component[@Id='ApplicationRecord']/w:RegistryValue",ns)
            self.assertEqual(record.get('Value'),'[#application.manifest]')
            fragment=ET.parse(Path(__file__).with_name('integration.wxs')).getroot()
            self.assertIsNotNone(fragment.find("./w:Fragment/w:CustomAction[@Id='CheckOpenCodeClosed']",ns))

    def test_catalog_diagnostics_match_the_native_installer(self):
        source = Path(__file__).parent
        host = (source / 'installer_host.c').read_text(encoding='utf-8')
        fragment = ET.parse(source / 'integration.wxs').getroot()
        policy = (build.ROOT / 'mcp-gateway/product/desktop_diagnostics.go').read_text(encoding='utf-8')
        prefix = re.search(r'Prefix:\s*"([^"]+)"', policy).group(1)
        for record in ('guard stage=[1]', 'child stage=[1]'):
            self.assertIn(f'L"{prefix} {record}', host)
        action_list = re.search(r'Actions:\s*\[\]string\{([^}]+)\}', policy).group(1)
        process_action = re.search(r'const processAction = "([^"]+)"', policy).group(1)
        settings_action = re.search(r'const settingsAction = "([^"]+)"', policy).group(1)
        expected = {process_action, settings_action, *re.findall(r'"([^"]+)"', action_list)}
        actual = {action.get('Id') for action in fragment.iter('{http://wixtoolset.org/schemas/v4/wxs}CustomAction')}
        self.assertEqual(expected, actual)
        worker = (source / 'config-lifecycle.js').read_text(encoding='utf-8')
        worker_exit = re.search(r'const pluginRegistrationConflictExit = (\d+);', worker).group(1)
        panel_exit = re.search(r'SettingsConflictExit:\s*"(\d+)"', policy).group(1)
        self.assertEqual(panel_exit, worker_exit)
        for category in ('FailureStages', 'ChildStages'):
            stages = re.search(rf'{category}:\s*\[\]string\{{([^}}]+)\}}', policy).group(1)
            for stage in re.findall(r'"([^"]+)"', stages):
                self.assertIn(f'L"{stage}"', host)

    def test_verified_deep_member_reaches_existing_extractor_using_filesystem_path_form(self):
        with tempfile.TemporaryDirectory(prefix='oa956-deep-') as directory:
            root=Path(directory).resolve()
            self.assertTrue(root.is_relative_to(Path(tempfile.gettempdir()).resolve()))
            member='/'.join(['node_modules','x'*100,'y'*100,'payload.js'])
            destination=root/'payload'
            target=destination.joinpath(*member.split('/'))
            self.assertGreater(len(str(target)),260)
            data=io.BytesIO()
            with zipfile.ZipFile(data,'w') as archive:archive.writestr(member,b'exact deep package member')
            filesystem_root=Path('\\\\?\\'+str(root)) if sys.platform=='win32' else root
            try:
                build.extract_payload(data.getvalue(),destination)
                self.assertEqual((filesystem_root/'payload').joinpath(*member.split('/')).read_bytes(),b'exact deep package member')
                helper=root/'file_boundary.py';helper.write_bytes(build.file_boundary_source().read_bytes())
                owner=build.app_builder();extractor=owner.extract
                def drift(value,path):
                    extractor(value,path);helper.write_bytes(helper.read_bytes()+b'\n')
                with patch.object(build,'file_boundary_source',return_value=helper),patch.object(owner,'extract',side_effect=drift):
                    with self.assertRaisesRegex(ValueError,'changed during extraction'):
                        build.extract_payload(data.getvalue(),root/'changed-helper-output')
            finally:
                if filesystem_root.exists():shutil.rmtree(filesystem_root)

    def test_native_compiler_and_resource_owner_receive_ordinary_output_paths(self):
        with tempfile.TemporaryDirectory(prefix='oa956-compiler-') as directory:
            calls=[]
            with patch.object(build.subprocess,'run',side_effect=lambda args,**kwargs:calls.append((args,kwargs))):
                build.build_native(Path(directory),'0.4.956','cl.exe')
            self.assertEqual([args[0] for args,_ in calls],['cl.exe','rc.exe','cl.exe'])
            for args,kwargs in calls:
                self.assertNotIn('\\\\?\\',str(kwargs['cwd']))
                for argument in args:self.assertNotIn('\\\\?\\',argument)

    def test_fixed_native_preflight_uses_as_invoker_and_the_same_qualified_host_pin(self):
        root=ET.parse(Path(__file__).with_name('desktop_preflight.manifest')).getroot()
        level=root.find('.//{urn:schemas-microsoft-com:asm.v3}requestedExecutionLevel')
        self.assertEqual(level.attrib,{'level':'asInvoker','uiAccess':'false'})
        self.assertEqual(build.UPSTREAM['version'],'1.18.34')
        self.assertEqual(build.DESKTOP_RELEASES[0]['version'],build.UPSTREAM['version'])
        self.assertEqual(build.DESKTOP_RELEASES[0]['installer'],{key:build.UPSTREAM['desktop'][key] for key in ('sha256','bytes')})
        self.assertEqual(build.DESKTOP_RELEASES[0]['exe_sha256'],build.UPSTREAM['desktop_exe_sha256'])

    def test_desktop_release_list_and_watcher_fix_pins_match_the_native_header(self):
        # Ruling OPENCODE-WATCHER-PIN: exact tested versions, each with its
        # installer, OpenCode.exe and stock watcher hash, in both owners.
        native=Path(__file__).with_name('desktop_state.h').read_text(encoding='utf-8')
        rows=re.findall(r'\{L"([0-9.]+)","([0-9a-f]{64})",(\d+),\s*"([0-9a-f]{64})","([0-9a-f]{64})"\}',native)
        self.assertEqual(rows,[(row['version'],row['installer']['sha256'],str(row['installer']['bytes']),row['exe_sha256'],row['watcher_stock']['sha256'])
                               for row in build.DESKTOP_RELEASES])
        self.assertEqual([row['version'] for row in build.DESKTOP_RELEASES],['1.18.34','1.19.5'])
        self.assertIn('#define OA_DESKTOP_VERSIONS_TEXT L"'+' or '.join(row['version'] for row in build.DESKTOP_RELEASES)+'"',native)
        self.assertIn('#define OA_DESKTOP_WATCHER_PATCHED_SHA256 "'+build.DESKTOP_FIX['files']['watcher.node']['sha256']+'"',native)
        self.assertIn('#define OA_DESKTOP_WATCHER_RELATIVE L"'+build.DESKTOP_FIX['live'].replace('/','\\\\')+'"',native)
        self.assertIn('#define OA_DESKTOP_WATCHER_PAYLOAD L"'+build.DESKTOP_FIX['members'][0].replace('/','\\\\')+'"',native)
        self.assertNotIn(build.DESKTOP_FIX['files']['watcher.node']['sha256'],{row['watcher_stock']['sha256'] for row in build.DESKTOP_RELEASES})
        self.assertEqual(build.DESKTOP_FIX['source'],{'repository':'https://github.com/parcel-bundler/watcher','tag':'v2.5.1',
            'base':'664695dbb2488d53f782c3e2b8791c4c114731fb','pull':277,'pull_commit':'d09df163078cd54e422c8fbacacbf0359be3f52e',
            'build_commit':'052dc8d70871bc73d8ea1a30675f23347d144d98'})
        self.assertIn('desktop_fix.h',build.NATIVE_SOURCES)

    def test_desktop_fix_payload_requires_the_pinned_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            image=portable_executable('watcher.pdb')
            (root/'watcher.node').write_bytes(image);(root/'LICENSE').write_bytes(b'MIT fixture')
            with self.assertRaisesRegex(ValueError,'differs from its exact size/hash pin'):build.desktop_fix_payload(root)
            files={'watcher.node':build.file_pin(root/'watcher.node'),'LICENSE':build.file_pin(root/'LICENSE')}
            files={name:{key:value[key] for key in ('sha256','bytes')} for name,value in files.items()}
            with patch.dict(build.DESKTOP_FIX,files=files):
                payload,receipt=build.desktop_fix_payload(root)
            self.assertEqual(payload,{'desktop-fix/watcher.node':image,'desktop-fix/LICENSE':b'MIT fixture'})
            self.assertEqual(receipt['stock'],['a8199cf7b6c5102267a5889f80321a4cc92e631d7504c474d32a35b4d440f315'])
            (root/'LICENSE').unlink()
            with patch.dict(build.DESKTOP_FIX,files=files),self.assertRaises(OSError):build.desktop_fix_payload(root)
        self.assertEqual(build.desktop_fix_directory().parts[-3:],('.forks','opencode-patched','artifact'))

    def test_supplier_pe_naming_an_absolute_builder_path_is_refused(self):
        for path in ('watcher.pdb','build\\Release\\watcher.pdb'):
            with self.subTest(path=path):
                image=portable_executable(path)
                self.assertEqual(build.pe_debug_paths(image),[path])
                self.assertIs(build.refuse_builder_paths('fixture.node',image),image)
        for path in ('C:\\Users\\someone\\watcher\\build\\Release\\watcher.pdb','D:/a/watcher/watcher.pdb',
                     '\\\\host\\share\\watcher.pdb','/home/someone/watcher.pdb'):
            with self.subTest(path=path),self.assertRaisesRegex(ValueError,'absolute builder path'):
                build.refuse_builder_paths('fixture.node',portable_executable(path))
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);image=portable_executable('C:\\Users\\someone\\watcher.pdb')
            (root/'watcher.node').write_bytes(image);(root/'LICENSE').write_bytes(b'MIT fixture')
            files={name:{key:build.file_pin(root/name)[key] for key in ('sha256','bytes')} for name in ('watcher.node','LICENSE')}
            with patch.dict(build.DESKTOP_FIX,files=files),self.assertRaisesRegex(ValueError,'absolute builder path'):
                build.desktop_fix_payload(root)
        self.assertEqual(build.DESKTOP_FIX['earlier'],('9ec8ac8abf41c8ff2a7cc2298961f2165e14939e8e3a6d9d2aa49757697e4fed',))
        native=Path(__file__).with_name('desktop_state.h').read_text(encoding='utf-8')
        self.assertIn('OA_DESKTOP_WATCHER_EARLIER[]={'+','.join('"'+value+'"' for value in build.DESKTOP_FIX['earlier'])+'}',native)

    @unittest.skipUnless((lambda: build.desktop_fix_directory().is_dir())(),'the pinned artifact exists only in a primary checkout that built it')
    def test_the_pinned_artifact_matches_its_pins(self):
        payload,_=build.desktop_fix_payload()
        self.assertEqual(set(payload),set(build.DESKTOP_FIX['members']))
        self.assertTrue(payload['desktop-fix/watcher.node'].startswith(b'MZ'))

    def test_repair_contract_reaches_the_watcher_step(self):
        # Setup's Repair runs the integration MSI's own repair (msiexec /fomus):
        # README "Repair: the watcher fix after an OpenCode update".
        root=ET.parse(Path(__file__).with_name('integration.wxs')).getroot()
        ns={'w':'http://wixtoolset.org/schemas/v4/wxs'}
        sequence={item.get('Action'):item for item in root.findall('.//w:Custom',ns)}
        actions={item.get('Id'):item for item in root.findall('.//w:CustomAction',ns)}
        self.assertEqual(sequence['InstallOpenCodeIntegration'].get('Condition'),'NOT REMOVE AND NOT UPGRADINGPRODUCTCODE')
        self.assertEqual((actions['InstallOpenCodeIntegration'].get('Execute'),actions['InstallOpenCodeIntegration'].get('DllEntry')),('deferred','Install'))
        self.assertEqual(sequence['CheckOpenCodeClosed'].get('Before'),'InstallInitialize')
        self.assertEqual(sequence['CheckOpenCodeClosed'].get('Condition'),'NOT UPGRADINGPRODUCTCODE')
        self.assertEqual(sequence['RollbackOpenCodeInstall'].get('Condition'),'NOT REMOVE AND NOT UPGRADINGPRODUCTCODE')
        host=Path(__file__).with_name('installer_host.c').read_text(encoding='utf-8')
        install=host[host.index('OA_FIX_RESULT fixed=desktop_fix_install'):host.index('else if(lstrcmpW(operation,L"remove")==0)')]
        # Only a failed write stops the step; patched, other and missing files succeed unchanged.
        self.assertIn('if(fixed==OA_FIX_FAILED){failureError=GetLastError();goto done;}',install)
        self.assertEqual(install.count('goto done'),1)
        self.assertIn('desktop_fix_unknown_left',install)
        # The patched source is the copy this package installs in its own folder.
        self.assertIn('combine(paths[4],root,OA_DESKTOP_WATCHER_PAYLOAD)',host)
        self.assertIn('desktop-fix/watcher.node',build.DESKTOP_FIX['members'])

    def test_setup_catalogue_fixup_names_the_same_releases_and_hashes(self):
        product=Path(__file__).resolve().parents[3]/'mcp-gateway/product'
        if not (product/'desktop_fixups.go').is_file():self.skipTest('this layout carries no product catalogue')
        declaration=json.loads((product/'installer_fixups.json').read_text(encoding='utf-8'))
        self.assertEqual(declaration,build.setup_fixup_declaration())

    @unittest.skipUnless(sys.platform=='win32' and shutil.which('cl.exe'),'requires an x64 MSVC environment')
    def test_desktop_fix_replaces_only_the_stock_watcher(self):
        # The hash-gated swap on real files: stock or an earlier OA build ->
        # patched, already patched, unknown left, stale markers cleared, Rollback
        # and Remove restore, Rollback of Remove re-applies.
        with tempfile.TemporaryDirectory(prefix='oa-desktop-fix-') as directory:
            out=Path(directory);host_inputs_fixture(out)
            common=['/nologo','/std:c17','/W4','/WX','/I'+str(build.native_identity_header().parent),'/I'+str(build.shared_native_header().parent),'/I'+str(out),'/I'+str(Path(__file__).parent)]
            compiled=subprocess.run(['cl.exe',*common,'/Fo'+str(out/'fix.obj'),'/Fe'+str(out/'fix.exe'),str(Path(__file__).with_name('test_desktop_fix.c')),
                                     '/link','msi.lib','shell32.lib','shlwapi.lib','ole32.lib','uuid.lib','bcrypt.lib','advapi32.lib','userenv.lib'],cwd=out,capture_output=True,text=True)
            self.assertEqual(compiled.returncode,0,compiled.stdout+compiled.stderr)
            work=out/'work';work.mkdir()
            ran=subprocess.run([str(out/'fix.exe'),str(work)],cwd=out,capture_output=True,text=True,timeout=60)
            self.assertEqual(ran.returncode,0,ran.stdout+ran.stderr)
            self.assertIn('desktop fix PASS',ran.stdout)

    def test_absent_host_preflight_requires_absent_canonical_destination_before_installer(self):
        native=Path(__file__).with_name('desktop_state.h').read_text(encoding='utf-8')
        preflight=Path(__file__).with_name('desktop_preflight.c').read_text(encoding='utf-8')
        self.assertIn('desktop_check_diagnostic(user,FALSE,program,&diagnostic)',preflight)
        host=Path(__file__).with_name('installer_host.c').read_text(encoding='utf-8')
        self.assertIn('desktop_check_diagnostic(token,TRUE,desktop,&diagnostic)',host)
        self.assertIn('!requireHost&&!state->program&&state->destinationAbsent',native)
        self.assertLess(native.index('desktop_canonical_directory(expected)'),native.index('desktop_destination_absent(GetFileAttributesW,expected)'))
        self.assertIn('if(!state->arp&&!state->location)',native)
        self.assertIn('unregistered_desktop_destination',native)
        self.assertIn('preexisting unregistered destination',preflight)

    def test_cached_msi_actions_run_as_original_user_and_cover_both_lifecycles(self):
        root = ET.parse(Path(__file__).with_name('integration.wxs')).getroot()
        ns = {'w': 'http://wixtoolset.org/schemas/v4/wxs'}
        self.assertEqual(root.find('.//w:Binary', ns).get('Id'), 'OpenCodeInstallerHost')
        actions = {item.get('Id'): item for item in root.findall('.//w:CustomAction', ns)}
        for name, action in actions.items():
            self.assertEqual(action.get('BinaryRef'), 'OpenCodeInstallerHost')
            self.assertEqual(action.get('Return'), 'check')
            if name != 'CheckOpenCodeClosed':
                self.assertEqual(action.get('Impersonate'), 'yes')
        sequence = {item.get('Action'): item for item in root.findall('.//w:Custom', ns)}
        self.assertEqual(sequence['InstallOpenCodeIntegration'].get('After'), 'RollbackOpenCodeInstall')
        self.assertEqual(sequence['RollbackOpenCodeInstall'].get('After'), 'InstallFiles')
        self.assertEqual(sequence['RemoveOpenCodeIntegration'].get('Before'), 'RemoveFiles')
        self.assertEqual(sequence['RollbackOpenCodeRemove'].get('Before'), 'RemoveOpenCodeIntegration')
        for action in sequence.values():
            self.assertIn('NOT UPGRADINGPRODUCTCODE', action.get('Condition'))
        self.assertEqual(actions['CommitOpenCodeRemove'].get('Execute'), 'commit')
        self.assertEqual(actions['CommitOpenCodeInstall'].get('Execute'), 'commit')

    def test_all_names_and_links_are_refused_before_materialization(self):
        for name in ['package/../escape', 'package/C:/escape', 'package/NUL.txt',
                     'package/x.', '/package/file', 'other/file']:
            with self.subTest(name=name), self.assertRaises(ValueError):
                build.plugin_files(archive([(name, b'x', tarfile.REGTYPE)]))
        for kind in [tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.DIRTYPE]:
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                build.plugin_files(archive([('package/file', b'', kind)]))

    def test_case_collisions_refuse_on_every_build_platform(self):
        with self.assertRaisesRegex(ValueError, 'case collision'):
            build.plugin_files(archive([('package/Readme', b'a', tarfile.REGTYPE),
                                        ('package/README', b'b', tarfile.REGTYPE)]))

    def test_exact_input_size_and_hash_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / 'input'
            file.write_bytes(b'input')
            digest = hashlib.sha256(b'input').hexdigest()
            self.assertEqual(build.pinned(file, digest, 5), b'input')
            for wrong_hash, wrong_size in [('0' * 64, 5), (digest, 4)]:
                with self.assertRaises(ValueError):
                    build.pinned(file, wrong_hash, wrong_size)

    def test_source_package_must_carry_its_native_client_and_bundled_closure(self):
        manifest = {'name': '@openabstractions/opencode', 'version': '0.8.0',
                    'files': ['runtime-connector.js'],
                    'exports': {'.': './index.js', './server': './server.js', './tui': './tui.js'},
                    'bundleDependencies': ['@openabstractions/ipc']}
        files = {'package.json': json.dumps(manifest).encode(),
                 **{name: b'' for name in ['runtime-connector.js', 'index.js', 'server.js', 'tui.js',
                     'npm-shrinkwrap.json', 'node_modules/@openabstractions/ipc/package.json']}}
        entries = [('package/' + name, data, tarfile.REGTYPE) for name, data in files.items()]
        with self.assertRaisesRegex(ValueError, 'abstraction_ipc.dll'):
            build.plugin_files(archive(entries))

    def test_installer_bundling_refuses_incomplete_normal_archive_set_before_npm(self):
        manifest={'name':'@openabstractions/opencode','version':'0.8.0','files':['runtime-connector.js'],
                  'exports':{'.':'./index.js','./server':'./server.js','./tui':'./tui.js'}}
        raw=archive([('package/'+name,data,tarfile.REGTYPE) for name,data in {
            'package.json':json.dumps(manifest).encode(),'runtime-connector.js':b'','index.js':b'',
            'server.js':b'','tui.js':b'','npm-shrinkwrap.json':b'{}'}.items()])
        with tempfile.TemporaryDirectory() as temporary,patch.object(build.subprocess,'run') as run:
            root=Path(temporary);packages=root/'packages';packages.mkdir()
            plugin=packages/'plugin.tgz';plugin.write_bytes(raw)
            with self.assertRaisesRegex(ValueError,'incomplete'):
                build.bundle_plugin(plugin,hashlib.sha256(raw).hexdigest(),len(raw),packages,root/'cache',root/'bundle.tgz')
            run.assert_not_called()
            self.assertFalse((root/'bundle.tgz').exists())

    def test_declared_appmodel_native_family_must_be_carried(self):
        manifest={'name':'@openabstractions/opencode','version':'0.8.0','files':['runtime-connector.js'],
                  'exports':{'.':'./index.js','./server':'./server.js','./tui':'./tui.js'},'bundleDependencies':[]}
        files={'package.json':json.dumps(manifest).encode(),**{name:b'' for name in ('runtime-connector.js','index.js','server.js','tui.js','npm-shrinkwrap.json')},
               'node_modules/@openabstractions/ipc-win32-x64/abstraction_ipc.dll':b'inert',
               'node_modules/@openabstractions/ipc-win32-x64/oa_ipc_node.node':b'inert',
               'node_modules/@openabstractions/facade/package.json':json.dumps({'optionalDependencies':{'@openabstractions/facade-native-win32-x64':'0.8.0'}}).encode()}
        packed=lambda:archive([('package/'+name,data,tarfile.REGTYPE) for name,data in files.items()])
        with self.assertRaisesRegex(ValueError,'AppModel'):
            build.plugin_files(packed())
        for name in build.FACADE_NATIVE_MEMBERS:
            files['node_modules/@openabstractions/facade-native-win32-x64/'+name]=b'inert source fixture'
        self.assertEqual(build.plugin_files(packed())[0],manifest)

    def test_prepared_bundle_rechecks_original_archive_before_payload(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);original=root/'original.tgz';original.write_bytes(b'original')
            bundled=root/'bundled.tgz';bundled.write_bytes(b'bundled')
            composition={'path':str(bundled),'sha256':hashlib.sha256(b'bundled').hexdigest(),'bytes':7,'packages':{},
                         'original_plugin':{'path':str(original),'sha256':hashlib.sha256(b'original').hexdigest(),'bytes':8}}
            (root/'payload-receipt.json').write_text(json.dumps({'bundled_plugin':composition}))
            original.write_bytes(b'mutated!')
            with self.assertRaisesRegex(ValueError,'differs'):
                build.check_prepared(root)



class BootstrapPayload(unittest.TestCase):
    def test_source_lock_matches_selected_cli_and_windows_x64(self):
        inputs = build.bootstrap_inputs()
        self.assertEqual(len(inputs), 27)
        helper = next(row for row in inputs if row['name'] == 'node_modules/@opencode-ai/plugin')
        self.assertEqual(helper['version'], build.UPSTREAM['version'])
        self.assertTrue(all('darwin' not in row['name'] and 'linux' not in row['name'] for row in inputs))
        self.assertEqual(len({row['file'] for row in inputs}), len(inputs))
        self.assertTrue(all(len(row['sha512']) == 128 for row in inputs))
        with self.assertRaisesRegex(ValueError, 'required'):
            build.bootstrap_payload(None, None)

    def fixture(self, root):
        package = {'name':'@opencode-ai/plugin','version':build.UPSTREAM['version'],'license':'MIT'}
        tar = archive([('package/package.json',json.dumps(package).encode(),tarfile.REGTYPE),
                       ('package/index.js',b'inert upstream helper',tarfile.REGTYPE),
                       ('package/LICENSE',b'fixture MIT notice',tarfile.REGTYPE)])
        import base64
        integrity = 'sha512-'+base64.b64encode(hashlib.sha512(tar).digest()).decode()
        lock = {'lockfileVersion':3,'packages':{'':{'dependencies':{'@opencode-ai/plugin':build.UPSTREAM['version']}},
                'node_modules/@opencode-ai/plugin':{'version':build.UPSTREAM['version'],'resolved':'https://registry.npmjs.org/@opencode-ai/plugin/-/plugin.tgz','integrity':integrity}}}
        raw_lock = json.dumps(lock).encode()
        inputs = root/'inputs';inputs.mkdir()
        source = inputs/(hashlib.sha256(b'node_modules/@opencode-ai/plugin').hexdigest()+'.tgz')
        source.write_bytes(tar)
        rg = root/'rg.zip';executable=b'inert rg fixture'
        with zipfile.ZipFile(rg,'w') as output:
            for name,raw in [('rg.exe',executable),('COPYING',b'copying'),('LICENSE-MIT',b'MIT'),('UNLICENSE',b'public domain')]:
                output.writestr('fixture/'+name,raw)
        policy=dict(build.RIPGREP,sha256=hashlib.sha256(rg.read_bytes()).hexdigest(),member='fixture/rg.exe',
                    executable={'bytes':len(executable),'sha256':hashlib.sha256(executable).hexdigest()})
        return inputs,source,rg,(raw_lock,lock),policy

    def test_exact_archives_compose_complete_files_and_refuse_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            inputs,source,rg,lock,policy=self.fixture(Path(directory))
            with patch.object(build,'bootstrap_lock',return_value=lock),patch.object(build,'RIPGREP',policy):
                payload,receipt=build.bootstrap_payload(inputs,rg)
                self.assertEqual(receipt['version'],build.UPSTREAM['version'])
                self.assertEqual(payload['bootstrap/config/node_modules/@opencode-ai/plugin/index.js'],b'inert upstream helper')
                self.assertEqual(payload['bootstrap/cache/bin/rg.exe'],b'inert rg fixture')
                inventory=json.loads(payload['bootstrap/files.json'])
                self.assertEqual(set(inventory),{name.removeprefix('bootstrap/') for name in payload if name.startswith(('bootstrap/config/','bootstrap/cache/'))})
                self.assertIn('bootstrap/licenses/ripgrep/LICENSE-MIT',payload)
                self.assertEqual(receipt['packages'][0]['license_files'],['LICENSE'])
                original=source.read_bytes();source.write_bytes(original+b'drift')
                with self.assertRaisesRegex(ValueError,'integrity'):build.bootstrap_payload(inputs,rg)
                source.write_bytes(original)
                extra=inputs/'unselected.tgz';extra.write_bytes(b'foreign')
                with self.assertRaisesRegex(ValueError,'roster'):build.bootstrap_payload(inputs,rg)
                extra.unlink();rg.write_bytes(rg.read_bytes()+b'drift')
                with self.assertRaisesRegex(ValueError,'ripgrep ZIP'):build.bootstrap_payload(inputs,rg)


if __name__ == '__main__':
    unittest.main()
