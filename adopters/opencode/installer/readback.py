"""Read-only receiving checks for this app's compiled MSI; never installs it."""
import ctypes
from ctypes import wintypes as w
import hashlib
import json
from pathlib import Path
import subprocess
import xml.etree.ElementTree as ET

NS='{http://wixtoolset.org/schemas/v4/wxs}'
ACTIONS={'CheckOpenCodeClosed':('CheckDesktopClosed',1),
 'InstallOpenCodeIntegration':('Install',1025),'RemoveOpenCodeIntegration':('Remove',1025),
 'RollbackOpenCodeInstall':('Rollback',1281),'RollbackOpenCodeRemove':('Rollback',1281),
 'CommitOpenCodeInstall':('CommitInstall',1537),'CommitOpenCodeRemove':('CommitRemove',1537)}

def table(msi_path,query,columns):
    api=ctypes.WinDLL('msi.dll')
    api.MsiOpenDatabaseW.argtypes=[w.LPCWSTR,w.LPCWSTR,ctypes.POINTER(w.UINT)]
    api.MsiDatabaseOpenViewW.argtypes=[w.UINT,w.LPCWSTR,ctypes.POINTER(w.UINT)]
    api.MsiViewExecute.argtypes=[w.UINT,w.UINT];api.MsiViewFetch.argtypes=[w.UINT,ctypes.POINTER(w.UINT)]
    api.MsiRecordGetStringW.argtypes=[w.UINT,w.UINT,w.LPWSTR,ctypes.POINTER(w.DWORD)]
    database=w.UINT();view=w.UINT();rows=[]
    def checked(status):
        if status:raise ValueError('MSI read-only query failed: '+str(status))
    checked(api.MsiOpenDatabaseW(str(msi_path),None,ctypes.byref(database)))
    try:
        checked(api.MsiDatabaseOpenViewW(database,query,ctypes.byref(view)));checked(api.MsiViewExecute(view,0))
        while True:
            record=w.UINT();status=api.MsiViewFetch(view,ctypes.byref(record))
            if status==259:break
            checked(status)
            try:
                row=[]
                for i in range(1,columns+1):
                    value=ctypes.create_unicode_buffer(32768);size=w.DWORD(32768)
                    checked(api.MsiRecordGetStringW(record,i,value,ctypes.byref(size)));row.append(value.value)
                rows.append(row)
            finally:api.MsiCloseHandle(record)
    finally:
        if view.value:api.MsiCloseHandle(view)
        api.MsiCloseHandle(database)
    return rows

