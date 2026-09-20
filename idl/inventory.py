"""Validate capability definitions and generation modes; never reports provider conformance."""
import argparse
import json
from pathlib import Path
import re

LAYERS = set('job download storage cas watch logging identity rights asks credentials inference config model router facade'.split())
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


PACKAGE_METADATA = {'python': 'py/pyproject.toml', 'javascript': 'package.json', 'rust': 'rust/Cargo.toml'}


def metadata_paths(root):
    """Package manifest each generated Python, JavaScript or Rust output is installed through."""
    paths = set()
    for rows in targets(root).values():
        for output, langs, _, _ in rows:
            paths.update(f'{output}/{PACKAGE_METADATA[lang]}' for lang in langs if lang in PACKAGE_METADATA)
    return paths


def validate_package_metadata(root, inventory):
    required = metadata_paths(root)
    exempt = inventory.get('unpackaged_outputs', {})
    for path, reason in exempt.items():
        if path not in required: raise ValueError(f'stale package metadata exemption {path}')
        if not reason: raise ValueError(f'package metadata exemption {path} needs a reason')
        if (root/path).is_file(): raise ValueError(f'exempted package metadata exists: {path}')
    missing = sorted(p for p in required if p not in exempt and not (root/p).is_file())
    if missing: raise ValueError(f'generated packages lack metadata: {missing}')


WIRE_NAME = re.compile(r'^service\s+\w+\s*\{.*?^\}\s*\(\s*wire_name\s*=\s*"([^"]+)"', re.M | re.S)


def profiles(root, inventory):
    """Each definition's service profiles by wire name, in declaration order."""
    return {d['path']: WIRE_NAME.findall((root/d['path']).read_text(encoding='utf-8-sig'))
            for row in inventory['layers'] for d in row['definitions']}


def validate(root, inventory, complete=False):
    rows=inventory['layers']
    if inventory.get('version')!=1 or len(rows)!=len(LAYERS) or {r['layer'] for r in rows}!=LAYERS:
        raise ValueError(f'exactly the {len(LAYERS)} intended layers are required')
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
            no_ipc = definition.get('no_ipc', mode == 'direct')
            if not isinstance(no_ipc, bool) or (no_ipc and mode not in ('direct', 'records')) or (mode == 'direct' and not no_ipc):
                raise ValueError(f'{name}: incorrect IPC classification for {path}')
            rows_for=generation.get(path,[])
            for language in definition['languages']:
                matches=[r for r in rows_for if language in r[1]]
                if not matches: raise ValueError(f'{name}: missing {language} generation target for {path}')
                if any(r[2]!=no_ipc for r in matches):
                    raise ValueError(f'{name}: incorrect IPC flag for {path}')
            if not any(r[3] for r in rows_for): raise ValueError(f'{name}: API documentation generation missing')
    actual={p.relative_to(root).as_posix() for name in LAYERS for p in (root/f'openabstractions-flat/abstraction-{name}').glob('*.thrift')}
    if actual!=declared: raise ValueError(f'unclassified definitions: {sorted(actual-declared)}; absent: {sorted(declared-actual)}')
    if complete and pending: raise ValueError(f'descriptor obligations remain: {", ".join(pending)}')
    validate_package_metadata(root, inventory)
    backlog = check_contract_rules(root, inventory)
    return {'layers':len(rows),'definitions':len(declared),'profiles':sum(len(v) for v in profiles(root,inventory).values()),'pending':pending,'descriptors_complete':not pending,'pending_interfaces':[r['layer'] for r in rows if r.get('interface_generation',{}).get('status')=='pending'],'contract_rule_backlog':backlog}


