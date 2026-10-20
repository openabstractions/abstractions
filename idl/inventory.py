"""Validate capability definitions and generation modes; never reports provider conformance."""
import argparse
import json
from pathlib import Path
import re

LANGUAGES = set('go cpp python rust javascript'.split())


def public_definitions(root):
    """Definitions in public abstraction trees selected by the split manifest."""
    owners = set()
    repo = None
    for line in (root/'scripts/split.manifest').read_text(encoding='utf-8-sig').splitlines():
        words = line.split('#', 1)[0].split()
        if not words: continue
        if words[0] == 'repo':
            repo = words[1] if len(words) == 2 else None
        elif (repo and repo.startswith('abstraction-') and len(words) == 3
              and words[:2] == ['tree', '.']):
            path = Path(words[2])
            if path.as_posix() == f'openabstractions-flat/{repo}':
                owners.add(repo.removeprefix('abstraction-'))
    return {p.relative_to(root).as_posix()
            for owner in owners
            for p in (root/f'openabstractions-flat/abstraction-{owner}').rglob('*.thrift')}


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


def metadata_paths(root, inventory=None):
    """Package manifest each generated Python, JavaScript or Rust output is installed through."""
    owners = (inventory or {}).get('package_metadata_owners', {})
    paths = set()
    for rows in targets(root).values():
        for output, langs, _, _ in rows:
            paths.update(owners.get(path, path) for lang in langs if lang in PACKAGE_METADATA
                         for path in [f'{output}/{PACKAGE_METADATA[lang]}'])
    return paths


def validate_package_metadata(root, inventory):
    generation = targets(root)
    defaults = metadata_paths(root)
    owners = inventory.get('package_metadata_owners', {})
    for output_manifest, owner in owners.items():
        if output_manifest not in defaults: raise ValueError(f'stale package metadata owner {output_manifest}')
        output = Path(output_manifest).parent
        owner_path = Path(owner)
        if (owner_path.is_absolute() or '..' in owner_path.parts or owner_path.name != 'package.json'
                or Path(output_manifest).name != 'package.json' or owner_path.parent not in output.parents):
            raise ValueError(f'package metadata owner must be an ancestor JavaScript manifest: {output_manifest}: {owner}')
        nearest = next((parent/'package.json' for parent in [output, *output.parents]
                        if (root/parent/'package.json').is_file()), None)
        if nearest != owner_path:
            raise ValueError(f'generated package metadata owner is missing or not nearest: {output_manifest}: {owner}')
        files = json.loads((root/owner).read_text(encoding='utf-8-sig')).get('files', [])
        for definition, rows in generation.items():
            if not any(path == output.as_posix() and 'javascript' in langs for path, langs, _, _ in rows): continue
            source = (root/definition).read_text(encoding='utf-8-sig')
            namespace = re.search(r'(?m)^namespace\s+(?:javascript|\*)\s+([\w.]+)\s*$', _blank(source))
            if not namespace: raise ValueError(f'generated JavaScript package has no namespace: {definition}')
            folder = output.relative_to(owner_path.parent)/'js'/Path(*namespace[1].split('.'))
            expected = [(folder/name).as_posix() for name in ('internal.mjs', 'index.mjs', 'index.d.mts')]
            if not isinstance(files, list) or any(name not in files for name in expected):
                raise ValueError(f'package metadata owner files omit generated modules: {owner}: {expected}')
    required = metadata_paths(root, inventory)
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
    if inventory.get('version')!=1 or len({r['layer'] for r in rows})!=len(rows):
        raise ValueError('inventory version 1 and unique schema-owner rows are required')
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
            if not path.startswith(f'openabstractions-flat/abstraction-{name}/'):
                raise ValueError(f'{name}: descriptor belongs to another public schema owner: {path}')
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
            private = definition.get('private', False)
            if not isinstance(private, bool):
                raise ValueError(f'{name}: incorrect private classification for {path}')
            if private and any(r[3] for r in rows_for):
                raise ValueError(f'{name}: private definition has API documentation generation')
            if not private and not any(r[3] for r in rows_for): raise ValueError(f'{name}: API documentation generation missing')
    actual=public_definitions(root)
    if actual!=declared: raise ValueError(f'unclassified definitions: {sorted(actual-declared)}; absent: {sorted(declared-actual)}')
    owners={p.split('/')[1].removeprefix('abstraction-') for p in actual}
    if {r['layer'] for r in rows}!=owners:
        raise ValueError(f'unclassified public schema owners: {sorted(owners-{r["layer"] for r in rows})}; absent: {sorted({r["layer"] for r in rows}-owners)}')
    if complete and pending: raise ValueError(f'descriptor obligations remain: {", ".join(pending)}')
    validate_package_metadata(root, inventory)
    backlog = check_contract_rules(root, inventory)
    return {'layers':len(rows),'definitions':len(declared),'profiles':sum(len(v) for v in profiles(root,inventory).values()),'pending':pending,'descriptors_complete':not pending,'pending_interfaces':[r['layer'] for r in rows if r.get('interface_generation',{}).get('status')=='pending'],'contract_rule_backlog':backlog}


