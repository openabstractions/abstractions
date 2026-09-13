"""Validate capability definitions and generation modes; never reports provider conformance."""
import argparse
import json
from pathlib import Path
import re

LAYERS = set('job download storage cas watch logging identity rights asks config model router facade'.split())
LANGUAGES = set('go cpp python rust javascript'.split())


def targets(root):
    result = {}
    for line in (root/'scripts/generate.targets').read_text(encoding='utf-8-sig').splitlines():
        words = line.split('#',1)[0].split()
        if not words: continue
        definition, output, *options = words
        langs = [x for x in options if x in LANGUAGES] or ([] if 'docs' in options else list(LANGUAGES))
        result.setdefault(definition, []).append((output, langs, '--no-ipc' in options, 'docs' in options))
    return result


def validate(root, inventory, complete=False):
    rows=inventory['layers']
    if inventory.get('version')!=1 or len(rows)!=len(LAYERS) or {r['layer'] for r in rows}!=LAYERS:
        raise ValueError('exactly the thirteen intended layers are required')
    declared=set(); pending=[]; generation=targets(root)
    for row in rows:
        name=row['layer']
        if not row.get('reason') or set(row['language_obligations'])!=LANGUAGES or any(not v for v in row['language_obligations'].values()):
            raise ValueError(f'{name}: explicit rationale and all language obligations required')
        modes={d['mode'] for d in row['definitions']}
        expected_status='pending' if 'vocabulary-pending' in modes or not modes else ('native-supplement' if 'vocabulary-native' in modes else 'described')
        interface=row.get('interface_generation',{})
        if interface.get('status')!=expected_status or not interface.get('reason'):
            raise ValueError(f'{name}: interface-generation status/rationale missing or inconsistent')
        for language, obligation in row['language_obligations'].items():
            expected=[d['path'] for d in row['definitions'] if language in d['languages']]
            if not isinstance(obligation,dict) or obligation.get('generated')!=expected or not obligation.get('binding') or not obligation.get('limitation'):
                raise ValueError(f'{name}: malformed language obligations for {language}')
        for path in row['api']+row['providers']:
            if not (root/path).is_file(): raise ValueError(f'{name}: missing authoritative source {path}')
        for roster in row.get('native_rosters',[]):
            source=(root/roster['source']).read_text(encoding='utf-8-sig')
            schema=(root/roster['definition']).read_text(encoding='utf-8-sig')
            expected=re.findall(roster['pattern'],source)
            match=re.search(r'const\s+list<string>\s+'+re.escape(roster['name'])+r'\s*=\s*(\[[^\]]*\])',schema)
            if not expected or not match or json.loads(match[1])!=expected:
                raise ValueError(f"{name}: native roster drift for {roster['name']}")
        if not row['definitions']:
            if row['descriptor_status']!='pending': raise ValueError(f'{name}: missing descriptor cannot be complete')
            pending.append(name)
        for definition in row['definitions']:
            path=definition['path']
            if path in declared: raise ValueError(f'duplicate descriptor {path}')
            declared.add(path)
            if not (root/path).is_file(): raise ValueError(f'{name}: missing descriptor {path}')
            source=(root/path).read_text(encoding='utf-8-sig')
            # Declaration anchors exclude comments and prose occurrences.
            services=re.findall(r'^service\s+(\w+)\s*\{',source,re.M)
            mode=definition['mode']
            if mode not in ('service','direct','records','legacy-protocol','vocabulary-native','vocabulary-pending'):
                raise ValueError(f'{name}: unknown generation mode')
            if bool(services)!=(mode in ('service','direct')):
                raise ValueError(f'{name}: interface classification disagrees with descriptor')
            rows_for=generation.get(path,[])
            for language in definition['languages']:
                matches=[r for r in rows_for if language in r[1]]
                if not matches: raise ValueError(f'{name}: missing {language} generation target for {path}')
                if any(r[2]!=(mode=='direct') for r in matches):
                    raise ValueError(f'{name}: incorrect IPC flag for {path}')
            if not any(r[3] for r in rows_for): raise ValueError(f'{name}: API documentation generation missing')
    actual={p.relative_to(root).as_posix() for name in LAYERS for p in (root/f'openabstractions-flat/abstraction-{name}').glob('*.thrift')}
    if actual!=declared: raise ValueError(f'unclassified definitions: {sorted(actual-declared)}; absent: {sorted(declared-actual)}')
    if complete and pending: raise ValueError(f'descriptor obligations remain: {", ".join(pending)}')
    return {'layers':len(rows),'definitions':len(declared),'pending':pending,'descriptors_complete':not pending,'pending_interfaces':[r['layer'] for r in rows if r.get('interface_generation',{}).get('status')=='pending']}


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',type=Path,default=Path(__file__).resolve().parents[1])
    p.add_argument('--inventory',type=Path,default=Path(__file__).with_name('definitions.json'))
    p.add_argument('--require-complete',action='store_true',help='refuse any explicitly pending descriptor obligation')
    a=p.parse_args()
    try: result=validate(a.root.resolve(),json.loads(a.inventory.read_text(encoding='utf-8-sig')),a.require_complete)
    except (ValueError,KeyError,OSError) as e:p.exit(1,f'definition inventory: {e}\n')
    print(json.dumps(result,indent=2))

if __name__=='__main__':main()