# Contract rules from feedback/protocol-lessons-audit-2026-09-15.md, "Rules for
# new contracts", the machine-checkable ones:
#   R1  every service call returns one outcome enum; a call a policy may gate
#       reserves forbidden, unavailable and invalid; a call addressing a record
#       (RequestIdentity, id, identity) reserves unknown; a conditional write
#       (an expected_* parameter) reserves conflict.
#   R3  enums named *Cause declare unknown = "grant" and a member named other.
#   R4  a catalogue says so: catalogue = "open" on a const, struct or field needs
#       a Register* method in the same definition; catalogue = "closed" needs
#       closed_by = "<rule tag>". A const named *_actions, *_keys or *_questions
#       without the annotation is an undeclared catalogue. A catalogue carried
#       by a struct or field has no name to find it by: a recorded R4 entry
#       naming Struct or Struct.field keeps failing until that subject is
#       annotated.
#   R7  every enum declares reader = "display", "validate" or "act". Displayed
#       and semantically validated vocabularies carry unknown words; vocabularies
#       acted on directly refuse them.
# Current violations are recorded whole in CONTRACT_RULES_RECORDED. A violation
# not recorded fails, and so does a recorded one that no longer occurs: the file
# is the fix backlog and only shrinks.
CONTRACT_RULES_RECORDED = 'idl/contract_rules.recorded'
POLICY_REFUSALS = ('forbidden', 'unavailable', 'invalid')
CATALOGUE = re.compile(r'_(actions|keys|questions)$')
FIELD = re.compile(r'\d+\s*:\s*(?:required\s+|optional\s+)?([\w.]+(?:<[^>]*>)?)\s+(\w+)')
READERS = {'display': 'grant', 'validate': 'grant', 'act': 'refuse'}


def _blank(text):
    """Comments removed and string contents replaced by spaces, offsets kept."""
    out = list(text); i = 0; n = len(text)
    while i < n:
        c = text[i]
        if c == '"':
            j = i + 1
            while j < n and text[j] != '"':
                j += 2 if text[j] == '\\' else 1
            for k in range(i + 1, min(j, n)): out[k] = ' ' if text[k] != '\n' else '\n'
            i = j + 1
        elif text.startswith('//', i):
            j = text.find('\n', i); j = n if j < 0 else j
            for k in range(i, j): out[k] = ' '
            i = j
        elif text.startswith('/*', i):
            j = text.find('*/', i + 2); j = n if j < 0 else j + 2
            for k in range(i, j): out[k] = ' ' if text[k] != '\n' else '\n'
            i = j
        else:
            i += 1
    return ''.join(out)


def _closing(blank, start, open_, close):
    depth = 0
    for i in range(start, len(blank)):
        if blank[i] == open_: depth += 1
        elif blank[i] == close:
            depth -= 1
            if depth == 0: return i
    raise ValueError(f'unbalanced {open_} at offset {start}')


def _annotations(text, blank, pos):
    """The (name = "value", ...) list starting at pos, after whitespace, or {}."""
    while pos < len(blank) and blank[pos] in ' \t\r\n': pos += 1
    if pos >= len(blank) or blank[pos] != '(': return {}
    inner = text[pos + 1:_closing(blank, pos, '(', ')')]
    return dict(re.findall(r'(\w+)\s*=\s*"((?:[^"\\]|\\.)*)"', inner))


def contract_model(text):
    """Enums, structs, services and consts declared in one Thrift definition."""
    blank = _blank(text)
    model = {'enums': {}, 'structs': {}, 'services': {}, 'consts': {}}
    for m in re.finditer(r'(?m)^\s*(enum|struct|union|exception|service)\s+(\w+)\s*\{', blank):
        end = _closing(blank, m.end() - 1, '{', '}')
        body = blank[m.end():end]
        kind, name = m[1], m[2]
        if kind == 'enum':
            annotations = _annotations(text, blank, end + 1)
            model['enums'][name] = {'members': re.findall(r'\d+\s*:\s*(\w+)', body),
                                    'unknown': annotations.get('unknown'), 'reader': annotations.get('reader')}
        elif kind == 'service':
            methods = {}
            for method in re.finditer(r'(?m)^\s*(?:oneway\s+)?([\w.]+(?:<[^>]*>)?)\s+(\w+)\s*\(', body):
                close = _closing(body, method.end() - 1, '(', ')')
                methods[method[2]] = {'result': method[1], 'params': FIELD.findall(body[method.end():close])}
            model['services'][name] = methods
        else:
            fields = []
            for field in FIELD.finditer(body):
                fields.append((field[1], field[2], _annotations(text, blank, m.end() + field.end())))
            model['structs'][name] = {'fields': fields, 'annotations': _annotations(text, blank, end + 1)}
    for m in re.finditer(r'(?m)^const\s+[\w.]+(?:<[^>]*>)?\s+(\w+)\s*=\s*', blank):
        end = _closing(blank, m.end(), '[', ']') + 1 if blank[m.end():m.end() + 1] == '[' else blank.find('\n', m.end())
        model['consts'][m[1]] = _annotations(text, blank, end if end >= 0 else len(blank))
    return model