def verify(wix,msi,out,payload,host,manifest,version,expected_manifest):
    destination=out/'readback';destination.mkdir(exist_ok=False)
    subprocess.run([wix,'msi','decompile',str(msi),'-o',str(destination/'package.wxs'),'-x',str(destination/'extracted')],check=True)
    root=ET.parse(destination/'package.wxs').getroot();package=root.find(NS+'Package')
    if package.get('Version')!=version or package.get('Scope')!='perUser':raise ValueError('MSI version/scope differs')
    expected={name:meta for name,meta in payload.items()}
    manifest_name='openabstractions-application.json'
    manifest_path='LocalAppDataFolder/Programs/OpenAbstractions/apps/opencode/'+manifest_name
    if expected_manifest.get('name')!=manifest_name:raise ValueError('Prepared registration manifest name differs')
    expected[manifest_name]={key:expected_manifest[key] for key in ('bytes','sha256')}
    found={};file_rows=[];file_ids=set();directories={}
    def index_directories(element,path=None):
        if element.tag==NS+'StandardDirectory':path=element.get('Id')
        elif element.tag==NS+'Directory':path=path+'/'+element.get('Name') if path is not None else None
        if element.tag in (NS+'Directory',NS+'StandardDirectory'):directories[element.get('Id')]=path
        for child in element:index_directories(child,path)
    index_directories(root)
    def walk(element,relative=None,declared=None):
        if element.tag==NS+'StandardDirectory':declared=element.get('Id')
        if element.tag==NS+'Directory':
            declared=declared+'/'+element.get('Name') if declared is not None else None
            relative='' if element.get('Id')=='INSTALLFOLDER' else (relative+'/'+element.get('Name') if relative is not None else None)
        if element.tag==NS+'Component' and element.get('Directory'):
            if element.get('Id')=='ApplicationManifest':
                if element.get('Directory')!='INSTALLFOLDER':raise ValueError('MSI registration manifest component differs')
                declared=directories.get('INSTALLFOLDER');relative=''
        if element.tag==NS+'File':
            identifier=element.get('Id')
            actual_path=(declared+'/'+element.get('Name')) if declared is not None else None
            if identifier=='application.manifest':
                if actual_path!=manifest_path:raise ValueError('MSI registration manifest destination differs')
                name=manifest_name
            else:
                if relative is None or declared is None:raise ValueError('MSI File outside anchored INSTALLFOLDER')
                name=(relative+'/'+element.get('Name')).lstrip('/')
            if identifier in file_ids or name in found:raise ValueError('Duplicate MSI File identity/path')
            file_ids.add(identifier)
            source=destination/'extracted/File'/identifier;raw=source.read_bytes()
            found[name]={'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
            file_rows.append(dict(found[name],id=identifier,path=actual_path))
        for child in element:walk(child,relative,declared)
    walk(root)
    if found!=expected:raise ValueError('Actual MSI payload bytes/names differ: '+str(set(found)^set(expected)))
    binary=root.find(".//"+NS+"Binary[@Id='OpenCodeInstallerHost']")
    if [node.get('Id') for node in root.findall('.//'+NS+'Binary')]!=['OpenCodeInstallerHost']:raise ValueError('MSI Binary roster differs')
    binary_path=destination/'extracted/Binary/OpenCodeInstallerHost'
    if binary is None or not binary_path.is_file() or binary_path.read_bytes()!=host.read_bytes():raise ValueError('Native action Binary missing or differs')
    icons=[];icon_ids=set();icon_dir=destination/'extracted/Icon'
    for node in root.findall('.//'+NS+'Icon'):
        identifier=node.get('Id')
        if not identifier or identifier in ('.','..') or any(char in identifier for char in '/\\:') or identifier in icon_ids:
            raise ValueError('Unsafe or duplicate MSI Icon identity')
        icon_ids.add(identifier)
    if icon_dir.is_symlink() or icon_dir.is_junction():raise ValueError('Linked MSI Icon directory')
    if icon_dir.exists():
        entries=list(icon_dir.iterdir())
        if any(entry.is_symlink() or entry.is_junction() or not entry.is_file() for entry in entries):raise ValueError('Non-plain MSI Icon member')
        if {entry.name for entry in entries}!=icon_ids:raise ValueError('Extracted MSI Icon roster differs')
    elif icon_ids:raise ValueError('MSI Icon bytes missing')
    for identifier in sorted(icon_ids):
        raw=(icon_dir/identifier).read_bytes()
        icons.append({'id':identifier,'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)})
    actions={row[0]:row[1:] for row in table(msi,'SELECT `Action`, `Type`, `Source`, `Target` FROM `CustomAction`',4)}
    sequence={row[0]:{'condition':row[1],'sequence':int(row[2])} for row in table(msi,'SELECT `Action`, `Condition`, `Sequence` FROM `InstallExecuteSequence`',3)}
    if sequence.get('RegisterApplication',{}).get('condition')!='OaPanel AND NOT REMOVE AND NOT UPGRADINGPRODUCTCODE AND NOT OAKEEPMAINTENANCE':
        raise ValueError('MSI registration must preserve opt-out during automatic maintenance')
    for name,(entry,kind) in ACTIONS.items():
        if actions.get(name)!=[str(kind),'OpenCodeInstallerHost',entry]:raise ValueError('Native action/token flags differ: '+name)
        if name not in sequence or 'NOT UPGRADINGPRODUCTCODE' not in sequence[name]['condition']:raise ValueError('Native action sequencing missing: '+name)
    def ordered(*names):
        values=[sequence[n]['sequence'] for n in names]
        if values!=sorted(set(values)):raise ValueError('Native sequence order differs: '+str(names))
    ordered('CheckOpenCodeClosed','InstallInitialize','InstallFiles','RollbackOpenCodeInstall','InstallOpenCodeIntegration','CommitOpenCodeInstall','InstallFinalize')
    ordered('CheckOpenCodeClosed','InstallInitialize','RemoveExistingProducts','InstallFiles')
    ordered('RollbackOpenCodeRemove','RemoveOpenCodeIntegration','RemoveFiles')
    ordered('RemoveOpenCodeIntegration','CommitOpenCodeRemove','InstallFinalize')
    for name in ['RollbackOpenCodeInstall','InstallOpenCodeIntegration','CommitOpenCodeInstall']:
        if sequence[name]['condition']!='NOT REMOVE AND NOT UPGRADINGPRODUCTCODE':raise ValueError('Install condition differs')
    for name in ['RollbackOpenCodeRemove','RemoveOpenCodeIntegration','CommitOpenCodeRemove']:
        if sequence[name]['condition']!='REMOVE="ALL" AND NOT UPGRADINGPRODUCTCODE':raise ValueError('Remove condition differs')
    result={'status':'Read-only compiled MSI receiving PASS; no install/execution','package':package.attrib,'files':len(found),'uncompressed_bytes':sum(x['bytes'] for x in found.values()),'payload':found,'binary_sha256':hashlib.sha256(binary_path.read_bytes()).hexdigest(),'actions':{n:actions[n] for n in ACTIONS},'sequence':{n:sequence[n] for n in list(ACTIONS)+['InstallInitialize','RemoveExistingProducts','InstallFiles','RemoveFiles','InstallFinalize']}}
    result['expected_payload']={'schema':1,'msi':{'name':msi.name,'sha256':hashlib.sha256(msi.read_bytes()).hexdigest(),'bytes':msi.stat().st_size},
        'files':file_rows,'binaries':[{'id':'OpenCodeInstallerHost','sha256':result['binary_sha256'],'bytes':binary_path.stat().st_size}],'icons':icons}
    (out/'msi-readback.json').write_text(json.dumps(result,indent=2));return result