# Contract rules from research/feedback-archive-2026-09-24/protocol-lessons-audit-2026-09-15.md, "Rules for
# new contracts", the machine-checkable ones:
#   R1  every service call returns one outcome enum; a call a policy may gate
#       reserves forbidden, unavailable and invalid; a call addressing a record
#       (RequestIdentity, id, identity) reserves unknown, or not_found where
#       absence is provable (idl/CONVENTIONS.md CONV-E2); a conditional write
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
#   R8  the refusal record is declared once, in rights (idl/CONVENTIONS.md
#       CONV-E5): a definition other than REFUSAL_OWNER that declares a struct
#       or enum named in REFUSAL_RECORD declares a second one.
#   R9  every binary, string, json, list and map field and method argument
#       declares a length (idl/LANGUAGE.md DEF-A11, DEF-A12): a value with no
#       bound gives its record no largest encoding. A grammar bounds a field
#       and never a collection element: a string typedef with a read/write
#       grammar bounds a field or argument of that type, and a list or map
#       element resolves to its base type, since the generator (typeRef
#       resolves list<stamp> to list<string>) checks no grammar inside a list
#       or map. A list or map whose elements (a map's keys included) are a
#       large type fails as `<field>[]` whatever its entry count, since the
#       language declares no bound for an element; gen --sizes reports the
#       same record as unbounded "(elements)". R9 also
#       judges the definitions scripts/generate.targets names outside the
#       public inventory.
#   R10 every service method declares a trait, readonly, idempotent or
#       idempotency_key (idl/LANGUAGE.md DEF-S3, idl/CONVENTIONS.md CONV-I2,
#       abstraction-ipc IPC-I1).
#   R11 every code a service declares in error_codes has a row in the one
#       status mapping, abstraction-ipc CONTRACT.md IPC-E2, read from that page.
# Current violations are recorded whole in CONTRACT_RULES_RECORDED. A violation
# not recorded fails, and so does a recorded one that no longer occurs: the file
# is the fix backlog and only shrinks.
CONTRACT_RULES_RECORDED = 'idl/contract_rules.recorded'
POLICY_REFUSALS = ('forbidden', 'unavailable', 'invalid')
CATALOGUE = re.compile(r'_(actions|keys|questions)$')
FIELD = re.compile(r'\d+\s*:\s*(?:required\s+|optional\s+)?([\w.]+(?:<[^>]*>)?)\s+(\w+)')
READERS = {'display': 'grant', 'validate': 'grant', 'act': 'refuse'}
REFUSAL_OWNER = 'openabstractions-flat/abstraction-rights/rights.thrift'
REFUSAL_RECORD = ('RefusalDetail', 'RefusalSource', 'RefusalDecision', 'RefusalAsk')


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