def _local(name):
    return name.split('.')[-1]


def _outcome_enum(result, enums, structs):
    result = _local(result)
    if result in enums: return result
    for field_type, field_name, _ in structs.get(result, {}).get('fields', []):
        field_type = _local(field_type)
        if field_type in enums and (field_name in ('outcome', 'status') or field_type.endswith(('Outcome', 'Status'))):
            return field_type
    return None


def _catalogue(path, subject, annotations, registers):
    kind = annotations.get('catalogue')
    if kind == 'open' and not registers:
        return ('R4', path, subject, 'open catalogue: no Register* method in this definition')
    if kind == 'closed' and not annotations.get('closed_by'):
        return ('R4', path, subject, 'closed catalogue: no closed_by rule tag')
    if kind not in (None, 'open', 'closed'):
        return ('R4', path, subject, f'catalogue = "{kind}"; the rule defines open and closed')
    return None


def definition_violations(path, model, service_calls, enums=None, structs=None, pending_catalogues=()):
    """(rule, path, subject, detail) for one definition; enums and structs default to its own.

    pending_catalogues names the recorded Struct or Struct.field catalogues of this definition.
    """
    enums = {**(enums or {}), **model['enums']}
    structs = {**(structs or {}), **model['structs']}
    found = []
    for name, enum in model['enums'].items():
        reader, unknown = enum['reader'], enum['unknown']
        if reader not in READERS:
            found.append(('R7', path, name, 'declares no reader = "display", "validate" or "act"'))
        elif unknown != READERS[reader]:
            found.append(('R7', path, name, f'reader = "{reader}" requires unknown = "{READERS[reader]}", found {unknown}'))
        if name.endswith('Cause') and (unknown != 'grant' or 'other' not in enum['members']):
            found.append(('R3', path, name, 'a cause enum is unknown = "grant" with member other'))
    registers = any(method.startswith('Register') for methods in model['services'].values() for method in methods)
    subjects = {}
    for name, annotations in model['consts'].items():
        subjects[name] = annotations
        if CATALOGUE.search(name) and 'catalogue' not in annotations:
            found.append(('R4', path, name, 'undeclared catalogue: no catalogue = "open" or "closed"'))
    for name, struct in model['structs'].items():
        subjects[name] = struct['annotations']
        for _, field_name, annotations in struct['fields']:
            subjects[f'{name}.{field_name}'] = annotations
    for subject, annotations in subjects.items():
        problem = _catalogue(path, subject, annotations, registers)
        if problem: found.append(problem)
        elif subject in pending_catalogues and 'catalogue' not in annotations:
            found.append(('R4', path, subject, 'undeclared catalogue: no catalogue = "open" or "closed"'))
    if service_calls:
        for service, methods in model['services'].items():
            for method, call in methods.items():
                subject = f'{service}.{method}'
                outcome = _outcome_enum(call['result'], enums, structs)
                if outcome is None:
                    found.append(('R1', path, subject, f'returns {call["result"]} without an outcome enum'))
                    continue
                need = list(POLICY_REFUSALS)
                if any(_local(t) == 'RequestIdentity' or n in ('id', 'identity') for t, n in call['params']):
                    need.append('unknown')
                if any(n.startswith('expected') for _, n in call['params']):
                    need.append('conflict')
                missing = [word for word in need if word not in enums[outcome]['members']]
                if missing:
                    found.append(('R1', path, subject, f'{outcome} lacks {", ".join(missing)}'))
    return found