def _type_at(blank, pos):
    match = re.match(r'\s*([\w.]+)', blank[pos:])
    if not match: raise ValueError('missing declared type')
    start = pos + match.start(1); end = pos + match.end()
    while end < len(blank) and blank[end].isspace(): end += 1
    if end < len(blank) and blank[end] == '<': end = _closing(blank, end, '<', '>') + 1
    return re.sub(r'\s+', '', blank[start:end]), end


def _fields(blank):
    for match in re.finditer(r'\b\d+\s*:\s*(?:(required|optional)\s+)?', blank):
        kind, end = _type_at(blank, match.end())
        name = re.match(r'\s*(\w+)', blank[end:])
        if not name: raise ValueError('missing declared field name')
        yield kind, name[1], end + name.end(), match[1] == 'optional'


def _methods(body):
    pos = 0
    while pos < len(body):
        while pos < len(body) and (body[pos].isspace() or body[pos] in ',;'): pos += 1
        if pos == len(body): break
        keyword = re.match(r'oneway\b', body[pos:])
        if keyword: pos += keyword.end()
        kind, end = _type_at(body, pos)
        name = re.match(r'\s*(\w+)\s*\(', body[end:])
        if not name: raise ValueError('missing service method declaration')
        opened = end + name.end() - 1; closed = _closing(body, opened, '(', ')')
        yield kind, name[1], opened + 1, closed
        pos = closed + 1
        while pos < len(body) and body[pos].isspace(): pos += 1
        if pos < len(body) and body[pos] == '(': pos = _closing(body, pos, '(', ')') + 1


def contract_model(text):
    """Enums, structs, services and consts declared in one Thrift definition."""
    blank = _blank(text)
    model = {'enums': {}, 'structs': {}, 'services': {}, 'consts': {}, 'includes': {}, 'typedefs': {},
             'service_annotations': {}, 'const_words': {}}
    for m in re.finditer(r'(?m)^\s*typedef\s+', blank):
        base, end = _type_at(blank, m.end())
        name = re.match(r'\s*(\w+)', blank[end:])
        if not name: raise ValueError('missing typedef name')
        model['typedefs'][name[1]] = {'base': base, 'annotations': _annotations(text, blank, end + name.end())}
    for m in re.finditer(r'(?m)^\s*include\s+', blank):
        included = re.match(r'"([^"\r\n]+)"', text[m.end():])
        if not included: raise ValueError('include requires a quoted relative Thrift path')
        relative = included[1]
        alias = Path(relative).stem
        if (Path(relative).is_absolute() or '\\' in relative or Path(relative).suffix != '.thrift'
                or not re.fullmatch(r'[A-Za-z_]\w*', alias)):
            raise ValueError(f'invalid include path {relative}')
        if alias in model['includes']: raise ValueError(f'ambiguous include alias {alias}')
        model['includes'][alias] = relative
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
            for result, method, opened, close in _methods(body):
                start = m.end() + opened
                params = blank[start:m.end() + close]
                fields = list(_fields(params))
                methods[method] = {'result': result, 'params': [(kind, name) for kind, name, _, _ in fields],
                                   'param_annotations': {name: _annotations(text, blank, start + end)
                                                         for _, name, end, _ in fields},
                                   'annotations': _annotations(text, blank, m.end() + close + 1)}
            model['services'][name] = methods
            model['service_annotations'][name] = _annotations(text, blank, end + 1)
        else:
            parsed = list(_fields(body))
            fields = [(kind, name, _annotations(text, blank, m.end() + end)) for kind, name, end, _ in parsed]
            optional = {name for _, name, _, optional in parsed if optional}
            model['structs'][name] = {'fields': fields, 'optional': optional,
                                      'annotations': _annotations(text, blank, end + 1)}
    for m in re.finditer(r'(?m)^const\s+[\w.]+(?:<[^>]*>)?\s+(\w+)\s*=\s*', blank):
        end = _closing(blank, m.end(), '[', ']') + 1 if blank[m.end():m.end() + 1] == '[' else blank.find('\n', m.end())
        model['consts'][m[1]] = _annotations(text, blank, end if end >= 0 else len(blank))
        if blank[m.end():m.end() + 1] == '[':
            model['const_words'][m[1]] = re.findall(r'"((?:[^"\\]|\\.)*)"', text[m.end():end])
    return model