def contract_violations(root, inventory, recorded=None):
    """Every current violation; recorded supplies the struct and field catalogues to hold to R4."""
    if recorded is None:
        recorded = read_recorded(root/CONTRACT_RULES_RECORDED)
    definitions = [(d['path'], d['mode']) for row in inventory['layers'] for d in row['definitions']]
    models = {path: contract_model((root/path).read_text(encoding='utf-8-sig')) for path, _ in definitions}
    found = []
    for path, mode in definitions:
        folder = path.rsplit('/', 1)[0]
        near = [m for p, m in models.items() if p.rsplit('/', 1)[0] == folder]
        enums = {k: v for m in near for k, v in m['enums'].items()}
        structs = {k: v for m in near for k, v in m['structs'].items()}
        pending = {s for r, p, s, _ in recorded if r == 'R4' and p == path and (s in models[path]['structs'] or '.' in s)}
        found += definition_violations(path, models[path], mode == 'service', enums, structs, pending)
    return sorted(set(found))


def read_recorded(path):
    """The recorded (rule, definition, subject, detail) entries."""
    recorded = set()
    if path.is_file():
        for line in path.read_text(encoding='utf-8').splitlines():
            if not line.strip() or line.startswith('#'): continue
            fields = line.split('\t')
            if len(fields) != 4: raise ValueError(f'malformed recorded contract rule line: {line!r}')
            recorded.add(tuple(fields))
    return recorded


def write_recorded(path, violations):
    header = ('# Contract rule violations recorded when the rules were introduced: the fix\n'
              '# backlog. idl/inventory.py fails on any violation not listed here and on any\n'
              '# listed one that no longer occurs. Rules: idl/inventory.py, R1 R3 R4 R7.\n'
              '# rule<TAB>definition<TAB>subject<TAB>detail\n')
    path.write_text(header + ''.join('\t'.join(v) + '\n' for v in violations), encoding='utf-8', newline='\n')


def check_contract_rules(root, inventory):
    recorded = read_recorded(root/CONTRACT_RULES_RECORDED)
    found = contract_violations(root, inventory, recorded)
    new = [v for v in found if v not in recorded]
    if new:
        raise ValueError('new contract rule violations: ' + '; '.join(f'{r} {p} {s}: {d}' for r, p, s, d in new))
    fixed = sorted(recorded - set(found))
    if fixed:
        raise ValueError(f'recorded contract rule violations no longer occur as recorded; update {CONTRACT_RULES_RECORDED}: '
                         + '; '.join(f'{r} {p} {s}: {d}' for r, p, s, d in fixed))
    return len(found)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',type=Path,default=Path(__file__).resolve().parents[1])
    p.add_argument('--inventory',type=Path,default=Path(__file__).with_name('definitions.json'))
    p.add_argument('--require-complete',action='store_true',help='refuse any explicitly pending descriptor obligation')
    p.add_argument('--record-contract-rules',action='store_true',help=f'rewrite {CONTRACT_RULES_RECORDED} with the current contract rule violations, then validate')
    a=p.parse_args()
    if a.record_contract_rules:
        root=a.root.resolve()
        write_recorded(root/CONTRACT_RULES_RECORDED,contract_violations(root,json.loads(a.inventory.read_text(encoding='utf-8-sig'))))
    try: result=validate(a.root.resolve(),json.loads(a.inventory.read_text(encoding='utf-8-sig')),a.require_complete)
    except (ValueError,KeyError,OSError) as e:p.exit(1,f'definition inventory: {e}\n')
    print(json.dumps(result,indent=2))

if __name__=='__main__':main()