def _local(name):
    return name.split('.')[-1]


def _resolved_type(name, path, models):
    parts = name.split('.')
    if len(parts) == 1: return path, name
    if len(parts) != 2: return None
    included = models[path]['includes'].get(parts[0])
    if included is None: return None
    owner = (Path(path).parent/included).as_posix()
    owner = str(Path(owner)).replace('\\', '/')
    # Paths in the loaded model map are normalized by the loader.
    owner = models[path].get('include_paths', {}).get(parts[0], owner)
    return (owner, parts[1]) if owner in models else None


def _outcome_enum(result, path, models):
    """The single required call outcome, following scalar record wrappers and typed includes."""
    def walk(type_name, owner, active):
        resolved = _resolved_type(type_name, owner, models)
        if resolved is None: return [], False
        owner, name = resolved
        model = models[owner]
        if name in model['enums']: return [(owner, name, model['enums'][name])], False
        struct = model['structs'].get(name)
        if struct is None: return [], False
        if resolved in active: return [], True
        fields = struct['fields']
        direct = []
        for field_type, field_name, _ in fields:
            target = _resolved_type(field_type, owner, models)
            if target and target[1] in models[target[0]]['enums']:
                direct.append((field_name, target))
        explicit = [(field, target) for field, target in direct if field in ('outcome', 'status')]
        candidates = explicit or [(field, target) for field, target in direct if target[1].endswith(('Outcome', 'Status'))]
        if candidates:
            if len(candidates) != 1 or candidates[0][0] in struct['optional']: return [], True
            _, (target_owner, target_name) = candidates[0]
            return [(target_owner, target_name, models[target_owner]['enums'][target_name])], False
        found = []; invalid = False
        for field_type, field_name, _ in fields:
            if field_name in struct['optional']: continue
            nested, refused = walk(field_type, owner, active | {resolved})
            # Unnamed enum fields describe data rather than the call's outcome.
            target = _resolved_type(field_type, owner, models)
            if target and target[1] in models[target[0]]['enums']: continue
            found.extend(nested); invalid |= refused
        return found, invalid
    found, invalid = walk(result, path, set())
    return found[0] if len(found) == 1 and not invalid else None


def _catalogue(path, subject, annotations, registers):
    kind = annotations.get('catalogue')
    if kind == 'open' and not registers:
        return ('R4', path, subject, 'open catalogue: no Register* method in this definition')
    if kind == 'closed' and not annotations.get('closed_by'):
        return ('R4', path, subject, 'closed catalogue: no closed_by rule tag')
    if kind not in (None, 'open', 'closed'):
        return ('R4', path, subject, f'catalogue = "{kind}"; the rule defines open and closed')
    return None


LARGE_TYPES = ('binary', 'string', 'json')


def _named_string_bound(typedef):
    """A named string's valid finite UTF-8 length bounds its scalar/elements."""
    if not typedef or typedef['base'] != 'string': return False
    spec = typedef['annotations'].get('length', '')
    match = re.fullmatch(r'(0|[1-9][0-9]*)?\.\.(0|[1-9][0-9]*)', spec)
    if not match: return False
    low, high = int(match[1] or 0), int(match[2])
    return low <= high < 2**62  # gen parseBounds uses signed 63-bit counts


def _large(type_name, model):
    """The large type a field of type_name carries, or None when it is bounded by its type."""
    typedef = model.get('typedefs', {}).get(type_name)
    if typedef is not None:
        if _named_string_bound(typedef): return None
        if typedef['annotations'].get('read') or typedef['annotations'].get('write'):
            return None
        type_name = typedef['base']
    if type_name in LARGE_TYPES or type_name.startswith(('list<', 'map<')):
        return type_name
    return None


def _large_elements(collection, model):
    """The large type a list's elements or a map's keys or values carry, or None."""
    m = re.fullmatch(r'(?:list|map)<(.*)>', collection.replace(' ', ''))
    if not m:
        return None
    def element_large(element):
        # A grammar bounds a field's text, never an element's: gen --sizes reports
        # list<stamp> unbounded, so the element resolves to its base type here.
        typedef = model.get('typedefs', {}).get(element)
        if _named_string_bound(typedef): return None
        base = typedef['base'] if typedef is not None else element
        return base if base in LARGE_TYPES or base.startswith(('list<', 'map<')) else None
    return next((large for element in m[1].split(',') if (large := element_large(element))), None)


def large_field_violations(path, model):
    """R9: a binary, string, json, list or map field or argument without a declared length,
    and a list or map whose elements no declaration can bound."""
    found = []
    def judge(subject, type_name, annotations):
        large = _large(type_name, model)
        if large and 'length' not in annotations:
            found.append(('R9', path, subject, f'unbounded {large}: declare length (idl/LANGUAGE.md DEF-A12)'))
        inner = _large_elements(large, model) if large else None
        if inner:
            found.append(('R9', path, subject + '[]',
                          f'unbounded {inner} elements: the language declares no element bound (idl/LANGUAGE.md DEF-A12)'))
    for name, struct in model['structs'].items():
        for field_type, field_name, annotations in struct['fields']:
            judge(f'{name}.{field_name}', field_type, annotations)
    for service, methods in model['services'].items():
        for method, call in methods.items():
            for param_type, param_name in call['params']:
                judge(f'{service}.{method}({param_name})', param_type, call.get('param_annotations', {}).get(param_name, {}))
    return found


STATUS_MAPPING = 'openabstractions-flat/abstraction-ipc/CONTRACT.md'


def method_trait_violations(path, model):
    """R10: a service method without a declared trait."""
    return [('R10', path, f'{service}.{method}', 'declares no trait: readonly, idempotent or idempotency_key (idl/LANGUAGE.md DEF-S3)')
            for service, methods in model['services'].items()
            for method, call in methods.items()
            if call.get('annotations', {}).get('readonly') != 'true'
            and call.get('annotations', {}).get('idempotent') not in ('true', 'false')
            and not call.get('annotations', {}).get('idempotency_key')]


REMOTE_PROFILES = {'local', 'see', 'call', 'delegate', 'person'}
JOB_DEFINITION = 'openabstractions-flat/abstraction-job/acceptance.thrift'


def _type_children(name):
    if '<' not in name: return []
    container, inner = name.split('<', 1)
    inner = inner[:-1]; depth = 0; start = 0; parts = []
    for index, char in enumerate(inner):
        if char == '<': depth += 1
        elif char == '>': depth -= 1
        elif char == ',' and depth == 0: parts.append(inner[start:index]); start = index + 1
    parts.append(inner[start:])
    if container not in ('list', 'map') or len(parts) != (2 if container == 'map' else 1):
        raise ValueError('unsupported declared container ' + name)
    return parts


def remote_violations(path, model, models):
    """R12: explicit profiles, recovery agreement and declared local/secret leaves."""
    found = []
    def problem(subject, reason): found.append(('R12', path, subject, reason))
    def marker(ann, chain):
        if 'secret' in ann and ann['secret'] != 'true': return chain + ': invalid secret marker'
        if 'local' in ann and ann['local'] not in ('path', 'pid', 'handle'): return chain + ': invalid local marker'
        if ann.get('secret') == 'true': return chain + ': secret'
        if 'local' in ann: return chain + ': local ' + ann['local']
        return None
    def walk(kind, owner, chain, active):
        children = _type_children(kind)
        if children:
            return [reason for i, child in enumerate(children) for reason in walk(child, owner, chain + ('[key]' if len(children) == 2 and i == 0 else '[]'), active)]
        resolved = _resolved_type(kind, owner, models)
        if resolved is None: return [chain + ': unresolved owner-qualified type ' + kind]
        owner, name = resolved; current = models[owner]
        if resolved in active: return []
        active = active | {resolved}
        if name in current['typedefs']:
            alias = current['typedefs'][name]
            return walk(alias['base'], owner, chain + ' -> ' + owner + ':' + name, active)
        record = current['structs'].get(name)
        if record is None:
            if name in {'void','bool','i16','i32','i64','double','string','binary','json'} or name in current['enums']: return []
            return [chain + ': unresolved declared type ' + name]
        reasons = []
        for field_type, field, ann in record['fields']:
            leaf = chain + ' -> ' + owner + ':' + name + '.' + field
            reason = marker(ann, leaf)
            if reason: reasons.append(reason)
            reasons.extend(walk(field_type, owner, leaf, active))
        return reasons
    def job_identity(kind, owner, active):
        # A scalar required result path to recoverable acceptance evidence.
        if _type_children(kind): return False
        resolved = _resolved_type(kind, owner, models)
        if resolved is None or resolved in active: return False
        owner, name = resolved; current = models[owner]; active = active | {resolved}
        if owner == JOB_DEFINITION and name == 'Receipt': return True
        if name in current['typedefs']: return job_identity(current['typedefs'][name]['base'], owner, active)
        record = current['structs'].get(name)
        return bool(record and any(field not in record['optional'] and job_identity(kind, owner, active)
                                   for kind, field, _ in record['fields']))
    for record, body in model['structs'].items():
        for _, field, ann in body['fields']:
            if ('secret' in ann and ann['secret'] != 'true') or ('local' in ann and ann['local'] not in ('path','pid','handle')):
                problem(record + '.' + field, 'invalid secret/local marker')
    for service, methods in model['services'].items():
        default = model['service_annotations'].get(service, {}).get('remote', 'local')
        if default not in REMOTE_PROFILES: problem(service, 'unknown or empty remote service profile')
        for method, call in methods.items():
            subject = service + '.' + method; ann = call['annotations']; profile = ann.get('remote', default)
            if profile not in REMOTE_PROFILES: problem(subject, 'unknown or empty remote method profile'); continue
            if profile == 'local': continue
            if profile == 'see' and ann.get('readonly') != 'true': problem(subject, 'remote see requires readonly')
            if profile == 'delegate' and ('idempotency_key' not in ann or not job_identity(call['result'], path, set())):
                problem(subject, 'remote delegate requires keyed recovery and canonical job acceptance Receipt identity')
            for kind, argument in call['params']:
                chain = subject + '.argument.' + argument
                direct = marker(call['param_annotations'].get(argument, {}), chain)
                if direct: problem(subject, direct)
                for reason in walk(kind, path, chain, set()): problem(subject, reason)
            for reason in walk(call['result'], path, subject + '.result', set()): problem(subject, reason)
    return found


def status_words(root):
    """The OA words and codes IPC-E2's one status mapping has a row for, or None without the page."""
    page = root/STATUS_MAPPING
    if not page.is_file():
        return None
    text = page.read_text(encoding='utf-8')
    start = text.find('**[IPC-E2]')
    if start < 0:
        raise ValueError(f'{STATUS_MAPPING} declares no IPC-E2 status mapping')
    end = text.find('**[IPC-', start + 1)
    words = set()
    for line in text[start:end if end > 0 else len(text)].splitlines():
        cells = line.split('|')
        if len(cells) > 2 and not set(cells[1].strip()) <= set('-: '):
            words.update(re.findall(r'`([a-z_]+)`', cells[1]))
    if not words:
        raise ValueError(f'{STATUS_MAPPING} IPC-E2 table has no rows')
    return words


def status_mapping_violations(path, model, words):
    """R11: an error code a service declares with no IPC-E2 row."""
    found = []
    for service, annotations in model.get('service_annotations', {}).items():
        const = annotations.get('error_codes')
        for code in model.get('const_words', {}).get(const, []) if const else []:
            if code not in words:
                found.append(('R11', path, f'{service}:{code}', 'error code without a row in abstraction-ipc IPC-E2'))
    return found


def definition_violations(path, model, service_calls, pending_catalogues=(), models=None):
    """(rule, path, subject, detail) for one definition, resolving its declared includes.

    pending_catalogues names the recorded Struct or Struct.field catalogues of this definition.
    """
    models = models or {path: model}
    found = []
    for name, enum in model['enums'].items():
        reader, unknown = enum['reader'], enum['unknown']
        if reader not in READERS:
            found.append(('R7', path, name, 'declares no reader = "display", "validate" or "act"'))
        elif unknown != READERS[reader]:
            found.append(('R7', path, name, f'reader = "{reader}" requires unknown = "{READERS[reader]}", found {unknown}'))
        if name.endswith('Cause') and (unknown != 'grant' or 'other' not in enum['members']):
            found.append(('R3', path, name, 'a cause enum is unknown = "grant" with member other'))
    if path != REFUSAL_OWNER:
        for name in REFUSAL_RECORD:
            if name in model['structs'] or name in model['enums']:
                found.append(('R8', path, name, f'declares the refusal record a second time; include {REFUSAL_OWNER}'))
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
                outcome = _outcome_enum(call['result'], path, models)
                if outcome is None:
                    found.append(('R1', path, subject, f'returns {call["result"]} without an outcome enum'))
                    continue
                need = list(POLICY_REFUSALS)
                outcome_owner, outcome_name, enum = outcome
                if any(_local(t) == 'RequestIdentity' or n in ('id', 'identity') for t, n in call['params']):
                    # CONV-E2: not_found answers a provable absence in unknown's place.
                    need.append('not_found' if 'not_found' in enum['members'] else 'unknown')
                if any(n.startswith('expected') for _, n in call['params']):
                    need.append('conflict')
                # The published Facade enum has always spelled this semantic
                # refusal invalid_request. Never accept that spelling by name
                # alone in another definition or a newly authored enum.
                legacy_invalid = (outcome_owner == 'openabstractions-flat/abstraction-facade/facade.thrift'
                                  and outcome_name == 'ResolutionStatus'
                                  and 'invalid_request' in enum['members'])
                missing = [word for word in need if word not in enum['members']
                           and not (word == 'invalid' and legacy_invalid)]
                if missing:
                    found.append(('R1', path, subject, f'{outcome_name} lacks {", ".join(missing)}'))
    found += large_field_violations(path, model)
    found += remote_violations(path, model, models)
    if service_calls:
        found += method_trait_violations(path, model)
    return found


def contract_violations(root, inventory, recorded=None):
    """Every current violation; recorded supplies the struct and field catalogues to hold to R4."""
    if recorded is None:
        recorded = read_recorded(root/CONTRACT_RULES_RECORDED)
    definitions = [(d['path'], d['mode']) for row in inventory['layers'] for d in row['definitions']]
    models = {}; active = set()
    def load(path):
        absolute = (root/path).resolve()
        try: path = absolute.relative_to(root.resolve()).as_posix()
        except ValueError: raise ValueError(f'include outside checkout: {path}')
        if path in active: raise ValueError(f'cyclic include: {path}')
        if path in models: return path
        if len(active) >= 64: raise ValueError('include depth exceeds 64 definitions')
        active.add(path)
        model = contract_model(absolute.read_text(encoding='utf-8-sig'))
        model['include_paths'] = {alias: load((Path(path).parent/relative).as_posix())
                                  for alias, relative in model['includes'].items()}
        models[path] = model
        active.remove(path)
        return path
    for path, _ in definitions: load(path)
    found = []
    words = status_words(root)
    for path, mode in definitions:
        pending = {s for r, p, s, _ in recorded if r == 'R4' and p == path and (s in models[path]['structs'] or '.' in s)}
        found += definition_violations(path, models[path], mode == 'service', pending, models)
        if words is not None:
            found += status_mapping_violations(path, models[path], words)
    inventoried = {path for path, _ in definitions}
    declared = targets(root) if (root/'scripts/generate.targets').is_file() else {}
    for path in sorted(declared):
        if path not in inventoried and (root/path).is_file():
            found += large_field_violations(path, contract_model((root/path).read_text(encoding='utf-8-sig')))
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
              '# listed one that no longer occurs. Rules: idl/inventory.py, R1 R3 R4 R7 R8 R9 R10 R11.\n'
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
