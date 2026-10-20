"""The installed owner's fixed native notification and permission acceptance.

This helper reads the installed Panel and runs the existing example callers.
The installed native presenter owns every Allow and notification action. Raw
Panel output contains its private bearer key and stays in the owner evidence.

The optional Windows ordinary scene uses the official OpenCode native console
and a real ComfyUI host. Native personal reviews show the verified Python or
registered Comfy application and its finite one-hour legacy permissions.
This example issues no typed permission request for unsupported profiles.
"""
import dataclasses
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath, PureWindowsPath
import re
import shutil
import socket
import stat
import struct
import subprocess
import sys
import time
import types
import urllib.parse
import urllib.error
import urllib.request
import zlib


class InferenceAuditGap(ValueError):
    def __init__(self, next_cursor):
        super().__init__('current inference audit was evicted')
        self.next_cursor = next_cursor


def now():
    return datetime.now(timezone.utc).isoformat()


def file(path):
    path = Path(path)
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return {"path": str(path.resolve()), "sha256": digest.hexdigest(), "bytes": path.stat().st_size}


def write(path, value):
    path = Path(path)
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def checked_core_path(row, root=None):
    if not isinstance(row, dict) or set(row) != {"path", "sha256", "bytes"}:
        raise ValueError("exact core input file binding required")
    path = Path(row["path"])
    if (not path.is_absolute() or path.is_symlink() or not path.is_file()
            or (root is not None and not path.resolve().is_relative_to(root.resolve()))):
        raise ValueError("native core input changed or left its owned root")
    return path


def checked(row, root=None):
    path = checked_core_path(row, root)
    if file(path) != row:
        raise ValueError("native core input changed or left its owned root")
    return path


def session_request(core, native_request, runtime, panel, directory):
    """The existing installer owner supplies already verified installed paths."""
    directory = Path(directory)
    directory.mkdir()
    value = {"schema": 1, "platform": sys.platform, "native_request": file(native_request),
             "runtime": file(runtime), "panel": file(panel), "core": core,
             "evidence_directory": str(directory.resolve())}
    path = directory / "core-request.json"
    write(path, value)
    return path


def installed_payload_members(native_request, installed_chain):
    """Join retained installed observations to original File/component records.

    Callers authenticate the original request and journal. This pure join does
    not reopen removed paths or establish authenticity, liveness or acceptance.
    """
    def inventory(rows, label):
        if not isinstance(rows, list) or not rows:
            raise ValueError('missing installed payload ' + label)
        result = {}
        for row in rows:
            if not isinstance(row, dict) or not isinstance(row.get('id'), str) or not row['id'] or row['id'] in result:
                raise ValueError('ambiguous installed payload ' + label)
            result[row['id']] = row
        return result

    def absolute_path(value):
        if not isinstance(value, str) or not value or '\x00' in value:
            raise ValueError('plain absolute installed component path required')
        path = PureWindowsPath(value)
        if (not path.is_absolute() or value.startswith(('\\\\?\\', '\\\\.\\'))
                or str(path) != value.replace('/', '\\')
                or any(part in ('.', '..') or part.rstrip(' .') != part
                       or any(c in part for c in ':<>"|?*') for part in path.parts[1:])):
            raise ValueError('plain absolute installed component path required')
        return path

    selected = inventory(native_request.get('selected_app_chain', {}).get('apps'), 'applications')
    observed = inventory(installed_chain.get('apps'), 'journal applications')
    readback = native_request.get('selected_app_readback')
    if not isinstance(readback, dict) or set(selected) != set(observed) or set(readback) != set(selected):
        raise ValueError('installed payload application membership differs')
    result, paths = {}, set()
    for app_id, package in selected.items():
        app = observed[app_id]
        if app.get('package') != package:
            raise ValueError('installed payload package differs')
        if package.get('variant') == 'oa-comfyui-native':
            compiled = readback[app_id]
            native = compiled.get('native', {})
            if (app_id != 'comfyui' or compiled.get('variant') != package['variant'] or
                    native.get('binary') != package.get('delivery', {}).get('binary') or
                    native.get('identity', {}).get('distribution') != package['variant']):
                raise ValueError('installed native archive differs from original delivery')
            version = native['identity'].get('version')
            if not isinstance(version, str) or not re.fullmatch('[A-Za-z0-9._-]{1,64}', version) or version in ('.', '..'):
                raise ValueError('installed native archive version differs')
            local = absolute_path(installed_chain.get('fixture_owner', {}).get('local_app_data'))
            root = local / 'OA' / 'a' / 'cu' / version
            if absolute_path(app.get('root')) != root:
                raise ValueError('installed native archive left the fixture KnownFolder root')
            expected = inventory(compiled.get('native_files'), 'original native files')
            actual = inventory(app.get('files'), 'observed native files')
            if set(actual) != set(expected):
                raise ValueError('installed native archive membership differs')
            members = {}
            for identity, pin in expected.items():
                relative = PureWindowsPath(pin.get('path', ''))
                row = actual[identity]
                path = absolute_path(row.get('path'))
                if (relative.drive or relative.root or not relative.parts or
                        any(part in ('.', '..') or part.rstrip(' .') != part or any(c in part for c in ':<>"|?*') for part in relative.parts) or
                        pin['path'] != identity or path != root / relative or str(path).casefold() in paths or
                        row.get('plain') is not True or row.get('ancestors_plain') is not True or
                        not isinstance(row.get('identity'), str) or not 1 <= len(row['identity']) <= 256 or
                        not re.fullmatch('[0-9a-f]{64}', str(pin.get('sha256'))) or
                        type(pin.get('bytes')) is not int or pin['bytes'] < 0 or
                        any(row.get(key) != pin[key] for key in ('sha256', 'bytes'))):
                    raise ValueError('installed native archive path/identity/bytes differs')
                paths.add(str(path).casefold())
                members[identity] = {key: row[key] for key in ('path', 'sha256', 'bytes', 'identity')}
            result[app_id] = members
            continue
        payload = readback[app_id].get('expected_payload', {})
        if (payload.get('schema') != 1 or any(payload.get('msi', {}).get(key) != package.get(key)
                                            for key in ('sha256', 'bytes'))):
            raise ValueError('installed payload container differs')
        expected = inventory(payload.get('files'), 'expected files')
        measured = inventory(app.get('measured_components'), 'measured components')
        actual = inventory(app.get('files'), 'observed files')
        if set(expected) != set(measured) or set(expected) != set(actual):
            raise ValueError('installed payload File membership differs')
        members, anchors = {}, {}
        for identity, pin in expected.items():
            component, row = measured[identity], actual[identity]
            path = row.get('path')
            windows = absolute_path(path)
            key_path = absolute_path(row.get('component_path'))
            name, key_name = component.get('name'), component.get('key_name')
            declared = pin.get('path')
            relative = PureWindowsPath(declared) if isinstance(declared, str) else PureWindowsPath()
            if (not isinstance(name, str) or not name or PureWindowsPath(name).name != name
                    or name in ('.', '..') or name.rstrip(' .') != name
                    or any(c in name for c in ':<>"|?*')
                    or not isinstance(declared, str) or not declared or relative.drive or relative.root
                    or str(relative) != declared.replace('/', '\\')
                    or any(part in ('.', '..') or part.rstrip(' .') != part
                           or any(c in part for c in ':<>"|?*') for part in relative.parts)
                    or name != relative.name
                    or not isinstance(key_name, str) or key_path.name != key_name
                    or str(windows).casefold() != str(key_path.parent / name).casefold()
                    or str(windows).casefold() in paths
                    or component.get('declared_path') != pin.get('path')
                    or not re.fullmatch(r'\{[0-9A-Fa-f-]{36}\}', str(component.get('component')))
                    or row.get('component') != component.get('component')
                    or not isinstance(row.get('identity'), str) or not row['identity'].strip() or len(row['identity']) > 256
                    or not re.fullmatch('[0-9a-f]{64}', str(pin.get('sha256')))
                    or type(pin.get('bytes')) is not int or pin['bytes'] < 0
                    or type(row.get('bytes')) is not int or type(component.get('bytes')) is not int
                    or any(row.get(key) != pin[key] or component.get(key) != pin[key] for key in ('sha256', 'bytes'))):
                raise ValueError('installed payload File/component/path binding differs')
            component_id = component['component'].casefold()
            anchor = (str(key_path).casefold(), key_name.casefold())
            if component_id in anchors and anchors[component_id] != anchor:
                raise ValueError('installed payload component anchor differs')
            anchors[component_id] = anchor
            paths.add(str(windows).casefold())
            members[identity] = {key: row[key] for key in ('path', 'sha256', 'bytes', 'identity', 'component', 'component_path')}
        result[app_id] = members
    return result


def installed_runtime_interval(native_request, installed_chain, runtime_program, account):
    """Validate retained installed-process observations, without endpoint proof.

    The caller authenticates the original native request, installation journal,
    actual owner/account and canonical runtime member. SDK connection binding
    remains an independent requirement. This returns no acceptance receipt.
    """
    report = installed_chain.get('runtime_interval', {})
    if (not isinstance(report, dict) or report.get('state') != 'stable' or report.get('failure') is not None or
            report.get('sid') != account or not re.fullmatch(r'S-1-[0-9-]+', str(account)) or
            any(not re.fullmatch('[0-9a-f]{64}', str(native_request.get(key))) or
                report.get(key) != native_request[key] for key in ('runtime_sha256', 'supervisor_sha256')) or
            runtime_program.get('sha256') != native_request['runtime_sha256']):
        raise ValueError('installed runtime interval differs from original runtime/account pins')
    def timestamp(value):
        if not isinstance(value, str):
            raise ValueError('installed runtime observation time missing')
        try:
            parsed = datetime.fromisoformat(value.replace('Z', '+00:00'))
        except ValueError as error:
            raise ValueError('installed runtime observation time invalid') from error
        if parsed.tzinfo is None or parsed.utcoffset().total_seconds() != 0:
            raise ValueError('installed runtime observation time must be UTC')
        return parsed
    start, end = timestamp(report.get('started_at')), timestamp(report.get('ended_at'))
    if end < start:
        raise ValueError('installed runtime interval time reversed')
    expected = {'runtime': (runtime_program['path'], native_request['runtime_sha256'], 'serve runtime --supervised'),
                'supervisor': (str(PureWindowsPath(runtime_program['path']).with_name('openabstractionsw.exe')),
                               native_request['supervisor_sha256'], 'serve supervisor(?: --service)?')}
    fields = {'role', 'pid', 'parent', 'birth', 'handle_birth', 'session', 'sid', 'path', 'command', 'sha256'}
    def roles(rows):
        if not isinstance(rows, list) or len(rows) != 2:
            raise ValueError('installed runtime role observations missing')
        result = {}
        for row in rows:
            if not isinstance(row, dict) or set(row) != fields or row.get('role') not in expected or row['role'] in result:
                raise ValueError('installed runtime role observations ambiguous')
            path, digest, command = expected[row['role']]
            if (row['path'] != path or row['sha256'] != digest or row['sid'] != account or
                    any(type(row[key]) is not int or row[key] <= 0 for key in ('pid', 'parent')) or
                    type(row['session']) is not int or row['session'] < 0 or
                    not re.fullmatch(r'"?' + re.escape(path) + r'"?\s+' + command, str(row['command'])) or
                    timestamp(row['birth']) > start or timestamp(row['handle_birth']) > start):
                raise ValueError('installed runtime process identity differs')
            result[row['role']] = row
        runtime, supervisor = result['runtime'], result['supervisor']
        if (runtime['pid'] == supervisor['pid'] or runtime['parent'] != supervisor['pid'] or
                runtime['session'] != supervisor['session'] or timestamp(supervisor['birth']) > timestamp(runtime['birth']) or
                timestamp(supervisor['handle_birth']) > timestamp(runtime['handle_birth'])):
            raise ValueError('installed runtime supervisor relationship differs')
        return result
    before, after = roles(report.get('before')), roles(report.get('after'))
    if before != after:
        raise ValueError('installed runtime process changed during the scene')
    return {'runtime': before['runtime'], 'supervisor': before['supervisor'],
            'started_at': report['started_at'], 'ended_at': report['ended_at']}


def fixture_secret_name(core, evidence_root):
    if not re.fullmatch('[0-9a-f]{64}', str(core.get('go', {}).get('sha256'))):
        raise ValueError('exact native permission caller digest required')
    return 'native-core-' + core['go']['sha256'][:12] + '-' + hashlib.sha256(str(evidence_root).encode()).hexdigest()[:12]


def validate_result(value, core, *, permission=False):
    """Reject an installer exit that lacks the accepted native outcomes."""
    if not isinstance(value, dict) or value.get("result") != "pass" or value.get("core") != core:
        raise ValueError("native core proof missing or bound to another caller/source")
    if (value.get("runtime", {}).get("sha256") != core.get("runtime_sha256")
            or value.get("panel", {}).get("sha256") != core.get("panel_sha256")
            or not re.fullmatch("[0-9a-f]{64}", str(core.get("runtime_sha256")))
            or not re.fullmatch("[0-9a-f]{64}", str(core.get("panel_sha256")))):
        raise ValueError("native core installed runtime/presenter differs from package")
    for language in ("go", "python"):
        proof = value.get(language, {})
        posting = proof.get("posting_process", {})
        expected_caller = core["go" if language == "go" else "python"]["path"]
        if (posting.get("exited") is not True or posting.get("exit_code") != 0
                or type(posting.get("pid")) is not int or posting["pid"] <= 0
                or not posting.get("argv") or posting["argv"][0] != expected_caller
                or proof.get("action") != "copy" or proof.get("acted") is not True
                or proof.get("acknowledged") is not True or proof.get("gone") is not True
                or proof.get("copied") != proof.get("expected")
                or not isinstance(proof.get("expected"), dict)
                or not re.fullmatch("[0-9a-f]{64}", str(proof["expected"].get("sha256")))
                or not proof.get("request_ref") or not proof.get("occurrence_id")):
            raise ValueError("native " + language + " notification effect/ack/gone proof incomplete")
    go = value['go']
    projection = go.get('native_projection', {})
    resumed = go.get('resumed_process', {})
    if (projection.get('id') != go['occurrence_id'] or projection.get('state') != 'acted'
            or projection.get('action') != 'Copy now' or projection.get('operation') != go.get('operation_id')
            or not go.get('operation_id') or resumed.get('exit_code') != 0
            or not resumed.get('argv') or resumed['argv'][0] != core['go']['path']
            or '--notification' not in resumed['argv']):
        raise ValueError('native Go action/current operation/resumed caller binding missing')
    result = value["python"].get("observed_result", {})
    if (result.get("action") != "copy" or result.get("id") != value["python"]["occurrence_id"]
            or os.path.normcase(result.get("decided_by", "")) != os.path.normcase(value['panel'].get('path', ''))
            or not result.get("decided_by") or type(result.get("decided_ms")) is not int or result["decided_ms"] <= 0):
        raise ValueError("Python native action attribution missing")
    try:
        review = value['personal_reviews']['notifications']
        account = value['receiving_identity']['runtime']['account']
        if not account:
            raise ValueError('native core receiving account missing')
        validate_personal_review(review, NOTIFICATION_PERMISSION, account, core['python']['path'],
            value['panel']['path'], {'feature': 'notifications', 'program': core['python']['path']}, core['python'])
        validate_personal_cleanup(review)
        instant = lambda text: datetime.fromisoformat(text.replace('Z', '+00:00'))
        if not (instant(value['started_at']) <= instant(review['started_at']) <= instant(review['observed_at'])
                <= instant(value['python']['posting_process']['started_at']) <= instant(value['ended_at'])):
            raise ValueError('Python post did not follow its completed native review')
    except (KeyError, TypeError, AttributeError) as error:
        raise ValueError('native Python personal permission proof missing') from error
    if permission:
        proof = value.get("permission", {})
        if any(proof.get(key) is not True for key in ("initial_refused", "native_allow", "scoped_read", "other_copy_refused", "removed")):
            raise ValueError("native Mac scoped permission effect/refusal/removal proof incomplete")
        try:
            evidence_root = PurePosixPath(core['directory']) / 'logs/native-core'
            name = fixture_secret_name(core, evidence_root)
            rule, pending = proof['rule'], proof['pending_request']
            selector = rule['Selector']
            subject, actor = rule['Subject'], rule['SetBy']
            permission_key = {'Action': 'abstraction.credentials/secret.read', 'ResourceProfile': 'abstraction.credentials/named-secret@1'}
            instant = lambda text: datetime.fromisoformat(text.replace('Z', '+00:00'))
            asked, set_at = instant(pending['Asked']), instant(rule['SetAt'])
            if (value['request']['path'] != str(evidence_root / 'core-request.json') or proof['name'] != name
                    or not proof['request_id'] or pending['ID'] != proof['request_id']
                    or pending['About'] != core['go']['path'] or pending['Key'] != 'rights.permission@2'
                    or pending.get('Option') or subject['Program'] != core['go']['path']
                    or not subject['Account'] or actor['Account'] != subject['Account']
                    or actor['Program'] != value['panel']['path'] or rule.get('Permit') is not True
                    or rule['Action'] != permission_key['Action'] or rule['Resource'] != name
                    or selector['Permission'] != permission_key or selector['Scope'] != 'one'
                    or selector['Name'] != name or selector['Target'] != name or selector.get('Editable') is not True
                    or not (instant(value['started_at']) <= asked <= set_at <= instant(value['ended_at']))):
                raise ValueError('native Mac permission request/rule receiving binding differs')
        except (KeyError, TypeError, AttributeError) as error:
            raise ValueError('native Mac permission exact request/rule proof missing') from error
    if value.get("owned_children_exited") is not True or value.get("private_evidence") is not True:
        raise ValueError("native core process ownership/private evidence incomplete")


def panel_url(text):
    match = re.search(r"^service control panel: (\S+)$", text, re.MULTILINE)
    if not match:
        return None
    parsed = urllib.parse.urlsplit(match[1])
    query = urllib.parse.parse_qs(parsed.query, strict_parsing=True)
    if (parsed.scheme != "http" or parsed.hostname != "127.0.0.1" or not parsed.port
            or parsed.username is not None or parsed.password is not None or parsed.fragment
            or parsed.path != "/start" or set(query) != {"k"} or len(query["k"]) != 1
            or not re.fullmatch("[0-9a-f]{48}", query["k"][0])):
        raise ValueError("owned Panel did not announce one keyed loopback URL")
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, "", "", "")), query["k"][0]


class LocalNoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        raise ValueError('fixed native local channel cannot redirect')


def local_opener():
    # This channel carries the private Panel key; host proxies and redirects
    # cannot become another recipient of the local owner's request.
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), LocalNoRedirect())


class NativeCore:
    personal_review_allowance = 180
    def __init__(self, request_path):
        self.request_path = Path(request_path)
        self.request = json.loads(self.request_path.read_text(encoding="utf-8-sig"))
        self.root = self.request_path.parent.resolve()
        if (self.request.get("schema") != 1 or self.request.get("platform") != sys.platform
                or self.request.get("evidence_directory") != str(self.root) or self.request_path.is_symlink()):
            raise ValueError("one fixed current-platform core request required")
        self.core = self.request["core"]
        for name in ("source_sha", "tool_source"):
            if not re.fullmatch("[0-9a-f]{40}", str(self.core.get(name))):
                raise ValueError("core source/tool identity missing")
        inputs_root = Path(self.core["directory"]).resolve()
        private_sandbox = self.core.get('proof_scope') == 'PRIVATE/SOURCE-BOUND' and self.core.get('release_sdk_eligible') is False
        if private_sandbox:
            import ctypes
            name=ctypes.create_unicode_buffer(257);size=ctypes.c_uint32(len(name))
            if (os.name!='nt' or not ctypes.windll.advapi32.GetUserNameW(name,ctypes.byref(size))
                    or name.value.casefold()!='wdagutilityaccount' or ctypes.windll.shell32.IsUserAnAdmin()
                    or not self.root.is_relative_to(Path(r'C:\oa-work\person-out\native'))):
                raise ValueError('private native source caller belongs only to its disposable Windows Sandbox owner')
        self.app = None if private_sandbox else checked(self.core["go"], inputs_root)
        self.script = checked(self.core["journey"], inputs_root)
        if checked(self.core["helper"], inputs_root) != Path(__file__).resolve():
            raise ValueError("native core helper differs from fixed source")
        self.library = checked(self.core["native_library"], inputs_root)
        self.python = checked(self.core["python"])
        self.runtime = checked(self.request["runtime"])
        self.panel = checked(self.request["panel"])
        if (self.request['runtime']['sha256'] != self.core['runtime_sha256']
                or self.request['panel']['sha256'] != self.core['panel_sha256']):
            raise ValueError('native core installed runtime/presenter differs from package')
        checked(self.request["native_request"])
        pythonpath = Path(self.core["pythonpath"]).resolve()
        expected = {checked(row, inputs_root) for row in self.core["python_files"]}
        if ({p.resolve() for p in pythonpath.rglob("*.py")} != expected or not pythonpath.is_relative_to(inputs_root)
                or any(p.is_symlink() for p in pythonpath.rglob("*"))):
            raise ValueError("native Python SDK inventory changed")
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("ABSTRACTION_RUNTIME_")}
        self.env.update(PYTHONPATH=str(pythonpath), PYTHONDONTWRITEBYTECODE="1")
        self.http = local_opener()
        started = time.monotonic()
        # The mandatory review has its own finite allowance; action time stays bounded.
        self.deadline = started + 180
        self.cleanup_deadline = started + 195  # at most 375s, inside the 390s installer owner
        self.cleaning = False
        self.children, self.commands = [], []
        self.data = {"schema": 1, "result": "running", "core": self.core, "request": file(self.request_path),
                     "native_request": self.request["native_request"], "runtime": self.request["runtime"],
                     "panel": self.request["panel"], "started_at": now(), "private_evidence": True,
                     "commands": self.commands}
        self.progress("starting", {})

    def progress(self, phase, pending):
        value = {"phase": phase, "pending": pending, "updated_at": now(), "source_sha": self.core["source_sha"],
                 "native_request_sha256": self.request["native_request"]["sha256"], "private_evidence": True}
        write(self.root / "core-progress.json", value)
        write(Path(self.request["native_request"]["path"]).parent / "core-progress.json", value)
        print(json.dumps({"native_core": phase, "pending": pending}), flush=True)

    def remaining(self):
        remaining = (self.cleanup_deadline if self.cleaning else self.deadline) - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("native core action missing within the existing owner bound")
        return remaining

    def start(self, name, argv):
        path = self.root / (name + ".log")
        stream = path.open("wb")
        try:
            process = subprocess.Popen([str(x) for x in argv], env=self.env, stdin=subprocess.PIPE,
                stdout=stream, stderr=subprocess.STDOUT, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        finally:
            stream.close()
        row = {"operation": name, "argv": [str(x) for x in argv], "pid": process.pid, "started_at": now(),
               "log_path": str(path), "private_evidence": str(argv[0]) == str(self.panel)}
        self.commands.append(row)
        self.children.append((process, row))
        return process, row, path

    def call(self, name, argv, *, input=None, acceptable=(0,), seconds=10):
        if not 0 < seconds <= 120:
            raise ValueError('native command deadline exceeds the existing bounded owner')
        process, row, path = self.start(name, argv)
        try:
            process.communicate(input=input, timeout=min(seconds, self.remaining()))
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)
            row.update(exit_code=process.returncode, exited=True, ended_at=now(), log=file(path))
            write(self.root / "core-result.json", self.data)
        if process.returncode not in acceptable:
            raise ValueError(name + " failed: " + path.read_text(errors="replace")[-1500:])
        return path.read_text(errors="replace"), row

    def until(self, read):
        while True:
            self.remaining()
            found = read()
            if found:
                return found
            time.sleep(min(.2, self.remaining()))

    def api(self, route, body=None):
        # Only the one created selector's cleanup writes through the Panel.
        if body is not None and (route != "/rights" or body.get("edit") != "revoke"):
            raise ValueError("native acceptance cannot dispatch a Panel action")
        request = urllib.request.Request(self.base + route, None if body is None else json.dumps(body).encode(),
                                        headers={"X-Panel-Key": self.key, "Content-Type": "application/json"})
        with self.http.open(request, timeout=min(3, self.remaining())) as response:
            return json.load(response)

    def notification(self, occurrence):
        page = self.api("/notifications")
        if page.get("outcome") != "page":
            raise ValueError("installed Panel notification readback unavailable")
        return next((row for row in page["notifications"] if row["id"] == occurrence), None)

    def personal_rights(self):
        first, rows, cursor = None, [], ''
        for _ in range(64):
            page = self.api('/rights?cursor=' + urllib.parse.quote(cursor, safe=''))
            if page.get('Outcome') != 'page':
                raise ValueError('current personal permission rules unavailable')
            if first is None:
                first = page
            elif page.get('Revision') != first.get('Revision'):
                return None
            rows.extend(page['Rules'])
            if page['Complete']:
                return {**first, 'Rules': rows, 'Complete': True}
            if not page['Next'] or page['Next'] == cursor:
                raise ValueError('personal permission cursor did not advance')
            cursor = page['Next']
        raise ValueError('personal permission page bound exceeded')

    def personal_review(self, feature, program, permissions, *, app=None, host=None, identity=None):
        if not self.personal_review_allowance:
            return self._personal_review(feature, program, permissions, app=app, host=host, identity=identity)
        if getattr(self, '_personal_review_claimed', False):
            raise ValueError('native core personal review allowance already used')
        self.remaining()
        self._personal_review_claimed = True
        started = time.monotonic()
        action_deadline = self.deadline
        self.deadline = started + self.personal_review_allowance
        try:
            return self._personal_review(feature, program, permissions, app=app, host=host, identity=identity)
        finally:
            elapsed = min(self.personal_review_allowance, time.monotonic() - started)
            self.deadline = action_deadline + elapsed
            self.cleanup_deadline += elapsed

    def _personal_review(self, feature, program, permissions, *, app=None, host=None, identity=None):
        receiving = self.api('/identity')
        account = receiving['runtime']['account']
        self.data['receiving_identity'] = receiving
        context = {'feature': feature, **({'app': app} if app else {'program': program}),
                   **({'host': host} if host else {})}
        before = self.until(self.personal_rights)
        personal_permission_baseline(before, permissions, account, program)
        receipt = self.root / ('personal-review-' + feature) / 'decision.json'
        if receipt.parent.exists():
            raise ValueError('personal review evidence target already exists')
        # The Panel's existing private-directory owner creates and checks this parent.
        argv = [self.panel, '-native=true', '-page', 'permission-review', '-review-feature', feature]
        argv.extend(['-review-app', app] if app else ['-review-program', program])
        if host:
            argv.extend(['-review-host', host])
        argv.extend(['-review-receipt', receipt])
        started = now()
        process, row, _ = self.start('personal-review-' + feature, argv)
        proof = {'account': account, 'program': program, 'permissions': permissions, 'context': context,
                 'before': before, 'started_at': started, 'process': row}
        self.data.setdefault('personal_reviews', {})[feature] = proof
        self.progress('native-' + feature + '-review', {'program': program, 'feature': feature,
            'native_owner_pid': process.pid, 'instruction': 'Review the application and one-hour permissions in the native window; choose Allow or Refuse.'})
        previous = None
        def decision_ready():
            nonlocal previous
            if receipt.is_file():
                if receipt.is_symlink():
                    raise ValueError('native personal review receipt became a link; outcome unknown')
                try:
                    raw = receipt.read_bytes()
                except OSError as error:
                    if process.poll() is not None:
                        raise ValueError('native personal review receipt unavailable after owner exit; outcome unknown') from error
                    return None
                try:
                    text = raw.decode('utf-8')
                    decision = json.loads(text)
                except (UnicodeDecodeError, json.JSONDecodeError) as error:
                    if process.poll() is not None:
                        raise ValueError('native personal review receipt incomplete after owner exit; outcome unknown') from error
                    previous = raw
                    return None
                if previous != raw:
                    previous = raw
                    return None  # The create-new writer exposes its path before Write/Sync/Close.
                proof['receipt'] = {'path': str(receipt.resolve()), 'sha256': hashlib.sha256(raw).hexdigest(), 'bytes': len(raw)}
                proof['decision_json'] = text
                proof['decision_outcome'] = decision.get('result', {}).get('outcome')
                if proof['decision_outcome'] != 'applied':
                    raise ValueError('native personal review ' + str(proof['decision_outcome']) + '; preserve decision and policy')
                page = self.personal_rights()
                if page is None:
                    return None
                proof.update(page=page, observed_at=now())
                proof['rules'] = personal_permission_rows(page, permissions, account, program, str(self.panel),
                    started, proof['observed_at'], decision.get('review', {}).get('id'))
                if not proof['rules']:
                    raise ValueError('native personal review applied without every required rule')
                validate_personal_review(proof, permissions, account, program, str(self.panel), context, identity)
                proof['validated'] = True
                return proof
            if process.poll() is not None:
                raise ValueError('native personal review ended without a decision; outcome unknown')
            return None
        return self.until(decision_ready)

    def cleanup_personal_review(self, proof):
        if proof.get('validated') is not True or not proof.get('rules') or proof.get('decision_outcome') != 'applied':
            raise ValueError('personal review incomplete; preserve its policy and evidence')
        remaining = list(proof['rules'])
        cleanup = proof.setdefault('cleanup', {'steps': []})
        for rule in proof['rules']:
            page = self.until(self.personal_rights)
            personal_permission_cleanup(page, dict(proof, rules=remaining))
            # This exact owned cleanup is the sole legacy write this fixture makes.
            response = NativeCore.api(self, '/rights', {'edit': 'revoke', 'revision': page['Revision'],
                'account': proof['account'], 'program': proof['program'],
                'action': rule['Action'], 'resource': rule['Resource']})
            if response.get('Outcome') != 'applied' or not response.get('Revision') or response.get('Current') is not None:
                raise ValueError('personal review cleanup uncertain; retain remaining rules')
            cleanup['steps'].append({'before': page, 'rule': rule, 'response': response})
            remaining.remove(rule)
        after = self.until(self.personal_rights)
        if personal_permission_targets(after, proof['permissions'], proof['account'], proof['program']):
            raise ValueError('personal review rule remained after cleanup')
        cleanup.update(after=after, complete=True)

    def run_go(self):
        source, _, source_log = self.start("source", [self.app, "--source"])
        announcement = self.until(lambda: re.search(r"example source: (http://127\.0\.0\.1:[0-9]+) ([0-9]+) sha256:([0-9a-f]{64})", source_log.read_text(errors="replace")))
        url, size, checksum = announcement.groups()
        out = self.root / "go-output"
        argv = [self.app, "--out", out, url + "/fixture.bin", size, "sha256:" + checksum]
        posted, posting = self.call("go-post", [*argv[:3], "--notify-when-done", *argv[3:]])
        match = re.search(r"notification ([a-z0-9-]+): (scheduled|posted); this app can exit", posted)
        operation = re.search(r"submitted ([a-z0-9-]+)", posted)
        refs = list(out.glob("*.notification.json"))
        if not match or not operation or len(refs) != 1 or source.poll() is not None:
            raise ValueError("Go post did not retain one exited caller/operation/ref")
        ref = json.loads(refs[0].read_text())
        occurrence = match[1]
        offered = self.until(lambda: (row if (row := self.notification(occurrence)) and row['state'] == 'posted' else None))
        if offered.get('operation') != operation[1] or not any(a.get('id') == 'copy' and a.get('title') == 'Copy now' for a in offered.get('actions', [])):
            raise ValueError('current native Go notification did not offer Copy now for this operation')
        self.progress("go-native-copy", {"id": occurrence, "request_ref": ref, "operation": operation[1],
             "caller": str(self.app), "action": "Copy now", "posting_pid_exited": posting["pid"]})
        current = self.until(lambda: (row if (row := self.notification(occurrence)) and row["state"] == "acted" else None))
        self.data["go_action_observation"] = {
            "expected": {"action": "Copy now", "operation": operation[1]},
            "offered": {key: offered.get(key) for key in ("id", "state", "action", "operation")},
            "observed": {key: current.get(key) for key in ("id", "state", "action", "operation")}}
        if current.get("operation") != operation[1]:
            raise ValueError("native Go notification operation differs: expected %r, observed %r" %
                             (operation[1], current.get("operation")))
        if current.get("action") != "Copy now":
            raise ValueError("native Go notification action differs: expected 'Copy now', observed %r" % current.get("action"))
        acted_at = now()
        resumed, resume = self.call("go-resume", [*argv[:3], "--notification", *argv[3:]])
        copied = file(out / "fixture.bin")
        expected = {"sha256": checksum, "bytes": int(size)}
        if "notification: acted (copy)" not in resumed or {k: copied[k] for k in expected} != expected:
            raise ValueError("Go caller did not deliver the native action's exact bytes")
        gone, _ = self.call("go-gone", [*argv[:3], "--notification", *argv[3:]])
        if "notification: gone" not in gone or file(out / "fixture.bin") != copied:
            raise ValueError("Go exact result was not acknowledged once after its copy")
        self.data["go"] = {"posting_process": posting, "request_ref": ref, "ref_file": file(refs[0]),
             "occurrence_id": occurrence, "operation_id": operation[1], "native_projection": current,
             "offered_projection": offered,
            "posted_at_ms": current["at"], "acted_observed_at": acted_at, "raw_actor_observed": False,
             "action": "copy", "acted": True, "copied": expected, "expected": expected,
             "resumed_process": resume, "acknowledged": True, "gone": True}
        with self.http.open(url + "/fixture.bin", timeout=min(3, self.remaining())) as response:
            body = response.read(int(size) + 1)
        payload = self.root / "python-source.bin"
        payload.write_bytes(body)
        if file(payload)["sha256"] != checksum or len(body) != int(size):
            raise ValueError("owned Python copy input differs from the Go fixture")
        return payload

    def run_python(self, payload):
        self.personal_review('notifications', str(self.python), NOTIFICATION_PERMISSION, identity=self.core['python'])
        python_request = self.root / "python-request.json"
        write(python_request, {"library": self.core["native_library"], "panel": self.request["panel"],
              "payload": file(payload), "out": str(self.root / "python-copy.bin"),
              "ref_path": str(self.root / "python-ref.json"), "receipt": str(self.root / "python-outcome.json")})
        posted, posting = self.call("python-post", [self.python, "-B", self.script, "--native-python", "post", "--native-request", python_request])
        post = json.loads(posted)
        ref = json.loads((self.root / "python-ref.json").read_text())
        offered = self.until(lambda: (row if (row := self.notification(post['id'])) and row['state'] == 'posted' else None))
        if not any(a.get('id') == 'copy' and a.get('title') == 'Copy now' for a in offered.get('actions', [])):
            raise ValueError('current Python notification did not offer Copy now')
        self.progress("python-native-copy", {"id": post["id"], "request_ref": ref, "caller": str(self.python),
              "action": "Copy now", "posting_pid_exited": posting["pid"]})
        self.until(lambda: (row if (row := self.notification(post["id"])) and row["state"] == "acted" else None))
        self.call("python-resume", [self.python, "-B", self.script, "--native-python", "resume", "--native-request", python_request])
        proof = json.loads((self.root / "python-outcome.json").read_text())
        proof.update(posting_process=posting, occurrence_id=post["id"], request_ref=ref, offered_projection=offered)
        self.data["python"] = proof

    def permission(self):
        name = fixture_secret_name(self.core, self.root)
        value = b"harmless-native-core-secret"
        rule, added = None, False
        try:
            self.call("credential-help", [self.runtime, "credentials", "--help"])
            self.call("credential-add", [self.runtime, "credentials", "add", name, "--target", "127.0.0.1", "--for", "abstraction.download/http-execution@1", "--from-stdin", "--json"], input=value)
            added = True
            refused, initial = self.call("permission-initial", [self.app, "--read-secret", name], acceptable=(1,))
            if "forbidden" not in refused:
                raise ValueError("raw named-secret Read was not refused")
            pending, _ = self.call("permission-pending", [self.runtime, "questions", "permissions", "--json"])
            requests = [r for r in json.loads(pending)["records"] if os.path.normcase(r.get("About", "")) == os.path.normcase(str(self.app)) and not r.get("Option")]
            if len(requests) != 1:
                raise ValueError("one current typed permission request required")
            self.progress("mac-native-allow", {"id": requests[0]["ID"], "program": str(self.app), "name": name,
                  "action": "Allow read saved secret value for this program and this one name"})
            def read_rule():
                page = self.api("/rights")
                if page.get("Outcome") != "page" or page.get("SelectorAvailable") is not True:
                    raise ValueError("native typed selector readback unavailable")
                rows = [r for r in page["Rules"] if r.get("Selector", {}).get("Name") == name
                        and r["Subject"]["Program"] == str(self.app)]
                if len(rows) > 1:
                    raise ValueError("native Allow created more than the one expected selector")
                return (page, rows[0]) if rows else None
            page, rule = self.until(read_rule)
            selector = rule["Selector"]
            if (rule.get("Permit") is not True or selector.get("Scope") != "one"
                    or selector["Permission"] != {"Action": "abstraction.credentials/secret.read", "ResourceProfile": "abstraction.credentials/named-secret@1"}
                    or rule.get("SetBy", {}).get("Program") != str(self.panel)
                    or not rule.get("SetAt") or rule["SetAt"] < self.data["started_at"]):
                raise ValueError("native Allow did not create the exact named-secret scope")
            allowed, _ = self.call("permission-read", [self.app, "--read-secret", name])
            if "read " + name + ": " + str(len(value)) + " bytes" not in allowed:
                raise ValueError("native named-secret permission had no receiving effect")
            other = self.root / self.app.name
            shutil.copyfile(self.app, other)
            other.chmod(0o755)
            denied, _ = self.call("permission-other-copy", [other, "--read-secret", name], acceptable=(1,))
            if "forbidden" not in denied:
                raise ValueError("native permission leaked to another program copy")
            self.data["permission"] = {"request_id": requests[0]["ID"], "name": name, "rule": rule,
                  "pending_request": requests[0],
                  "initial_refused": True, "native_allow": True, "scoped_read": True, "other_copy_refused": True}
        finally:
            self.cleaning = True
            if rule is not None:
                page = self.api("/rights")
                present = [r for r in page["Rules"] if r.get("Selector", {}).get("Name") == name and r["Subject"] == rule["Subject"]]
                if present != [rule]:
                    raise ValueError("created selector changed; preserve it for owner inspection")
                removed = self.api("/rights", {"edit": "revoke", "revision": page["Revision"],
                    "account": rule["Subject"]["Account"], "program": rule["Subject"]["Program"],
                    "action": "abstraction.credentials/secret.read", "resource_profile": "abstraction.credentials/named-secret@1",
                    "scope": "one", "credential_name": name})
                if removed.get("Outcome") != "applied":
                    raise ValueError("created selector cleanup uncertain")
                after = self.api("/rights")
                if any(r.get("Selector", {}).get("Name") == name and r["Subject"] == rule["Subject"] for r in after["Rules"]):
                    raise ValueError("created selector remained after removal")
                self.data.setdefault("permission", {})["removed"] = True
            if added:
                self.call("credential-revoke", [self.runtime, "credentials", "revoke", name, "--json"])
            self.cleaning = False

    def close_owned_children(self, *, redact_panel_key=False):
        for process, row in reversed(self.children):
            if process.poll() is None:
                process.kill()  # This helper owns this exact Popen handle/PID.
                process.wait(timeout=5)
            if redact_panel_key and getattr(self,'key',None):
                path=Path(row['log_path'])
                path.write_text(path.read_text(errors='replace').replace(self.key,'[closed-panel-key]'),encoding='utf-8')
            row.update(exit_code=process.returncode, exited=True, ended_at=row.get('ended_at',now()), log=file(row['log_path']))

    def run(self):
        error = None
        try:
            panel, _, panel_log = self.start("panel-readback", [self.panel, "-native=false", "-open=false", "-addr", "127.0.0.1:0"])
            self.base, self.key = self.until(lambda: panel_url(panel_log.read_text(errors="replace")))
            payload = self.run_go()
            self.run_python(payload)
            if sys.platform == "darwin":
                self.permission()
        except BaseException as caught:
            error = caught
            self.data["failure"] = str(caught)
        finally:
            self.cleaning = True
            for proof in self.data.get('personal_reviews', {}).values():
                try:
                    self.cleanup_personal_review(proof)
                except BaseException as caught:
                    error = error or caught
                    self.data['personal_review_cleanup_failure'] = str(caught)
            self.close_owned_children()
            self.data.update(result="failed" if error else "pass", ended_at=now(), owned_children_exited=True)
            self.data["public_projection"] = {"source_sha": self.core["source_sha"], "platform": sys.platform,
                   "result": self.data["result"], "native_notifications": not error, "native_permission": sys.platform == "darwin" and not error}
            write(self.root / "core-result.json", self.data)
            self.progress("failed" if error else "complete", {})
        if error:
            raise error
        validate_result(self.data, self.core, permission=sys.platform == "darwin")
        return self.root / "core-result.json"


def python_caller(phase, request_path):
    from abstraction.asks.api import (NotificationAction, NotificationActivation, NotificationApplicationClient,
                                     NotificationDelivery, NotificationRequest, RequestRef)
    from abstraction.facade import Scope
    from abstraction.facade.client import Machine
    from abstraction.ipc import Library
    request = json.loads(Path(request_path).read_text())
    client = NotificationApplicationClient(Machine(library=Library(str(checked(request["library"]))))
             .resolve_service("abstraction.asks/notification@1", scope=Scope.LOCAL))
    ref_path = Path(request["ref_path"])
    if phase == "post":
        stamp = int(time.time() * 1000)
        ref = RequestRef(key="native-python-" + hashlib.sha256(str(ref_path).encode()).hexdigest()[:32], replay_until_ms=stamp + 300000)
        write(ref_path, dataclasses.asdict(ref))
        posted = client.post(NotificationRequest(request_ref=ref, title="Python copy ready", body="Copy the verified native acceptance fixture.",
             actions=[NotificationAction(id="copy", title="Copy now", activation=NotificationActivation.APP)],
             expires_ms=stamp + 90000, delivery=NotificationDelivery.NOW))
        write(Path(request['receipt']).with_name('python-post-result.json'),
              {'outcome': str(posted.outcome), 'id': str(posted.id or '')})
        if posted.outcome != "posted" or not posted.id:
            raise ValueError("Python notification was not posted")
        print(json.dumps(dataclasses.asdict(posted)), flush=True)
        return
    ref = RequestRef(**json.loads(ref_path.read_text()))
    seen = client.observe(ref, 0)
    result = seen.result
    if (seen.outcome != "acted" or result is None or result.id != seen.id or result.action != "copy"
            or os.path.normcase(result.decided_by) != os.path.normcase(request["panel"]["path"]) or result.decided_ms <= 0):
        raise ValueError("Python native action is absent, stale or attributed to another program")
    source = checked(request["payload"])
    out = Path(request["out"])
    if out.exists():
        raise ValueError("native Python effect output must be new")
    shutil.copyfile(source, out)
    expected = {k: request["payload"][k] for k in ("sha256", "bytes")}
    copied = {k: file(out)[k] for k in expected}
    if copied != expected:
        raise ValueError("native Python effect differs")
    ack = client.acknowledge(ref, result)
    gone = client.observe(ref, 0)
    if ack.outcome != "acknowledged" or gone.outcome != "gone":
        raise ValueError("native Python exact action acknowledgement/gone missing")
    write(request["receipt"], {"action": "copy", "acted": True, "observed_result": dataclasses.asdict(result),
         "copied": copied, "expected": expected, "acknowledged": True, "gone": True})


def main(request_path, python_phase=None):
    if python_phase:
        python_caller(python_phase, request_path)
    else:
        print(NativeCore(request_path).run(), flush=True)


def ordinary_readonly_root(core, ordinary, receipt, root):
    """Receive the preparer's finite immutable acquisition, never an output root."""
    asset_row = receipt['asset_receipt']
    asset_path = checked(asset_row)
    acquired = asset_path.parent
    if asset_path.name != 'asset-preparation.json':
        raise ValueError('ordinary acquisition receipt owner differs')
    if not acquired.is_relative_to(root):
        acquire = receipt['commands']['acquire']
        witness = ordinary['preflight'].get('acquisition_reuse', {})
        if (acquire.get('outcome') != 'reused_verified_complete_assets'
                or acquire.get('transfers') != 0 or acquire.get('chat_copy_bytes') != 0
                or acquire.get('acquisition_receipt') != {key: asset_row[key] for key in ('path', 'sha256')}):
            raise ValueError('ordinary retained acquisition proof missing')
        if (witness.get('kind') != 'completed_assets' or witness.get('root') != str(acquired)
                or witness.get('acquisition_receipt') != acquire['acquisition_receipt']
                or witness.get('operation_receipt') != acquire.get('operation_receipt')
                or any(acquire.get(key) != value for key, value in witness.get('observation', {}).items())
                or set(witness.get('observation', {})) != {'source_sha', 'run_id', 'intent_id', 'transfers', 'chat_copy_bytes'}):
            raise ValueError('ordinary retained acquisition witness differs')
        owner_row = acquire['operation_receipt']
        owner_path = Path(owner_row['path'])
        if (set(owner_row) != {'path', 'sha256'} or not owner_path.is_absolute()
                or owner_path.is_symlink() or not owner_path.is_file()
                or file(owner_path)['sha256'] != owner_row['sha256']
                or acquired != owner_path.parent / 'native/ordinary/acquired'):
            raise ValueError('ordinary retained acquisition owner changed')
        owner = json.loads(owner_path.read_text(encoding='utf-8-sig'))
        binding = owner['binding']
        prior = owner['response']['receipt']['binding']['core']['ordinary']['preparation_receipt']
        prepared = json.loads(checked(prior).read_text(encoding='utf-8-sig'))
        if (owner.get('source_sha') != acquire.get('source_sha')
                or binding.get('run_id') != acquire.get('run_id')
                or binding.get('intent_id') != acquire.get('intent_id')
                or binding.get('operation') != 'local_windows_msi_acceptance'
                or Path(prior['path']) != acquired.parent / 'preparation.json'
                or prepared.get('asset_receipt') != asset_row):
            raise ValueError('ordinary retained acquisition binding differs')
    if any(path.is_symlink() or path.is_junction() for path in (acquired, *acquired.parents)):
        raise ValueError('ordinary acquisition owner is linked')
    assets = json.loads(asset_path.read_text(encoding='utf-8-sig'))
    if not acquired.is_relative_to(root) and assets != witness.get('assets'):
        raise ValueError('ordinary retained acquisition payload differs')
    names = {'opencode', 'lemonade', 'image_backend', 'llama', 'klein_main', 'klein_text_encoder', 'klein_vae', 'chat'}
    if (assets.get('status') != 'prepared_assets' or set(assets.get('files', {})) != names
            or set(assets.get('extracted', {})) != {'opencode', 'lemonade'}):
        raise ValueError('ordinary immutable acquisition inventory differs')
    mapping = {'lemonade_archive': 'lemonade', 'image_backend_archive': 'image_backend', 'llama_archive': 'llama',
               'klein_main': 'klein_main', 'klein_encoder': 'klein_text_encoder', 'klein_vae': 'klein_vae', 'chat': 'chat'}
    receiving = dict(assets['files'])
    package = acquired / 'lemonade'
    if not acquired.is_relative_to(root):
        copies = receipt['commands']['acquire']['consumable_copies']
        expected = []
        for name in ('klein_main', 'klein_text_encoder', 'klein_vae'):
            source = assets['files'][name]
            receiving[name] = dict(source, path=str(root / 'consumables/assets' / (name + '.safetensors')))
            expected.append({'source': source, 'destination': receiving[name]})
        package = root / 'consumables/lemonade'
        tree = []
        for source in assets['extracted']['lemonade']:
            relative = Path(source['path']).relative_to(acquired / 'lemonade')
            target = dict(source, path=str(package / relative))
            tree.append(target)
            expected.append({'source': source, 'destination': target})
        if (copies.get('outcome') != 'complete' or copies.get('disposed') is True or copies.get('preparation_failed') is True
                or copies.get('files') != expected or copies.get('bytes') != sum(row['source']['bytes'] for row in expected)
                or copies.get('duration_seconds', -1) < 0
                or datetime.fromisoformat(copies['ended_at']) < datetime.fromisoformat(copies['started_at'])
                or any(path.is_symlink() or path.is_junction() for path in (root / 'consumables', package))
                or tree != [file(path) for path in sorted(package.rglob('*')) if path.is_file()]):
            raise ValueError('ordinary owned consumable copies differ')
    if (ordinary['opencode']['archive'] != assets['files']['opencode']
            or ordinary['opencode']['program'] != assets.get('opencode_program')
            or any(ordinary['assets'][key] != receiving[name] for key, name in mapping.items())
            or Path(ordinary['assets']['lemonade_package_directory']) != package):
        raise ValueError('ordinary immutable receiving descriptors differ')
    for name, row in assets['files'].items():
        suffix = '.gguf' if name == 'chat' else '.safetensors' if name.startswith('klein_') else '.zip'
        if Path(row['path']) != acquired / 'assets' / (name + suffix):
            raise ValueError('ordinary immutable asset path differs')
    for name, rows in assets['extracted'].items():
        directory = acquired / name
        found = sorted((path for path in directory.rglob('*') if path.is_file()))
        if (directory.is_symlink() or directory.is_junction()
                or any(path.is_symlink() or path.is_junction() for path in directory.rglob('*'))
                or rows != [file(path) for path in found]):
            raise ValueError('ordinary immutable extracted inventory changed')
    programs = [row for row in assets['extracted']['opencode'] if Path(row['path']).name.lower() == 'opencode.exe']
    if len(programs) != 1 or assets.get('opencode_program') != programs[0]:
        raise ValueError('ordinary immutable program differs')
    return acquired


def ordinary_inputs(core):
    """The preparer supplies files; this installed child owns live setup."""
    ordinary = core.get('ordinary')
    required = {'source_sha', 'tool_source', 'directory', 'opencode', 'comfy', 'assets',
                'roots', 'preflight', 'preparation_receipt'}
    if not isinstance(ordinary, dict) or set(ordinary) != required:
        raise ValueError('one fixed prepared ordinary scene required')
    if any(ordinary[key] != core[key] for key in ('source_sha', 'tool_source')):
        raise ValueError('ordinary source/tool differs from native core')
    original_root = Path(ordinary['directory'])
    root = original_root.resolve()
    if not root.is_relative_to(Path(core['directory']).resolve()) or original_root.is_symlink():
        raise ValueError('ordinary inputs left the native owned directory')
    receipt = json.loads(checked(ordinary['preparation_receipt'], root).read_text())
    if (receipt.get('source_sha') != core['source_sha'] or receipt.get('preflight') != ordinary['preflight']
            or not receipt.get('qualified_source_files') or receipt.get('comfy_commit') != ordinary['comfy']['source_commit']
            or receipt.get('cached_inputs', {}).get('invoking_account') != ordinary['comfy']['invoking_account']):
        raise ValueError('ordinary preparation/preflight source binding differs')
    acquired = ordinary_readonly_root(core, ordinary, receipt, root)
    roots = ordinary['roots']
    if set(roots) != {'project', 'xdg_config', 'xdg_cache', 'xdg_data', 'modelhost_state'}:
        raise ValueError('fixed ordinary owned output roots required')
    for key in roots:
        path = Path(roots[key])
        if not path.is_absolute() or not path.resolve().is_relative_to(root) or path.is_symlink():
            raise ValueError('ordinary owned root differs: ' + key)
    opencode = ordinary['opencode']
    if set(opencode) != {'version', 'archive', 'program', 'node', 'plugin_directory', 'plugin_files',
                         'sdk_directory', 'sdk_files', 'configure', 'official_install', 'config'} or opencode['version'] != '1.18.34':
        raise ValueError('fixed official OpenCode and qualified plugin inputs required')
    for key in ('archive', 'program', 'configure', 'official_install'):
        checked(opencode[key], acquired if key in ('archive', 'program') else root)
    checked(opencode['node'])
    if Path(opencode['config']).resolve() != Path(roots['xdg_config']).resolve() / 'opencode/opencode.json':
        raise ValueError('ordinary OpenCode config must be its new owned output')
    project = Path(roots['project']).resolve()
    for protected in (opencode['plugin_directory'], opencode['sdk_directory'], opencode['program']['path'], opencode['config']):
        path = Path(protected).resolve()
        if path == project or path.is_relative_to(project) or project.is_relative_to(path):
            raise ValueError('ordinary project overlaps protected OpenCode installation/config')
    comfy = ordinary['comfy']
    if set(comfy) != {'python', 'records_sha256', 'source_directory', 'source_commit', 'invoking_account', 'main', 'launcher',
                      'node_directory', 'node_files', 'extension_directory', 'extension_files'} or not re.fullmatch('[0-9a-f]{64}', str(comfy['records_sha256'])):
        raise ValueError('fixed real Comfy host/extension inputs required')
    checked(comfy['python'])  # LocalAccount's actual SID must be able to read it.
    if not re.fullmatch('S-1-[0-9-]+', str(comfy['invoking_account'])) or not re.fullmatch('[0-9a-f]{40}', str(comfy['source_commit'])):
        raise ValueError('ordinary real Comfy source/account identity missing')
    checked(comfy['main'], root)
    checked(comfy['launcher'], root)
    for directory, rows in ((opencode['plugin_directory'], opencode['plugin_files']),
                            (opencode['sdk_directory'], opencode['sdk_files']),
                            (comfy['node_directory'], comfy['node_files']),
                            (comfy['extension_directory'], comfy['extension_files'])):
        path = Path(directory).resolve()
        if not path.is_relative_to(root) or not rows:
            raise ValueError('ordinary source inventory root missing')
        wanted = {checked(row, path) for row in rows}
        actual = {file.resolve() for file in path.rglob('*') if file.is_file()}
        if wanted != actual or any(file.is_symlink() for file in path.rglob('*')):
            raise ValueError('ordinary source inventory changed')
    assets = ordinary['assets']
    if set(assets) != {'lemonade_archive', 'lemonade_package_directory', 'image_backend_archive', 'llama_archive',
                       'klein_main', 'klein_encoder', 'klein_vae', 'chat'}:
        raise ValueError('fixed sole Klein and chat asset inputs required')
    for key, row in assets.items():
        if key != 'lemonade_package_directory':
            checked(row, root if key in ('klein_main', 'klein_encoder', 'klein_vae') and not acquired.is_relative_to(root) else acquired)
    package = Path(assets['lemonade_package_directory']).resolve()
    expected_package = root / 'consumables/lemonade' if not acquired.is_relative_to(root) else acquired / 'lemonade'
    if package != expected_package or package.is_symlink() or not package.is_dir():
        raise ValueError('owned reviewed Lemonade package directory missing')
    return ordinary


def ordinary_tool_answer(export, session_id, choice_id, option):
    """Read a real official export; this helper never invokes the answer tool."""
    if export.get('info', {}).get('id') != session_id:
        raise ValueError('ordinary answer came from another OpenCode conversation')
    found = []
    for message in export.get('messages', []):
        info = message.get('info', {})
        if info.get('role') != 'assistant' or info.get('sessionID') != session_id:
            continue
        for part in message.get('parts', []):
            state = part.get('state', {})
            given = state.get('input', {})
            if (part.get('type') != 'tool' or part.get('tool') != 'OpenAbstractions_oa_asks_answer_choice'
                    or part.get('sessionID') != session_id or part.get('messageID') != info.get('id')
                    or state.get('status') != 'completed' or given != {'id': choice_id, 'option': option}):
                continue
            output = state.get('output', '')
            if not isinstance(output, str):
                continue
            # The catalogue notice is display text. Native answer attribution
            # is checked separately; this receiving check matches its wire record.
            _, separator, raw = output.partition('\n')
            if not separator:
                continue
            try:
                result = json.loads(raw)
            except (TypeError, ValueError):
                continue
            record = result.get('record') if isinstance(result, dict) else None
            answer = record.get('answer') if isinstance(record, dict) else None
            if (isinstance(result, dict) and result.get('outcome') == 'answered' and isinstance(answer, dict)
                    and record.get('id') == choice_id and answer.get('id') == choice_id and answer.get('option') == option):
                found.append(part)
    if len(found) != 1:
        raise ValueError('one exact completed native-session answer tool record required')
    return found[0]


def ordinary_selection_read(body, program, policy):
    if (not isinstance(body, dict) or set(body) != {'edit', 'target_program', 'name'}
            or body != {'edit': 'read', 'target_program': program, 'name': policy}
            or not Path(program).is_absolute() or not re.fullmatch('ordinary-image-[0-9a-f]{12}', policy)):
        raise ValueError('only the bound Comfy fixture policy read is permitted')
    return body


def ordinary_png(path):
    data = Path(path).read_bytes()
    if len(data) > 8 * 1024 * 1024 or not data.startswith(b'\x89PNG\r\n\x1a\n'):
        raise ValueError('one bounded generated PNG required')
    position, pixels, header, ended = 8, b'', None, False
    while position < len(data):
        size = int.from_bytes(data[position:position + 4], 'big')
        kind = data[position + 4:position + 8]
        body = data[position + 8:position + 8 + size]
        end = position + 12 + size
        if end > len(data) or zlib.crc32(kind + body) != int.from_bytes(data[end - 4:end], 'big'):
            raise ValueError('generated PNG chunk differs')
        if kind == b'IHDR':
            if header is not None or position != 8 or len(body) != 13:
                raise ValueError('generated PNG header differs')
            header = struct.unpack('>IIBBBBB', body)
        elif kind == b'IDAT':
            pixels += body
        elif kind == b'IEND':
            ended = size == 0 and end == len(data)
        position = end
    if header != (512, 512, 8, 2, 0, 0, 0) or not ended:
        raise ValueError('generated PNG dimensions/encoding differs from this scene')
    decoder = zlib.decompressobj()
    raw = decoder.decompress(pixels, 512 * (1 + 512 * 3) + 1)
    if (len(raw) != 512 * (1 + 512 * 3) or not decoder.eof or decoder.unused_data
            or any(raw[offset] > 4 for offset in range(0, len(raw), 1 + 512 * 3))):
        raise ValueError('generated PNG pixel stream differs')
    return {'file': file(path), 'width': 512, 'height': 512, 'verified_png': True}


ORDINARY_IMAGE_PERMISSIONS = (
    ('abstraction.asks/question.ask', 'account'),
    ('abstraction.router/inventory.read', 'abstraction.router/inventory'),
    ('abstraction.inference/complete', 'host:lemonade-image'),
    ('abstraction.storage/content.read', 'output:image'),
    ('abstraction.storage/content.write', 'output:image'),
)

NOTIFICATION_PERMISSION = (('abstraction.asks/notification.post', 'account'),)


def personal_permission_targets(page, permissions, account, program):
    """Select exact legacy targets from a complete, revision-bound policy page."""
    if (not isinstance(page, dict) or page.get('Outcome') != 'page'
            or page.get('Complete') is not True or not page.get('Revision')
            or not isinstance(page.get('Rules'), list)
            or not all(action in page.get('Catalog', []) for action, _ in permissions)):
        raise ValueError('complete personal permission policy unavailable')
    keys = {tuple(permission) for permission in permissions}
    return [row for row in page['Rules']
            if row.get('Subject') == {'Account': account, 'Program': program}
            and (row.get('Action'), row.get('Resource')) in keys]


def personal_permission_rows(page, permissions, account, program, panel, after, observed_at, review_id):
    """Receive every exact fresh one-hour rule from one native personal review."""
    rows = personal_permission_targets(page, permissions, account, program)
    instant = lambda text: datetime.fromisoformat(text.replace('Z', '+00:00'))
    if not isinstance(review_id, str) or not review_id:
        raise ValueError('native personal review identity missing')
    found = []
    for action, resource in permissions:
        matches = [row for row in rows if (row['Action'], row['Resource']) == (action, resource)]
        if not matches:
            return None
        if len(matches) != 1:
            raise ValueError('personal permission target has duplicate rules')
        row = matches[0]
        try:
            set_at, expires = instant(row['SetAt']), instant(row['Expires'])
            if (row.get('Permit') is not True or row.get('Expired') is not False or row.get('Selector')
                    or row.get('SetBy') != {'Account': account, 'Program': panel}
                    or row.get('Why') != 'personal review ' + review_id
                    or not instant(after) <= set_at <= instant(observed_at) < expires
                    or (expires - set_at).total_seconds() != 3600):
                raise ValueError('personal permission is blocked, foreign, stale or outside its exact hour')
        except (KeyError, TypeError, AttributeError) as error:
            raise ValueError('personal permission provenance missing') from error
        found.append(row)
    return found


def personal_permission_baseline(page, permissions, account, program):
    """A fixture review never replaces a person's retained target decision."""
    if personal_permission_targets(page, permissions, account, program):
        raise ValueError('personal review target already has a rule; preserve the prior decision')
    return page


def personal_permission_cleanup(page, proof):
    """Return exact current landed rules; any intervening change preserves evidence."""
    rows = personal_permission_targets(page, proof['permissions'], proof['account'], proof['program'])
    if sorted(rows, key=lambda row: (row['Action'], row['Resource'])) != sorted(
            proof['rules'], key=lambda row: (row['Action'], row['Resource'])):
        raise ValueError('personal review rules changed; preserve them for owner inspection')
    return rows


def ordinary_choice_instruction(title, *, image=False):
    """The assistant discovers protocol IDs; the person names the visible question."""
    option = 'the sole listed image-model option' if image else 'the option labeled Keep'
    effect = 'Generate this one image; keep saved preferences unchanged.' if image else 'Leave the existing file unchanged.'
    return ('In the SAME OpenCode conversation, ask: Use the OpenAbstractions questions tool to list current '
            'shared questions. Find the unique question titled ' + json.dumps(title) + ', discover its current '
            'question and option IDs from that list, and choose ' + option + '. If the question or option is '
            'ambiguous, leave it unanswered. ' + effect)


def validate_personal_review(proof, permissions, account, program, panel, context, identity=None):
    """Bind an explicit native decision to the exact independently received rules."""
    try:
        if (proof['account'] != account or proof['program'] != program or proof['context'] != context
                or [tuple(row) for row in proof['permissions']] != list(permissions)):
            raise ValueError('personal review receiving target differs')
        personal_permission_baseline(proof['before'], permissions, account, program)
        raw = proof['decision_json'].encode('utf-8')
        if (hashlib.sha256(raw).hexdigest() != proof['receipt']['sha256']
                or len(raw) != proof['receipt']['bytes']):
            raise ValueError('native personal review receipt snapshot differs')
        decision = json.loads(proof['decision_json'])
        view, result, process = decision['review'], decision['result'], proof['process']
        instant = lambda text: datetime.fromisoformat(text.replace('Z', '+00:00'))
        if (decision['profile'] != 'oa-personal-permission-review@1'
                or type(decision['pid']) is not int or decision['pid'] != process['pid']
                or process['argv'][0] != panel or process['pid'] <= 0
                or decision['native_actor'] != {'Account': account, 'Program': panel}
                or view['context'] != context or view['subject'] != {'Account': account, 'Program': program}
                or view['identity']['program'] != program
                or not view['identity']['file_id'] or not re.fullmatch('[0-9a-f]{64}', view['identity']['sha256'])
                or type(view['identity']['bytes']) is not int or view['identity']['bytes'] <= 0
                or not re.fullmatch('[0-9a-f]{64}', view['id']) or not view['nonce']
                or len(view['rules']) != len(permissions)
                or sorted(view['rules'], key=lambda row: (row['action'], row['resource'])) != sorted(
                    [{'action': action, 'resource': resource} for action, resource in permissions],
                    key=lambda row: (row['action'], row['resource']))
                or view['ttl_ms'] != 3600000 or view['lifetime'] != 'One hour'
                or not view['revision'] or result['outcome'] != 'applied' or not result['revision']
                or not instant(proof['started_at']) <= instant(decision['started_at'])
                    <= instant(decision['decided_at']) <= instant(proof['observed_at'])):
            raise ValueError('native personal review decision/context binding differs')
        if identity and any(view['identity'].get(key) != identity[key] for key in ('sha256', 'bytes')):
            raise ValueError('native personal review program bytes differ')
        if context['feature'] == 'picture-workflow' and (
                not view.get('registration') or not view.get('host_revision')
                or view.get('host_identity', {}).get('Name') != context['host']
                or 'image' not in view.get('host_identity', {}).get('Profiles', [])):
            raise ValueError('native picture review registration or receiving host missing')
        expected_flags = ['-native=true', '-page', 'permission-review', '-review-feature', context['feature']]
        for key in ('program', 'app', 'host'):
            if context.get(key):
                expected_flags.extend(['-review-' + key, context[key]])
        if process['argv'][1:] != [*expected_flags, '-review-receipt', proof['receipt']['path']]:
            raise ValueError('native personal review launch proposal differs')
        rows = personal_permission_rows(proof['page'], permissions, account, program, panel,
            proof['started_at'], proof['observed_at'], view['id'])
        landed = [{**row['Rule'], **{key: row[key] for key in ('SetBy', 'SetAt', 'Why', 'Expires')}}
                  for row in result['landed']]
        received = [{key: row[key] for key in landed[0]} for row in rows] if rows and landed else []
        order = lambda row: (row['Action'], row['Resource'])
        if (not rows or len(landed) != len(permissions) or proof['rules'] != rows
                or sorted(landed, key=order) != sorted(received, key=order)):
            raise ValueError('native personal review did not land every exact received rule')
        return rows
    except (KeyError, TypeError, AttributeError, IndexError) as error:
        raise ValueError('native personal review proof incomplete') from error


def validate_personal_cleanup(proof):
    try:
        cleanup = proof['cleanup']
        remaining = list(proof['rules'])
        if cleanup.get('complete') is not True or len(cleanup['steps']) != len(remaining):
            raise ValueError('personal review cleanup incomplete')
        for step in cleanup['steps']:
            personal_permission_cleanup(step['before'], dict(proof, rules=remaining))
            if (step['rule'] not in remaining or step['response'].get('Outcome') != 'applied'
                    or not step['response'].get('Revision') or step['response'].get('Current') is not None):
                raise ValueError('personal review cleanup lacks exact conditional removal')
            remaining.remove(step['rule'])
        if personal_permission_targets(cleanup['after'], proof['permissions'], proof['account'], proof['program']):
            raise ValueError('personal review cleanup retained a target rule')
    except (KeyError, TypeError, AttributeError) as error:
        raise ValueError('personal review cleanup proof missing') from error


ORDINARY_COMFY_SDK = ('abstraction.facade', 'abstraction.facade.client', 'abstraction.inference.api',
                      'abstraction.router', 'abstraction.asks.api', 'abstraction.ipc')


def ordinary_sdk_origins(origins, core):
    """The real host imports each public client from the checked current SDK."""
    root = Path(core['pythonpath']).resolve()
    expected = {row['path']: row for row in core['python_files']}
    if not isinstance(origins, dict) or set(origins) != set(ORDINARY_COMFY_SDK):
        raise ValueError('current Comfy SDK import origins missing')
    for name, row in origins.items():
        if expected.get(row['path']) != row or checked(row, root).resolve() != root / (name.replace('.', '/') +
                ('.py' if name == 'abstraction.facade.client' else '/__init__.py')):
            raise ValueError('Comfy imported another SDK source: ' + name)
    return origins


def ordinary_comfy_arguments(core, port):
    if type(port) is not int or not 1 <= port <= 65535:
        raise ValueError('one owned Comfy loopback port required')
    root = Path(core['ordinary']['directory']).resolve() / 'scene'
    return [core['ordinary']['comfy']['main']['path'], '--cpu', '--listen', '127.0.0.1', '--port', str(port),
            '--disable-api-nodes', '--user-directory', str(root / 'comfy-user'), '--input-directory', str(root / 'comfy-input'),
            '--temp-directory', str(root / 'comfy-temp'), '--output-directory', str(root / 'comfy-output')]


def ordinary_comfy_host(request_path, port):
    """The RECORD launcher enters this fixed scene before the real Comfy main."""
    import importlib
    import runpy
    request_path = Path(request_path).resolve()
    request = json.loads(request_path.read_text(encoding='utf-8-sig'))
    core = request['core']
    root = Path(core['ordinary']['directory']).resolve() / 'scene'
    arguments = ordinary_comfy_arguments(core, port)
    if (sys.platform != 'win32' or request_path != root / 'ordinary-request.json'
            or request.get('schema') != 1 or request.get('platform') != sys.platform
            or request.get('evidence_directory') != str(root) or checked(core['helper']) != Path(__file__).resolve()
            or checked(request['runtime']) != Path(request['runtime']['path'])
            or request['runtime']['sha256'] != core['runtime_sha256']):
        raise ValueError('real Comfy scene request/source/install differs')
    checked(request['native_request'])
    main = checked(core['ordinary']['comfy']['main'], Path(core['ordinary']['comfy']['source_directory']))
    pythonpath = Path(core['pythonpath']).resolve()
    expected = {checked(row, Path(core['directory'])) for row in core['python_files']}
    if (os.environ.get('PYTHONPATH') != str(pythonpath) or {p.resolve() for p in pythonpath.rglob('*.py')} != expected
            or not pythonpath.is_relative_to(Path(core['directory']).resolve()) or any(p.is_symlink() for p in pythonpath.rglob('*'))):
        raise ValueError('real Comfy current SDK path/inventory differs')
    origins = {name: file(importlib.import_module(name).__file__) for name in ORDINARY_COMFY_SDK}
    ordinary_sdk_origins(origins, core)
    write(root / 'comfy-sdk-origins.json', {'pid': os.getpid(), 'request': file(request_path), 'origins': origins})
    sys.argv = arguments
    sys.path.insert(0, str(main.parent))
    runpy.run_path(str(main), run_name='__main__')


def ordinary_stop_result(stopped, core, launch, panel, root, earlier):
    """Receive the fresh unanswered occurrence after the actual native Stop."""
    instant = lambda text: datetime.fromisoformat(text.replace('Z', '+00:00'))
    millis = lambda text: int(instant(text).timestamp() * 1000)
    pending, rule, process = stopped['pending'], stopped['rule'], stopped['process']
    request = pending['Request']
    asked, deadline = pending['AskedMs'], request['PendingUntilMs']
    identity = {key: pending[key] for key in ('ID', 'Request', 'Asker', 'AskedMs')}
    program, account = core['ordinary']['opencode']['program']['path'], launch['account']
    if (stopped['native_session'] != launch['id'] or not pending['ID'] or not pending['Asker']
            or pending['ID'] in (earlier['keep']['pending']['ID'], earlier['image']['pending']['ID'])
            or pending['State'] != 'pending' or pending.get('Answer') is not None or type(asked) is not int
            or type(deadline) is not int or not asked < deadline <= request['RequestRef']['ReplayUntilMs']
            or not re.fullmatch('pretend-lemonade-copy-[0-9a-f]{32}', request['RequestRef']['Key'])
            or rule['Permit'] is not False or rule['Expired'] is not False or rule.get('Selector')
            or rule['Subject'] != {'Account': account, 'Program': program}
            or rule['SetBy'] != {'Account': account, 'Program': panel}
            or rule['Action'] != 'abstraction.asks/question.answer' or rule['Resource'] != 'apps'
            or not millis(earlier['image']['answer']['answered']) <= millis(rule['SetAt']) <= asked):
        raise ValueError('native Stop rule/fresh request identity differs')
    page = stopped['after_page']
    observed_at = millis(stopped['after_page_at'])
    if (stopped['before']['program'] != program or millis(stopped['before']['expires']) <= millis(rule['SetAt'])
            or page['outcome'] != 'page' or not page['revision']
            or any(os.path.normcase(row['program']) == os.path.normcase(program) for row in page['assistants'])
            or not millis(rule['SetAt']) <= observed_at <= asked):
        raise ValueError('actual post-Stop answering eligibility proof missing')
    observations = stopped['observations']
    if not observations:
        raise ValueError('fresh stopped choice observations missing')
    for row in observations:
        record = row['record']
        if ({key: record[key] for key in identity} != identity or record['State'] != 'pending'
                or record.get('Answer') is not None or not asked <= millis(row['at']) < deadline):
            raise ValueError('stopped observation differs from the fresh full request')
    terminal = stopped['terminal']
    ended, terminal_at = millis(process['ended_at']), millis(stopped['terminal_observed_at'])
    expected_out = root / 'go-after-stop'
    if ({key: terminal[key] for key in identity} != identity or terminal['State'] != 'expired' or terminal.get('Answer') is not None
            or not deadline <= ended <= terminal_at or process['exited'] is not True
            or type(process['pid']) is not int or process['pid'] <= 0 or type(process['exit_code']) is not int
            or process['exit_code'] == 0 or process['operation'] != 'go-after-stop'
            or not millis(process['started_at']) <= asked
            or process['argv'][:5] != [core['go']['path'], '--out', str(expected_out), '--ask-before-replace', '--wait-output-choice']
            or process['argv'][5:] != earlier['keep']['process']['argv'][5:]
            or stopped['before_file'] != stopped['after_file']
            or checked(stopped['after_file'], root) != expected_out / 'fixture.bin'
            or pending['ID'] not in checked(process['log'], root).read_text(errors='replace')):
        raise ValueError('fresh stopped process/expiry/unchanged effect proof missing')
    if rule['Expires'] and millis(rule['Expires']) <= max(terminal_at, int(time.time() * 1000)):
        raise ValueError('native Stop rule expired during the fresh pending proof')


def validate_ordinary_result(value, core):
    """The release receiver requires the actual separately bound ordinary scene."""
    try:
        ordinary, prepared = value['ordinary'], core['ordinary']
        root = Path(prepared['directory']).resolve() / 'scene'
        if (value.get('result') != 'pass' or value.get('core') != core or ordinary.get('prepared') != prepared
                or value.get('owned_children_exited') is not True or value.get('native_owner_closed') is not True
                or value.get('private_evidence') is not True or ordinary.get('cleanup_completed') is not True
                or value['runtime']['sha256'] != core['runtime_sha256'] or value['panel']['sha256'] != core['panel_sha256']):
            raise ValueError('ordinary scene is incomplete or bound to another source/install')
        checked(value['request'], root)
        checked(value['native_request'])
        prior = json.loads(checked(ordinary['initial_core_receipt']).read_text(encoding='utf-8-sig'))
        validate_result(prior, core)
        if prior['native_request'] != value['native_request']:
            raise ValueError('ordinary scene differs from the completed native core request')
        launch, closed = ordinary['native_launch'], ordinary['native_closed']
        for key in ('id', 'account', 'owner_program', 'owner_pid', 'owner_start', 'child_pid', 'child_start',
                    'guest_program_id', 'package_root_id', 'runtime_program_id', 'state_id'):
            if not launch[key] or launch[key] != closed[key]:
                raise ValueError('ordinary native session identity/closure changed')
        if (launch['phase'] != 'running' or launch['closed_job'] or closed['phase'] != 'closed' or not closed['closed_job']
                or launch['account'] != prepared['comfy']['invoking_account'] or not launch['initial']['manual_answer']
                or launch['initial']['answered_by'] != 'operator' or not launch['initial']['approved']
                or Path(launch['initial']['folder']).resolve() != Path(prepared['roots']['project']).resolve()):
            raise ValueError('ordinary scene lacks actual native approval/closed owner')
        helper = str((Path(prepared['comfy']['extension_directory']) / 'helper/python.exe').resolve())
        registration = ordinary['comfy_registration']
        if registration['name'] != 'comfyui' or registration['program'] != helper or registration['subject']['Account'] != launch['account']:
            raise ValueError('ordinary Comfy registration differs from its actual owned helper')
        checked(ordinary['comfy_host']['helper'], Path(prepared['comfy']['extension_directory']))
        sdk = ordinary['comfy_host']['sdk_imports']
        if (sdk['pid'] != ordinary['comfy_host']['process']['pid'] or sdk['request'] != value['request']
                or ordinary_sdk_origins(sdk['origins'], core) != sdk['origins']):
            raise ValueError('actual Comfy host SDK import proof differs')
        permission = ordinary['guided_personal_permissions']
        if permission.get('typed_permission_request') is not False:
            raise ValueError('ordinary personal legacy permission proof mislabeled')
        validate_personal_review(permission, ORDINARY_IMAGE_PERMISSIONS, launch['account'], helper,
            value['panel']['path'], {'feature': 'picture-workflow', 'app': 'comfyui', 'host': 'lemonade-image'},
            ordinary['comfy_host']['helper'])
        validate_personal_cleanup(permission)
        conversation = None
        for name, asker, option in (('keep', core['go']['path'], 'keep'), ('image', helper, ordinary['image']['operation']['Model'])):
            proof = ordinary[name]
            pending, answer = proof['pending'], proof['answer']
            decided = int(datetime.fromisoformat(answer['answered'].replace('Z', '+00:00')).timestamp() * 1000)
            if (proof.get('gone') is not True or proof.get('exact_ack_source_bound') is not True or proof['native_session'] != launch['id']
                    or answer['id'] != pending['ID'] or answer['option'] != option or answer['kind'] != 'choice'
                    or answer['state'] != 'chosen' or answer['answerer'] != prepared['opencode']['program']['path']
                    or answer['program'] != asker or not pending['AskedMs'] <= decided < pending['Request']['PendingUntilMs']):
                raise ValueError('ordinary current attributed answer/effect/ack binding missing')
            tool = proof['tool']
            current = tool['conversation']['id']
            if conversation is not None and current != conversation:
                raise ValueError('ordinary answers came from different conversations')
            conversation = current
            exported = json.loads(checked(tool['export'], root).read_text())
            if ordinary_tool_answer(exported, current, pending['ID'], option) != tool['part']:
                raise ValueError('ordinary completed official tool export differs')
        keep = ordinary['keep']
        if keep['before'] != keep['after'] or keep['process']['exit_code'] != 0 or checked(keep['after'], root) != checked(keep['before'], root):
            raise ValueError('ordinary Go Keep destination changed')
        image = ordinary['image']
        operation = image['operation']
        if (operation['Program'] != helper or operation.get('Profile') != 'image' or operation['Outcome'] != 'completed'
                or operation.get('Images') != 1 or not operation['Operation'] or image['policy_before'] != image['policy_after']
                or image['policy_before']['Outcome'] != 'unknown' or not image['policy_before']['Revision']
                or ordinary_png(checked(image['receiving']['file'], root)) != image['receiving']):
            raise ValueError('ordinary one-image receiving/saved-preference proof missing')
        image_operations = [row for row in image['inference_audit'] if row['Program'] == helper and row.get('Profile') == 'image' and row['Operation']]
        if image_operations != [operation]:
            raise ValueError('ordinary Comfy image operation duplicated')
        ordinary_stop_result(ordinary['stop'], core, launch, value['panel']['path'], root, ordinary)
        for phase in ordinary['phases']:
            if (phase['kind'] != ('operator_wait' if phase['pending'] else 'compute') or phase['wall_seconds'] < 0
                    or datetime.fromisoformat(phase['ended_at']) < datetime.fromisoformat(phase['started_at'])):
                raise ValueError('ordinary phase timing is incomplete')
    except (KeyError, TypeError, AttributeError) as error:
        raise ValueError('ordinary native result fields missing') from error


class OrdinaryScene(NativeCore):
    """One Windows installed scene; application and native owners do the work."""
    personal_review_allowance = 0  # its existing 570s scene budget includes its review

    def __init__(self, core_request):
        started, started_at = time.monotonic(), now()
        envelope = json.loads(Path(core_request).read_text(encoding='utf-8-sig'))
        core = envelope['core']
        prior = json.loads((Path(core_request).parent / 'core-result.json').read_text(encoding='utf-8-sig'))
        validate_result(prior, core)
        if prior.get('native_request') != envelope['native_request'] or sys.platform != 'win32':
            raise ValueError('ordinary scene requires the completed same Windows native core')
        prepared = ordinary_inputs(core)
        root = Path(prepared['directory']) / 'scene'
        root.mkdir()  # An interrupted scene is retained, never automatically replayed.
        envelope['evidence_directory'] = str(root.resolve())
        request = root / 'ordinary-request.json'
        write(request, envelope)
        super().__init__(request)
        self.prepared, self.roots = prepared, prepared['roots']
        self.deadline = started + 570
        self.cleanup_deadline = started + 590
        self.owner = None
        self.created_providers = []
        self.hosted_chat = None
        self.native_state = Path(os.environ['LOCALAPPDATA']) / 'openabstractions/runtime-v1'
        self.control_path = self.native_state / 'apps/opencode/session.json'
        self.data['started_at'] = started_at
        self.data['ordinary'] = {'prepared': prepared, 'phases': [{'phase': 'prepared-input-validation', 'kind': 'compute',
            'pending': {}, 'started_at': started_at, '_clock': started}], 'initial_core_receipt': file(Path(core_request).parent / 'core-result.json')}

    def progress(self, phase, pending):
        value = {'phase': 'ordinary:' + phase, 'pending': pending, 'updated_at': now(),
                 'source_sha': self.core['source_sha'], 'native_request_sha256': self.request['native_request']['sha256'],
                 'private_evidence': True}
        write(self.root / 'ordinary-progress.json', value)
        write(Path(self.request['native_request']['path']).parent / 'core-progress.json', value)
        print(json.dumps(value), flush=True)

    def phase(self, phase, pending=None):
        if self.data.get('ordinary', {}).get('phases'):
            previous = self.data['ordinary']['phases'][-1]
            previous['ended_at'] = now()
            previous['wall_seconds'] = time.monotonic() - previous.pop('_clock')
        row = {'phase': phase, 'kind': 'operator_wait' if pending else 'compute', 'pending': pending or {},
               'started_at': now(), '_clock': time.monotonic()}
        self.data['ordinary']['phases'].append(row)
        self.progress(phase, pending or {})

    def call(self, name, argv, *, input=None, acceptable=(0,), seconds=30, env=None):
        saved = self.env
        if env is not None:
            self.env = env
        try:
            process, row, path = self.start(name, argv)
        finally:
            self.env = saved
        try:
            process.communicate(input=input, timeout=min(seconds, self.remaining()))
        finally:
            if process.poll() is None:
                process.kill()  # The exact owned synchronous command, never the native session owner.
                process.wait(timeout=5)
            row.update(exit_code=process.returncode, exited=True, ended_at=now(), log=file(path))
            write(self.root / 'ordinary-result.json', self.data)
        if process.returncode not in acceptable:
            raise ValueError(name + ' failed; retain ' + str(path))
        return path.read_text(errors='replace'), row

    def api(self, route, body=None):
        if body is not None:
            raise ValueError('ordinary scene only reads Panel state; native persons/tools own edits')
        return super().api(route)

    def local(self, base, route, body=None):
        parsed = urllib.parse.urlsplit(base)
        if parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or not parsed.port or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError('ordinary Comfy channel must be its owned loopback origin')
        request = urllib.request.Request(base + route, None if body is None else json.dumps(body).encode(), headers={'Content-Type': 'application/json'})
        with self.http.open(request, timeout=min(3, self.remaining())) as response:
            return json.load(response)

    def provider(self, name, argv):
        self.call('provider-' + name, [self.runtime, 'provider', 'add', name, *argv, '--json'])
        self.created_providers.append(name)

    def place_lemonade(self, state, packages, source, info):
        # Absence was established by setup-info and the filesystem before this
        # owning record. Retain it before any mkdir or partial member move.
        if state.exists() or packages.exists():
            raise ValueError('owned normal Lemonade targets are no longer absent')
        self.data['ordinary']['lemonade_owned'] = {'state': str(state), 'packages': str(packages), 'before': info}
        write(self.root / 'ordinary-result.json', self.data)
        version = packages / 'v2026.39.1'
        version.mkdir(parents=True)
        for member in source.iterdir():
            shutil.move(str(member), str(version / member.name))

    def setup(self):
        self.phase('provider-setup')
        assets = self.prepared['assets']
        tools = self.runtime.parent
        lemonade = tools / 'lemonaded.exe'
        info, _ = self.call('lemonade-setup-info', [lemonade, 'setup-info', '--json'])
        info = json.loads(info)
        default = Path(os.environ['LOCALAPPDATA']) / 'OpenAbstractions'
        state = default / 'providers/lemonade'
        packages = default / 'apps/lemonade'
        if Path(info['provider_root']) != state or info['selection_present'] or state.exists() or packages.exists():
            raise ValueError('ordinary scene requires the absent fresh-account installed Lemonade setup')
        packages.parent.mkdir(parents=True, exist_ok=True)
        # The reviewed prepared tree is already extracted. Move the one owned
        # copy into the installed owner's normal location on this same volume.
        source = Path(assets['lemonade_package_directory'])
        self.call('lemonade-package-check', [lemonade, 'package', 'check', '--version', 'v2026.39.1', '--root', source, '--json'])
        self.place_lemonade(state, packages, source, info)
        self.call('lemonade-configure', [lemonade, 'configure', '--activate-reviewed-package', '--package', 'v2026.39.1',
            '--vulkan-backend-archive', assets['image_backend_archive']['path'], '--bf16-main', assets['klein_main']['path'],
            '--bf16-encoder', assets['klein_encoder']['path'], '--bf16-vae', assets['klein_vae']['path']], seconds=180)
        configured, _ = self.call('lemonade-configured', [lemonade, 'setup-info', '--json'])
        self.data['ordinary']['lemonade_setup'] = json.loads(configured)
        if self.data['ordinary']['lemonade_setup'].get('active_package', {}).get('version') != 'v2026.39.1':
            raise ValueError('installed Lemonade selected another package')
        placements = []
        for key in ('klein_main', 'klein_encoder', 'klein_vae'):
            wanted = assets[key]
            candidates = [p for p in state.rglob('*') if p.is_file() and p.stat().st_size == wanted['bytes']]
            observed = [file(p) for p in candidates]
            matches = [row for row in observed if row['sha256'] == wanted['sha256']]
            if len(matches) != 1 or Path(wanted['path']).exists():
                raise ValueError('existing Setup did not consume/place the exact owned Klein role')
            placements.append({'input': wanted, 'destination': matches[0], 'consumed': True})
        self.data['ordinary']['lemonade_owned']['placements'] = placements
        inventory = tools / 'inventoryd.exe'
        modelhost = tools / 'modelhostd.exe'
        chat = Path(assets['chat']['path'])
        store = 'ordinary-chat-' + assets['chat']['sha256'][:12]
        config = self.root / 'chat-inventory.json'
        write(config, {'extra_roots': [{'store': store, 'path': str(chat.parent), 'holder': '', 'expected_file_key': ''}]})
        self.provider(store, ['--program', inventory, '--provider-endpoint', store, '--contract', 'abstraction.storage/inventory-source@1',
            '--store', store, '--arg', 'serve', '--arg', '--endpoint', '--arg', '{endpoint}', '--arg', '--config', '--arg', config,
            '--arg', '--config-roots-only', '--arg', '--ignore-environment-roots'])
        installed, _ = self.call('chat-engine-install', [modelhost, 'engine', 'install', '--archive', assets['llama_archive']['path'],
            '--dir', self.root / 'chat-engine', '--state', self.roots['modelhost_state']], seconds=90)
        engine = re.search(r'([A-Za-z]:[^\r\n]*llama-server\.exe)', installed)
        if not engine:
            raise ValueError('existing modelhost engine installer did not name its verified engine')
        # models host resolves this locator through the real runtime inventory
        # and its source's Verify before it declares the model.
        self.hosted_chat = store + '/' + chat.name
        self.data['ordinary']['chat_inventory'] = {'config': file(config), 'store': store, 'locator': chat.name, 'file': assets['chat']}
        self.call('chat-host', [self.runtime, 'models', 'host', self.hosted_chat, '--engine', engine[1], '--state', self.roots['modelhost_state'],
            '--as', 'ordinary-chat', '--json'])
        hosts, _ = self.call('chat-hosts', [self.runtime, 'models', 'hosts', '--json'])
        self.data['ordinary']['chat_hosts'] = json.loads(hosts)
        self.phase('provider-readiness')
        offers = self.until(lambda: self.offers('image'))
        names = list(dict.fromkeys(name for offer in offers['Offers'] for name in offer['Names']))
        servers = list(dict.fromkeys(server for offer in offers['Offers'] for server in offer['Servers']))
        if (len(names) != 1 or not re.fullmatch('[^\x00-\x1f\x7f]{1,64}', names[0]) or 'klein' not in names[0].lower()
                or servers != ['lemonade-image']):
            raise ValueError('ordinary scene needs the complete sole current Klein image offer')
        self.data['ordinary']['image_offers'], self.image_model = offers, names[0]
        self.data['ordinary']['chat_offers'] = self.until(lambda: self.offers('chat'))

    def offers(self, profile):
        try:
            page = self.api('/inference/models?profile=' + profile)
        except urllib.error.HTTPError as error:
            if error.code == 503:
                return None
            raise
        return page if page.get('Outcome') == 'offered' and page.get('Offers') else None

    def wait(self, read, seconds=90):
        deadline = time.monotonic() + min(seconds, self.remaining())
        while time.monotonic() < deadline:
            self.remaining()
            value = read()
            if value:
                return value
            time.sleep(.2)
        raise TimeoutError('ordinary scene outcome missing; retain current progress and native owner state')

    def choices(self):
        records, shared, cursor = [], [], ''
        for _ in range(16):
            page = self.api('/choices?cursor=' + urllib.parse.quote(cursor, safe=''))
            if page.get('Outcome') != 'page':
                raise ValueError('current ordinary choices unavailable')
            records.extend(page['Records'])
            shared.extend(page['Shared'])
            if page['Complete']:
                return records, shared
            if not page['Next'] or page['Next'] == cursor:
                raise ValueError('ordinary choice cursor did not advance')
            cursor = page['Next']
        raise ValueError('ordinary choice page bound exceeded')

    def choice(self, identity):
        return next((r for r in self.choices()[0] if r['ID'] == identity), None)

    def enrolled(self):
        page = self.api('/rights/assistant')
        if page.get('outcome') != 'page':
            raise ValueError('current answering eligibility unavailable')
        row = next((r for r in page['assistants'] if os.path.normcase(r['program']) == os.path.normcase(self.prepared['opencode']['program']['path'])), None)
        if row and datetime.fromisoformat(row['expires'].replace('Z', '+00:00')) > datetime.now(timezone.utc):
            return row
        return None

    def control(self, *, running=True):
        if not self.control_path.is_file() or self.control_path.is_symlink():
            return None
        value = json.loads(self.control_path.read_text())
        if value.get('owner_pid') != self.owner.pid:
            return None
        if (value.get('format') != 1 or value.get('account') != self.prepared['comfy']['invoking_account']
                or os.path.normcase(value.get('owner_program', '')) != os.path.normcase(str(self.runtime))
                or not value.get('id') or not value.get('owner_start') or not value.get('guest_program_id')
                or not value.get('package_root_id') or not value.get('state_id') or not value.get('runtime_program_id')):
            raise ValueError('native OpenCode control differs from this installed owner/session/account')
        if running:
            initial = value.get('initial', {})
            if (value.get('phase') != 'running' or value.get('closed_job') or not value.get('child_pid')
                    or not value.get('child_start') or not initial.get('approved') or not initial.get('manual_answer')
                    or initial.get('answered_by') != 'operator' or not initial.get('answered') or not initial.get('file_id')
                    or Path(initial.get('folder', '')).resolve() != Path(self.roots['project']).resolve()):
                return None
        return value

    def official_read(self, name, arguments):
        # The official CLI's list/export commands read the same native guest
        # database. They send no prompt and carry no answering delegation.
        data = self.native_state / 'opencode-data'
        env = dict(self.env)
        env.update({key: str(data / folder) for key, folder in (
            ('XDG_CONFIG_HOME', 'config'), ('XDG_DATA_HOME', 'data'), ('XDG_STATE_HOME', 'state'),
            ('XDG_CACHE_HOME', 'cache'), ('LOCALAPPDATA', 'local'), ('USERPROFILE', 'home'),
            ('HOME', 'home'), ('TEMP', 'temp'), ('TMP', 'temp'))})
        env.update(OPENCODE_CONFIG=str(data / 'config/opencode.json'), OPENCODE_DISABLE_AUTOUPDATE='1', OPENCODE_DISABLE_MODELS_FETCH='1')
        out, err = self.root / (name + '.json'), self.root / (name + '.stderr.log')
        argv = [self.prepared['opencode']['program']['path'], *arguments]
        with out.open('wb') as stdout, err.open('wb') as stderr:
            process = subprocess.Popen(argv, env=env, stdout=stdout, stderr=stderr, stdin=subprocess.DEVNULL,
                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        row = {'operation': name, 'argv': argv, 'pid': process.pid, 'started_at': now(), 'private_evidence': True}
        try:
            process.wait(timeout=min(10, self.remaining()))
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)
            row.update(exited=True, exit_code=process.returncode, ended_at=now(), stdout=file(out), stderr=file(err))
            self.commands.append(row)
        if process.returncode:
            raise ValueError('official OpenCode read failed: ' + str(err))
        return json.loads(out.read_text()) if out.stat().st_size else []

    def session(self):
        opencode = self.prepared['opencode']
        project = Path(self.roots['project'])
        project.mkdir(exist_ok=True)
        self.phase('opencode-configure')
        self.call('opencode-configure', [opencode['node']['path'], opencode['configure']['path'], 'configure',
            '--config', opencode['config'], '--adapter', opencode['plugin_directory'], '--scope', 'local', '--profile', 'local'])
        self.data['ordinary']['opencode_config'] = file(opencode['config'])
        if self.enrolled():
            raise ValueError('ordinary scene requires new native answering consent')
        if self.control_path.exists():
            raise ValueError('ordinary scene requires absent native launch control; retained owner state is preserved')
        argv = [str(self.runtime), 'opencode', 'open', '--project', str(project), '--program', opencode['program']['path'],
                '--plugin', opencode['plugin_directory'], '--config', opencode['config']]
        self.owner = subprocess.Popen(argv, env=self.env, creationflags=getattr(subprocess, 'CREATE_NEW_CONSOLE', 0))
        self.owner_row = {'operation': 'opencode-native', 'argv': argv, 'pid': self.owner.pid, 'started_at': now(), 'native_console': True}
        self.commands.append(self.owner_row)
        self.phase('native-folder-approval', {'native_owner_pid': self.owner.pid, 'project': str(project),
            'instruction': 'Allow this project in the installed native Questions window; the official OpenCode console opens after approval.'})
        control = self.wait(lambda: self.control() if self.owner.poll() is None else self.owner_failed(), 40)
        self.native_id = control['id']
        self.data['ordinary']['native_launch'] = control
        self.phase('native-answering-enrollment', {'native_owner_pid': self.owner.pid, 'native_session': self.native_id,
            'program': opencode['program']['path'], 'instruction': 'In the installed Panel Questions, let this OpenCode answer routine questions once. Keep its native console open.'})
        self.data['ordinary']['enrollment'] = self.wait(self.enrolled, 60)

    def owner_failed(self):
        raise ValueError('native OpenCode owner exited before the required native interaction')

    def attributed(self, identity, option, asker, pending):
        page = self.api('/rights/answered')
        if page.get('outcome') != 'page' or page.get('choices'):
            raise ValueError('current attributed choice answer audit unavailable')
        record = next((r for r in page['answers'] if r['id'] == identity), None)
        if not record:
            return None
        instant = int(datetime.fromisoformat(record['answered'].replace('Z', '+00:00')).timestamp() * 1000)
        if (record.get('kind') != 'choice' or record.get('state') != 'chosen' or record.get('option') != option
                or os.path.normcase(record.get('answerer', '')) != os.path.normcase(self.prepared['opencode']['program']['path'])
                or os.path.normcase(record.get('program', '')) != os.path.normcase(asker)
                or not pending['AskedMs'] <= instant < pending['Request']['PendingUntilMs'] or not self.enrolled()
                or self.control()['id'] != self.native_id):
            raise ValueError('choice answer is not current, attributed or within the same enrolled native session')
        return record

    def tool(self, identity, option):
        sessions = self.official_read('official-sessions', ['session', 'list', '--format', 'json', '--max-count', '20'])
        candidates = [s for s in sessions if Path(s.get('directory', '')).resolve() == Path(self.roots['project']).resolve()]
        if not candidates:
            return None
        if len(candidates) != 1 or not candidates[0].get('id'):
            raise ValueError('one real native project conversation required')
        conversation = candidates[0]['id']
        if hasattr(self, 'conversation') and conversation != self.conversation:
            raise ValueError('ordinary answers changed the real native conversation')
        self.conversation = conversation
        exported = self.official_read('official-export-' + identity, ['export', conversation])
        try:
            part = ordinary_tool_answer(exported, conversation, identity, option)
        except ValueError:
            return None  # The actual completed tool can lag the attributed service answer.
        return {'conversation': candidates[0], 'export': file(self.root / ('official-export-' + identity + '.json')), 'part': part}

    def pending(self, process, log, prefix, title=None):
        if process.poll() is not None:
            raise ValueError('ordinary caller exited before sharing its current choice: ' + str(log))
        matches = [r for r in self.choices()[0] if r['State'] == 'pending'
            and r['Request']['RequestRef']['Key'].startswith(prefix) and (title is None or r['Request']['Title'] == title)]
        if len(matches) > 1:
            raise ValueError('more than one current ordinary scene choice matched')
        if matches and prefix == 'pretend-lemonade-copy-' and matches[0]['ID'] not in log.read_text(errors='replace'):
            return None
        return matches[0] if len(matches) == 1 else None

    def go_choice(self, name):
        if not hasattr(self, 'source_url'):
            source, _, source_log = self.start('ordinary-source', [self.app, '--source'])
            match = self.wait(lambda: re.search(r'example source: (http://127\.0\.0\.1:[0-9]+) ([0-9]+) sha256:([0-9a-f]{64})', source_log.read_text(errors='replace')), 10)
            self.source_url, self.source_size, self.source_digest = match.groups()
        out = self.root / name
        out.mkdir()
        destination = out / 'fixture.bin'
        destination.write_bytes(b'Keep this existing ordinary-scene file unchanged.\n')
        before = file(destination)
        process, row, log = self.start(name, [self.app, '--out', out, '--ask-before-replace', '--wait-output-choice',
            self.source_url + '/fixture.bin', self.source_size, 'sha256:' + self.source_digest])
        current = self.wait(lambda: self.pending(process, log, 'pretend-lemonade-copy-'), 30)
        # The current record and caller's own announcement identify this one invocation.
        if current['ID'] not in log.read_text(errors='replace') or current['ID'] not in self.choices()[1]:
            raise ValueError('Go current shared occurrence does not match its caller announcement')
        return process, row, log, destination, before, current

    def keep(self):
        process, row, log, destination, before, pending = self.go_choice('go-keep')
        identity = pending['ID']
        self.phase('opencode-keep', {'id': identity, 'request_ref': pending['Request']['RequestRef'], 'native_session': self.native_id,
            'instruction': ordinary_choice_instruction(pending['Request']['Title'])})
        answer = self.wait(lambda: self.attributed(identity, 'keep', str(self.app), pending))
        tool = self.wait(lambda: self.tool(identity, 'keep'), 20)
        self.phase('go-keep-effect')
        process.wait(timeout=min(5, self.remaining()))
        row.update(exited=True, exit_code=process.returncode, ended_at=now(), log=file(log))
        if process.returncode or 'kept existing output' not in log.read_text(errors='replace') or file(destination) != before:
            raise ValueError('Go Keep did not preserve the destination and finish its exact acknowledgement')
        self.wait(lambda: self.choice(identity) is None, 5)
        self.data['ordinary']['keep'] = {'pending': pending, 'answer': answer, 'tool': tool,
            'process': row, 'before': before, 'after': file(destination), 'gone': True,
            'exact_ack_source_bound': True, 'native_session': self.native_id}

    def registrations(self):
        page = self.api('/apps/registrations?source=applications&wait_ms=0')
        if page.get('outcome') != 'page' or page.get('identity_error'):
            raise ValueError('registered native application identity unavailable')
        return page['entries']

    def registered_comfy(self):
        rows = [r for r in self.registrations() if r['name'] == 'comfyui']
        if not rows:
            return None
        if len(rows) != 1 or os.path.normcase(rows[0]['program']) != os.path.normcase(self.comfy_program):
            raise ValueError('registered Comfy differs from the actual owned extension helper')
        if rows[0].get('subject', {}).get('Account') != self.prepared['comfy']['invoking_account']:
            raise ValueError('registered Comfy belongs to another account')
        return rows[0]

    def selection(self):
        body = ordinary_selection_read({'edit': 'read', 'target_program': self.comfy_program, 'name': self.policy}, self.comfy_program, self.policy)
        if self.registered_comfy() is None:
            raise ValueError('selection read requires the current actual registered Comfy helper')
        request = urllib.request.Request(self.base + '/registry/selection', json.dumps(body).encode(),
            headers={'X-Panel-Key': self.key, 'Content-Type': 'application/json'})
        with self.http.open(request, timeout=min(3, self.remaining())) as response:
            result = json.load(response)
        read = result.get('read', {})
        if read.get('Outcome') != 'unknown' or not read.get('Revision') or read.get('Policy') is not None:
            raise ValueError('ordinary scene requires the genuinely absent fixture selection policy')
        return read

    def audit(self, cursor, *, pages=32, fresh=False, deadline=None):
        rows = []
        for _ in range(pages):
            if deadline is not None and deadline - time.monotonic() < 3:
                raise TimeoutError('current inference audit window closed')
            page = self.api('/inference/audit?cursor=' + str(cursor))
            next_cursor = page.get('Next')
            if page.get('Outcome') == 'gap' and type(next_cursor) is int and next_cursor > cursor:
                if not fresh:
                    raise InferenceAuditGap(next_cursor)
                rows.clear()
                cursor = next_cursor
                continue
            if page.get('Outcome') != 'page' or type(next_cursor) is not int or next_cursor < cursor:
                raise ValueError('current inference audit missing or evicted')
            rows.extend(page['Entries'])
            if page['AtEnd']:
                return rows, page['Next']
            if page['Next'] == cursor:
                raise ValueError('inference audit cursor did not advance')
            cursor = page['Next']
        raise ValueError('inference audit page bound exceeded')

    def comfy(self):
        prepared = self.prepared['comfy']
        if any(r['name'] == 'comfyui' for r in self.registrations()):
            raise ValueError('existing Comfy registration must be preserved; this scene requires its absence')
        self.phase('comfy-start')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        self.comfy_base = 'http://127.0.0.1:' + str(port)
        output = self.root / 'comfy-output'
        output.mkdir()
        argv = [prepared['python']['path'], '-B', '-s', prepared['launcher']['path'], '--records', prepared['records_sha256'],
            '--state', self.root / 'comfy-launch-state', '--allow-installer', '--', self.core['helper']['path'],
            '--ordinary-comfy-request', self.request_path, '--ordinary-comfy-port', str(port)]
        saved = self.env
        self.env = {k: v for k, v in saved.items() if not k.startswith('ABSTRACTION_') and k != 'PYTHONPATH'}
        self.env.update(PYTHONPATH=str(Path(self.core['pythonpath']).resolve()), PYTHONDONTWRITEBYTECODE='1', PYTHONUTF8='1')
        try:
            process, row, log = self.start('comfy-real-host', argv)
        finally:
            self.env = saved
        self.comfy_process = process
        def ready():
            if process.poll() is not None:
                raise ValueError('real Comfy host exited: ' + str(log))
            try:
                return self.local(self.comfy_base, '/object_info/OAImageGenerate')
            except (OSError, urllib.error.URLError):
                return None
        self.wait(ready, 60)
        sdk = json.loads((self.root / 'comfy-sdk-origins.json').read_text())
        if (sdk['pid'] != process.pid or sdk['request'] != self.data['request']
                or ordinary_sdk_origins(sdk['origins'], self.core) != sdk['origins']):
            raise ValueError('real Comfy SDK imports differ before Connect/workflow')
        connected = self.local(self.comfy_base, '/openabstractions/connect', {})
        if connected.get('ok') is not True:
            raise ValueError('existing real Comfy Connect action unavailable')
        manifest = Path(prepared['extension_directory']) / 'application-manifest.json'
        value = json.loads(manifest.read_text())
        self.comfy_program = str((Path(prepared['extension_directory']) / 'helper/python.exe').resolve())
        if value != {'name': 'comfyui', 'program': self.comfy_program, 'title': 'ComfyUI',
                    'start_guidance': 'OpenAbstractions starts ComfyUI when an app opens it. While Comfy Desktop is open, start ComfyUI there yourself.'}:
            raise ValueError('real Comfy Connect manifest differs')
        self.data['ordinary']['comfy_host'] = {'process': row, 'manifest': file(manifest), 'helper': file(self.comfy_program),
            'connect': connected, 'sdk_imports': sdk}
        self.phase('native-comfy-registration', {'program': self.comfy_program,
            'instruction': 'Confirm this actual ComfyUI in the installed native registration window.'})
        self.data['ordinary']['comfy_registration'] = self.wait(self.registered_comfy, 60)
        self.policy = 'ordinary-image-' + hashlib.sha256(str(self.root).encode()).hexdigest()[:12]
        self.policy_before = self.selection()
        self.phase('native-comfy-personal-review', {'program': self.comfy_program,
            'instruction': 'Review ComfyUI and its picture permissions in the native window; choose Allow or Refuse.'})
        proof = self.personal_review('picture-workflow', self.comfy_program, ORDINARY_IMAGE_PERMISSIONS,
                                     app='comfyui', host='lemonade-image', identity=self.data['ordinary']['comfy_host']['helper'])
        proof['typed_permission_request'] = False
        self.data['ordinary']['guided_personal_permissions'] = proof

    def image(self):
        proof = self.data['ordinary']['guided_personal_permissions']
        page = self.rights()
        if (self.registered_comfy() is None or page is None or not personal_permission_rows(page,
                ORDINARY_IMAGE_PERMISSIONS, proof['account'], self.comfy_program, str(self.panel),
                proof['started_at'], now(), json.loads(checked(proof['receipt']).read_text(encoding='utf-8'))['review']['id'])):
            raise ValueError('current registered Comfy native permissions missing; no image workflow submitted')
        self.phase('comfy-image-start')
        _, cursor = self.audit(0)
        workflow = {'1': {'class_type': 'OAImageGenerate', 'inputs': {'model': '', 'selection_policy': self.policy,
                'prompt': 'A glass of lemonade on a blue table, simple illustration.', 'size': '512x512'}},
            '2': {'class_type': 'SaveImage', 'inputs': {'images': ['1', 0], 'filename_prefix': 'ordinary-oa-image'}}}
        # Exactly one fixed workflow request. The actual node retains and
        # observes its choice, starts image@1 once, verifies delivery and acks.
        submitted = self.local(self.comfy_base, '/prompt', {'prompt': workflow, 'client_id': 'ordinary-' + self.native_id})
        prompt = submitted.get('prompt_id')
        if not prompt or submitted.get('node_errors'):
            raise ValueError('real Comfy did not accept this one fixed image workflow')
        current = self.wait(lambda: self.pending(self.comfy_process, self.root / 'comfy-real-host.log', 'comfyui-image-',
            'Choose an image model for this ComfyUI run'), 20)
        identity = current['ID']
        names = [o['ID'] for o in current['Request']['Options']]
        offers = self.offers('image')
        offered = list(dict.fromkeys(n for o in offers['Offers'] for n in o['Names'])) if offers else []
        if names != offered or names != [self.image_model] or identity not in self.choices()[1]:
            raise ValueError('Comfy choice does not contain the complete current sole Klein offer')
        self.phase('opencode-comfy-choice', {'id': identity, 'request_ref': current['Request']['RequestRef'],
            'native_session': self.native_id, 'conversation': self.conversation, 'model': self.image_model,
            'instruction': ordinary_choice_instruction(current['Request']['Title'], image=True)})
        answer = self.wait(lambda: self.attributed(identity, self.image_model, self.comfy_program, current))
        tool = self.wait(lambda: self.tool(identity, self.image_model), 20)
        self.phase('comfy-image-effect')
        def complete():
            result = self.local(self.comfy_base, '/history/' + urllib.parse.quote(prompt, safe='')).get(prompt)
            if result and result.get('status', {}).get('status_str') == 'error':
                raise ValueError('real Comfy image workflow failed; retained history: ' + json.dumps(result))
            return result if result and result.get('status', {}).get('completed') else None
        history = self.wait(complete, 300)
        images = history.get('outputs', {}).get('2', {}).get('images', [])
        if len(images) != 1 or images[0].get('type') != 'output':
            raise ValueError('real Comfy did not save exactly one receiving image')
        image = (self.root / 'comfy-output' / images[0]['subfolder'] / images[0]['filename']).resolve()
        if not image.is_relative_to(self.root / 'comfy-output') or image.is_symlink():
            raise ValueError('Comfy image left its owned output directory')
        receiving = ordinary_png(image)
        entries, _ = self.audit(cursor)
        operations = [e for e in entries if os.path.normcase(e['Program']) == os.path.normcase(self.comfy_program)
            and e.get('Profile') == 'image' and e['Operation']]
        if (len(operations) != 1 or operations[0]['Model'] != self.image_model
                or operations[0]['Outcome'] != 'completed' or operations[0].get('Images') != 1):
            raise ValueError('real registered Comfy did not complete exactly one explicit image operation')
        after = self.selection()
        if after != self.policy_before:
            raise ValueError('Comfy changed its saved image preference')
        self.wait(lambda: self.choice(identity) is None, 5)
        self.data['ordinary']['image'] = {'pending': current, 'answer': answer, 'tool': tool, 'native_session': self.native_id,
            'workflow': workflow, 'submission': submitted, 'history': history, 'inference_audit': entries,
            'operation': operations[0], 'receiving': receiving, 'policy_before': self.policy_before, 'policy_after': after,
            'exact_ack_source_bound': True, 'gone': True, 'raw_delivery_observed': False}

    def answering_rule(self, permit):
        page = self.rights()
        if page is None:
            return None
        if page.get('Outcome') != 'page':
            raise ValueError('answering permission provenance unavailable')
        rows = [r for r in page['Rules'] if r['Subject']['Account'] == self.prepared['comfy']['invoking_account']
            and os.path.normcase(r['Subject']['Program']) == os.path.normcase(self.prepared['opencode']['program']['path'])
            and r['Action'] == 'abstraction.asks/question.answer' and r['Resource'] == 'apps' and r['Permit'] is permit]
        if len(rows) != 1:
            return None
        rule = rows[0]
        if (os.path.normcase(rule.get('SetBy', {}).get('Program', '')) != os.path.normcase(str(self.panel))
                or rule.get('SetBy', {}).get('Account') != self.prepared['comfy']['invoking_account']
                or not rule.get('SetAt') or datetime.fromisoformat(rule['SetAt'].replace('Z', '+00:00')) < datetime.fromisoformat(self.data['started_at'])):
            raise ValueError('answering permission was not written by this installed native Panel in this scene')
        return rule

    def rights(self):
        first, rows, cursor = None, [], ''
        for _ in range(64):
            page = self.api('/rights?cursor=' + urllib.parse.quote(cursor, safe=''))
            if page.get('Outcome') != 'page':
                raise ValueError('current native permission rules unavailable')
            if first is None:
                first = page
            elif page.get('Revision') != first.get('Revision'):
                return None  # A person edit restarts the caller's bounded next observation.
            rows.extend(page['Rules'])
            if page['Complete']:
                return {**first, 'Rules': rows, 'Complete': True}
            if not page['Next'] or page['Next'] == cursor:
                raise ValueError('native permission cursor did not advance')
            cursor = page['Next']
        raise ValueError('native permission page bound exceeded')

    def stop(self):
        before = self.enrolled()
        if before is None:
            raise ValueError('native Stop requires this current enrolled answerer')
        self.phase('native-stop-answering', {'program': self.prepared['opencode']['program']['path'], 'native_session': self.native_id,
            'instruction': 'In the installed native Panel Questions, click Stop for this OpenCode. Keep its console open until the fresh pending choice is verified.'})
        self.wait(lambda: not self.enrolled(), 60)
        rule = self.wait(lambda: self.answering_rule(False), 5)
        after_page, after_page_at = self.api('/rights/assistant'), now()
        if (after_page.get('outcome') != 'page' or not after_page.get('revision')
                or any(os.path.normcase(r['program']) == os.path.normcase(before['program']) for r in after_page['assistants'])):
            raise ValueError('native Stop did not remove the actual answerer')
        process, row, log, destination, unchanged, current = self.go_choice('go-after-stop')
        self.phase('stopped-fresh-choice', {'id': current['ID'], 'request_ref': current['Request']['RequestRef'],
            'instruction': 'Leave this new ordinary choice for the person. OpenCode has no answering eligibility.'})
        until = time.monotonic() + min(5, self.remaining())
        observations = []
        while time.monotonic() < until:
            observed = self.choice(current['ID'])
            if (not observed or observed['State'] != 'pending' or observed.get('Answer') is not None
                    or any(observed[k] != current[k] for k in ('ID', 'Request', 'Asker', 'AskedMs'))
                    or self.enrolled() or file(destination) != unchanged):
                raise ValueError('fresh choice after native Stop did not stay pending for the person')
            observations.append({'at': now(), 'record': observed})
            time.sleep(.5)
        self.phase('stopped-choice-expiration')
        process.wait(timeout=min(90, self.remaining()))
        row.update(exited=True, exit_code=process.returncode, ended_at=now(), log=file(log))
        if process.returncode == 0 or file(destination) != unchanged or self.enrolled():
            raise ValueError('stopped fresh caller chose an effect or regained answering eligibility')
        terminal = self.choice(current['ID'])
        terminal_at = now()
        if (not terminal or terminal['State'] != 'expired' or terminal.get('Answer') is not None
                or any(terminal[k] != current[k] for k in ('ID', 'Request', 'Asker', 'AskedMs'))):
            raise ValueError('unanswered fresh choice did not expire without a decision')
        self.data['ordinary']['stop'] = {'before': before, 'rule': rule, 'after_page': after_page, 'after_page_at': after_page_at,
            'pending': current, 'observations': observations, 'terminal': terminal, 'terminal_observed_at': terminal_at,
            'process': row, 'before_file': unchanged, 'after_file': file(destination), 'native_session': self.native_id}

    def close_owner(self):
        if self.owner is None:
            return
        if self.owner.poll() is None:
            self.phase('native-console-close', {'native_owner_pid': self.owner.pid, 'native_session': getattr(self, 'native_id', ''),
                'instruction': 'Exit OpenCode with /exit or Ctrl+C INSIDE its TUI. Leave the owner console open until authoritative closure restores its recorded grants.'})
            self.owner.wait(timeout=min(20, self.remaining()))
        self.owner_row.update(exited=True, exit_code=self.owner.returncode, ended_at=now())
        current = self.control(running=False)
        if not current or not current.get('closed_job') or current.get('phase') != 'closed' or not current.get('child_pid') or not current.get('child_start'):
            raise ValueError('native session closure is unknown; retain its installed runtime and recovery state')
        self.data['ordinary']['native_closed'] = current

    def cleanup(self):
        self.close_owner()  # No provider/data cleanup precedes authoritative native closure.
        self.phase('owned-cleanup')
        for proof in self.data.get('personal_reviews', {}).values():
            self.cleanup_personal_review(proof)
        for process, row in reversed(self.children):
            if row['operation'] == 'panel-readback':
                continue
            if process.poll() is None:
                process.kill()  # Exact Popen handle owned here; relay helper observes stdin EOF.
                process.wait(timeout=min(5, self.remaining()))
            row.update(exited=True, exit_code=process.returncode, ended_at=row.get('ended_at', now()), log=file(row['log_path']))
        if hasattr(self, 'comfy_program') and self.registered_comfy():
            self.call('comfy-unregister', [self.runtime, 'applications', 'remove', 'comfyui', '--json'])
            if self.registered_comfy():
                raise ValueError('exact created Comfy registration remained')
        if self.hosted_chat:
            self.call('chat-unhost', [self.runtime, 'models', 'unhost', self.hosted_chat, '--json'])
            self.hosted_chat = None
        for name in reversed(self.created_providers):
            self.call('provider-remove-' + name, [self.runtime, 'provider', 'remove', name, '--json'])
        if (self.root / 'chat-engine').exists():
            self.call('chat-engine-unlock', [self.runtime.parent / 'modelhostd.exe', 'engine', 'unlock',
                '--dir', self.root / 'chat-engine', '--state', self.roots['modelhost_state']])
        lemonade = self.data['ordinary'].get('lemonade_owned')
        if lemonade:
            self.call('lemonade-stop', [self.runtime, 'provider', 'remove', 'lemonade-image', '--json'])
            self.call('lemonade-unlock', [self.runtime.parent / 'lemonaded.exe', 'engine', 'unlock'])
            for key in ('state', 'packages'):
                target = Path(lemonade[key])
                expected = Path(os.environ['LOCALAPPDATA']) / 'OpenAbstractions' / ('providers/lemonade' if key == 'state' else 'apps/lemonade')
                if target != expected or target.is_symlink():
                    raise ValueError('created Lemonade cleanup target differs')
                if target.exists():
                    shutil.rmtree(target)
                lemonade[key + '_removed'] = not target.exists()
        self.data['ordinary']['cleanup_completed'] = True

    def run(self):
        error = None
        try:
            panel, _, log = self.start('panel-readback', [self.panel, '-native=false', '-open=false', '-addr', '127.0.0.1:0'])
            self.base, self.key = self.wait(lambda: panel_url(log.read_text(errors='replace')), 10)
            identity = self.api('/identity')
            if identity.get('runtime', {}).get('account') != self.prepared['comfy']['invoking_account']:
                raise ValueError('ordinary prepared interpreter is bound to another native account')
            self.data['ordinary']['receiving_identity'] = identity
            self.setup()
            self.start('native-person-panel', [self.panel, '-native=true', '-page', 'questions'])
            self.session()
            self.data['ordinary']['enrollment_rule'] = self.wait(lambda: self.answering_rule(True), 5)
            self.keep()
            self.comfy()
            self.image()
            self.stop()
        except BaseException as caught:
            error = caught
            self.data['failure'] = str(caught)
        finally:
            self.cleaning = True
            try:
                self.cleanup()
            except BaseException as caught:
                self.data['cleanup_failure'] = str(caught)
                error = error or caught
            # These exact handles own only fixture servers/callers. The native
            # OpenCode owner is separate and remains intact on unknown closure.
            for process, row in reversed(self.children):
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
                row.update(exited=True, exit_code=process.returncode, ended_at=row.get('ended_at', now()), log=file(row['log_path']))
            self.phase('failed' if error else 'complete')
            final = self.data['ordinary']['phases'][-1]
            final.update(ended_at=now(), wall_seconds=time.monotonic() - final.pop('_clock'))
            self.data.update(result='failed' if error else 'pass', ended_at=now(),
                owned_children_exited=all(p.poll() is not None for p, _ in self.children),
                native_owner_closed=self.owner is None or bool(self.data['ordinary'].get('native_closed')))
            self.data['public_projection'] = {'source_sha': self.core['source_sha'], 'platform': sys.platform,
                'result': self.data['result'], 'ordinary_adopters': not error,
                'operator_wait_seconds': sum(p['wall_seconds'] for p in self.data['ordinary']['phases'] if p['kind'] == 'operator_wait'),
                'compute_seconds': sum(p['wall_seconds'] for p in self.data['ordinary']['phases'] if p['kind'] == 'compute')}
            if error is None:
                try:
                    validate_ordinary_result(self.data, self.core)
                except BaseException as caught:
                    error = caught
                    self.data.update(result='failed', failure=str(caught))
                    self.data['public_projection'].update(result='failed', ordinary_adopters=False)
            write(self.root / 'ordinary-result.json', self.data)
            self.progress('failed' if error else 'complete', {})
        if error:
            raise error
        return self.root / 'ordinary-result.json'


def adopter_journey_command(program, project, coding_model, title, prompt):
    """The verified official 1.18.34 run interface; no permission auto-approval."""
    if (not Path(program).is_absolute() or not Path(project).is_absolute()
            or not re.fullmatch(r'[A-Za-z0-9_.:/+-]{1,256}', coding_model)
            or not re.fullmatch(r'j1-[0-9a-f]{32}', title) or not isinstance(prompt, str) or len(prompt) > 4096):
        raise ValueError('fixed owned official OpenCode invocation required')
    return [str(program), 'run', '--format', 'json', '--model', coding_model,
            '--dir', str(project), '--title', title, prompt]


def adopter_journey_session(events):
    """Read the real CLI JSON event stream, never an invented conversation ID."""
    if not isinstance(events, str) or len(events.encode()) > 16 * 1024 * 1024:
        raise ValueError('bounded official OpenCode event stream required')
    sessions = set()
    for line in events.splitlines():
        if not line.strip():
            continue
        row = json.loads(line)
        if not isinstance(row, dict) or row.get('type') == 'error':
            raise ValueError('official OpenCode run reported an error')
        if row.get('sessionID'):
            sessions.add(row['sessionID'])
    if len(sessions) != 1 or not all(isinstance(v, str) and v for v in sessions):
        raise ValueError('one real official OpenCode conversation required')
    return sessions.pop()


def adopter_journey_image_instruction(request):
    return ('Use openabstractions_generate_image exactly once with these exact arguments: '
            + json.dumps(request, sort_keys=True) + '. Return the image attachment. Do not use other tools.')


def adopter_journey_image_export(export, session, request):
    """Receive the completed official tool's actual attachment and metadata."""
    if export.get('info', {}).get('id') != session:
        raise ValueError('image export belongs to another official conversation')
    found = []
    for message in export.get('messages', []):
        info = message.get('info', {})
        if info.get('role') != 'assistant' or info.get('sessionID') != session:
            continue
        for part in message.get('parts', []):
            state = part.get('state', {})
            if (part.get('type') != 'tool' or part.get('tool') != 'openabstractions_generate_image'
                    or part.get('sessionID') != session or part.get('messageID') != info.get('id')
                    or state.get('status') != 'completed' or state.get('input') != request):
                continue
            metadata, attachments = state.get('metadata', {}), state.get('attachments', [])
            if not isinstance(metadata, dict) or len(attachments) != 1 or not isinstance(attachments[0], dict):
                raise ValueError('official image tool lacks one visible attachment')
            attachment = attachments[0]
            prefix = 'data:image/png;base64,'
            if attachment.get('type') != 'file' or attachment.get('mime') != 'image/png' or not str(attachment.get('url', '')).startswith(prefix):
                raise ValueError('official attachment is not a bounded inline PNG')
            encoded = attachment['url'][len(prefix):]
            if len(encoded) > 12 * 1024 * 1024:
                raise ValueError('official attachment exceeds image bound')
            data = base64.b64decode(encoded, validate=True)
            digest = 'sha256:' + hashlib.sha256(data).hexdigest()
            if (not data or len(data) > 8 * 1024 * 1024 or not data.startswith(b'\x89PNG\r\n\x1a\n')
                    or metadata.get('digest') != digest or metadata.get('bytes') != len(data)
                    or metadata.get('mediaType') != 'image/png' or not metadata.get('operation')):
                raise ValueError('official attachment bytes differ from tool delivery')
            found.append((part, data))
    if len(found) != 1:
        raise ValueError('one exact completed official image tool required')
    return found[0]


def adopter_journey_image_correlation(export, session, request, history_before, history_after,
                                      audit, program, account, host, saved, *, stock_assets=None):
    """Correlate actual app export, runtime OS-peer audit and stock Comfy output.

    Device presence is retained separately and does not itself prove GPU work.
    The complete GPU/negative/step cases remain required by the aggregate owner.
    """
    part, image = adopter_journey_image_export(export, session, request)
    metadata = part['state']['metadata']
    if metadata.get('host') != host or metadata.get('model') != request['model']:
        raise ValueError('official tool selected another registered host/model')
    operations = [row for row in audit if row.get('Profile') == 'image' and row.get('Operation')]
    if (len(operations) != 1 or operations[0].get('Operation') != metadata['operation']
            or os.path.normcase(operations[0].get('Program', '')) != os.path.normcase(program)
            or operations[0].get('Account') != account
            or operations[0].get('Host') != host or operations[0].get('Model') != request['model']
            or operations[0].get('Outcome') != 'completed' or operations[0].get('Images') != 1):
        raise ValueError('image tool lacks one matching receiver-attributed operation')
    if checked(saved).read_bytes() != image:
        raise ValueError('visible official attachment differs from saved image bytes')
    loaders = {'diffusion_model': ('UNETLoader', 'unet_name'),
               'text_encoder': ('CLIPLoader', 'clip_name'), 'vae': ('VAELoader', 'vae_name')}
    filenames = {}
    if stock_assets is not None:
        if (not isinstance(stock_assets, list) or len(stock_assets) != len(loaders) or
                any(not isinstance(asset, dict) or not isinstance(asset.get('role'), str) for asset in stock_assets)):
            raise ValueError('J1 stock recipe asset pins incomplete')
        filenames = {asset.get('role'): asset.get('filename') for asset in stock_assets}
        if set(filenames) != set(loaders) or any(not isinstance(name, str) or not name for name in filenames.values()):
            raise ValueError('J1 stock recipe asset pins differ')
    candidates = []
    for identity in set(history_after) - set(history_before):
        entry = history_after[identity]
        prompt = entry.get('prompt', [])
        if not isinstance(prompt, list) or len(prompt) < 3 or prompt[1] != identity or not isinstance(prompt[2], dict):
            continue
        graph = prompt[2]
        if any(row.get('class_type', '').startswith('OA') for row in graph.values()):
            continue  # An OA wrapper graph would reverse the required backend direction.
        classes = {row.get('class_type') for row in graph.values()}
        stock = {'UNETLoader', 'CLIPLoader', 'VAELoader', 'PrimitiveInt', 'RandomNoise', 'KSamplerSelect',
                 'CLIPTextEncode', 'ConditioningZeroOut', 'CFGGuider', 'Flux2Scheduler',
                 'EmptyFlux2LatentImage', 'SamplerCustomAdvanced', 'VAEDecode', 'SaveImage'}
        if not classes <= stock:
            continue
        texts = [row.get('inputs', {}).get('text') for row in graph.values() if row.get('class_type') == 'CLIPTextEncode']
        if not {'UNETLoader', 'CLIPLoader', 'VAELoader', 'SamplerCustomAdvanced', 'SaveImage'} <= classes or request['prompt'] not in texts:
            continue
        if filenames and any([node.get('inputs', {}).get(field) for node in graph.values()
                              if node.get('class_type') == loader] != [filenames[role]]
                             for role, (loader, field) in loaders.items()):
            continue
        outputs = [image for output in entry.get('outputs', {}).values() for image in output.get('images', [])]
        if (entry.get('status', {}).get('status_str') == 'success' and len(outputs) == 1
                and outputs[0].get('type') == 'output' and outputs[0].get('filename') == Path(saved['path']).name):
            candidates.append(identity)
    if len(candidates) != 1:
        raise ValueError('one new exact stock Comfy prompt/history output required')
    return {'session_id': session, 'operation_id': metadata['operation'], 'prompt_id': candidates[0],
            'attachment_sha256': hashlib.sha256(image).hexdigest(), 'bytes': len(image),
            'saved_image': saved, 'tool_part': part}


def adopter_journey_inputs(core):
    """Require the existing accepted scope and all selected immutable inputs."""
    prepared = core.get('adopter_journey', {})
    fields = {'scope', 'scope_sha256', 'source_sha', 'version', 'inputs_sha256',
              'probe_source_sha256', 'candidate_artifacts_sha256'}
    if not isinstance(prepared, dict) or set(prepared) != fields:
        raise ValueError('exact static journey identities required; dynamic observations belong beside core')
    scope = prepared.get('scope', {})
    scope_hash = hashlib.sha256(json.dumps(scope, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest()
    if (prepared.get('source_sha') != core.get('source_sha') or prepared.get('version') != '0.8.0'
            or scope.get('release') != prepared.get('version') or prepared.get('scope_sha256') != scope_hash
            or any(not re.fullmatch('[0-9a-f]{64}', str(prepared.get(key))) for key in
                   ('inputs_sha256', 'probe_source_sha256', 'candidate_artifacts_sha256'))):
        raise ValueError('current accepted scope and immutable pre-execution input identities required')
    if prepared['probe_source_sha256'] != core.get('helper', {}).get('sha256'):
        raise ValueError('exact native probe source required')
    return prepared


def validate_j1_selection_sdk(spec):
    """Verify the original prepared SDK closure and explicit test-tool pin."""
    if (not isinstance(spec, dict) or set(spec) != {'schema', 'source_sha', 'node', 'sdk'} or
            spec['schema'] != 1 or not re.fullmatch('[0-9a-f]{40}', str(spec['source_sha']))):
        raise ValueError('J1 requires the explicit prepared current SDK and Node test tool')
    checked(spec['node'])
    sdk = spec['sdk']
    if not isinstance(sdk, dict) or set(sdk) != {'directory', 'manifest', 'sources'}:
        raise ValueError('J1 prepared SDK descriptor differs')
    root = Path(sdk['directory'])
    if not root.is_absolute() or root.is_symlink() or root.is_junction():
        raise ValueError('J1 SDK root must be a regular absolute directory')
    if any(parent.is_symlink() or parent.is_junction() for parent in root.parents):
        raise ValueError('J1 SDK root has redirected ancestry')
    manifest_path = checked(sdk['manifest'], root)
    if manifest_path != root.resolve() / 'manifest.json' or manifest_path.stat().st_size > 4 * 1024**2:
        raise ValueError('J1 SDK manifest location/bound differs')
    manifest = json.loads(manifest_path.read_text(encoding='utf-8-sig'))
    if (set(manifest) != {'schema', 'sources', 'files'} or manifest['schema'] != 1 or
            manifest['sources'] != sdk['sources'] or not isinstance(manifest['files'], dict) or
            not 1 <= len(manifest['files']) <= 40000):
        raise ValueError('J1 SDK manifest differs from original selected source graph')
    seen, total, pending = {}, 0, [root]
    while pending:
        for path in pending.pop().iterdir():
            if path.is_symlink() or path.is_junction():
                raise ValueError('J1 SDK closure contains redirected members')
            if path.is_dir():
                pending.append(path)
            elif path.is_file():
                if path == manifest_path:
                    continue
                name = path.relative_to(root).as_posix()
                total += path.stat().st_size
                if total > 512 * 1024**2 or len(seen) >= 40000:
                    raise ValueError('J1 SDK closure exceeds bound')
                seen[name] = hashlib.sha256(path.read_bytes()).hexdigest()
            else:
                raise ValueError('J1 SDK member is not regular')
    if seen != manifest['files'] or 'j1_selection_probe.mjs' not in seen:
        raise ValueError('J1 prepared SDK member bytes differ')
    return root.resolve(), manifest


def j1_selection_observation(native, observations, program, runtime, account):
    """Reread canonical child bytes using the original source-bound descriptor."""
    spec = native.get('j1_selection')
    root, manifest = validate_j1_selection_sdk(spec)
    if spec['source_sha'] != native['core']['source_sha']:
        raise ValueError('J1 SDK source differs from original core')
    pins = observations.get('j1_selection')
    if not isinstance(pins, dict) or set(pins) != {'request', 'result', 'process'}:
        raise ValueError('J1 canonical selection receiver files required')
    request_path = checked(pins['request'])
    result_path = checked(pins['result'], request_path.parent)
    process_path = checked(pins['process'], request_path.parent)
    if (request_path.name != 'j1-selection-request.json' or result_path.name != 'j1-selection-result.json' or
            process_path.name != 'j1-selection-process.json' or result_path.stat().st_size > 1024**2):
        raise ValueError('J1 canonical selection receiver location/bound differs')
    request = json.loads(request_path.read_text(encoding='utf-8-sig'))
    if request != {'schema': 1, 'account': account, 'program': program, 'runtime': runtime}:
        raise ValueError('J1 selection request differs from actual installed program/account/runtime')
    value = json.loads(result_path.read_text(encoding='utf-8-sig'))
    process = json.loads(process_path.read_text(encoding='utf-8-sig'))
    if (set(value) != {'schema', 'request', 'process', 'target', 'selected', 'origins', 'before', 'offer', 'after', 'offer_request'} or
            value['schema'] != 1 or value['request'] != pins['request'] or value['target'] != program or
            value['selected'] != {'principalKind': 1, 'principal': account, 'program': runtime['path']} or
            value['process'].get('image') != spec['node'] or type(value['process'].get('pid')) is not int or
            value['process']['pid'] != process.get('ProcessId') or process.get('RequestedFileName') != spec['node']['path'] or
            process.get('RequestedArguments') != '"' + str(root / 'j1_selection_probe.mjs') + '" "' + str(request_path) + '" "' + str(result_path) + '"' or
            process.get('Closure') != 'known' or process.get('ExitCode') != 0 or not process.get('CreationUtc') or
            process.get('State') != 'completed' or process.get('TerminationAttempted') is not False or process.get('Failure') is not None or
            process.get('DirectExited') is not True or process.get('OutputClosed') is not True or
            value['offer_request'] != {'profile': '', 'placement': 'local', 'reserve': '4096', 'fresh': True}):
        raise ValueError('J1 SDK child/server/fixture identity differs')
    if set(value['origins']) != {'ipc', 'facade', 'inference', 'router'}:
        raise ValueError('J1 current SDK import origins missing')
    for name, pin in value['origins'].items():
        path = checked(pin, root)
        relative = path.relative_to(root).as_posix()
        if (not relative.startswith('node_modules/@openabstractions/' + name + '/') or
                manifest['files'].get(relative) != pin['sha256']):
            raise ValueError('J1 SDK imported another source')
    before, after, offer = value['before'], value['after'], value['offer']
    if before != after:
        raise ValueError('J1 selected policy revision changed during observation')
    if (not isinstance(before, dict) or before.get('outcome') not in ('found', 'unknown', 'invalid', 'forbidden', 'unavailable') or
            not isinstance(offer, dict) or offer.get('outcome') not in ('offered', 'invalid', 'unavailable', 'forbidden', 'not_permitted') or
            not isinstance(offer.get('offers'), list) or len(offer['offers']) > 512):
        raise ValueError('J1 selection/offer result is malformed')
    missing = []
    if before.get('outcome') != 'found':
        missing.append('selection-policy:' + str(before.get('outcome')) +
                       (' (existing selection.manage_for required)' if before.get('outcome') == 'forbidden' else ''))
    else:
        policy = before.get('policy', {})
        if (not before.get('revision') or policy.get('name') != 'oa-chat-default' or
                not isinstance(policy.get('candidates'), list) or not 1 <= len(policy['candidates']) <= 8):
            raise ValueError('J1 selected policy is malformed')
        for candidate in policy['candidates']:
            if (not isinstance(candidate, dict) or not {'model', 'placement'} <= set(candidate) <= {'model', 'placement', 'provider', 'credential'} or
                    not isinstance(candidate['model'], str) or not 1 <= len(candidate['model']) <= 512 or
                    candidate['placement'] not in ('local', 'hosted') or
                    any(not isinstance(candidate[key], str) or len(candidate[key]) > 512 for key in ('provider', 'credential') if key in candidate)):
                raise ValueError('J1 selected candidate is malformed')
        for row in offer['offers']:
            if (not isinstance(row, dict) or not isinstance(row.get('family'), str) or
                    not isinstance(row.get('names'), list) or not isinstance(row.get('servers'), list) or
                    any(not isinstance(name, str) for name in row['names'] + row['servers']) or
                    row.get('context_source') not in ('effective', 'maximum', 'default', 'unknown', 'none') or
                    any(not re.fullmatch(r'[0-9]{1,19}', str(row.get(key))) for key in ('context', 'input'))):
                raise ValueError('J1 local offer row is malformed')
        if offer.get('outcome') != 'offered':
            missing.append('local-coding-offer:' + str(offer.get('outcome')))
        else:
            # Offer proves family/local/context availability. It supplies no
            # credential or logical provider-declaration join for pinned rows.
            eligible = [candidate for candidate in policy['candidates'] if candidate.get('placement') == 'local' and
                not candidate.get('provider') and not candidate.get('credential') and
                any(candidate.get('model') in [row.get('family'), *row.get('names', [])] and
                    row.get('context_source') in ('effective', 'maximum', 'default') and
                    int(row.get('input', 0)) > 0 and int(row.get('context', 0)) > 4096 and row.get('servers')
                    for row in offer.get('offers', []))]
            if not eligible:
                missing.append('eligible-local-coding-candidate')
    return {'files': pins, 'policy': before, 'offer': offer, 'target': program,
            'server': value['selected'], 'sdk_manifest': spec['sdk']['manifest']}, missing


J1_COMFY_FRONTEND_CASES = (
    "unsaved canvas changes go through ComfyUI's own confirmation",
    'a frontend without a workflow store counts as unsaved: confirm, or keep the canvas',
    'graph digest mismatch refuses the load without changing the canvas',
    'a second click while the workflow loads reads and loads it once',
)
J1_COMFY_FRONTEND = 'oa-backend/ComfyUI/custom_nodes/abstraction_downloads/web/oa_downloads.js'


def j1_native_payload_observation(native, chain, pins):
    """Authenticate original held-file observations before using native members."""
    spec = native.get('j1_selection')
    sdk, manifest = validate_j1_selection_sdk(spec)
    if spec['source_sha'] != native['core']['source_sha'] or not isinstance(pins, dict) or set(pins) != {'request', 'result', 'process'}:
        raise ValueError('J1 native payload SDK/files differ')
    paths = {role: checked(pin) for role, pin in pins.items()}
    directory = paths['request'].parent
    if (any(path.parent != directory or path.name != 'j1-native-payload-' + role + '.json' for role, path in paths.items()) or
            paths['request'].stat().st_size > 128 * 1024**2 or paths['result'].stat().st_size > 128 * 1024**2):
        raise ValueError('J1 native payload canonical location/bound differs')
    request, value, process = (json.loads(paths[role].read_text(encoding='utf-8-sig')) for role in ('request', 'result', 'process'))
    compiled = native['selected_app_readback']['comfyui']
    root = PureWindowsPath(chain['fixture_owner']['local_app_data']) / 'OA/a/cu' / compiled['native']['identity']['version']
    if request != {'schema': 1, 'root': str(root), 'files': compiled['native_files']}:
        raise ValueError('J1 native payload request differs from original source/KnownFolder')
    probe = file(sdk / 'go_probe.exe')
    if probe['sha256'] != manifest['files'].get('go_probe.exe'):
        raise ValueError('J1 native payload observer differs from admitted SDK')
    if (process.get('RequestedFileName') != probe['path'] or
            process.get('RequestedArguments') != '--payload-observation "' + str(paths['request']) + '" "' + str(paths['result']) + '"' or
            type(process.get('ProcessId')) is not int or not process.get('CreationUtc') or process.get('CreationError') is not None or
            process.get('Closure') != 'known' or process.get('ExitCode') != 0 or process.get('State') != 'completed' or
            process.get('DirectExited') is not True or process.get('OutputClosed') is not True or
            process.get('TerminationAttempted') is not False or process.get('Failure') is not None or
            process.get('Diagnostics') or process.get('Output')):
        raise ValueError('J1 native payload OS child/closure differs')
    apps = [row for row in chain['apps'] if row.get('id') == 'comfyui']
    if (set(value) != {'root', 'root_identity', 'files'} or value['root'] != str(root) or
            not isinstance(value['root_identity'], str) or not 1 <= len(value['root_identity']) <= 256 or
            len(apps) != 1 or apps[0].get('root') != value['root'] or apps[0].get('files') != value['files']):
        raise ValueError('J1 native payload original journal differs from held observations')
    members = installed_payload_members(native, chain)['comfyui']
    return {'files': pins, 'root': str(root), 'root_identity': value['root_identity'], 'members': members}


def j1_frontend_observation(native, chain, pins):
    """Bind the existing gating corpus to exact independently installed bytes."""
    spec = native.get('j1_selection')
    sdk, manifest = validate_j1_selection_sdk(spec)
    if spec['source_sha'] != native['core']['source_sha'] or not isinstance(pins, dict) or set(pins) != {'request', 'result', 'process'}:
        raise ValueError('J1 frontend SDK/files differ')
    paths = {role: checked(pin) for role, pin in pins.items()}
    directory = paths['request'].parent
    expected_names = {'request': 'j1-comfy-frontend-request.json', 'process': 'j1-comfy-frontend-process.json', 'result': 'j1-comfy-frontend.tap'}
    if any(path.parent != directory or path.name != expected_names[role] or path.stat().st_size > 1024**2 for role, path in paths.items()):
        raise ValueError('J1 frontend canonical files/location differ')
    member = installed_payload_members(native, chain)['comfyui'][J1_COMFY_FRONTEND]
    frontend = {key: member[key] for key in ('path', 'sha256', 'bytes')}
    corpus = file(sdk / 'j1_comfy_frontend.test.mjs')
    if corpus['sha256'] != manifest['files'].get('j1_comfy_frontend.test.mjs'):
        raise ValueError('J1 frontend corpus differs from admitted source')
    request = json.loads(paths['request'].read_text(encoding='utf-8-sig'))
    if request != {'schema': 1, 'source_sha': native['core']['source_sha'], 'frontend': frontend,
                   'identity': member['identity'], 'corpus': corpus, 'cases': list(J1_COMFY_FRONTEND_CASES)}:
        raise ValueError('J1 frontend request differs from actual installed bytes/source/corpus')
    process = json.loads(paths['process'].read_text(encoding='utf-8-sig'))
    pattern = '^(' + '|'.join(J1_COMFY_FRONTEND_CASES) + ')$'
    arguments = '--test --test-reporter=tap "--test-name-pattern=' + pattern + '" "' + corpus['path'] + '"'
    if (process.get('RequestedFileName') != spec['node']['path'] or process.get('RequestedArguments') != arguments or
            type(process.get('ProcessId')) is not int or not process.get('CreationUtc') or process.get('CreationError') is not None or
            process.get('Closure') != 'known' or process.get('ExitCode') != 0 or process.get('State') != 'completed' or
            process.get('DirectExited') is not True or process.get('OutputClosed') is not True or
            process.get('TerminationAttempted') is not False or process.get('Failure') is not None or process.get('Diagnostics')):
        raise ValueError('J1 frontend native child/closure differs')
    output = paths['result'].read_bytes().decode('utf-8')
    if (output != process.get('Output') or not re.search(r'^# pass 4\r?$', output, re.MULTILINE) or
            not re.search(r'^# fail 0\r?$', output, re.MULTILINE) or
            any(output.count('# Subtest: ' + name + '\n') != 1 for name in J1_COMFY_FRONTEND_CASES)):
        raise ValueError('J1 installed frontend gating corpus did not execute every required case')
    return {'files': pins, 'frontend': frontend, 'identity': member['identity'],
            'validated_cases': ['canvas-unsaved-decline', 'canvas-unknown-decline', 'canvas-no-confirmer',
                'canvas-accept-one-verified-load', 'canvas-saved-no-extra-confirmation', 'canvas-no-render-post-or-oa-approval']}


def j1_application_observation(native, pins, runtime, account, operation):
    """Replay the fixed native collector against its original SDK/OS owner.

    This authenticates a collector and its protocol observations. Registration
    still needs the independently installed native payload join before it can
    contribute a functional receipt.
    """
    spec = native.get('j1_selection')
    root, manifest = validate_j1_selection_sdk(spec)
    if spec['source_sha'] != native['core']['source_sha'] or operation not in ('observe', 'activate', 'graph'):
        raise ValueError('J1 application collector source/operation differs')
    script = root / 'j1_application_probe.mjs'
    if not script.is_file() or manifest['files'].get('j1_application_probe.mjs') != file(script)['sha256']:
        raise ValueError('J1 application collector is absent from the admitted SDK')
    if not isinstance(pins, dict) or set(pins) != {'request', 'result', 'process'}:
        raise ValueError('J1 original application collector files required')
    paths = {role: checked(pin) for role, pin in pins.items()}
    directory = paths['request'].parent
    if any(path.parent != directory or path.name != 'j1-application-' + operation + '-' + role + '.json'
           for role, path in paths.items()) or paths['result'].stat().st_size > 1024**2:
        raise ValueError('J1 application collector location/bound differs')
    request, value, process = (json.loads(paths[role].read_text(encoding='utf-8-sig'))
                               for role in ('request', 'result', 'process'))
    if request != {'schema': 1, 'account': account, 'runtime': runtime, 'operation': operation}:
        raise ValueError('J1 application request differs from installed runtime/account')
    if (set(value) != {'schema', 'request', 'process', 'selected', 'origins', 'operations'} or
            value['schema'] != 1 or value['request'] != pins['request'] or
            value['selected'] != {'principalKind': 1, 'principal': account, 'program': runtime['path']} or
            value['process'].get('image') != spec['node'] or type(value['process'].get('pid')) is not int or
            value['process']['pid'] != process.get('ProcessId') or process.get('RequestedFileName') != spec['node']['path'] or
            process.get('RequestedArguments') != '"' + str(root / 'j1_application_probe.mjs') + '" "' + str(paths['request']) + '" "' + str(paths['result']) + '"' or
            process.get('Closure') != 'known' or process.get('ExitCode') != 0 or not process.get('CreationUtc') or
            process.get('State') != 'completed' or process.get('TerminationAttempted') is not False or process.get('Failure') is not None or
            process.get('DirectExited') is not True or process.get('OutputClosed') is not True):
        raise ValueError('J1 application native child/server/closure differs')
    if set(value['origins']) != {'facade', 'ipc', 'applications', 'appmodel'}:
        raise ValueError('J1 application current SDK import origins missing')
    for name, pin in value['origins'].items():
        path = checked(pin, root)
        relative = path.relative_to(root).as_posix()
        package = name if name in ('facade', 'ipc') else 'facade'
        if (not relative.startswith('node_modules/@openabstractions/' + package + '/') or
                manifest['files'].get(relative) != pin['sha256']):
            raise ValueError('J1 application imported another SDK source')
    rows = value['operations']
    names = {'observe': ['observe'], 'activate': ['observe', 'activate'],
             'graph': ['observe', 'describe', 'graph.read', 'graph.edit', 'graph.read', 'graph.edit', 'graph.read']}[operation]
    if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows) or [row.get('operation') for row in rows] != names:
        raise ValueError('J1 application fixed operation sequence differs')
    if rows[0]['input'] != {'application': 'comfyui', 'cursor': '', 'limit': 1, 'waitMs': 0}:
        raise ValueError('J1 application observation target differs')
    if operation == 'activate' and rows[1]['input'] != {'application': 'comfyui', 'verb': 'open', 'payload': '{}', 'context': ''}:
        raise ValueError('J1 application activation differs')
    if operation == 'graph':
        described = rows[1]['output']
        description = described.get('answer', {})
        if (rows[1]['input'] != {'application': 'comfyui'} or described.get('outcome') != 'applied' or description.get('outcome') != 'page' or
                len(description.get('applications', [])) != 1 or description['applications'][0].get('application') != 'comfyui' or
                len(description['applications'][0].get('instances', [])) != 1):
            raise ValueError('J1 native graph instance is absent or ambiguous')
        instance = description['applications'][0]['instances'][0]
        contexts = [entry['context'] for entry in instance.get('contexts', [])
                    if entry.get('context', {}).get('context') == 'graph']
        if (instance.get('interface') != 'abstraction.facade/appmodel@1' or len(contexts) != 1 or
                contexts[0].get('instance') != instance['instance']):
            raise ValueError('J1 native graph contract/context differs')
        context = contexts[0]

        def command(index, args, expected_revision=''):
            row = rows[index]
            arguments = json.loads(row['input']['arguments'])
            key = arguments.get('idempotency_key')
            result = row.get('output', {}).get('answer', {}).get('result', {})
            if (row['input'].get('application') != 'comfyui' or row['input'].get('instance') != instance['instance'] or
                    row['input'].get('tool') != row['operation'] or
                    (not re.fullmatch('j1-[0-9a-f]{32}', str(key)) if expected_revision else key != '') or
                    arguments != {'context': context, 'arguments': args, 'mode': 'apply',
                                  'expected_revision': expected_revision, 'idempotency_key': key} or
                    row['output'].get('outcome') != 'applied' or result.get('outcome') != 'applied' or
                    not isinstance(result.get('revision'), str) or not result['revision']):
                raise ValueError('J1 native graph command differs')
            effects = json.loads(result['effects'])
            if effects.get('revision') != result['revision']:
                raise ValueError('J1 native graph command revision differs')
            return effects

        def read(index, steps):
            effects = command(index, {})
            revision = effects.pop('revision')
            if effects != {'nodes': [{'id': '7', 'type': 'KSampler', 'title': 'KSampler', 'mode': 'always',
                                     'widgets': [{'name': 'steps', 'type': 'int', 'value': steps, 'min': 1, 'max': 10000}],
                                     'inputs': [], 'outputs': []}], 'links': [], 'groups': []}:
                raise ValueError('J1 source-owned graph projection differs')
            return revision

        def edit(index, steps, revision):
            effects = command(index, {'ops': [{'op': 'set_widget', 'node': '7', 'widget': 'steps', 'value': steps}]},
                              revision)
            if effects != {'applied': 1, 'revision': effects['revision']} or effects['revision'] == revision:
                raise ValueError('J1 native conditional graph edit differs')
            return effects['revision']

        before = read(2, 20)
        edited = edit(3, 21, before)
        after = read(4, 21)
        if after != edited:
            raise ValueError('J1 native graph read/edit revision differs')
        restored = edit(5, 20, after)
        if read(6, 20) != restored:
            raise ValueError('J1 native graph read/edit/read/restore revision differs')
    return value


def adopter_gpu_execution(before, after, actor):
    """Require measured OS compute time for the same held native process.

    The lifecycle authenticates these original OS observations and its single
    prompt interval. GPU presence and Comfy system_stats are insufficient.
    """
    if not all(isinstance(row, dict) for row in (before, after, actor)) or not isinstance(actor.get('image'), dict):
        raise ValueError('J1 native GPU process observations missing')
    keys = ('pid', 'creation_utc', 'image')
    if (not isinstance(actor, dict) or type(actor.get('pid')) is not int or actor['pid'] < 1 or
            not actor.get('creation_utc') or not actor.get('image', {}).get('identity') or
            any(before.get(key) != actor.get(key) or after.get(key) != actor.get(key) for key in keys) or
            not re.fullmatch('[0-9a-f]{64}', str(actor['image'].get('sha256'))) or
            type(actor['image'].get('bytes')) is not int or actor['image']['bytes'] <= 0):
        raise ValueError('J1 GPU process birth/image/native identity differs')
    start = datetime.fromisoformat(before['observed_at'])
    end = datetime.fromisoformat(after['observed_at'])
    if start.tzinfo is None or end.tzinfo is None or not 0 < (end - start).total_seconds() <= 390:
        raise ValueError('J1 GPU execution interval differs')
    def counters(sample):
        rows = sample.get('counters')
        if not isinstance(rows, list) or len(rows) > 128:
            raise ValueError('J1 GPU counter bound differs')
        result = {}
        pattern = 'pid_' + str(actor['pid']) + r'_luid_[0-9A-Fa-fx]+_[0-9A-Fa-fx]+_phys_[0-9]+_eng_[0-9]+_engtype_(?:Compute|Compute_[0-9]+)'
        for row in rows:
            if (not isinstance(row, dict) or set(row) != {'name', 'running_time', 'timestamp', 'frequency'} or
                    not re.fullmatch(pattern, str(row.get('name'))) or row['name'] in result or
                    any(not re.fullmatch('[0-9]{1,20}', str(row.get(key))) for key in ('running_time', 'timestamp', 'frequency')) or
                    int(row['frequency']) <= 0):
                raise ValueError('J1 GPU native counter differs')
            result[row['name']] = row
        return result
    old, new = counters(before), counters(after)
    measured = {}
    for name in old.keys() & new.keys():
        a, b = old[name], new[name]
        if a['frequency'] != b['frequency'] or int(b['timestamp']) <= int(a['timestamp']) or int(b['running_time']) < int(a['running_time']):
            raise ValueError('J1 GPU native counter reset or clock changed')
        if int(b['running_time']) > int(a['running_time']):
            measured[name] = int(b['running_time']) - int(a['running_time'])
    if not measured:
        raise ValueError('J1 has no measured owned-process GPU compute execution')
    return {'actor': actor, 'engines': sorted(measured), 'running_time_delta': dict(sorted(measured.items())),
            'started_at': before['observed_at'], 'ended_at': after['observed_at']}


def native_comfy_setup_observation(pin, binary, root):
    """Replay the source-owned Setup transcript, retaining actual approval."""
    path = checked(pin, Path(root))
    if path.name != 'j1-comfy-setup.json' or path.stat().st_size > 16 * 1024**2:
        raise ValueError('J1 native Setup canonical transcript differs')
    rows = json.loads(path.read_text(encoding='utf-8'))
    if not isinstance(rows, list) or not 5 <= len(rows) <= 1024:
        raise ValueError('J1 native Setup transcript incomplete')
    review = rows[0].get('answer', {})
    components = [row for row in review.get('components', []) if row.get('id') == 'comfyui-app']
    if (rows[0].get('route') != '/setup/plan' or rows[0].get('body') != {'selected': ['comfyui', 'pytorch']} or
            not review.get('id') or review.get('preparable') is not True or review.get('blockers') or
            len(components) != 1 or any(components[0].get('artifact', {}).get(key) != binary.get(key) for key in ('sha256', 'bytes'))):
        raise ValueError('J1 native Setup source archive/review differs')
    plan, approved, installed, registered, previous = review['id'], False, False, False, None
    for row in rows:
        at = datetime.fromisoformat(row['observed_at'])
        if at.tzinfo is None or (previous is not None and at < previous):
            raise ValueError('J1 native Setup observation order differs')
        previous = at
    for row in rows[1:]:
        route, body, answer = row.get('route'), row.get('body'), row.get('answer', {})
        if route == '/setup/install':
            if body is not None or answer.get('plan', {}).get('id') != plan:
                raise ValueError('J1 native Setup approval read differs')
            if answer.get('approved') is True:
                if answer.get('install', {}).get('plan_id') != plan:
                    raise ValueError('J1 native Setup actual approval differs')
                approved = True
        elif route == '/setup/advance-next':
            if (not approved or body != {'plan_id': plan} or answer.get('plan', {}).get('id') != plan or
                    answer.get('current_catalog') is not True):
                raise ValueError('J1 native Setup advanced without current actual approval')
        elif route == '/setup/comfy/register':
            if not approved or not installed or body != {'setup_plan_id': plan, 'confirm': True}:
                raise ValueError('J1 native registration preceded approved installed payload')
            registered = True
        elif route == '/setup/comfy/status':
            if body is not None or not registered:
                raise ValueError('J1 native registration readback preceded registration')
        else:
            raise ValueError('J1 native Setup transcript contains a foreign operation')
        if route in ('/setup/install', '/setup/advance-next'):
            effects = answer.get('components', [])
            if any(item.get('effect', {}).get('phase') in ('failed', 'uncertain') for item in effects):
                raise ValueError('J1 native Setup has a failed or uncertain effect')
            if effects and all(item.get('effect', {}).get('phase') == 'installed' for item in effects):
                installed = True
        if route in ('/setup/comfy/register', '/setup/comfy/status') and (
                answer.get('registered') is not True or answer.get('matching') is not True or answer.get('application') != 'comfyui'):
            raise ValueError('J1 native registration differs')
    if not (approved and installed and registered) or rows[-1].get('route') != '/setup/comfy/status':
        raise ValueError('J1 native Setup has no complete approved installation/registration observation')
    return {'plan_id': plan, 'binary': binary, 'records': pin}


def comfy_verified_store(plan):
    """Receive the exact recipe owner plan from an explicit verify read."""
    assets = plan.get('assets')
    if (not isinstance(assets, list) or not 1 <= len(assets) <= 64
            or any(not isinstance(asset, dict) or not re.fullmatch('[0-9a-f]{64}', str(asset.get('sha256')))
                   or type(asset.get('size_bytes')) is not int or asset['size_bytes'] <= 0 for asset in assets)
            or len({asset['url'] for asset in assets}) != len(assets)):
        raise ValueError('J1 verified recipe file pins differ')
    static = {'id': plan['id'], 'title': plan['title'], 'template': plan['template'],
              'assets': [{key: value for key, value in asset.items() if key not in ('state', 'source_path')}
                         for asset in plan['assets']]}
    if hashlib.sha256(json.dumps(static, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest() != plan['manifest_sha256']:
        raise ValueError('J1 original recipe manifest does not bind asset/template bytes')
    states = [asset.get('state') for asset in plan['assets']]
    if any(state not in ('missing', 'ready', 'present_unverified', 'invalid') for state in states):
        raise ValueError('J1 recipe store state is unknown')
    store = 'warm' if all(state == 'ready' for state in states) else 'empty' if all(state == 'missing' for state in states) else 'partial'
    if plan.get('ready') is not (store == 'warm'):
        raise ValueError('J1 recipe ready flag differs from verified assets')
    return store


def comfy_recipe_response_rows(rows, root, stem, routes):
    """Bind each recipe observation to the original bounded HTTP response."""
    if not isinstance(rows, list) or len(rows) != len(routes) or [r.get('route') for r in rows] != routes:
        raise ValueError('J1 recipe observation sequence differs')
    previous = None
    for index, row in enumerate(rows):
        response = row['response']
        observed = datetime.fromisoformat(row['observed_at'])
        if (Path(response['path']) != Path(root)/('%s-%02d.response' % (stem, index))
                or type(row.get('status')) is not int or response['sha256'] != row.get('sha256')
                or observed.tzinfo is None or (previous is not None and observed < previous)):
            raise ValueError('J1 recipe original response binding missing')
        original = json.loads(checked_j1_comparison_file(response, Path(root), 16 * 1024**2, capture=True))
        if json.dumps(original, sort_keys=True) != json.dumps(row.get('answer'), sort_keys=True):
            raise ValueError('J1 recipe answer differs from its original response')
        previous = observed


def comfy_unchanged_jobs(before, after):
    if (before['status'] != 200 or after['status'] != 200 or before['answer'].get('outcome') != 'page' or
            before['answer'].get('complete') is not True or after['answer'].get('outcome') != 'page' or
            after['answer'].get('complete') is not True or
            not isinstance(before['answer'].get('operations'), list) or
            before['answer']['operations'] != after['answer'].get('operations') or
            not isinstance(before['answer'].get('rows'), list) or before['answer']['rows'] != after['answer'].get('rows')):
        raise ValueError('J1 refusal checks created/changed work or lack complete caller inventory')


def comfy_cold_refusal_observation(pin, root):
    """Replay an empty-store graph refusal independently of the warm scene."""
    stem = 'j1-comfy-cold-refusal'
    if pin['path'] != str(root/(stem + '.json')):
        raise ValueError('J1 cold refusal left its original scene')
    rows = json.loads(checked_j1_comparison_file(pin, root, 16 * 1024**2, capture=True))
    prefix = '/abstraction/workflows/flux2-klein-distilled'
    comfy_recipe_response_rows(rows, root, stem,
        ['/abstraction/jobs', prefix, prefix + '/graph', prefix, '/abstraction/jobs'])
    before, plan, graph, later, after = rows
    if (any(row.get('body') is not None for row in rows)
            or plan['status'] != 200 or later['status'] != 200 or plan['answer'] != later['answer']
            or comfy_verified_store(plan['answer']) != 'empty'
            or graph['status'] != 409 or graph.get('graph_sha256') is not None
            or graph['answer'] != {'detail': plan['answer'].get('detail') or 'Prepare and verify every model file first.'}):
        raise ValueError('J1 cold graph refusal is not bound to the empty recipe')
    comfy_unchanged_jobs(before, after)
    return {'manifest_sha256': plan['answer']['manifest_sha256'], 'variant': plan['answer']['variant'],
            'ended_at': after['observed_at'], 'files': [pin, *(row['response'] for row in rows)]}


def comfy_preparation_observations(rows, root):
    """Derive only observed store/refusal components from canonical HTTP data."""
    prefix = '/abstraction/workflows/flux2-klein-distilled'
    comfy_recipe_response_rows(rows, root, 'j1-comfy-preparation',
        ['/abstraction/jobs', prefix + '?verify=1', prefix + '/prepare', prefix + '/prepare',
         prefix + '/graph', prefix + '?verify=1', '/abstraction/jobs'])
    plan = rows[1]['answer']
    if (rows[1]['status'] != 200 or rows[5]['status'] != 200 or rows[5]['answer'] != plan or
            not re.fullmatch('[0-9a-f]{64}', str(plan.get('manifest_sha256'))) or
            not isinstance(plan.get('assets'), list) or not plan['assets'] or len(plan['assets']) > 64):
        raise ValueError('J1 verified recipe/store changed during refusal checks')
    store = comfy_verified_store(plan)
    stale = '0' * 64 if plan['manifest_sha256'] != '0' * 64 else '1' * 64
    for index, body, status in ((2, {'manifest_sha256': plan['manifest_sha256'], 'approve': False}, 400),
                                (3, {'manifest_sha256': stale, 'approve': True}, 409)):
        row = rows[index]
        if row['body'] != body or row['status'] != status or row['answer'].get('outcome') != 'refused':
            raise ValueError('J1 missing-consent or stale-manifest preparation did not refuse')
    comfy_unchanged_jobs(rows[0], rows[6])
    graph = rows[4]
    if store == 'warm':
        # The route's digest covers the served graph including its OA metadata;
        # the template digest covers the original graph before that annotation.
        plain = json.loads(json.dumps(graph['answer']))
        if not isinstance(plain, dict) or not isinstance(plain.get('extra'), dict):
            raise ValueError('J1 ready graph has no recipe annotation')
        annotation = plain.get('extra', {}).pop('oa_workflow', None)
        candidates = [plain]
        if plain.get('extra') == {}:
            candidates.append({key:value for key,value in plain.items() if key != 'extra'})
        hashes = {hashlib.sha256(json.dumps(value,sort_keys=True,separators=(',', ':'),ensure_ascii=False).encode()).hexdigest()
                  for value in candidates}
        if (graph['status'] != 200 or graph.get('graph_sha256') != graph['sha256'] or
                annotation != {'id': plan['id'], 'manifest_sha256': plan['manifest_sha256'], 'variant': plan.get('variant')} or
                plan.get('template', {}).get('graph_sha256') not in hashes):
            raise ValueError('J1 ready graph bytes differ from the pinned recipe')
    elif graph['status'] != 409:
        raise ValueError('J1 unprepared store exposed a runnable graph')
    if any(rows[index].get('body') is not None for index in (0, 1, 4, 5, 6)):
        raise ValueError('J1 observation read became a mutation')
    cases = ['missing-app-preparation-intent-refused', 'stale-preparation-plan-refused',
             'verified-' + store + '-store', 'ready-only-graph']
    if store != 'warm':
        cases.append('native-picture-missing-assets-graph-refused')
    return {'store': store, 'validated_cases': cases,
            'pending_cases': ['explicit-app-preparation-intent', 'setup-and-sensitive-first-use-asks', 'empty-store-preparation-completed',
                'canvas-replacement-consent', 'restart-cancel-failure-retry', 'step-comparison']}


def native_comfy_models_root(observed, owner, backend):
    """Bind the fixture's directory identity to the held native backend's argument."""
    local = PureWindowsPath(owner.get('local_app_data', ''))
    expected = str(local/'OpenAbstractions'/'ComfyUI'/'models')
    identity = observed.get('identity_before') if isinstance(observed, dict) else None
    if (not isinstance(observed, dict) or set(observed) != {'path', 'kind', 'identity_before', 'identity_after'}
            or observed['kind'] != 'directory'
            or owner.get('nonadmin') is not True or not local.is_absolute()
            or not re.fullmatch(r'[A-Za-z]:', local.drive) or '..' in local.parts
            or observed['path'] != expected or not isinstance(identity, str)
            or not 0 < len(identity) <= 256 or identity != observed['identity_after']):
        raise ValueError('native Comfy models-root differs from the disposable KnownFolder or file identity')

    # Parse the actual Windows command line. Quoted values may contain
    # spaces and flag-shaped text, so a substring match cannot admit a root.
    import ctypes
    from ctypes import wintypes
    shell = ctypes.WinDLL('shell32.dll', use_last_error=True, winmode=0x800)
    kernel = ctypes.WinDLL('kernel32.dll', use_last_error=True, winmode=0x800)
    split = shell.CommandLineToArgvW
    split.restype = ctypes.POINTER(wintypes.LPWSTR)
    split.argtypes = (wintypes.LPCWSTR, ctypes.POINTER(ctypes.c_int))
    free = kernel.LocalFree
    free.argtypes = (ctypes.c_void_p,)
    free.restype = ctypes.c_void_p
    command = backend['command']
    if len(command) > 32767 or '\x00' in command:
        raise ValueError('native Comfy models-root command line exceeds its bound')
    count = ctypes.c_int()
    vector = split(command, ctypes.byref(count))
    if not vector:
        raise ValueError('native Comfy models-root command line could not be read')
    try:
        argv = [vector[index] for index in range(count.value)]
    finally:
        free(ctypes.cast(vector, ctypes.c_void_p))
    positions = [index for index, argument in enumerate(argv) if argument == '--models-directory']
    if (len(positions) != 1 or argv[positions[0]+1:positions[0]+2] != [expected]
            or any(argument.split('=', 1)[0] in ('--base-directory', '--extra-model-paths-config')
                   or argument.startswith('--models-directory=') for argument in argv)):
        raise ValueError('native Comfy backend selected an alternate models-root')
    return {'path': expected, 'identity': identity}


def native_comfy_instance_observation(native, chain, pin, activation):
    """Join the existing fixture's held occurrences and native listener read."""
    path = checked(pin)
    if path.name != 'j1-comfy-instance.json' or path.stat().st_size > 64 * 1024:
        raise ValueError('native Comfy instance observation location/bound differs')
    value = json.loads(path.read_text(encoding='utf-8-sig'))
    rows = value.get('processes')
    if not isinstance(rows, list) or len(rows) != 2 or {row.get('role') for row in rows} != {'application', 'backend'}:
        raise ValueError('native Comfy held occurrence inventory differs')
    owner = chain['fixture_owner']
    pictures = PureWindowsPath(owner.get('pictures', ''))
    if (not pictures.is_absolute() or pictures.drive.startswith('\\\\') or
            any(part in ('.', '..') for part in pictures.parts) or value.get('pictures') != str(pictures)):
        raise ValueError('native Comfy Pictures KnownFolder differs from fixture owner')
    members = installed_payload_members(native, chain)['comfyui']
    roles = {'application': native['selected_app_readback']['comfyui']['native']['identity']['programs']['application'],
             'backend': 'oa-backend/standalone-env/python.exe'}
    started = datetime.fromisoformat(json.loads(checked(activation['process']).read_text(encoding='utf-8-sig'))['CreationUtc'])
    observed = datetime.fromisoformat(value['observed_at'])
    if started.tzinfo is None or observed.tzinfo is None or not 0 <= (observed-started).total_seconds() <= 120:
        raise ValueError('native Comfy instance observation is stale')
    by_role = {}
    for row in rows:
        member = members[roles[row['role']]]
        birth = datetime.fromisoformat(row['birth'])
        handle_birth = datetime.fromisoformat(row['handle_birth'])
        if (type(row.get('pid')) is not int or row['pid'] <= 0 or type(row.get('parent')) is not int or
                row.get('sid') != owner['sid'] or row.get('session') != owner['session'] or
                any(row.get(key) != member[key] for key in ('path', 'sha256', 'identity')) or
                birth.tzinfo is None or handle_birth.tzinfo is None or
                not started <= birth <= observed or not started <= handle_birth <= observed or
                not isinstance(row.get('command'), str) or not row['command']):
            raise ValueError('native Comfy held process/source/owner differs')
        by_role[row['role']] = row
    backend = by_role['backend']
    if (backend['parent'] != by_role['application']['pid'] or backend['pid'] == by_role['application']['pid'] or
            value.get('listener') != {'address': '127.0.0.1', 'port': 8188, 'pid': backend['pid']}):
        raise ValueError('native Comfy backend/listener occurrence differs')
    instance = {'files': [pin], 'base': 'http://127.0.0.1:8188', 'output_root': str(pictures/'OpenAbstractions/ComfyUI'),
                'application': by_role['application'], 'backend': backend}
    if 'models_root' in value:
        instance['models_root'] = native_comfy_models_root(value['models_root'], owner, backend)
    return instance


def native_phase_result(pin, root, native, phase):
    """Bind one fixed lifecycle phase to its original held parent invocation."""
    root = Path(root);path = checked(pin, root)
    if path != root/'adopter-journey-result.json':raise ValueError('native phase result location differs')
    request_path, process_path = root/'core-request.json', root/'adopter-process.json'
    if any(path.is_symlink() for path in (request_path, process_path)):
        raise ValueError('native phase parent files redirected')
    request = json.loads(request_path.read_text(encoding='utf-8-sig'))
    process = json.loads(process_path.read_text(encoding='utf-8-sig'))
    value = json.loads(path.read_text());raw = value.get('native_comfy_lifecycle', {})
    if (request.get('core') != native['core'] or request.get('native_comfy_phase') != phase or
            json.loads(checked(request['native_request']).read_text(encoding='utf-8-sig')) != native or
            value.get('core') != native['core'] or value.get('native_request') != request['native_request'] or
            value.get('request') != file(request_path) or value.get('result') != 'partial' or
            value.get('owned_children_exited') is not True or raw.get('phase') != phase or
            process.get('RequestedFileName') != native['core']['python']['path'] or
            process.get('RequestedArguments') != '-B "' + native['core']['helper']['path'] + '" --adopter-journey-request "' + str(request_path) + '"' or
            process.get('State') != 'completed' or process.get('Closure') != 'known' or process.get('ExitCode') != 0 or
            process.get('DirectExited') is not True or process.get('OutputClosed') is not True or
            process.get('TerminationAttempted') is not False or process.get('Failure') is not None):
        raise ValueError('native phase original request/source/held invocation differs')
    return value, [pin, file(request_path), file(process_path)]


def native_comfy_components(native, chain, observations, context):
    """Receive independently produced native components, with no J1 waiver."""
    payload = j1_native_payload_observation(native, chain, observations.get('j1_native_payload'))
    compiled = native['selected_app_readback']['comfyui']
    delivery = next(row for row in native['selected_app_chain']['apps'] if row['id'] == 'comfyui')['delivery']
    application = payload['members'][compiled['native']['identity']['programs']['application']]
    components = {'payload': {key: payload[key] for key in ('files', 'root', 'root_identity')},
        'member_count': len(payload['members']), 'validated_cases': ['native-original-payload-observed']}
    lifecycle = next(row for row in chain['apps'] if row['id'] == 'comfyui')
    if lifecycle.get('setup_result') is not None:
        setup_path = checked(lifecycle['setup_result'])
        directory = Path(observations['j1_native_payload']['request']['path']).parent
        if setup_path != directory/'comfy-setup/adopter-journey-result.json':
            raise ValueError('native Setup result left its canonical phase')
        setup, phase_files = native_phase_result(lifecycle['setup_result'], setup_path.parent, native, 'setup')
        raw = setup.get('native_comfy_lifecycle', {})
        if (setup.get('core') != native['core'] or setup.get('result') != 'partial' or
                setup.get('owned_children_exited') is not True or raw.get('phase') != 'setup'):
            raise ValueError('native Setup source/closure differs')
        native_comfy_setup_observation(raw['setup']['records'], delivery['binary'], setup_path.parent)
        j1_selection_observation(native, {'j1_selection': raw['sdk']},
            {key: context['opencode']['program'][key] for key in ('path', 'sha256', 'bytes')},
            context['runtime_program'], context['account'])
        commands = [row for row in setup.get('commands', []) if row.get('operation') == 'native-model-setup']
        if (len(commands) != 1 or commands[0].get('argv') != [context['panel']['path'], '--setup'] or
                commands[0].get('exited') is not True or commands[0].get('exit_code') != 0):
            raise ValueError('native model Setup did not complete through its installed presenter')
        checked(raw['model_setup'], setup_path.parent)
        questions = native_questions_observation(raw['questions']['records'], setup_path.parent)
        start, end = (datetime.fromisoformat(commands[0][key]) for key in ('started_at', 'ended_at'))
        matches = [row['record'] for row in questions['records'] if row['record'].get('Key') == 'setup.models' and
            row['record'].get('About') and row['record'].get('Option') == 'setup' and row['record'].get('Yes') is True and
            start <= datetime.fromisoformat(row['record']['Asked']) <= datetime.fromisoformat(row['record']['Answered']) <= end]
        components['setup'] = {'files': [*phase_files, raw['identity'], raw['model_setup'], raw['setup']['records'],
                                        *raw['sdk'].values(), *questions['files']],
                               'questions': [row['ID'] for row in matches]}
        if len(matches) == 1:
            components['validated_cases'].append('native-setup-person-question-observed')
    applications = observations.get('j1_application', {})
    if not isinstance(applications, dict) or not set(applications) <= {'observe', 'activate', 'graph'}:
        raise ValueError('native Comfy application observations differ')
    received = {}
    for operation, pins in applications.items():
        value = j1_application_observation(native, pins, context['runtime_program'], context['account'], operation)
        page = value['operations'][0]['output']
        registrations = page.get('registrations', [])
        if (page.get('outcome') != 'page' or len(registrations) != 1 or
                registrations[0].get('spec', {}).get('program') != application['path'] or
                registrations[0].get('identityEvidence') != 'program' or registrations[0].get('verification') != 'verified' or
                registrations[0].get('state') != 'enabled' or not registrations[0].get('revision')):
            raise ValueError('native Comfy registration is not the exact installed verified program')
        facets = registrations[0]['spec'].get('applications', [])
        if len(facets) != 1 or facets[0].get('name') != 'comfyui' or facets[0].get('manifest', {}).get('name') != 'comfyui':
            raise ValueError('native Comfy application facet differs from installed source')
        if operation == 'activate':
            answer = value['operations'][1]['output']
            if answer.get('outcome') != 'ready' or not isinstance(answer.get('instance'), str) or not 1 <= len(answer['instance']) <= 128:
                raise ValueError('native Comfy activation is not ready')
        received[operation] = value
        components.setdefault('applications', {})[operation] = {'files': pins}
        components['validated_cases'].append('native-' + operation + '-observed')
    if 'activate' in received and 'graph' in received:
        instance = received['graph']['operations'][1]['output']['answer']['applications'][0]['instances'][0]['instance']
        if received['activate']['operations'][1]['output']['instance'] != instance:
            raise ValueError('native Comfy graph belongs to another activated occurrence')
    if observations.get('j1_comfy_instance') is not None:
        if 'activate' not in received:
            raise ValueError('native Comfy instance has no current SDK activation')
        components['instance'] = native_comfy_instance_observation(native, chain,
            observations['j1_comfy_instance'], applications['activate'])
        components['validated_cases'].append('native-owned-backend-listener-observed')
    if observations.get('j1_comfy_frontend') is not None:
        frontend = j1_frontend_observation(native, chain, observations['j1_comfy_frontend'])
        components['frontend'] = frontend
        components['validated_cases'].extend(frontend['validated_cases'])
    selected = {'id': 'comfyui', 'target': 'windows-x64', 'mode': 'official-plus-extension',
        'package_sha256': native['adopter_package']['sha256'], 'native_archive_sha256': delivery['binary']['sha256'],
        'executable_sha256': application['sha256'], 'build_config_sha256': delivery['context']['source_policy']['sha256'],
        'runtime_sha256': context['runtime_program']['sha256'], 'sdk_inputs_sha256': native['j1_selection']['sdk']['manifest']['sha256'],
        'adopter_source_sha': compiled['native']['identity']['upstream_commit'],
        'extension_source_sha': compiled['native']['identity']['producer_source_commit'],
        'extension_sha256': delivery['context']['oa_integration']['sha256']}
    return selected, components


def adopter_execution_context(native_request, installed_chain, observations):
    """Reconstruct available installed observations beside immutable inputs.

    The lifecycle receiver authenticates these three inputs. This pure function
    grants no authority to serialized context and produces no facts selection.
    """
    if any(not isinstance(value, dict) for value in (native_request, installed_chain, observations)):
        raise ValueError('original request, installed journal and raw owner observations required')
    prepared = adopter_journey_inputs(native_request.get('core', {}))
    owner = observations.get('fixture_owner', {})
    if (owner != installed_chain.get('fixture_owner') or
            not re.fullmatch(r'S-1-[0-9-]+', str(owner.get('sid'))) or
            type(owner.get('session')) is not int or owner['session'] < 0 or
            owner.get('nonadmin') is not True):
        raise ValueError('actual fixture account/session/nonadmin observation required')
    local = PureWindowsPath(owner.get('local_app_data', ''))
    tools = PureWindowsPath(owner.get('tools', ''))
    private_sandbox = (native_request.get('kind') == 'sandbox_upgrade'
        and native_request.get('proof_scope') == 'PRIVATE/SOURCE-BOUND'
        and native_request.get('release_sdk_eligible') is False
        and re.fullmatch('[0-9a-f]{32}', str(owner.get('sandbox_guest_id'))))
    machine = PureWindowsPath(owner.get('program_files', ''))
    installed_tools = (machine / 'OpenAbstractions' / 'tools' if private_sandbox
                       else local / 'Programs' / 'OpenAbstractions' / 'tools')
    if not local.is_absolute() or (private_sandbox and not machine.is_absolute()) or tools != installed_tools:
        raise ValueError('actual fixture KnownFolder tools observation required')
    core = native_request['core']
    bindings = {}
    for role, name in (('runtime', 'openabstractions.exe'), ('panel', 'Abstraction Panel.exe')):
        binding = observations.get(role, {})
        expected = core.get(role + '_sha256')
        if (not re.fullmatch('[0-9a-f]{64}', str(expected)) or binding.get('sha256') != expected or
                type(binding.get('bytes')) is not int or binding['bytes'] <= 0 or
                PureWindowsPath(binding.get('path', '')) != tools / name):
            raise ValueError('installed ' + role + ' differs from original payload and KnownFolder')
        bindings[role] = dict(binding)
    members = installed_payload_members(native_request, installed_chain)
    roles = native_request.get('selected_app_readback', {}).get('opencode', {}).get('journey_payload_roles', {})

    def member(pin):
        if not isinstance(pin, dict):
            raise ValueError('source-owned installed OpenCode role required')
        row = members.get('opencode', {}).get(pin.get('id'))
        if row is None or any(row.get(key) != pin.get(key) for key in ('sha256', 'bytes')):
            raise ValueError('source-owned installed OpenCode role differs')
        return dict(row)

    plugins = roles.get('plugin_files', {})
    if not isinstance(plugins, dict) or not plugins:
        raise ValueError('full source-owned installed plugin membership required')
    opencode = {'program': member(roles.get('official_cli')),
                'plugin_files': {name: member(pin) for name, pin in plugins.items()},
                'config_tools': {name: member(pin) for name, pin in roles.get('config_tools', {}).items()},
                'sdk_native': {name: member(pin) for name, pin in roles.get('sdk_native', {}).items()},
                'registration_manifest': member(roles.get('registration_manifest')),
                'original_plugin': roles.get('original_plugin')}
    context = {**prepared, 'account': owner['sid'], 'fixture_owner': dict(owner),
            'runtime_program': bindings['runtime'], 'panel': bindings['panel'], 'opencode': opencode,
            'selections': [], 'missing_owner_inputs': ['selected-model-config-transaction',
                'official-comfy-instance-and-recipe', 'current-sdk-server-connection']}
    if private_sandbox:context.update(proof_scope='PRIVATE/SOURCE-BOUND',release_sdk_eligible=False)
    if private_sandbox:
        context['coding_selection'] = sandbox_operator_selection(native_request, observations,
            {key:opencode['program'][key] for key in ('path','sha256','bytes')},bindings['runtime'],bindings['panel'],owner['sid'])
        context['missing_owner_inputs'].remove('current-sdk-server-connection')
    elif observations.get('j1_selection') is not None:
        selection, missing = j1_selection_observation(native_request, observations,
                                                     {key: opencode['program'][key] for key in ('path', 'sha256', 'bytes')},
                                                     bindings['runtime'], owner['sid'])
        context['coding_selection'] = selection
        context['missing_owner_inputs'].remove('current-sdk-server-connection')
        context['missing_owner_inputs'].extend(missing)
        # Saved policy and current eligibility do not prove approved optimization,
        # installed config registration, preparation consent or actual admission.
    if core.get('pythonpath'):
        expected = Path(core['pythonpath']) / 'abstraction/facade/_codec.py'
        pins = [pin for pin in core.get('python_files', []) if Path(pin['path']) == expected]
        if len(pins) != 1:
            raise ValueError('original current caller codec missing')
        checked(pins[0], Path(core['directory']))
        context['caller_codec'] = pins[0]
    if any(row.get('variant') == 'oa-comfyui-native' for row in native_request['selected_app_chain']['apps']):
        remaining = list(context['missing_owner_inputs'])
        context = opencode_selected_context(native_request, context, observations)
        selected, components = native_comfy_components(native_request, installed_chain, observations, context)
        context['selections'].append(selected)
        context['comfy_components'] = components
        context['missing_owner_inputs'] = remaining + ['step-comparison']
    return context


def require_adopter_execution_context(context):
    """Keep unavailable source-owner observations explicit before any child."""
    missing = context.get('missing_owner_inputs')
    if missing:
        raise ValueError('J1 owner input missing: ' + ', '.join(missing))
    if not context.get('selections') or not context.get('coding_selection') or not context.get('comfy'):
        raise ValueError('J1 actual selected execution inputs are incomplete')
    return context


def opencode_adopter_context(native, chain, observations):
    """Reuse installed roles/current SDK, without asserting any Comfy/J1 input."""
    context = adopter_execution_context(native, chain, observations)
    if native.get('adopter_case') != 'opencode':
        raise ValueError('the existing native receiver must select the OpenCode case')
    return opencode_selected_context(native, context, observations)


def sandbox_operator_selection(native, observations, program, runtime, panel, account):
    """Receive the installed operator's real projection for the private smoke.

    This is neither a J1 SDK observation nor a grant. The Sandbox lifecycle
    authenticates the collector and its guest files before calling this join.
    """
    if (native.get('kind')!='sandbox_upgrade' or native.get('proof_scope')!='PRIVATE/SOURCE-BOUND'
            or native.get('release_sdk_eligible') is not False):
        raise ValueError('private operator selection belongs only to the Sandbox smoke')
    pins=observations.get('private_selection')
    if not isinstance(pins,dict) or set(pins)!={'request','before','offer','after','process'}:
        raise ValueError('private operator selection observations missing')
    values={key:json.loads(checked(pin).read_text(encoding='utf-8-sig')) for key,pin in pins.items()}
    expected={'account':account,'runtime':runtime,'program':program,'name':'oa-chat-default',
              'route':'/registry/selection','edit':'read'}
    if values['request']!=expected:
        raise ValueError('private operator selection target/account/runtime differs')
    process=values['process'];commands=process.get('commands')
    owner=native['fixture_owner']
    state=Path(owner['local_app_data'])/'openabstractions'/'runtime-v1'
    if owner.get('sid')!=account or not state.is_absolute():
        raise ValueError('private installed operator state owner differs')
    argv=[panel['path'],'-native=false','-open=false','-addr','127.0.0.1:0']
    if (process.get('account')!=account or process.get('runtime')!=runtime or process.get('panel')!=panel
            or process.get('runtime_state_dir')!=str(state)
            or process.get('closed') is not True or not isinstance(commands,list) or len(commands)!=1
            or commands[0].get('argv')!=argv or type(commands[0].get('pid')) is not int or commands[0]['pid']<=0
            or not commands[0].get('started_at') or commands[0].get('exited') is not True):
        raise ValueError('private installed operator image/target/closed process differs')
    before=json.loads(values['before'].get('read','null'));after=json.loads(values['after'].get('read','null'))
    raw=values['offer']
    if not isinstance(before,dict) or before!=after or before.get('outcome')!='found' or not before.get('revision') or raw.get('Outcome')!='offered':
        raise ValueError('private operator selection/offer is unavailable or changed')
    policy=before.get('policy',{});candidates=policy.get('candidates')
    if (policy.get('name')!='oa-chat-default' or not isinstance(candidates,list) or len(candidates)!=1 or not isinstance(candidates[0],dict)
            or candidates[0].get('placement')!='local' or candidates[0].get('provider') or candidates[0].get('credential')):
        raise ValueError('private tiny completion needs its exact local-only saved selection')
    offers=[{key.lower() if key!='ContextSource' else 'context_source':value for key,value in row.items()}
            for row in raw.get('Offers',[]) if isinstance(row,dict)]
    if not any(candidates[0].get('model') in [row.get('family'),*row.get('names',[])] and row.get('servers')
               and int(row.get('input',0))>0 and int(row.get('context',0))>=128 for row in offers):
        raise ValueError('private tiny selection has no actual eligible local offer')
    return {'policy':before,'offer':{'outcome':'offered','offers':offers},'target':program,
            'operator_files':pins,'operator_inputs_sha256':hashlib.sha256(json.dumps(pins,sort_keys=True,
                separators=(',',':')).encode()).hexdigest()}


def opencode_selected_context(native, context, observations):
    """Reuse the same original OpenCode producer/SDK selection in either case."""
    producer = native.get('opencode_producer', {})
    if (set(producer) != {'source_sha', 'payload_receipt'} or
            not re.fullmatch('[0-9a-f]{40}', str(producer.get('source_sha')))):
        raise ValueError('authenticated OpenCode producer source/receipt required')
    payload = json.loads(checked(producer['payload_receipt']).read_text(encoding='utf-8-sig'))
    roles = native['selected_app_readback']['opencode']['journey_payload_roles']
    if payload.get('plugin') != roles.get('original_plugin'):
        raise ValueError('original plugin archive differs from admitted installed roles')
    sdk = context.get('coding_selection')
    if not sdk or sdk['policy'].get('outcome') != 'found' or sdk['offer'].get('outcome') != 'offered':
        raise ValueError('OpenCode needs the current SDK selection and local coding offer')
    missing = [name for name in context['missing_owner_inputs'] if name not in
               ('selected-model-config-transaction', 'official-comfy-instance-and-recipe')]
    if missing:
        raise ValueError('OpenCode owner input missing: ' + ', '.join(missing))
    profile = Path(observations.get('profile', ''))
    if (not profile.is_absolute() or
            any(path.is_symlink() or path.is_junction() for path in (profile, *profile.parents))):
        raise ValueError('actual ordinary fixture KnownFolder profile required')
    app = next((row for row in native['selected_app_chain']['apps'] if row['id'] == 'opencode'), None)
    if not app:
        raise ValueError('original selected OpenCode package missing')
    selection = {'id': 'opencode', 'target': 'windows-x64', 'mode': 'official-plus-plugin',
        'package_sha256': file(checked(native['adopter_package']))['sha256'],
        'executable_sha256': context['opencode']['program']['sha256'],
        'build_config_sha256': producer['payload_receipt']['sha256'],
        'runtime_sha256': context['runtime_program']['sha256'],
        'adopter_source_sha': producer['source_sha'], 'plugin_source_sha': producer['source_sha'],
        'plugin_sha256': payload['plugin']['sha256']}
    if native.get('kind')=='sandbox_upgrade' and native.get('release_sdk_eligible') is False:
        selection.update(source_operator_inputs_sha256=sdk['operator_inputs_sha256'],proof_scope='PRIVATE/SOURCE-BOUND')
    else:
        selection['sdk_inputs_sha256']=sdk['sdk_manifest']['sha256']
    context.update(selections=[selection], missing_owner_inputs=[], profile=str(profile))
    return context


def opencode_adopter_observations(raw, context, *, installed=False):
    """Validate actual official outputs and receiving audit, collecting partial cases.

    The caller has authenticated producer/installed roles and current SDK. No
    requested prompt, tool name or installer success alone is an observation.
    """
    required = ('plugin-install', 'model-list', 'model-completion', 'tools', 'oa-integration')
    files = {name: checked(pin) for name, pin in raw.get('files', {}).items()}
    if set(files) != {'registration', 'models', 'events', 'export', 'audit'}:
        raise ValueError('complete canonical OpenCode observation files required')
    registration = json.loads(files['registration'].read_text(encoding='utf-8-sig'))
    opencode = context['opencode']
    # The lifecycle helper sits inside plugin/<version>/installer; configure is
    # at its root. Resolve the role named by the source, never a supplied label.
    plugin_root = str(Path(opencode['config_tools']['configure']['path']).parent)
    provider = registration.get('configured_provider', {})
    documents = registration.get('documents', [])
    names = {'opencode.jsonc': 'server', 'opencode.json': 'server', 'config.json': 'server',
             'tui.jsonc': 'tui', 'tui.json': 'tui'}
    if (registration.get('schema') != 1 or registration.get('plugin') != plugin_root or
            registration.get('directory') != str(Path(context['profile']) / '.config/opencode') or
            provider.get('scope') != 'local' or provider.get('guarantees') != ['abstraction.inference/local-only@1'] or
            provider.get('npm') != (Path(plugin_root) / 'index.js').as_uri() or
            not re.fullmatch('[0-9a-f]{64}', str(provider.get('sha256'))) or
            {row.get('kind') for row in registration.get('owned_plugins', [])} != {'server', 'tui'} or
            any(row.get('module') != plugin_root or not re.fullmatch('[0-9a-f]{64}', str(row.get('sha256')))
                for row in registration.get('owned_plugins', [])) or
            len(documents) != 5 or {Path(row.get('path', '')).name: row.get('kind') for row in documents} != names or
            not any(row.get('path') == provider.get('path') and row.get('kind') == 'server' and row.get('sha256')
                    for row in documents)):
        raise ValueError('source-owned committed plugin registration required')
    for document in documents:
        path = Path(document['path'])
        if path.parent != Path(context['profile']) / '.config/opencode':
            raise ValueError('installed configuration left its real fixture profile')
        if (document.get('sha256') is not None and (not re.fullmatch('[0-9a-f]{64}', str(document['sha256'])) or
                type(document.get('bytes')) is not int or document['bytes'] < 0)):
            raise ValueError('installed configuration descriptor malformed')
        if not installed:
            continue  # The normal lifecycle has already removed this installation.
        if document.get('sha256') is not None:
            checked({key: document[key] for key in ('path', 'sha256', 'bytes')})
        elif path.exists():
            raise ValueError('previously missing configuration appeared')
    if installed:
        checked({key: opencode['program'][key] for key in ('path', 'sha256', 'bytes')})
        for pin in opencode['plugin_files'].values():
            checked({key: pin[key] for key in ('path', 'sha256', 'bytes')})
    models = files['models'].read_text(encoding='utf-8').splitlines()
    model = raw.get('model')
    policy = context['coding_selection']['policy']['policy']
    allowed = {'openabstractions/' + row['model'] for row in policy['candidates']
               if row.get('placement') == 'local' and not row.get('provider') and not row.get('credential')}
    if model not in allowed or model not in models:
        raise ValueError('actual official model list lacks the selected local coding model')
    offers = [row for row in context['coding_selection']['offer']['offers']
              if model.removeprefix('openabstractions/') in [row['family'], *row['names']]]
    session = adopter_journey_session(files['events'].read_text(encoding='utf-8'))
    exported = json.loads(files['export'].read_text(encoding='utf-8'))
    if exported.get('info', {}).get('id') != session:
        raise ValueError('official export differs from the actual run session')
    tool_input = {'action': 'read', 'name': 'oa-chat-default'}
    tools, text, all_tools = [], [], []
    for message in exported.get('messages', []):
        info = message.get('info', {})
        if info.get('role') != 'assistant' or info.get('sessionID') != session:
            continue
        for part in message.get('parts', []):
            if part.get('sessionID') != session or part.get('messageID') != info.get('id'):
                continue
            if part.get('type') == 'text' and isinstance(part.get('text'), str) and part['text'].strip():
                text.append(part['text'])
            state = part.get('state', {})
            if part.get('type') == 'tool':
                all_tools.append(part)
            if (part.get('type') == 'tool' and part.get('tool') == 'openabstractions_selection_policy'
                    and state.get('status') == 'completed' and state.get('input') == tool_input):
                output = json.loads(state.get('output', 'null'))
                if (isinstance(output, dict) and output.get('kind') == 'stored_policy' and
                        output.get('action') == 'read' and output.get('name') == tool_input['name'] and
                        output.get('outcome') == 'found' and
                        output.get('revision') == context['coding_selection']['policy']['revision'] and
                        output.get('candidates') == policy['candidates']):
                    tools.append(part)
    audit = json.loads(files['audit'].read_text(encoding='utf-8'))
    if not isinstance(audit, list) or any(not isinstance(row, dict) for row in audit):
        raise ValueError('actual receiving audit must be a list')
    program = opencode['program']['path']
    received = [row for row in audit if row.get('Profile', '') in ('', 'chat') and row.get('Outcome') == 'completed'
                and row.get('Operation') and os.path.normcase(row.get('Program', '')) == os.path.normcase(program)
                and any(row.get('Model') in [offer['family'], *offer['names']] and row.get('Host') in offer['servers']
                        for offer in offers)
                and type(row.get('TokensOut')) is int and row['TokensOut'] >= 0]
    if len({row['Operation'] for row in received}) != len(received):
        raise ValueError('duplicate receiving completion operation')
    observations = [{'behavior': 'plugin-install', 'result': 'pass'},
                    {'behavior': 'model-list', 'result': 'pass'}]
    completed = text and raw.get('nonce', '') in '\n'.join(text) and received
    if completed:
        observations.append({'behavior': 'model-completion', 'result': 'pass',
                             'operation_ids': [row['Operation'] for row in received]})
    if len(tools) == 1 and len(all_tools) == 1:
        observations.append({'behavior': 'tools', 'result': 'pass', 'session_id': session})
    if completed and len(tools) == 1 and len(all_tools) == 1:
        observations.append({'behavior': 'oa-integration', 'result': 'pass', 'session_id': session,
                             'operation_ids': [row['Operation'] for row in received]})
    observed = {row['behavior'] for row in observations}
    return observations, [name for name in required if name not in observed]


def opencode_e1_operations(audit, program):
    if not isinstance(audit, list) or any(not isinstance(row, dict) for row in audit):
        raise ValueError('OpenCode E1 native audit is malformed')
    return [row for row in audit if row.get('Profile', '') in ('', 'chat') and row.get('Operation')
            and os.path.normcase(row.get('Program', '')) == os.path.normcase(program)]


def opencode_e1_local_operation(row, context, model):
    selected = model.removeprefix('openabstractions/')
    return (row.get('Outcome') == 'completed' and row.get('Route') == 'native'
            and not any(row.get(name) for name in ('Credential', 'Domain', 'Claim'))
            and any(selected in (offer['family'], *offer['names'])
                    and row.get('Model') in (offer['family'], *offer['names'])
                    and row.get('Host') in offer['servers']
                    for offer in context['coding_selection']['offer']['offers']))


def opencode_e1_reply(exported, session, row, command, loads, nonce=None, *, exact_nonce=False):
    replies = []
    for message in exported.get('messages', []):
        info = message.get('info', {})
        if info.get('role') != 'assistant' or info.get('sessionID') != session or not info.get('id'):
            continue
        for part in message.get('parts', []):
            if part.get('sessionID') != session or part.get('messageID') != info['id']:
                continue
            if part.get('type') == 'tool' and exact_nonce:
                return None
            if part.get('type') != 'text' or not isinstance(part.get('text'), str) or not part['text'].strip():
                continue
            metadata = part.get('metadata')
            if not isinstance(metadata, dict) or not isinstance(metadata.get('openabstractions'), dict):
                continue
            metadata = metadata['openabstractions']
            if metadata.get('operation') == row['Operation']:
                replies.append((part, metadata))
    if len(replies) != 1:
        return None
    [(part, metadata)] = replies
    if (nonce is not None and nonce not in part['text']
            or exact_nonce and part['text'].strip() != nonce):
        return None
    terminal = metadata.get('chat3Terminal')
    if not isinstance(terminal, dict) or not terminal.keys() <= {'load', 'loadMs', 'servedBy'}:
        return None
    raw_load_ms = terminal.get('loadMs')
    if (metadata.get('outcome') != 'completed' or metadata.get('model') != row['Model']
            or terminal.get('load') not in loads
            or terminal.get('servedBy') != {'place': 'local', 'server': row['Host']}
            or (raw_load_ms is not None and (not isinstance(raw_load_ms, str)
                or not re.fullmatch(r'0|[1-9][0-9]{0,18}', raw_load_ms)
                or int(raw_load_ms) > 9223372036854775807))):
        return None
    load_ms = int(raw_load_ms) if raw_load_ms is not None else None
    start, end = (datetime.fromisoformat(command[key]) for key in ('started_at', 'ended_at'))
    if any(at.tzinfo is None or at.utcoffset().total_seconds() != 0 for at in (start, end)) or start > end:
        raise ValueError('OpenCode E1 command interval is not ordered UTC')
    start_ms, end_ms = (round(at.timestamp() * 1000) for at in (start, end))
    clock = part.get('time') or {}
    received = (clock.get('start'), clock.get('end'))
    if (any(type(at) is not int for at in (*received, row.get('UnixMs')))
            or not start_ms <= received[0] <= received[1] <= end_ms
            or not start_ms <= row['UnixMs'] <= end_ms
            or (loads == {'cold'} and (not load_ms or received[1] - start_ms > 120000))):
        return None
    return {'operation_id': row['Operation'], 'server': row['Host'], 'load': terminal['load'],
            'load_ms': load_ms, 'reply_at_ms': received[1]}, start_ms, received[0]


def opencode_e1_command(value, name, pin, argv):
    rows = [row for row in value.get('commands', []) if row.get('operation') == name]
    if (len(rows) != 1 or rows[0].get('log') != pin or rows[0].get('exited') is not True
            or rows[0].get('exit_code') != 0 or rows[0].get('argv') != [str(arg) for arg in argv]):
        raise ValueError('OpenCode E1 official command or output differs: ' + name)
    return rows[0]


def opencode_e1_prompt(nonce):
    return 'Reply with exactly ' + nonce + '. Use no tools and change no files.'


def opencode_e1_probe(value, context, root, model, stage, probe):
    prefix = 'j1-e1-' + stage
    nonce, files = probe['nonce'], probe['files']
    if not re.fullmatch('[0-9a-f]{32}', nonce) or set(files) != {'run', 'export', 'audit'}:
        raise ValueError('OpenCode E1 needs one original nonce and three evidence pins')
    originals = {}
    for name, pin in files.items():
        if pin['path'] != str(root / (prefix + '-' + name + '.json')):
            raise ValueError('OpenCode E1 probe left its original scene')
        originals[name] = checked_j1_comparison_file(pin, root, 16 * 1024**2, capture=True).decode('utf-8')
    session = adopter_journey_session(originals['run'])
    exported = json.loads(originals['export'])
    if exported.get('info', {}).get('id') != session:
        raise ValueError('OpenCode E1 export differs from its owned session')
    program = context['opencode']['program']['path']
    command = opencode_e1_command(value, prefix + '-run', files['run'],
        adopter_journey_command(program, root/'opencode-project', model, 'j1-' + nonce, opencode_e1_prompt(nonce)))
    export = opencode_e1_command(value, prefix + '-export', files['export'], [program, 'export', session])
    if datetime.fromisoformat(export['started_at']) < datetime.fromisoformat(command['ended_at']):
        raise ValueError('OpenCode E1 export precedes its completed run')
    window = json.loads(originals['audit'])
    before, after, rows = window['before'], window['next'], window['entries']
    if (type(before) is not int or type(after) is not int or before < 0 or not isinstance(rows, list)
            or len(rows) > 1024 or after != before + len(rows)
            or any(not isinstance(row, dict) or type(row.get('Sequence')) is not int or row['Sequence'] != before + index
                   for index, row in enumerate(rows))):
        return None
    operations = opencode_e1_operations(rows, program)
    if len(operations) != 1 or not opencode_e1_local_operation(operations[0], context, model):
        return None
    received = opencode_e1_reply(exported, session, operations[0], command,
                                {'cold'} if stage == 'cold' else {'warm', 'loaded'}, nonce, exact_nonce=True)
    if received is None:
        return None
    reply, started, reply_started = received
    return {'reply': reply, 'started': started, 'reply_started': reply_started,
            'before': before, 'next': after, 'session': session, 'model': operations[0]['Model'],
            'run_started_at': command['started_at'], 'export_ended_at': export['ended_at']}


def opencode_cold_first_reply(value, context, root):
    """Receive E1 only from the official export and the matching native audit."""
    raw = value['adopter_journey']
    if 'opencode_e1' in value:
        probes = value['opencode_e1']
        if not isinstance(probes, list) or len(probes) > 2:
            raise ValueError('OpenCode E1 probe roster differs')
        received = [opencode_e1_probe(value, context, root, raw['model'], stage, probe)
                    for stage, probe in zip(('cold', 'warm'), probes)]
        if len(received) < 2:
            return None
        first, following = received
        if (first is None or following is None or first['next'] != following['before']
                or first['session'] == following['session'] or probes[0]['nonce'] == probes[1]['nonce']
                or first['reply']['operation_id'] == following['reply']['operation_id']
                or first['reply']['server'] != following['reply']['server'] or first['model'] != following['model']
                or datetime.fromisoformat(following['run_started_at']) < datetime.fromisoformat(first['export_ended_at'])):
            return None
        return {'first_reply_ms': first['reply']['reply_at_ms'] - first['started'],
                'operations': [first['reply'], following['reply']],
                'files': [pin for probe in probes for pin in probe['files'].values()]}

    files = raw['files']
    paths = {'export': 'opencode-export.json', 'events': 'opencode-run.json', 'audit': 'opencode-audit.json'}
    if any(Path(files[name]['path']) != root / filename for name, filename in paths.items()):
        return None

    def read(name):
        return checked_j1_comparison_file(files[name], root, 16 * 1024**2, capture=True).decode('utf-8')

    exported = json.loads(read('export'))
    session = adopter_journey_session(read('events'))
    if exported.get('info', {}).get('id') != session:
        raise ValueError('OpenCode E1 export differs from its owned session')
    nonce = raw.get('nonce', '')
    operations = opencode_e1_operations(json.loads(read('audit')), context['opencode']['program']['path'])
    if (not re.fullmatch('[0-9a-f]{32}', nonce) or len(operations) < 2
            or len({row['Operation'] for row in operations}) != len(operations)
            or any(type(row.get('Sequence')) is not int for row in operations)
            or any(a['Sequence'] >= b['Sequence'] for a, b in zip(operations, operations[1:]))):
        return None
    first, following = operations[:2]
    if (not all(opencode_e1_local_operation(row, context, raw['model']) for row in (first, following))
            or first['Host'] != following['Host']):
        return None
    commands = [row for row in value.get('commands', []) if row.get('operation') == 'opencode-run']
    if len(commands) != 1 or commands[0].get('log') != files['events']:
        raise ValueError('OpenCode E1 command and original event stream differ')
    cold = opencode_e1_reply(exported, session, first, commands[0], {'cold'}, nonce)
    warm = opencode_e1_reply(exported, session, following, commands[0], {'warm', 'loaded'})
    if cold is None or warm is None or warm[2] < cold[0]['reply_at_ms']:
        return None
    return {'first_reply_ms': cold[0]['reply_at_ms'] - cold[1], 'operations': [cold[0], warm[0]],
            'files': [files[name] for name in ('events', 'export', 'audit')]}


def opencode_adopter_receiving(value, core, context, scope, *, installed=False):
    """Normalize only the actual OpenCode observer after lifecycle validation."""
    if (value.get('core') != core or value.get('execution_context') != context or
            value.get('result') != 'partial' or value.get('owned_children_exited') is not True or
            context.get('scope') != scope):
        raise ValueError('OpenCode observer differs from the authenticated native lifecycle')
    root = checked(value['request']).parent
    request = json.loads(checked(value['request']).read_text(encoding='utf-8-sig'))
    if (request.get('core') != core or request.get('execution_context') != context or
            request.get('native_request') != value.get('native_request') or
            json.loads(checked(value['native_request']).read_text(encoding='utf-8-sig')).get('core') != core):
        raise ValueError('OpenCode original session context differs')
    raw = value.get('adopter_journey', {})
    for pin in raw.get('files', {}).values():
        checked(pin, root)
    program = context['opencode']['program']['path']
    nonce = raw.get('nonce', '')
    if not re.fullmatch('[0-9a-f]{32}', nonce):
        raise ValueError('one bounded owned OpenCode run nonce required')
    project = root / 'opencode-project'
    expected = {
        'opencode-models': [program, 'models', 'openabstractions'],
        'opencode-run': adopter_journey_command(program, project, raw.get('model'), 'j1-' + nonce,
            opencode_run_prompt(core,context,nonce)),
    }
    registration_request = root / 'opencode-registration-request.json'
    config_request = {'operation': 'observe', 'directory': str(Path(context['profile']) / '.config/opencode'),
        'state': str(Path(context['fixture_owner']['local_app_data']) / 'OpenAbstractionsApps/opencode-installer'),
        'plugin': str(Path(context['opencode']['config_tools']['configure']['path']).parent)}
    if json.loads(registration_request.read_text(encoding='utf-8')) != config_request:
        raise ValueError('source-owned registration observation request differs')
    registration = [row for row in value.get('commands', []) if row.get('operation') == 'opencode-registration']
    registration_argv = [program, 'run', '--no-env-file', '--no-install', '--no-addons', '--no-macros',
        '--config=' + str(Path(program).parent.parent / 'installer/empty-bunfig.toml'),
        context['opencode']['config_tools']['lifecycle']['path'], '--observe-request', str(registration_request)]
    if (len(registration) != 1 or registration[0].get('argv') != registration_argv or
            registration[0].get('exit_code') != 0 or registration[0].get('exited') is not True or
            registration[0].get('log') != raw['files']['registration']):
        raise ValueError('actual installed registration owner command differs')
    session = adopter_journey_session(checked(raw['files']['events'], root).read_text(encoding='utf-8'))
    expected['opencode-export'] = [program, 'export', session]
    for operation, argv in expected.items():
        rows = [row for row in value.get('commands', []) if row.get('operation') == operation]
        if (len(rows) != 1 or rows[0].get('argv') != [str(x) for x in argv] or
                rows[0].get('exit_code') != 0 or rows[0].get('exited') is not True or
                rows[0].get('log') != raw['files'][{'opencode-models': 'models',
                    'opencode-run': 'events', 'opencode-export': 'export'}[operation]]):
            raise ValueError('actual official OpenCode command/output differs: ' + operation)
    if private_upgrade_completion(core,context):
        pin=value.get('private_pending_permissions')
        permission= json.loads(checked(pin,root).read_text(encoding='utf-8'))
        rows=[row for row in value.get('commands',[]) if row.get('operation')=='tiny-pending-permissions']
        argv=[str(context['runtime_program']['path']),'questions','permissions','--json','--timeout','30s',
            '--endpoint',r'\\.\pipe\openabstractions-user-'+context['account']+'-runtime-v1',
            '--runtime-program',context['runtime_program']['path']]
        if (len(rows)!=1 or rows[0].get('argv')!=argv or rows[0].get('exit_code')!=0
                or rows[0].get('exited') is not True or rows[0].get('log')!=pin
                or permission.get('outcome')!='page' or permission.get('records')!=[]):
            raise ValueError('private tiny completion has an unavailable or pending typed permission')
    observations, missing = opencode_adopter_observations(raw, context,installed=installed)
    if core.get('opencode_messages') is True:
        declaration = opencode_messages_declaration(context, nonce)
        permission_pin = value['opencode_messages'].get('permission')
        permission = json.loads(checked(permission_pin, root).read_text(encoding='utf-8')) if permission_pin else None
        control = opencode_messages_control(declaration, json.loads(checked(raw['files']['export'], root).read_text(encoding='utf-8')),
            permission, installed=True)
        if value['opencode_messages'].get('declaration') != declaration or value['opencode_messages'].get('observation') != control:
            raise ValueError('OpenCode Messages receiving differs from original native observations')
        observations.extend({'behavior': name, 'result': 'pass', 'session_id': session} for name in ('tools', 'oa-integration'))
        missing = [name for name in missing if name not in ('tools', 'oa-integration')]
    receiver = opencode_adopter_proof(context, observations, missing)
    pin = value.get('receiver_evidence')
    if json.loads(checked(pin, root).read_text(encoding='utf-8')) != receiver:
        raise ValueError('OpenCode receiver proof differs from owning observations')
    receipts = [] if missing else [dict(receiver, id='opencode', target='windows-x64', receiver_evidence=pin)]
    return {'selections': context['selections'], 'adopter_receipts': receipts, 'journey_receipts': [],
            'receiver_files': [pin] if receipts else [], 'validated_cases': observations,
            'pending_cases': missing, 'inputs_sha256': context['inputs_sha256']}


def opencode_adopter_proof(context, observations, missing):
    return {'source_sha': context['source_sha'], 'scope_sha256': context['scope_sha256'],
        'inputs_sha256': context['inputs_sha256'], 'probe_source_sha256': context['probe_source_sha256'],
        'candidate_artifacts_sha256': context['candidate_artifacts_sha256'],
        'selection_sha256': hashlib.sha256(json.dumps(context['selections'][0], sort_keys=True,
            separators=(',', ':'), ensure_ascii=True).encode()).hexdigest(),
        'result': 'partial' if missing else 'pass', 'executed': True, 'exit_code': 0,
        'observations': observations}


def private_upgrade_completion(core,context):
    return (core.get('upgrade_local_completion_only') is True
        and core.get('proof_scope')=='PRIVATE/SOURCE-BOUND' and core.get('release_sdk_eligible') is False
        and context.get('proof_scope')=='PRIVATE/SOURCE-BOUND' and context.get('release_sdk_eligible') is False)


def opencode_run_prompt(core,context,nonce):
    if private_upgrade_completion(core,context):
        if core.get('opencode_messages') is True:
            raise ValueError('private tiny completion is separate from the full Messages journey')
        return 'Reply with exactly '+nonce+'. Use no tools and change no files.'
    return (opencode_messages_declaration(context,nonce)['prompt'] if core.get('opencode_messages') is True
        else opencode_adopter_prompt(nonce))


def opencode_adopter_prompt(nonce):
    return ('Use openabstractions_selection_policy exactly once with action read and name oa-chat-default. '
            'Report its current result and the text ' + nonce + '. Do not use any other tools or change any state.')


def opencode_messages_declaration(context, nonce):
    """Prepare the existing installed scene's Messages leg without executing it.

    The host lifecycle/kept-child source owner is exercised separately. A
    declaration or a constructed transcript supplies no person consent.
    """
    if not re.fullmatch('[0-9a-f]{32}', str(nonce)):
        raise ValueError('one bounded OpenCode journey nonce required')
    if not re.fullmatch(r'S-1-[0-9-]+', str(context.get('account'))):
        raise ValueError('installed disposable account binding required')
    runtime, opencode = context['runtime_program'], context['opencode']
    selection = context.get('coding_selection', {})
    if selection.get('policy', {}).get('outcome') != 'found' or selection.get('offer', {}).get('outcome') != 'offered':
        raise ValueError('admitted local model and current native SDK selection required')
    catalogues = [pin for name, pin in opencode['plugin_files'].items() if name.endswith('generated/agent-tools.json')]
    pending = ['actual-installed-tool-roundtrip', 'actual-native-person-popup-and-committed-grant']
    names, catalogue = [], None
    if len(catalogues) == 1:
        catalogue = {key: catalogues[0][key] for key in ('path', 'sha256', 'bytes')}
        tools = json.loads(checked(catalogue).read_text(encoding='utf-8'))['tools']
        names = [tool['name'] for tool in tools]
        required = {'oa_api_describe', 'oa_messages_inboxes_list', 'oa_messages_prepare',
                    'oa_messages_request_access', 'oa_messages_send', 'oa_messages_reply', 'oa_messages_wait'}
        if (len(names) != 29 or len(set(names)) != 29 or not required <= set(names)
                or any(not re.fullmatch('oa_[a-z0-9_]+', name) for name in names)):
            raise ValueError('original full public OA catalogue required')
    else:
        pending.insert(0, 'installed-791-full-agent-catalogue')
    return {'kind': 'opencode-native-messages', 'state': 'declared', 'executed': False,
        'host_version': '1.18.34', 'host_api': 'V1', 'nonce': nonce,
        'account': context['account'], 'runtime': dict(runtime), 'program': dict(opencode['program']), 'panel': dict(context['panel']),
        'local_selection_sha256': hashlib.sha256(json.dumps(selection, sort_keys=True, separators=(',', ':')).encode()).hexdigest(),
        'catalogue': catalogue, 'tool_names': names, 'pending': pending,
        'permission_authority': 'native-person-only', 'installed_acceptance': 'pending',
        'prompt': ('In this same OpenCode conversation, list agents through oa_messages_inboxes_list. '
            'Use oa_messages_prepare with receiver Claude, kind question and body ' + json.dumps('R6 ' + nonce) + '. '
            'If access is forbidden, request that direction exactly once with oa_messages_request_access and a fresh idempotency_key. '
            'Wait for the person in the native popup; never answer any permission question or edit rights. '
            'An Allow answer alone proves no committed grant. Retry prepare only after the enforcing capability permits it. '
            'Resolve the exact authorized Claude chat, retain send_arguments and send that exact object once. '
            'Wait on this same chat for the reply to its returned message_id. Never resend after an unknown outcome. '
            'Run oa_api_describe exactly once with contract abstraction.rights/authorization@1 and method Decide '
            'as the static OA schema read tool. Do not call oa_api_read or oa_models_list or request any other permission. '
            'Report the reply with ' + nonce + '.')}


def opencode_messages_tools(exported):
    session = exported.get('info', {}).get('id')
    if not isinstance(session, str) or not session:
        raise ValueError('fixed OpenCode session required')
    calls = []
    for message in exported.get('messages', []):
        details = message.get('info', {})
        if details.get('role') != 'assistant':
            continue
        for part in message.get('parts', []):
            if part.get('type') != 'tool':
                continue
            state = part.get('state', {})
            if (details.get('sessionID') != session or part.get('sessionID') != session
                    or part.get('messageID') != details.get('id') or state.get('status') != 'completed'):
                raise ValueError('completed tool observation from the same OpenCode chat required')
            output = json.loads(state.get('output', 'null'))
            if not isinstance(output, dict):
                raise ValueError('structured native tool output required')
            calls.append((part['tool'], state.get('input'), output))
    return session, calls


def opencode_messages_control(declaration, exported, permission=None, *, installed=False):
    """Join the existing official export and native permission owner observations.

    Its caller authenticates the installed lifecycle and native person owner.
    Source-only controls retain installed execution and person consent as pending.
    Host event/kept-child behavior remains with the existing 791 source controls.
    """
    session, calls = opencode_messages_tools(exported)
    if len(declaration['tool_names']) != 29:
        raise ValueError('original full catalogue required')
    expected = ['oa_messages_inboxes_list', 'oa_messages_prepare', 'oa_messages_request_access',
                'oa_messages_prepare', 'oa_messages_send']
    if ([name for name, _, _ in calls[:5]] != expected or not 7 <= len(calls) <= 14
            or calls[-1][0] != 'oa_api_describe' or any(name != 'oa_messages_wait' for name, _, _ in calls[5:-1])):
        raise ValueError('one bounded Messages permission/send/reply/read sequence required')
    listed, refused, access, prepared, sent = calls[:5]
    read = calls[-1]
    own = [inbox for inbox in listed[2].get('inboxes', []) if inbox.get('mine') is True]
    native = own[0].get('context') if len(own) == 1 else None
    if (not isinstance(native, str) or not native or len(native.encode()) > 64
            or own[0].get('program') != 'app:oa-opencode-integration'):
        raise ValueError('one own canonical native inbox observation required')
    first = {'receiver': 'Claude', 'kind': 'question', 'body': 'R6 ' + declaration['nonce']}
    if (listed[1] != {} or listed[2].get('outcome') != 'page' or not isinstance(listed[2].get('inboxes'), list)
            or refused[1] != first or refused[2].get('outcome') != 'forbidden'
            or refused[2].get('reason') != 'access:oa-claude-integration'
            or refused[2].get('send_arguments') or refused[2].get('choices')
            or set(access[1]) != {'receiver', 'idempotency_key'} or access[1].get('receiver') != 'Claude' or not access[1].get('idempotency_key')
            or access[2].get('outcome') != 'permitted' or not access[2].get('policy_revision')
            or prepared[1] != first or prepared[2].get('outcome') != 'prepared'
            or sent[1] != prepared[2].get('send_arguments') or sent[2].get('outcome') != 'sent'
            or not sent[2].get('message_id')
            or read[1] != {'contract': 'abstraction.rights/authorization@1', 'method': 'Decide'}
            or not any(method.get('contract') == read[1]['contract'] and method.get('method') == read[1]['method']
                and method.get('class') == 'read' and isinstance(method.get('arguments_schema'), dict)
                and isinstance(method.get('result_schema'), dict) for method in read[2].get('methods', []))):
        raise ValueError('friendly resolution, committed retry and exact prepared send required')
    arguments = sent[1]
    if (arguments.get('to') != 'app:oa-claude-integration' or not arguments.get('context')
            or arguments.get('body') != first['body'] or arguments.get('kind') != 'question'
            or not arguments.get('idempotency_key')):
        raise ValueError('exact native Claude destination required')
    delivered, previous = [], None
    for _, arguments_wait, page in calls[5:-1]:
        if (not isinstance(arguments_wait, dict) or set(arguments_wait)-{'after', 'wait_ms'}
                or type(arguments_wait.get('wait_ms')) is not int or not 0 <= arguments_wait['wait_ms'] <= 30000
                or (arguments_wait.get('after') if previous is not None else None) != previous
                or (previous is None and arguments_wait.get('after'))
                or page.get('outcome') != 'page' or not page.get('cursor')):
            raise ValueError('advance only the actually received native inbox cursor')
        previous = page['cursor']; delivered.extend(page.get('messages', []))
    replies = [message for message in delivered
               if message.get('header', {}).get('kind') == 'reply'
               and message['header'].get('reply_to') == sent[2]['message_id']
               and message['header'].get('from') == 'app:oa-claude-integration'
               and message['header'].get('from_context') == arguments['context']
               and message['header'].get('to') == 'app:oa-opencode-integration'
               and message['header'].get('context') == native and message['header'].get('refused') is False and message.get('body')]
    if len(replies) != 1:
        raise ValueError('reply to the exact question and original native chat required')
    question = access[2].get('answer', {}).get('id')
    if permission is not None:
        records = permission.get('records', [])
        question = records[0].get('ID') if len(records) == 1 else None
        rules = permission.get('after', {}).get('Rules', [])
        matching = lambda row: (row.get('Subject') == {'Account': declaration['account'], 'Program': permission.get('sender_program')}
            and row.get('Action') == 'abstraction.facade/application.message' and row.get('Resource') == 'app:oa-claude-integration')
        grants = [row for row in rules if matching(row)]
        if (not question or records[0].get('Option') != 'allow' or records[0].get('Key') != 'rights.permission@2'
            or records[0].get('About') != permission.get('sender_program')
            or permission.get('panel') != declaration['panel']['path']
            or permission.get('request_arguments') != access[1]
            or any(matching(row) and row.get('Permit') for row in permission.get('before', {}).get('Rules', []))
            or len(grants) != 1 or grants[0].get('Permit') is not True
            or grants[0].get('SetBy') != {'Account': declaration['account'], 'Program': permission.get('panel')}
            or not grants[0].get('SetAt') or permission.get('after', {}).get('Complete') is not True
            or permission['after'].get('Revision') != access[2]['policy_revision']):
            raise ValueError('optional native person observation differs from committed direction')
    return {'result': 'observed' if installed else 'source-control-pass', 'executed': installed, 'session_id': session, 'native_context': native,
        'message_id': sent[2]['message_id'], 'checks': ['same-session-native-inbox', 'full-public-catalogue',
            'messages-agent-discovery', 'forbidden-before-access', 'single-explicit-native-access', 'committed-grant-retry',
            'exact-native-chat-send-reply', 'oa-read-tool'],
        'question_id': question, 'permission_result': access[2],
        'question_ids_available': question is not None,
        'installed_acceptance': 'observed' if installed else 'pending',
        'human_consent': 'native-owner-observed' if installed else 'pending',
        'pending': [] if installed else list(declaration['pending'])}


def native_panel_observation(identity, panel, owned_pid, account, codec_pin=None):
    """Decode diagnostic callerView data, separate from SDK/held OS authority."""
    view = identity.get('runtime', {})
    record = view.get('record')
    if (view.get('absent') is not False or view.get('error') or not isinstance(record, str) or len(record.encode()) > 65536):
        raise ValueError('native Panel caller record unavailable')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate native Panel caller field')
            result[key] = value
        return result
    if codec_pin is not None:
        path = checked(codec_pin)
        raw = path.read_bytes()
        if len(raw) > 2 * 1024**2 or len(raw) != codec_pin['bytes'] or hashlib.sha256(raw).hexdigest() != codec_pin['sha256']:
            raise ValueError('admitted caller codec bytes changed')
        name = '_oa_native_caller_codec_' + codec_pin['sha256']
        codec = sys.modules.get(name)
        if codec is None:
            codec = types.ModuleType(name); codec.__file__ = str(path)
            sys.modules[name] = codec
            try:
                exec(compile(raw, str(path), 'exec'), codec.__dict__)
            except BaseException:
                sys.modules.pop(name, None)
                raise
        try:
            record = codec.to_json(codec.from_json(codec.CallerObservation, record.encode('utf-8'))).decode('utf-8')
        except Exception as error:
            raise ValueError('native caller record codec refused: ' + str(error)) from error
    actual = json.loads(record, object_pairs_hook=unique)
    if set(actual) != {'outcome', 'mechanism', 'account', 'program', 'pid', 'attributes', 'platform', 'transport', 'bindable', 'stronger'}:
        raise ValueError('native Panel caller record shape differs')
    if (actual.get('outcome') != 'observed' or actual.get('mechanism') != 'identity/windows' or
            actual.get('platform') != 'windows' or actual.get('transport') != 'npipe' or actual.get('bindable') is not True or
            actual.get('account') != account or not re.fullmatch(r'S-1-[0-9-]+', str(account)) or
            type(owned_pid) is not int or owned_pid <= 0 or type(actual.get('pid')) is not int or actual['pid'] != owned_pid or
            checked(panel) != Path(actual.get('program', '')).resolve()):
        raise ValueError('native Panel caller differs from held program/process/account')
    attributes = actual.get('attributes')
    if (not isinstance(attributes, list) or len(attributes) != 5 or
            any(not isinstance(row, dict) for row in attributes) or
            {row.get('attribute') for row in attributes} != {'user', 'process', 'path', 'package', 'code'}):
        raise ValueError('native Panel caller attributes differ')
    by_name = {row['attribute']: row for row in attributes}
    for name, ceiling in (('user', 'kernel'), ('process', 'kernel'), ('path', 'bound'), ('package', 'signed'), ('code', 'bound')):
        row = by_name[name]
        if row.get('ceiling') != ceiling or (name in ('user', 'process', 'path') and row.get('proof') != ceiling):
            raise ValueError('native Panel caller proof/ceiling differs')
    return actual


def adopter_journey_panel_identity(identity, prepared, panel, owned_pid):
    """Bind ObserveCaller's echo to our Panel, retaining server selection apart."""
    actual = native_panel_observation(identity, panel, owned_pid, prepared['account'], prepared.get('caller_codec'))
    expected = prepared.get('panel_actor', {})
    fields = ('outcome', 'mechanism', 'account', 'program', 'attributes')
    if (not expected or any(actual.get(key) != expected.get(key) for key in fields)
            or actual.get('absent') or actual.get('error') or actual.get('account') != prepared['account']
            or not isinstance(owned_pid, int) or owned_pid <= 0 or actual.get('pid') != owned_pid
            or not actual.get('mechanism') or not actual.get('attributes')
            or not Path(actual.get('program', '')).is_absolute()):
        raise ValueError('native caller echo differs from the owned Panel program/process/proof')
    if checked(panel) != Path(actual['program']).resolve():
        raise ValueError('native caller echo names another Panel executable')
    runtime = prepared.get('runtime_program', {})
    selection = identity.get('selection', {})
    if (checked(runtime) != Path(selection.get('program', '')).resolve()
            or selection.get('account') != prepared['account']
            or runtime.get('sha256') != prepared['selections'][0]['runtime_sha256']):
        raise ValueError('selected-server metadata differs from expected runtime bytes')
    # Selection metadata does not establish a live server process or its build.
    return actual


def native_questions_observation(pin, root, *, interval=None, authenticated=None):
    """Replay the native operator's original bounded, complete account pages."""
    root = Path(root)
    path = Path(pin['path'])
    if path.name not in ('j1-setup-questions.json', 'j1-command-questions.json', 'j1-render-questions.json'):
        raise ValueError('native Asks transcript location/bound differs')
    if authenticated is None:
        path = checked(pin, root)
        if path.stat().st_size > 16 * 1024**2:
            raise ValueError('native Asks transcript location/bound differs')
        rows = json.loads(path.read_text())
    else:
        rows = authenticated
    if not isinstance(rows, list) or not 1 <= len(rows) <= 64:
        raise ValueError('native Asks page inventory incomplete')
    cursor, previous, records, files, ids, cursors = '', None, [], [pin], set(), set()
    for index, row in enumerate(rows):
        response_pin = row['response']
        response = Path(response_pin['path'])
        if response != root/(path.stem + '-%02d.response' % index):
            raise ValueError('native Asks original response left its scene')
        if authenticated is None:
            checked(response_pin, root)
            if response.stat().st_size > 1024**2:
                raise ValueError('native Asks original response exceeds its byte bound')
            if json.loads(response.read_bytes()) != row['answer']:
                raise ValueError('native Asks original response/page/order differs')
        at = datetime.fromisoformat(row['observed_at'])
        answer = row['answer']
        if (not isinstance(answer, dict) or at.tzinfo is None or
                (interval is not None and not interval[0] <= at <= interval[1]) or
                (previous is not None and at < previous) or row['route'] != '/questions?cursor=' + urllib.parse.quote(cursor, safe='')):
            raise ValueError('native Asks original response/page/order differs')
        if interval is not None and answer.get('Outcome') != 'page':
            raise J1AnswerAttributionUnavailable('J1 native question attribution is unavailable')
        if (answer.get('Outcome') != 'page' or not isinstance(answer.get('Records'), list) or len(answer['Records']) > 64 or
                answer.get('Complete') is not (index == len(rows)-1)):
            raise ValueError('native Asks original response/page/order differs')
        previous = at
        for record in answer['Records']:
            identifier = record.get('ID')
            if not isinstance(identifier, str) or not identifier or identifier in ids:
                raise ValueError('native Asks occurrence absent or repeated')
            ids.add(identifier); records.append({'record': record, 'asker': answer.get('askers', {}).get(identifier)})
        next_cursor = answer.get('Next')
        if (index == len(rows)-1 and next_cursor not in (None, '')) or (index < len(rows)-1 and (
                not isinstance(next_cursor, str) or not next_cursor or len(next_cursor.encode()) > 256 or next_cursor in cursors)):
            raise ValueError('native Asks continuation differs')
        if next_cursor:
            cursors.add(next_cursor)
        cursor = next_cursor; files.append(row['response'])
    return {'records': records, 'files': files, 'observed_at': previous}


def comfy_picture_jobs(answer, assets, *, include_cache=False):
    """Join selected visible delivery rows to the service's actual operations."""
    rows, operations = answer.get('rows'), answer.get('operations')
    if (answer.get('outcome') != 'page' or answer.get('complete') is not True
            or not isinstance(rows, list) or not isinstance(operations, list)
            or max(len(rows), len(operations)) > 1024):
        raise ValueError('native recovery retained-work page incomplete')
    pins = {asset['url']: asset for asset in assets}
    selected = {}
    for row in rows:
        if row.get('kind') != 'file' or row.get('url') not in pins:
            continue
        identity = row.get('identity')
        cache = identity is None and row.get('source') == 'reused' and row.get('present') is True
        if identity is None and not (include_cache and cache):
            continue
        identifier, destination = row['id'], row['destination']
        name = Path(destination)
        if (identifier != 'f' + hashlib.sha256(destination.encode()).hexdigest()[:16]
                or identifier in selected or not name.is_absolute() or '..' in name.parts
                or name.name != pins[row['url']]['filename']):
            raise ValueError('native picture delivery destination differs')
        if cache:
            selected[identifier] = (row, None)
            continue
        if (not isinstance(identity, dict)
                or not all(isinstance(identity.get(key), str) and identity[key] for key in ('key', 'history_epoch'))
                or type(identity.get('attempt')) is not int or identity['attempt'] < 0):
            raise ValueError('native picture delivery identity differs')
        matches = [operation for operation in operations
                   if (operation.get('receipt') or {}).get('identity') == identity]
        if len(matches) > 1 or (matches and (
                not row.get('operation_id') or not matches[0]['receipt'].get('logical_owner')
                or matches[0]['receipt']['operation_id'] != row['operation_id']
                or (row.get('source') != 'reused' and (row.get('state'), row.get('failure'))
                    != (matches[0].get('state'), matches[0].get('failure'))))):
            raise ValueError('native picture delivery differs from its service operation')
        selected[identifier] = (row, matches[0] if matches else None)
    return selected


def comfy_recovery_observations(pin, root, before, plan):
    """Replay bounded read-only Jobs and verified store observations."""
    root = Path(root)
    limit = 16 * 1024**2
    if Path(pin['path']) != root/'j1-comfy-recovery.json':
        raise ValueError('native recovery transcript location differs')
    rows = json.loads(checked_j1_comparison_file(pin, root, limit, capture=True))
    if not isinstance(rows, list) or not 1 <= len(rows) <= 128 or rows[0].get('route') != '/abstraction/jobs':
        raise ValueError('native recovery transcript incomplete')
    initial = comfy_picture_jobs(before, plan['assets'])
    known = initial
    files, previous, total, stores, observed, retries = [pin], None, 0, [], {}, set()
    fresh, retired, pages, store_pages = {}, {}, {}, {}
    retired_visible = True
    cases = ['native-retained-selected-work-after-restart-observed'] if initial else []
    verify = '/abstraction/workflows/flux2-klein-distilled?verify=1'
    for index, row in enumerate(rows):
        response = row['response']
        total += response['bytes']
        at = datetime.fromisoformat(row['observed_at'])
        if (Path(response['path']) != root/('j1-comfy-recovery-%02d.response' % index)
                or total > limit or row['status'] != 200 or row.get('body') is not None
                or at.tzinfo is None or (previous is not None and at < previous)):
            raise ValueError('native recovery original response/order differs')
        answer = json.loads(checked_j1_comparison_file(response, root, limit, capture=True))
        if json.dumps(answer, sort_keys=True) != json.dumps(row['answer'], sort_keys=True):
            raise ValueError('native recovery original response differs from retained answer')
        previous = at
        files.append(response)
        if row['route'] == verify:
            if rows[index-1]['route'] != '/abstraction/jobs':
                raise ValueError('native verified store read lacks its Jobs inventory')
            if (answer.get('manifest_sha256'), answer.get('variant')) != (plan['manifest_sha256'], plan['variant']):
                raise ValueError('native recovery changed the pinned recipe')
            store = comfy_verified_store(answer)
            if not stores or stores[-1] != store:
                stores.append(store)
                store_pages[store] = index
            continue
        if row['route'] != '/abstraction/jobs':
            raise ValueError('native recovery permits only the held Jobs and recipe reads')
        current = comfy_picture_jobs(answer, plan['assets'])
        if not known.keys() <= current.keys():
            raise ValueError('native recovery lost retained work')
        for identifier, (job, operation) in current.items():
            previous_job, previous_operation = known.get(identifier, (job, None))
            identity, original = job['identity'], previous_job['identity']
            if any(job[key] != previous_job[key] for key in ('destination', 'url')):
                raise ValueError('native recovery rebound retained work')
            attempt, prior_attempt = identity['attempt'], original['attempt']
            if identity['key'] != original['key']:
                prior_id = previous_job['operation_id']
                if (index == 0 or prior_attempt != 0 or attempt != 0 or operation is None or previous_operation is None
                        or ('native-picture-transfer-cancelled-observed', identifier, prior_id) not in observed
                        or previous_operation.get('state') != 'cancelled' or job['operation_id'] == prior_id
                        or identity['history_epoch'] != original['history_epoch']):
                    raise ValueError('native fresh preparation has no observed cancelled predecessor')
                retired[prior_id] = previous_operation
                fresh[(identifier, identity['key'])] = prior_id
            elif identity['history_epoch'] != original['history_epoch']:
                raise ValueError('native recovery rebound retained work')
            elif attempt != prior_attempt:
                if (index == 0 or attempt != prior_attempt + 1 or previous_operation is None
                        or previous_operation.get('state') != 'failed' or not job['operation_id']
                        or job['operation_id'] == previous_job['operation_id']):
                    raise ValueError('native recovery has no eligible preceding retry attempt')
                retries.add((identifier, identity['key'], attempt))
            elif previous_job.get('operation_id') and job['operation_id'] != previous_job['operation_id']:
                raise ValueError('native recovery replaced its retained operation')
            if operation is None:
                continue
            receipt = operation['receipt']
            state = operation.get('state')
            if previous_operation is not None:
                prior_state = previous_operation.get('state')
                if (receipt['logical_owner'] != previous_operation['receipt']['logical_owner']
                        or (identity['key'] == original['key'] and attempt == prior_attempt
                            and prior_state in ('failed', 'cancelled', 'complete')
                            and state != prior_state)):
                    raise ValueError('native recovery changed a retained owner or terminal attempt')
            case, detail = None, {}
            if state == 'cancelled' and previous_operation is not None and previous_operation.get('state') == 'running':
                progress = previous_operation.get('progress') or {}
                if (type(progress.get('done')) is int and type(progress.get('total')) is int
                        and 0 < progress['done'] < progress['total']):
                    case = 'native-picture-transfer-cancelled-observed'
            elif state == 'failed':
                failure = operation.get('failure') or {}
                if (failure.get('cause') == 'transport' and failure.get('classification') == 'retryable'
                        and isinstance(failure.get('message'), str) and failure['message']):
                    case, detail = 'native-picture-transport-failure-observed', {'failure': failure}
            elif state == 'complete' and job.get('source') == 'fetched':
                # These are service terminals. Neither proves the selected
                # destination bytes merely because the row reports presence.
                if attempt == 0 and (identifier, identity['key']) in fresh:
                    case = 'native-picture-fresh-delivery-native-terminal-observed'
                    detail['cancelled_operation_id'] = fresh[(identifier, identity['key'])]
                elif (identifier, identity['key'], attempt) in retries:
                    case = 'native-picture-retained-retry-native-terminal-observed'
            if case:
                token = (case, identifier, receipt['operation_id'])
                pages.setdefault(token, index)
                observed.setdefault(token, dict(behavior=case, id=identifier, identity=identity,
                    operation_id=receipt['operation_id'], destination=job['destination'], **detail))
        for old in retired.values():
            receipt = old['receipt']
            original = receipt['identity']
            matches = [snapshot for snapshot in answer['operations']
                       if (snapshot.get('receipt') or {}).get('logical_owner') == receipt['logical_owner']
                       and (snapshot['receipt'].get('identity') or {}).get('key') == original['key']
                       and (snapshot['receipt'].get('identity') or {}).get('history_epoch') == original['history_epoch']]
            if not matches:
                retired_visible = False
                continue
            if (len(matches) != 1 or matches[0].get('state') != 'cancelled'
                    or matches[0]['receipt']['identity'] != original
                    or matches[0]['receipt']['operation_id'] != receipt['operation_id']):
                raise ValueError('native fresh preparation changed or retried the cancelled operation')
            if not any(row.get('kind') == 'operation' and row.get('state') == 'cancelled'
                       and row.get('identity') == original and row.get('operation_id') == receipt['operation_id']
                       for row in answer['rows']):
                retired_visible = False
        known = current
    cases.extend(dict.fromkeys(row['behavior'] for row in observed.values()))
    if stores == ['empty', 'partial', 'warm']:
        cases.append('native-picture-verified-empty-partial-warm-observed')
        names = ('native-picture-transfer-cancelled-observed', 'native-picture-fresh-delivery-native-terminal-observed',
                 'native-picture-transport-failure-observed', 'native-picture-retained-retry-native-terminal-observed')
        groups = [[token for token in observed if token[0] == name] for name in names]
        if retired_visible and all(len(group) == 1 for group in groups):
            cancel, new, failed, retry = (group[0] for group in groups)
            before, after = observed[failed], observed[retry]
            if (len({token[2] for token in (cancel, new, failed, retry)}) == 4
                    and cancel[1] == new[1] != failed[1] == retry[1]
                    and observed[new]['cancelled_operation_id'] == cancel[2]
                    and before['identity']['attempt'] == 0 and after['identity']['attempt'] == 1
                    and all(before['identity'][key] == after['identity'][key] for key in ('key', 'history_epoch'))
                    and store_pages['empty'] < pages[cancel] < store_pages['partial'] < pages[new]
                    < pages[failed] < pages[retry] < store_pages['warm']):
                cases.append('native-picture-ordered-delivery-terminals-observed')
    return {'files': files, 'validated_cases': cases, 'selected': list(initial), 'stores': stores,
            'job_observations': list(observed.values())}


def native_comfy_recovery_observation(value, context, native, chain):
    lifecycle = next(row for row in chain['apps'] if row['id'] == 'comfyui')
    root = Path(value['request']['path']).resolve().parent
    pin = lifecycle['recovery_result'];path = checked(pin, root)
    if path != root/'comfy-recovery/adopter-journey-result.json':
        raise ValueError('native recovery result left its canonical parent')
    phase, phase_files = native_phase_result(pin, path.parent, native, 'recovery')
    raw = phase.get('native_comfy_lifecycle', {})
    request = path.parent/'core-request.json';process = path.parent/'adopter-process.json'
    observed_process = json.loads(process.read_text(encoding='utf-8-sig'))
    if (phase.get('core') != native['core'] or phase.get('native_request') != value['native_request'] or
            phase.get('request') != file(request) or phase.get('result') != 'partial' or
            phase.get('owned_children_exited') is not True or raw.get('phase') != 'recovery' or
            raw.get('original') != file(root/'adopter-journey-result.json') or
            observed_process.get('State') != 'completed' or observed_process.get('Closure') != 'known' or
            observed_process.get('ExitCode') != 0 or observed_process.get('DirectExited') is not True or
            observed_process.get('OutputClosed') is not True or observed_process.get('TerminationAttempted') is not False):
        raise ValueError('native recovery source/parent/closure differs')
    activation = j1_application_observation(native, raw['activation'], context['runtime_program'], context['account'], 'activate')
    if activation['operations'][1]['output'].get('outcome') != 'ready':
        raise ValueError('native recovery activation is not ready')
    instance = native_comfy_instance_observation(native, chain, raw['instance'], raw['activation'])
    registrations = activation['operations'][0]['output'].get('registrations', [])
    if (len(registrations) != 1 or registrations[0].get('spec', {}).get('program') != instance['application']['path'] or
            registrations[0].get('verification') != 'verified' or registrations[0].get('state') != 'enabled'):
        raise ValueError('native recovery registration differs from the held installed application')
    original = context['comfy_components']['instance']
    if (instance['base'] != original['base'] or instance['output_root'] != original['output_root'] or
            any(instance[role]['pid'] == original[role]['pid'] and instance[role]['birth'] == original[role]['birth']
                for role in ('application', 'backend'))):
        raise ValueError('native recovery did not replace both original occurrences')
    before_pin = value['comfy_preparation']['records']
    if Path(before_pin['path']) != root/'j1-comfy-preparation.json':
        raise ValueError('native recovery preparation transcript left its original scene')
    before_rows = json.loads(checked_j1_comparison_file(before_pin, root, 16 * 1024**2, capture=True))
    comfy_preparation_observations(before_rows, root)
    received = comfy_recovery_observations(raw['recovery']['records'], path.parent, before_rows[6]['answer'], before_rows[1]['answer'])
    if any(raw['recovery'].get(key) != received[key] for key in ('selected', 'stores', 'job_observations')):
        raise ValueError('native recovery supplied labels differ from original work')
    received['validated_cases'].insert(0, 'native-source-owned-app-and-backend-restart-observed')
    received['files'].extend([*phase_files, raw['original'], raw['instance'], *raw['activation'].values()])
    return received


def native_comfy_image_observation(value, context, terminal, *, warm_plan):
    """Replay adopted image delivery and the parent's terminal GPU interval."""
    root = Path(value['request']['path']).resolve().parent
    raw = value['comfy_image']
    instance = context['comfy_components']['instance']
    files = {name: checked(pin, root) for name, pin in raw['files'].items()}
    if set(files) != {'hosts', 'events', 'export', 'history_before', 'history_after', 'audit', 'saved_image', 'project_image'}:
        raise ValueError('J1 native image originals incomplete')
    request = raw['request']
    model = warm_plan.get('id') if warm_plan else None
    if not isinstance(model, str) or not model or request.get('model') != model:
        raise ValueError('J1 image model differs from the checked warm recipe')
    prefix = 'A blue square. Evidence nonce '
    nonce = request.get('prompt', '').removeprefix(prefix)
    if (not re.fullmatch('[0-9a-f]{32}', nonce) or request != {
            'model': model, 'prompt': prefix + nonce, 'size': '512x512', 'save_to': 'j1-image.png'}):
        raise ValueError('J1 image request differs from the fixed adopted scene')
    session = adopter_journey_session(files['events'].read_text(encoding='utf-8'))
    program = context['opencode']['program']['path']
    project = root/'opencode-project'
    expected = {
        'j1-opencode-run': adopter_journey_command(program, project, value['adopter_journey']['model'],
            'j1-' + nonce, adopter_journey_image_instruction(request)),
        'j1-opencode-export': [program, 'export', session],
    }
    commands = {}
    for operation, argv in expected.items():
        rows = [row for row in value.get('commands', []) if row.get('operation') == operation]
        role = 'events' if operation == 'j1-opencode-run' else 'export'
        if (len(rows) != 1 or rows[0].get('argv') != argv or rows[0].get('exited') is not True or
                rows[0].get('exit_code') != 0 or rows[0].get('log') != raw['files'][role]):
            raise ValueError('actual installed image command/output differs: ' + operation)
        commands[operation] = rows[0]
    if files['project_image'] != project/request['save_to']:
        raise ValueError('J1 image output left the exact official project')
    hosts = json.loads(files['hosts'].read_text())
    matches = [row for row in hosts.get('Hosts', []) if row.get('Entry', {}).get('Kind') == 'comfyui' and
               row['Entry'].get('Base', '').rstrip('/') == instance['base'] and row.get('Up') is True]
    if hosts.get('Outcome') != 'page' or len(matches) != 1 or matches[0]['Entry'].get('Name') != raw['host']:
        raise ValueError('J1 image host differs from the held native listener')
    def unique_history_object(items):
        obj = dict(items)
        if len(obj) != len(items):
            raise ValueError('J1 stock Comfy history contains a repeated key')
        return obj
    before, after = (json.loads(files[name].read_text(), object_pairs_hook=unique_history_object)
                     for name in ('history_before', 'history_after'))
    if not isinstance(before, dict) or not isinstance(after, dict):
        raise ValueError('J1 stock Comfy history must contain prompt records')
    if len(set(after) - set(before)) != 1:
        raise ValueError('J1 GPU interval requires exactly one new stock prompt')
    result = adopter_journey_image_correlation(json.loads(files['export'].read_text()),
        session, raw['request'], before, after,
        json.loads(files['audit'].read_text()), context['opencode']['program']['path'], context['account'],
        raw['host'], raw['files']['saved_image'], stock_assets=warm_plan['assets'])
    if result != raw.get('correlation') or files['project_image'].read_bytes() != files['saved_image'].read_bytes():
        raise ValueError('J1 image originals differ from actual attachment/output correlation')
    if not isinstance(terminal, dict) or set(terminal) != {'before', 'after'}:
        raise ValueError('J1 held-parent terminal GPU observations missing')
    samples = {}
    for name, pin in terminal.items():
        path = checked(pin, root)
        if path != root/('j1-gpu-' + name + '.json') or path.stat().st_size > 64 * 1024:
            raise ValueError('J1 GPU observation left its canonical native parent')
        samples[name] = json.loads(path.read_text(encoding='utf-8-sig'))
    actor = samples['before']
    backend = instance['backend']
    if (actor.get('pid') != backend['pid'] or
            datetime.fromisoformat(actor['creation_utc']) != datetime.fromisoformat(backend['handle_birth']) or
            any(actor.get('image', {}).get(key) != backend[key] for key in ('path', 'identity', 'sha256'))):
        raise ValueError('J1 GPU actor differs from held original backend')
    execution = adopter_gpu_execution(samples['before'], samples['after'],
        {key: actor[key] for key in ('pid', 'creation_utc', 'image')})
    command = commands['j1-opencode-run']
    if (not datetime.fromisoformat(execution['started_at']) <= datetime.fromisoformat(command['started_at']) <
            datetime.fromisoformat(command['ended_at']) <= datetime.fromisoformat(execution['ended_at'])):
        raise ValueError('J1 GPU parent interval does not enclose the actual single image command')
    return {'correlation': result, 'gpu': execution, 'render_interval': (command['started_at'], command['ended_at']),
            'files': [*raw['files'].values(), *terminal.values()]}


J1_COLD_FIRST_REPLY = 'Exact installed official OpenCode plus OA plugin uses a fast local coding model through OA.'
J1_IMAGE_OUTCOMES = (
    'The existing OpenCode plugin requests the supported real ComfyUI GPU workflow through its OA extension.',
    'Record prompt_id, GPU execution, saved output and returned visible OpenCode attachment bytes/hash.',
)
J1_STEP_COMPARISON = 'Count steps against the same app without OA and the retained MuseTree comparison; OA adds no step.'
J1_CANVAS_CONFIRMATION = 'comfy-canvas-confirmation'


def checked_j1_comparison_file(pin, root, limit, *, capture=False):
    """Check the same ordinary name and one held stream through bounded hashing."""
    if not isinstance(pin, dict) or type(pin.get('bytes')) is not int or not 0 < pin['bytes'] <= limit:
        raise ValueError('J1 comparison input is outside its byte bound')
    path = checked_core_path(pin, root)
    if str(path.resolve()) != pin['path'] or ':' in path.name or path.name.endswith(('.', ' ')):
        raise ValueError('J1 comparison input is not its canonical ordinary path')
    parent = root.lstat()
    def check_parent():
        named = path.parent.lstat()
        if (not stat.S_ISDIR(parent.st_mode) or not stat.S_ISDIR(named.st_mode)
                or any(getattr(info, 'st_file_attributes', 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT for info in (parent, named))
                or not os.path.samestat(parent, root.lstat()) or not os.path.samestat(parent, named)):
            raise ValueError('J1 comparison input has a different physical parent')
    check_parent()
    expected = pin['bytes']
    remaining = expected + 1
    digest = hashlib.sha256()
    raw = bytearray() if capture else None
    with path.open('rb') as stream:
        original = os.fstat(stream.fileno())
        def check_name(current):
            check_parent()
            named = path.stat()
            if (path.is_symlink() or str(path.resolve()) != pin['path'] or not os.path.samestat(original, current)
                    or not os.path.samestat(current, named) or
                    any(not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                        or getattr(info, 'st_file_attributes', 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT
                        or (info.st_size, info.st_mtime_ns) != (expected, original.st_mtime_ns)
                        for info in (original, current, named))):
                raise ValueError('J1 comparison input size or held identity differs from its pin')
        check_name(original)
        while remaining and (chunk := stream.read(min(remaining, 1024 * 1024))):
            remaining -= len(chunk)
            digest.update(chunk)
            if capture:
                raw.extend(chunk)
        check_name(os.fstat(stream.fileno()))
    if remaining != 1 or digest.hexdigest() != pin['sha256']:
        raise ValueError('J1 comparison input changed during bounded read')
    return raw


class J1AnswerAttributionUnavailable(ValueError):
    """The genuine same-account observations cannot establish this run's floor."""


def native_j1_asks_originals(pin, root):
    """Authenticate every submitted original before interpreting attribution."""
    stem = Path(pin['path']).stem
    pages = json.loads(checked_j1_comparison_file(pin, root, 16 * 1024**2, capture=True))
    if not isinstance(pages, list) or not 1 <= len(pages) <= 64:
        raise ValueError('J1 native Asks original pages incomplete')
    for index, page in enumerate(pages):
        if not isinstance(page, dict) or set(page) != {'route', 'observed_at', 'answer', 'response'}:
            raise ValueError('J1 native Asks original page record differs')
        response = page['response']
        if Path(response.get('path', '')) != root/(stem + '-%02d.response' % index):
            raise ValueError('J1 native Asks original response left its scene')
        raw = json.loads(checked_j1_comparison_file(response, root, 1024**2, capture=True))
        if not isinstance(raw, dict) or raw != page['answer']:
            raise ValueError('J1 native Asks original response differs from its retained page')
        pages[index] = dict(page, answer=raw)
    return pages


def native_j1_operator_answers(evidence, value, prepared, image, ready_at):
    """Join complete typed Activity attribution to this same-account scene."""
    root = Path(value['request']['path']).resolve().parent
    if not isinstance(evidence, dict) or set(evidence) != {'identity', 'questions', 'answers'}:
        raise ValueError('J1 native answer audit input differs')
    start, render_start, end = (datetime.fromisoformat(text) for text in (ready_at, *image['render_interval']))
    if not datetime.fromisoformat(image['gpu']['started_at']) <= start <= render_start < end:
        raise ValueError('J1 answer interval differs from the checked warm render')
    panels = [row for row in value['commands'] if row.get('operation') == 'panel-readback']
    panel = prepared['panel']
    if (len(panels) != 1 or panels[0].get('argv') != [panel['path'], '-native=false', '-open=false', '-addr', '127.0.0.1:0']
            or panels[0].get('exited') is not True):
        raise ValueError('J1 answer audit requires the owned Panel occurrence')
    panel_start, panel_end = (datetime.fromisoformat(panels[0][key]) for key in ('started_at', 'ended_at'))
    if not panel_start <= start < end <= panel_end:
        raise ValueError('J1 answer audit left its owned Panel interval')
    if {name: Path(pin.get('path', '')) for name, pin in evidence.items()} != {
            'identity': root/'j1-render-answer-identity.json', 'questions': root/'j1-render-questions.json',
            'answers': root/'j1-render-answers.json'}:
        raise ValueError('J1 answer audit left its canonical scene files')
    identity = json.loads(checked_j1_comparison_file(evidence['identity'], root, 1024**2, capture=True))
    native_panel_observation(identity, panel, panels[0]['pid'], prepared['account'], prepared.get('caller_codec'))
    selection = identity.get('selection', {})
    if selection.get('account') != prepared['account'] or selection.get('program') != prepared['runtime_program']['path']:
        raise ValueError('J1 answer audit Panel selected another account or runtime')

    pin = evidence['answers']
    question_pages = native_j1_asks_originals(evidence['questions'], root)
    pages = native_j1_asks_originals(pin, root)
    questions = native_questions_observation(evidence['questions'], root, interval=(end, panel_end),
                                             authenticated=question_pages)
    epoch = datetime(1970, 1, 1, tzinfo=timezone.utc)
    def millis(at):
        return (at - epoch) // timedelta(milliseconds=1)
    start_ms, end_ms = millis(start), millis(end)
    previous_at, previous_ms, coverage, cursor = questions['observed_at'], None, None, ''
    typed_seen, joined = False, {}
    cursors, ids = set(), set()
    files = [evidence['identity'], *questions['files'], pin]
    known = {PureWindowsPath(prepared['opencode']['program']['path'])}
    known.update(PureWindowsPath(prepared['comfy_components']['instance'][role]['path']) for role in ('application', 'backend'))
    records = {row['record']['ID']: row for row in questions['records']}
    for index, page in enumerate(pages):
        response = page['response']
        original = page['answer']
        at = datetime.fromisoformat(page['observed_at'])
        if (page['route'] != '/activity/answers?cursor=' + urllib.parse.quote(cursor, safe='')
                or at.tzinfo is None or not previous_at <= at <= panel_end):
            raise ValueError('J1 original answer audit page/order differs')
        if original.get('outcome') != 'page':
            raise J1AnswerAttributionUnavailable('J1 native answer attribution is unavailable')
        if (not isinstance(original.get('events'), list)
                or len(original['events']) > 64 or original.get('complete') is not (index == len(pages)-1)):
            raise ValueError('J1 original answer audit page/order differs')
        previous_at = at
        since = original.get('completeSince', 0)
        if type(since) is not int or since < 0:
            raise ValueError('J1 native answer audit coverage field differs')
        if since >= start_ms or (coverage is not None and since != coverage):
            raise J1AnswerAttributionUnavailable('J1 native answer audit lacks stable coverage before ready start')
        coverage = since
        for event in original['events']:
            if not isinstance(event, dict):
                raise ValueError('J1 native answer audit event differs')
            answer_ms = event.get('at')
            if type(answer_ms) is not int or not 0 < answer_ms <= millis(at) or (previous_ms is not None and answer_ms > previous_ms):
                raise ValueError('J1 native answer audit time/order differs')
            previous_ms = answer_ms
            identifier, actor = event.get('questionId'), event.get('answeredBy')
            typed = isinstance(identifier, str) and bool(identifier) and actor in ('operator', 'responder')
            if typed:
                if identifier in ids:
                    raise J1AnswerAttributionUnavailable('J1 native answer audit repeats an occurrence')
                typed_seen = True
                ids.add(identifier)
            if not start_ms <= answer_ms <= end_ms:
                continue
            if not typed or identifier not in records:
                raise J1AnswerAttributionUnavailable('J1 answer audit has missing or ambiguous typed question attribution')
            row = records[identifier]
            record = row['record']
            answered = datetime.fromisoformat(record['Answered'])
            asked = datetime.fromisoformat(record['Asked'])
            if (answered.tzinfo is None or asked.tzinfo is None or not record.get('Option')
                    or millis(answered) != answer_ms or asked > answered):
                raise ValueError('J1 answer audit timestamp differs from its native occurrence')
            if not start <= answered <= end:
                continue
            asker = (row.get('asker') or {}).get('program')
            if asked < start or not isinstance(asker, str) or PureWindowsPath(asker) not in known:
                raise J1AnswerAttributionUnavailable('J1 answer audit has a stale or foreign native occurrence')
            joined[identifier] = actor
        cursor = original.get('next', '')
        if (not isinstance(cursor, str) or len(cursor.encode()) > 256 or cursor in cursors
                or bool(cursor) is not (index < len(pages)-1)):
            raise ValueError('J1 native answer audit continuation differs')
        cursors.add(cursor)
        files.append(response)
    for identifier, row in records.items():
        answered = row['record'].get('Answered')
        if answered:
            at = datetime.fromisoformat(answered)
            if at.tzinfo is None:
                raise ValueError('J1 native question answer timestamp lacks an offset')
            if start <= at <= end and identifier not in joined:
                raise J1AnswerAttributionUnavailable('J1 same-run native question has no exact typed answer')
    if not typed_seen:
        return None, files
    return tuple(identifier for identifier, actor in joined.items() if actor == 'operator'), files


def native_j1_owner_relay(pin, value, *, operator_answer_ids):
    """Validate an installed-owner relay after replay of its original image scene."""
    if operator_answer_ids is not None and (not isinstance(operator_answer_ids, tuple) or len(operator_answer_ids) > 512
            or any(not isinstance(answer, str) or not answer for answer in operator_answer_ids)
            or len(set(operator_answer_ids)) != len(operator_answer_ids)):
        raise ValueError('J1 typed same-run operator answer identities differ')
    relay_path = Path(pin.get('path', '')) if isinstance(pin, dict) else Path()
    if relay_path.name != 'j1-owner-relay.json' or not relay_path.is_absolute():
        raise ValueError('J1 owner relay left its installed acceptance input')
    raw = checked_j1_comparison_file(pin, relay_path.parent, 64 * 1024, capture=True)
    def unique(items):
        result = dict(items)
        if len(result) != len(items):
            raise ValueError('duplicate J1 owner relay field')
        return result
    relay = json.loads(raw, object_pairs_hook=unique)
    request = value['comfy_image']['request']
    routes = {'oa', 'same_app', 'musetree'}
    if (not isinstance(relay, dict) or set(relay) - {'canvas'} != {'channel', 'words', 'build_sha', 'app_versions',
            'counts', 'prompt', 'target', 'recording'}
            or relay['channel'] != 'chat-relay' or not isinstance(relay['words'], str) or not relay['words'].strip()
            or not re.fullmatch('[0-9a-f]{40}', str(relay['build_sha'])) or relay['build_sha'] != value['core']['source_sha']
            or relay['prompt'] != request['prompt'] or relay['target'] != request['size']):
        raise ValueError('J1 owner relay differs from the actual build, prompt or target')
    versions, counts = relay['app_versions'], relay['counts']
    if (not isinstance(versions, dict) or set(versions) != routes
            or any(not isinstance(version, str) or not version.strip() for version in versions.values())
            or not isinstance(counts, dict) or set(counts) != routes
            or any(type(count) is not int or not 0 < count <= 512 for count in counts.values())):
        raise ValueError('J1 owner relay needs all three app versions and positive bounded counts')
    steps, canvas_provenance = None, None
    if operator_answer_ids is not None:
        if counts['oa'] < len(operator_answer_ids) or counts['oa'] > min(counts['same_app'], counts['musetree']):
            raise ValueError('J1 OA count is below the observed operator answers or exceeds a baseline')
        steps = {'baseline': 'owner-attested', 'channel': 'chat-relay', 'oa_steps': counts['oa'],
                 'same_app_steps': counts['same_app'], 'musetree_steps': counts['musetree']}
    if 'canvas' in relay:
        canvas = relay['canvas']
        if (not isinstance(canvas, dict) or set(canvas) != {'words', 'dialog', 'accept', 'decline'}
                or not isinstance(canvas['words'], str) or not canvas['words'].strip() or canvas['dialog'] != 'comfyui'):
            raise ValueError('J1 canvas relay needs the owner words and ComfyUI confirmation')
        for decision, after in (('accept', 'prepared-workflow'), ('decline', 'unchanged')):
            case = canvas[decision]
            if (not isinstance(case, dict) or set(case) != {'before', 'after', 'oa_ask', 'prompt_queued'}
                    or case['before'] != 'unsaved-canvas' or case['after'] != after
                    or case['oa_ask'] is not False or case['prompt_queued'] is not False):
                raise ValueError('J1 canvas relay lacks the exact owner-observed ' + decision + ' case')
        canvas_provenance = {'source': 'owner-attested', 'channel': 'chat-relay'}

    recording = relay['recording']
    recording_path = Path(recording.get('path', '')) if isinstance(recording, dict) else Path()
    if recording_path == relay_path:
        raise ValueError('J1 walkthrough recording left its installed acceptance owner')
    checked_j1_comparison_file(recording, relay_path.parent, 2 * 1024**3)
    return steps, canvas_provenance, [pin, recording]


def validate_adopter_journey_result(value, core, selected_context, scope, terminal=None):
    """Receive this owner's real partial execution, preserving every missing case.

    selected_context is supplied in process by the existing installed receiver
    after authenticated producer/installed-file validation. Serialized input is
    data. This slice never turns a happy image correlation into aggregate PASS.
    """
    prepared = selected_context
    if prepared.get('comfy_components'):
        # The existing OpenCode observer remains independently receivable in a
        # mixed chain. Native component observations do not stand in for its
        # official command/plugin execution or a complete Comfy/J1 receipt.
        normalized = opencode_adopter_receiving(value, core, prepared, scope)
        normalized['validated_cases'].extend({'behavior': name, 'result': 'pass', 'component': True}
            for name in prepared['comfy_components']['validated_cases'])
        if prepared['comfy_components'].get('setup'):
            normalized['receiver_files'].extend(prepared['comfy_components']['setup']['files'])
        warm_plan, warm_ready_at = None, None
        cold = None
        if value.get('comfy_cold_refusal') is not None:
            if 'instance' not in prepared['comfy_components']:
                raise ValueError('J1 cold refusal has no held native backend/listener')
            cold = comfy_cold_refusal_observation(value['comfy_cold_refusal'], Path(value['request']['path']).resolve().parent)
            normalized['receiver_files'].extend(cold['files'])
            normalized['validated_cases'].append({'behavior': 'native-picture-missing-assets-graph-refused',
                                                 'result': 'pass', 'component': True})
        if value.get('comfy_preparation') is not None:
            if 'instance' not in prepared['comfy_components']:
                raise ValueError('Comfy recipe observation has no held native backend/listener')
            root = Path(value['request']['path']).resolve().parent
            pin = value['comfy_preparation']['records']
            path = checked(pin, root)
            if path != root/'j1-comfy-preparation.json' or path.stat().st_size > 16 * 1024**2:
                raise ValueError('Comfy recipe transcript left the actual scene')
            rows = json.loads(path.read_text(encoding='utf-8'))
            observed = comfy_preparation_observations(rows, root)
            if cold is not None:
                if ((cold['manifest_sha256'], cold['variant']) !=
                        (rows[1]['answer']['manifest_sha256'], rows[1]['answer']['variant'])
                        or datetime.fromisoformat(cold['ended_at']) > datetime.fromisoformat(rows[0]['observed_at'])):
                    raise ValueError('J1 cold refusal differs from or follows the ordinary recipe scene')
                if 'native-picture-missing-assets-graph-refused' in observed['validated_cases']:
                    observed['validated_cases'].remove('native-picture-missing-assets-graph-refused')
            if observed['store'] == 'warm':
                warm_plan = rows[1]['answer']
                warm_ready_at = rows[5]['observed_at']
            for name in observed['validated_cases']:
                case = {'behavior': name, 'result': 'pass', 'component': True}
                if name == 'ready-only-graph' and warm_plan is not None:
                    case['annotated_ready_graph_sha256'] = rows[4]['response']['sha256']
                normalized['validated_cases'].append(case)
            normalized['receiver_files'].extend([pin, *(row['response'] for row in rows)])
        verified_j1 = set()
        if 'opencode_e1' in value or any(row['behavior'] == 'model-completion' for row in normalized['validated_cases']):
            root = Path(value['request']['path']).resolve().parent
            cold_reply = opencode_cold_first_reply(value, prepared, root)
            if cold_reply is not None:
                normalized['receiver_files'].extend(cold_reply.pop('files'))
                normalized['validated_cases'].append({'behavior': J1_COLD_FIRST_REPLY, 'result': 'pass',
                    'component': True, 'cold_first_reply': cold_reply})
                verified_j1.add(J1_COLD_FIRST_REPLY)
        if value.get('comfy_image') is not None:
            terminal = terminal or {}
            gpu = {name: row for name, row in terminal.items() if name != 'step_comparison'}
            image = native_comfy_image_observation(value, prepared, gpu, warm_plan=warm_plan)
            normalized['receiver_files'].extend(image['files'])
            correlation = image['correlation']
            normalized['validated_cases'].extend([
                {'behavior': 'native-stock-image-visible-in-opencode', 'result': 'pass', 'component': True,
                 'prompt_id': correlation['prompt_id'], 'attachment_sha256': correlation['attachment_sha256'],
                 'saved_image_sha256': correlation['saved_image']['sha256']},
                {'behavior': 'owned-backend-gpu-compute-observed', 'result': 'pass', 'component': True,
                 'prompt_id': correlation['prompt_id'], 'gpu': image['gpu']},
            ])
            normalized['validated_cases'].extend({'behavior': behavior, 'result': 'pass', 'component': True}
                for behavior in J1_IMAGE_OUTCOMES)
            verified_j1.update(J1_IMAGE_OUTCOMES)
            relay, audit = terminal.get('step_comparison'), value.get('j1_answer_audit')
            operator_ids = None
            if audit is not None:
                try:
                    operator_ids, files = native_j1_operator_answers(audit, value, prepared, image, warm_ready_at)
                except J1AnswerAttributionUnavailable:
                    pass
                else:
                    normalized['receiver_files'].extend(files)
            if relay is not None:
                steps, canvas, files = native_j1_owner_relay(relay, value, operator_answer_ids=operator_ids)
                normalized['receiver_files'].extend(files)
                if steps is not None:
                    normalized['validated_cases'].append({'behavior': J1_STEP_COMPARISON, 'result': 'pass',
                        'component': True, 'step_comparison': steps})
                    verified_j1.add(J1_STEP_COMPARISON)
                if canvas is not None:
                    normalized['validated_cases'].append({'behavior': J1_CANVAS_CONFIRMATION, 'result': 'pass',
                        'component': True, 'canvas_confirmation': canvas})
        if value.get('comfy_questions') is not None:
            questions = native_questions_observation(value['comfy_questions']['records'],
                Path(value['request']['path']).resolve().parent)
            normalized['receiver_files'].extend(questions['files'])
            # The native command owner admits this catalogue key only for its
            # bound one-call receipt and dispatches after the person's Once.
            graph = prepared['comfy_components'].get('applications', {}).get('graph')
            if graph is not None:
                process = json.loads(checked(graph['files']['process']).read_text(encoding='utf-8-sig'))
                start, end = (datetime.fromisoformat(process[key]) for key in ('CreationUtc', 'FinishedUtc'))
                matches = [row['record'] for row in questions['records'] if
                    row['record'].get('Key') == 'application.command.once' and row['record'].get('Option') == 'once' and
                    row['record'].get('Yes') is True and (row.get('asker') or {}).get('program') ==
                    process['RequestedFileName'] and
                    start <= datetime.fromisoformat(row['record']['Asked']) <= datetime.fromisoformat(row['record']['Answered']) <= end]
                if len(matches) == 2:
                    normalized['validated_cases'].append({'behavior': 'native-person-once-records-observed-during-graph-edit',
                        'result': 'pass', 'component': True, 'questions': [row['ID'] for row in matches]})
        observed = {'opencode'} if normalized['adopter_receipts'] else set()
        normalized['pending_cases'] = [{'kind': 'adopters', 'id': row['id'], 'target': target, 'behavior': behavior}
            for row in scope['adopters'] if row['id'] not in observed for target in row['targets'] for behavior in row['behavior']]
        normalized['pending_cases'] += [{'kind': 'journeys', 'id': row['id'], 'target': target, 'outcome': outcome}
            for row in scope['journeys'] if row['id'] == 'J-1' for target in row['targets'] for outcome in row['outcomes']
            if outcome not in verified_j1]
        normalized['pending_provenance'] = 'The installed journey still requires every pending observation.'
        return normalized
    require_adopter_execution_context(prepared)
    identity = ('source_sha', 'version', 'inputs_sha256', 'probe_source_sha256', 'candidate_artifacts_sha256', 'scope_sha256', 'account')
    if (not isinstance(selected_context, dict) or any(prepared.get(k) != selected_context.get(k) for k in identity)
            or prepared.get('scope') != scope or prepared.get('selections') != selected_context.get('selections') or not selected_context.get('selections')
            or prepared.get('source_sha') != core.get('source_sha') or prepared.get('version') != scope.get('release')
            or prepared.get('scope_sha256') != hashlib.sha256(json.dumps(scope, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest()
            or value.get('result') != 'partial' or value.get('core') != core or value.get('request') != file(value['request']['path'])
            or value.get('owned_children_exited') is not True):
        raise ValueError('native adopter execution differs from receiving producer/source/request')
    root = Path(value['request']['path']).resolve().parent
    request = json.loads(checked(value['request'], root).read_text(encoding='utf-8-sig'))
    if (request.get('core') != core or request.get('native_request') != value.get('native_request')
            or request.get('runtime', {}).get('sha256') != core.get('runtime_sha256')):
        raise ValueError('native canonical request/runtime differs')
    checked(value['native_request'])
    raw = value.get('adopter_journey', {})
    files = {key: checked(binding, root) for key, binding in raw.get('files', {}).items()}
    required = {'events', 'export', 'history_before', 'history_after', 'audit', 'saved_image', 'project_image', 'identity'}
    if set(files) != required:
        raise ValueError('canonical native app/history/audit/image evidence incomplete')
    session = adopter_journey_session(files['events'].read_text(encoding='utf-8'))
    opencode = prepared['opencode']
    checked(opencode['program']);checked(opencode['config'])
    chosen = [row for row in selected_context['selections'] if row.get('id') == 'opencode' and row.get('target') == 'windows-x64']
    if len(chosen) != 1 or chosen[0].get('executable_sha256') != opencode['program']['sha256']:
        raise ValueError('official CLI differs from the selected installed executable')
    panels = [row for row in value.get('commands', []) if row.get('operation') == 'panel-readback']
    if len(panels) != 1 or panels[0].get('argv', [None])[0] != value.get('panel', {}).get('path'):
        raise ValueError('one owned Panel command required for native caller echo')
    adopter_journey_panel_identity(json.loads(files['identity'].read_text()), prepared, value['panel'], panels[0].get('pid'))
    result = adopter_journey_image_correlation(json.loads(files['export'].read_text()), session, raw['image_request'],
        json.loads(files['history_before'].read_text()), json.loads(files['history_after'].read_text()),
        json.loads(files['audit'].read_text()), opencode['program']['path'], prepared['account'], prepared['comfy']['host'], raw['files']['saved_image'])
    if raw.get('correlation') != result:
        raise ValueError('native output correlation differs on canonical reread')
    if files['project_image'].read_bytes() != files['saved_image'].read_bytes():
        raise ValueError('official project save differs from stock Comfy output')
    pending = [{'kind': 'adopters', 'id': row['id'], 'target': target, 'behavior': behavior}
               for row in scope['adopters'] for target in row['targets'] for behavior in row['behavior']]
    pending += [{'kind': 'journeys', 'id': row['id'], 'target': target, 'outcome': outcome}
                for row in scope['journeys'] if row['id'] == 'J-1' for target in row['targets'] for outcome in row['outcomes']]
    return {'selections': selected_context['selections'], 'adopter_receipts': [], 'journey_receipts': [],
            'receiver_files': [], 'validated_cases': [], 'unvalidated_cases': [result],
            'pending_cases': pending, 'pending_provenance':
            'Independent installed plugin/config, Comfy process/base/output and current SDK/build joins remain required.'}


class AdopterJourneyScene(NativeCore):
    """Automated official-app→registered stock Comfy image execution slice.

    This consumes already prepared/authorized installed inputs. It starts no
    backend, installs nothing and grants no permission. The unimplemented full
    acceptance matrix stays pending in the canonical result.
    """
    audit = OrdinaryScene.audit

    def collect_native_questions(self, filename):
        """Retain account-scoped native Asks reads; never submit an answer."""
        if filename not in ('j1-setup-questions.json', 'j1-command-questions.json', 'j1-render-questions.json'):
            raise ValueError('fixed native question transcript required')
        return self.collect_native_asks_pages(filename, '/questions', ('Outcome', 'Records', 'Complete', 'Next'))

    def collect_native_asks_pages(self, filename, endpoint, fields):
        outcome, records, complete, next_field = fields
        rows, cursor = [], ''
        for index in range(64):
            route = endpoint + '?cursor=' + urllib.parse.quote(cursor, safe='')
            req = urllib.request.Request(self.base + route, headers={'X-Panel-Key': self.key})
            with self.http.open(req, timeout=min(3, self.remaining())) as response:
                raw = response.read(1024**2 + 1)
            if len(raw) > 1024**2:
                raise ValueError('native question page exceeds bound')
            path = self.root/(filename[:-5] + '-%02d.response' % index)
            with path.open('xb') as output:output.write(raw)
            answer = json.loads(raw)
            rows.append({'route': route, 'answer': answer, 'response': file(path), 'observed_at': now()})
            write(self.root/filename, rows)
            if answer.get(outcome) != 'page' or not isinstance(answer.get(records), list):
                raise ValueError('native question inventory was not independently readable')
            if answer.get(complete) is True:
                if answer.get(next_field):
                    raise ValueError('native complete question page carries a continuation')
                return {'records': file(self.root/filename)}
            next_cursor = answer.get(next_field)
            if not isinstance(next_cursor, str) or not next_cursor or next_cursor == cursor:
                raise ValueError('native question inventory has no fresh continuation')
            cursor = next_cursor
        raise ValueError('native question inventory exceeds page bound')

    def collect_j1_answer_audit(self, context, panel_pid):
        """Keep read-only originals while the existing same-account Panel is held."""
        identity = self.api('/identity')
        native_panel_observation(identity, context['panel'], panel_pid, context['account'], context.get('caller_codec'))
        path = self.root/'j1-render-answer-identity.json'
        write(path, identity)
        questions = self.collect_native_questions('j1-render-questions.json')
        answers = self.collect_native_asks_pages('j1-render-answers.json', '/activity/answers',
                                               ('outcome', 'events', 'complete', 'next'))
        return {'identity': file(path), 'questions': questions['records'], 'answers': answers['records']}

    def collect_comfy_recovery(self, base, before, plan):
        """Observe retained work without cancelling, retrying or preparing it."""
        records = []
        deadline = time.monotonic() + min(240, self.remaining())
        verify = '/abstraction/workflows/flux2-klein-distilled?verify=1'

        def request(route, timeout):
            remaining = min(timeout, self.remaining(), deadline - time.monotonic())
            if remaining <= 0:
                raise TimeoutError('native recovery read window closed')
            request = urllib.request.Request(base.rstrip('/') + route)
            with self.http.open(request, timeout=remaining) as response:
                limit = 16 * 1024**2 - sum(row['response']['bytes'] for row in records)
                raw = response.read(limit + 1)
                status = response.code
            if len(raw) > limit:
                raise ValueError('native recovery response exceeds aggregate bound')
            path = self.root/('j1-comfy-recovery-%02d.response' % len(records))
            with path.open('xb') as output:
                output.write(raw)
            row = {'route': route, 'body': None, 'status': status, 'answer': json.loads(raw),
                   'response': file(path), 'observed_at': now()}
            records.append(row)
            write(self.root/'j1-comfy-recovery.json', records)
            return row['answer']

        request('/abstraction/jobs', 3)
        if comfy_verified_store(plan) == 'empty':
            store = comfy_verified_store(request(verify, 120))
            if store == 'empty':
                self.progress('native-comfy-picture-recovery', {
                    'instruction': 'Use the existing ComfyUI preparation and Jobs controls during the owner walkthrough.'})
                for _ in range(64):
                    if deadline - time.monotonic() <= 3:
                        break
                    time.sleep(1)
                    try:
                        page = request('/abstraction/jobs', 3)
                        jobs = comfy_picture_jobs(page, plan['assets'], include_cache=True)
                        present = {row['url'] for row, _ in jobs.values() if row.get('present') is True}
                        if (store == 'empty' and present) or (store == 'partial' and len(present) == len(plan['assets'])):
                            store = comfy_verified_store(request(verify, 120))
                    except TimeoutError:
                        break
                    if store == 'warm':
                        break
        pin = file(self.root/'j1-comfy-recovery.json')
        received = comfy_recovery_observations(pin, self.root, before, plan)
        return {'records': pin, **{key: received[key] for key in ('selected', 'stores', 'job_observations')}}

    def collect_native_comfy_setup(self, binary):
        """Drive mechanical Setup steps only after its actual Install approval.

        The original producer supplies binary; this method never authors a
        consent answer or accepts a caller-provided installation root.
        """
        records = []
        def call(route, body=None):
            if route not in ('/setup/plan', '/setup/install', '/setup/advance-next',
                             '/setup/comfy/register', '/setup/comfy/status') or (
                    route == '/setup/install' and body is not None):
                raise ValueError('J1 permits only its fixed approved Setup operations')
            request = urllib.request.Request(self.base + route,
                None if body is None else json.dumps(body).encode(),
                headers={'X-Panel-Key': self.key, 'Content-Type': 'application/json'})
            with self.http.open(request, timeout=min(3, self.remaining())) as response:
                raw = response.read(16 * 1024**2 + 1)
            if len(raw) > 16 * 1024**2:
                raise ValueError('J1 Setup observation exceeds bound')
            answer = json.loads(raw)
            records.append({'route': route, 'body': body, 'answer': answer, 'observed_at': now()})
            write(self.root / 'j1-comfy-setup.json', records)
            return answer
        review = call('/setup/plan', {'selected': ['comfyui', 'pytorch']})
        components = [row for row in review.get('components', []) if row.get('id') == 'comfyui-app']
        if (review.get('preparable') is not True or review.get('blockers') or not review.get('id') or
                len(components) != 1 or any(components[0].get('artifact', {}).get(key) != binary.get(key)
                                            for key in ('sha256', 'bytes'))):
            raise ValueError('J1 native Setup review differs from authenticated archive')
        self.progress('native-comfy-setup-review', {'plan_id': review['id'],
            'instruction': 'Review and approve this exact picture setup in the installed Panel.'})
        def approved():
            state = call('/setup/install')
            if state.get('plan', {}).get('id') != review['id']:
                raise ValueError('J1 native Setup plan changed before approval')
            return state if state.get('approved') is True and state.get('install', {}).get('plan_id') == review['id'] else None
        state = self.until(approved)
        for _ in range(512):
            self.remaining()
            rows = state.get('components', [])
            if rows and all(row.get('effect', {}).get('phase') == 'installed' for row in rows):
                break
            if any(row.get('effect', {}).get('phase') in ('failed', 'uncertain') for row in rows):
                raise ValueError('J1 approved native Setup stopped; retain its original journal')
            state = call('/setup/advance-next', {'plan_id': review['id']})
            if state.get('plan', {}).get('id') != review['id'] or state.get('current_catalog') is not True:
                raise ValueError('J1 approved native Setup source changed')
            time.sleep(min(.2, self.remaining()))
        else:
            raise ValueError('J1 native Setup exceeded mechanical step bound')
        registered = call('/setup/comfy/register', {'setup_plan_id': review['id'], 'confirm': True})
        status = call('/setup/comfy/status')
        for answer in (registered, status):
            if answer.get('registered') is not True or answer.get('matching') is not True or answer.get('application') != 'comfyui':
                raise ValueError('J1 native application registration differs')
        return {'plan_id': review['id'], 'records': file(self.root / 'j1-comfy-setup.json')}

    def collect_comfy_preparation(self, base, *, cold_refusal=False):
        """Observe the real recipe store and no-consent/stale-plan refusals.

        This collector starts no preparation. Explicit app intent, completed
        empty-store preparation, canvas replacement and recovery remain separate
        observations; a warm store cannot supply them.
        """
        parsed = urllib.parse.urlsplit(base)
        if (parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or not parsed.port or
                parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/')):
            raise ValueError('J1 requires the independently owned loopback Comfy instance')
        prefix = '/abstraction/workflows/flux2-klein-distilled'
        verify_route = prefix + '?verify=1'
        graph_route = prefix + '/graph'
        # Both routes can read and hash the 12.45 GB pinned FP8 plan.
        # Allow roughly 100 MiB/s while respecting the native owner deadline.
        full_scan_timeout = 120
        records = []
        stem = 'j1-comfy-cold-refusal' if cold_refusal else 'j1-comfy-preparation'
        transcript = self.root/(stem + '.json')
        def request(route, body=None):
            allowed = (('/abstraction/jobs', prefix, graph_route) if cold_refusal else
                       ('/abstraction/jobs', verify_route, graph_route, prefix + '/prepare'))
            if route not in allowed or (body is not None and route != prefix + '/prepare'):
                raise ValueError('J1 fixed recipe observation required')
            req = urllib.request.Request(base.rstrip('/') + route,
                None if body is None else json.dumps(body).encode(),
                headers={'Content-Type': 'application/json', 'Origin': base.rstrip('/')})
            try:
                timeout = full_scan_timeout if not cold_refusal and route in (verify_route, graph_route) else 3
                response = self.http.open(req, timeout=min(timeout, self.remaining()))
            except urllib.error.HTTPError as error:
                response = error
            with response:
                raw = response.read(16 * 1024**2 + 1)
                status, headers = response.code, dict(response.headers)
            if len(raw) > 16 * 1024**2:
                raise ValueError('J1 recipe observation exceeds bound')
            response_path = self.root / ('%s-%02d.response' % (stem, len(records)))
            with response_path.open('xb') as output:
                output.write(raw)
            row = {'route': route, 'body': body, 'status': status,
                'answer': json.loads(raw), 'sha256': hashlib.sha256(raw).hexdigest(),
                'response': file(response_path), 'graph_sha256': headers.get('X-OA-Graph-SHA256'), 'observed_at': now()}
            records.append(row)
            write(transcript, records)
            return row
        request('/abstraction/jobs')
        if cold_refusal:
            plan = request(prefix)
            if plan['status'] != 200 or comfy_verified_store(plan['answer']) != 'empty':
                return None
            request(graph_route)
            request(prefix)
            request('/abstraction/jobs')
            pin = file(transcript)
            comfy_cold_refusal_observation(pin, self.root)
            return pin
        plan = request(verify_route)['answer']
        if not re.fullmatch('[0-9a-f]{64}', str(plan.get('manifest_sha256'))):
            raise ValueError('J1 current recipe manifest missing')
        request(prefix + '/prepare', {'manifest_sha256': plan['manifest_sha256'], 'approve': False})
        stale = '0' * 64 if plan['manifest_sha256'] != '0' * 64 else '1' * 64
        request(prefix + '/prepare', {'manifest_sha256': stale, 'approve': True})
        request(graph_route)
        request(verify_route)
        request('/abstraction/jobs')
        observations = comfy_preparation_observations(records, self.root)
        return {'records': file(transcript), **observations}

    def isolated_app_environment(self, config):
        # Same operating-system allowlist as the app's config-lifecycle owner.
        allowed = {'systemroot', 'windir', 'comspec', 'pathext', 'processor_architecture'}
        env = {key: value for key, value in os.environ.items() if key.lower() in allowed}
        for key in ('XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME', 'LOCALAPPDATA', 'APPDATA', 'USERPROFILE', 'HOME', 'TEMP', 'TMP'):
            path = self.root / ('opencode-' + key.lower());path.mkdir()
            env[key] = str(path)
        env.update(OPENCODE_CONFIG=str(config), OPENCODE_DISABLE_AUTOUPDATE='1',
                   OPENCODE_DISABLE_MODELS_FETCH='1', OPENCODE_DISABLE_PROJECT_CONFIG='true')
        return env

    def local(self, base, route):
        parsed = urllib.parse.urlsplit(base)
        if (parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or not parsed.port
                or parsed.username or parsed.password or parsed.query or parsed.fragment
                or parsed.path not in ('', '/') or route != '/history'):
            raise ValueError('fixed owned Comfy history read required')
        with self.http.open(base.rstrip('/') + route, timeout=min(3, self.remaining())) as response:
            raw = response.read(16 * 1024 * 1024 + 1)
        if len(raw) > 16 * 1024 * 1024:
            raise ValueError('Comfy history exceeded evidence bound')
        value = json.loads(raw)
        if not isinstance(value, dict):
            raise ValueError('Comfy history must be an object')
        return value

    def command(self, name, argv, *, timeout=None):
        limit = time.monotonic() + timeout if timeout is not None else None
        # Official run emits JSONL on stdout. Export emits a diagnostic on
        # stderr even on success; preserve its separate private evidence.
        log, diagnostic = self.root / (name + '.json'), self.root / (name + '.stderr')
        started_at = now()
        with log.open('wb') as output, diagnostic.open('wb') as errors:
            process = subprocess.Popen([str(x) for x in argv], env=self.app_env,
                stdin=subprocess.DEVNULL, stdout=output, stderr=errors,
                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        row = {'operation': name, 'argv': [str(x) for x in argv], 'pid': process.pid,
               'started_at': started_at, 'log_path': str(log), 'private_evidence': True}
        self.commands.append(row);self.children.append((process, row))
        try:
            remaining = self.remaining()
            if limit is not None:
                remaining = min(remaining, limit - time.monotonic())
            process.communicate(timeout=remaining)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)
            row.update(exit_code=process.returncode, exited=True, ended_at=now(), log=file(log), diagnostic=file(diagnostic))
        if process.returncode or log.stat().st_size > 16 * 1024 * 1024 or diagnostic.stat().st_size > 1024 * 1024:
            raise ValueError('official app command failed or exceeded its evidence bound: ' + name)
        return log

    def run(self):
        error = None
        try:
            native = json.loads(checked(self.request['native_request']).read_text(encoding='utf-8-sig'))
            if native.get('core') != self.core:
                raise ValueError('canonical core differs from immutable original native request')
            if self.request.get('native_comfy_phase') is not None:
                return self._run_native_comfy_phase(native)
            constructor = opencode_adopter_context if native.get('adopter_case') == 'opencode' else adopter_execution_context
            context = constructor(native, self.request.get('installed_chain', {}), self.request.get('observations', {}))
            self.request['execution_context'] = context
            write(self.request_path, self.request)
            self.data.update(request=file(self.request_path), execution_context=context)
            if native.get('adopter_case') == 'opencode':
                return self._run_opencode(context)
            if native.get('adopter_case') == 'j1' and context.get('comfy_components'):
                return self._run_opencode(context, j1=True)
            require_adopter_execution_context(context)
            return self._run_prepared(context)
        except BaseException as caught:
            error = caught
            self.data.update(result='failed', failure=str(caught), ended_at=now(),
                             owned_children_exited=all(p.poll() is not None for p, _ in self.children))
            write(self.root / 'adopter-journey-result.json', self.data)
        raise error

    def _run_native_comfy_phase(self, native):
        """Existing lifecycle's setup/removal child; never a functional PASS."""
        phase = self.request['native_comfy_phase']
        if phase not in ('setup', 'recovery', 'remove') or native.get('adopter_case') != 'j1':
            raise ValueError('fixed native Comfy lifecycle phase required')
        adopter_journey_inputs(self.core)
        chain = self.request.get('installed_chain', {})
        observations = self.request.get('observations', {})
        owner = chain.get('fixture_owner', {})
        if (owner != observations.get('fixture_owner') or owner.get('nonadmin') is not True or
                not re.fullmatch(r'S-1-[0-9-]+', str(owner.get('sid')))):
            raise ValueError('actual disposable native lifecycle owner required')
        selected = [row for row in native['selected_app_chain']['apps'] if row.get('id') == 'comfyui']
        observed = [row for row in chain['apps'] if row.get('id') == 'comfyui']
        compiled = native['selected_app_readback']['comfyui']
        if (len(selected) != 1 or len(observed) != 1 or observed[0].get('package') != selected[0] or
                selected[0].get('variant') != 'oa-comfyui-native' or compiled.get('variant') != selected[0]['variant'] or
                selected[0]['delivery']['binary'] != compiled['native']['binary']):
            raise ValueError('original native Comfy delivery/lifecycle differs')
        version = compiled['native']['identity']['version']
        if not re.fullmatch('[A-Za-z0-9._-]{1,64}', version) or version in ('.', '..'):
            raise ValueError('native Comfy source version differs')
        expected_root = PureWindowsPath(owner['local_app_data']) / 'OA/a/cu' / version
        if observed[0].get('root') != str(expected_root):
            raise ValueError('native Comfy lifecycle root differs from KnownFolder/source')
        # Pending native files are not represented as installed MSI members.
        # Existing MSI roles authenticate the actual OpenCode SDK target before
        # this source-owned setup phase; the accepted scope remains unchanged.
        msi_ids = {row['id'] for row in native['selected_app_chain']['apps'] if row.get('variant') != 'oa-comfyui-native'}
        msi_native = dict(native, selected_app_chain=dict(native['selected_app_chain'], apps=[row for row in native['selected_app_chain']['apps'] if row['id'] in msi_ids]),
            selected_app_readback={key: value for key, value in native['selected_app_readback'].items() if key in msi_ids})
        msi_chain = dict(chain, apps=[row for row in chain['apps'] if row['id'] in msi_ids])
        members = installed_payload_members(msi_native, msi_chain)
        role = native['selected_app_readback']['opencode']['journey_payload_roles']['official_cli']['id']
        cli = {key: members['opencode'][role][key] for key in ('path', 'sha256', 'bytes')}
        sdk, missing = j1_selection_observation(native, observations, cli, self.request['runtime'], owner['sid'])
        if missing and phase != 'setup':
            raise ValueError('native Comfy lifecycle current SDK/server owner input missing: ' + ', '.join(missing))
        if phase == 'setup' and Path(str(expected_root)).exists():
            raise ValueError('preexisting native Comfy installation is protected')
        error = None
        try:
            process, _, log = self.start('panel-readback', [self.panel, '-native=false', '-open=false', '-addr', '127.0.0.1:0'])
            self.base, self.key = self.until(lambda: panel_url(log.read_text(errors='replace')))
            identity = self.api('/identity')
            # This is observed diagnostic data. Current SDK peer selection and
            # this owned process/image remain separate receiving prerequisites.
            codec_path = Path(self.core['pythonpath']) / 'abstraction/facade/_codec.py'
            codecs = [pin for pin in self.core['python_files'] if Path(pin['path']) == codec_path]
            if len(codecs) != 1:
                raise ValueError('admitted current caller record codec missing')
            native_panel_observation(identity, self.request['panel'], process.pid, owner['sid'], codecs[0])
            if identity.get('selection', {}).get('account') != sdk['server']['principal'] or identity['selection'].get('program') != sdk['server']['program']:
                raise ValueError('native Comfy Panel selection differs from current SDK peer')
            write(self.root / 'native-phase-identity.json', identity)
            raw = {'phase': phase, 'identity': file(self.root / 'native-phase-identity.json'), 'sdk': sdk['files']}
            self.data['native_comfy_lifecycle'] = raw
            if phase == 'setup':
                raw['setup'] = self.collect_native_comfy_setup(compiled['native']['binary'])
                # The installed presenter owns setup.models and every resulting
                # model/policy effect. Its native question receives the person's
                # answer; this fixture submits no Asks answer or resource grant.
                self.app_env = dict(self.env)
                raw['model_setup'] = file(self.command('native-model-setup', [self.panel, '--setup']))
                raw['questions'] = self.collect_native_questions('j1-setup-questions.json')
            elif phase == 'recovery':
                recovery = observations.get('j1_recovery', {})
                resumed = dict(observations, j1_application={'activate': recovery['activate']},
                               j1_comfy_instance=recovery['instance'])
                context = adopter_execution_context(native, chain, resumed)
                instance = context['comfy_components']['instance']
                original_path = self.root.parent/'adopter-journey-result.json'
                original = json.loads(original_path.read_text())
                if (original.get('core') != self.core or original.get('native_request') != self.request['native_request'] or
                        original.get('result') != 'partial' or original.get('owned_children_exited') is not True):
                    raise ValueError('native recovery requires its completed original scene')
                before_pin = original['comfy_preparation']['records']
                if Path(before_pin['path']) != self.root.parent/'j1-comfy-preparation.json':
                    raise ValueError('native recovery preparation transcript left its original scene')
                rows = json.loads(checked_j1_comparison_file(before_pin, self.root.parent, 16 * 1024**2, capture=True))
                comfy_preparation_observations(rows, self.root.parent)
                prior = original['execution_context']['comfy_components']['instance']
                if (instance['base'] != prior['base'] or instance['output_root'] != prior['output_root'] or
                        any(instance[role]['pid'] == prior[role]['pid'] and instance[role]['birth'] == prior[role]['birth']
                            for role in ('application', 'backend'))):
                    raise ValueError('native recovery did not replace both closed occurrences at the same source location')
                raw.update(original=file(original_path), activation=recovery['activate'], instance=recovery['instance'])
                raw['recovery'] = self.collect_comfy_recovery(instance['base'], rows[6]['answer'], rows[1]['answer'])
            else:
                setup_root = self.root.parent / 'comfy-setup'
                setup_path = checked(observed[0]['setup_result'], setup_root)
                if setup_path.name != 'adopter-journey-result.json':
                    raise ValueError('original native setup result required before removal')
                setup = json.loads(setup_path.read_text())
                if (setup.get('core') != self.core or setup.get('native_request') != self.request['native_request'] or
                        setup.get('result') != 'partial' or setup.get('owned_children_exited') is not True or
                        setup.get('native_comfy_lifecycle', {}).get('phase') != 'setup'):
                    raise ValueError('native removal differs from original setup/source')
                original = native_comfy_setup_observation(setup['native_comfy_lifecycle']['setup']['records'],
                    compiled['native']['binary'], setup_root)
                body = {'plan_id': original['plan_id'], 'component_id': 'comfyui-app', 'confirm': True}
                request = urllib.request.Request(self.base + '/setup/uninstall', json.dumps(body).encode(),
                    headers={'X-Panel-Key': self.key, 'Content-Type': 'application/json'})
                with self.http.open(request, timeout=min(3, self.remaining())) as response:
                    raw_response = response.read(16 * 1024**2 + 1)
                if len(raw_response) > 16 * 1024**2:
                    raise ValueError('native Comfy removal response exceeds bound')
                removed = json.loads(raw_response)
                rows = [row for row in removed.get('components', []) if row.get('id') == 'comfyui-app']
                if removed.get('plan', {}).get('id') != original['plan_id'] or len(rows) != 1 or rows[0].get('effect', {}).get('phase') != 'removed':
                    raise ValueError('native Comfy uninstall did not confirm its exact approved component')
                write(self.root / 'j1-comfy-removal.json', {'body': body, 'answer': removed})
                raw['removal'] = file(self.root / 'j1-comfy-removal.json')
        except BaseException as caught:
            error = caught
            self.data['failure'] = str(caught)
        finally:
            for process, row in reversed(self.children):
                if process.poll() is None:
                    process.kill(); process.wait(timeout=5)
                row.update(exited=True, exit_code=process.returncode, ended_at=row.get('ended_at', now()))
            self.data.update(result='failed' if error else 'partial', ended_at=now(),
                owned_children_exited=all(process.poll() is not None for process, _ in self.children))
            write(self.root / 'adopter-journey-result.json', self.data)
        if error:
            raise error
        return self.root / 'adopter-journey-result.json'

    def collect_opencode_e1(self, context, program, model, project, cursor):
        probes = self.data['opencode_e1']
        # Preserve time for the independent tool and image legs under the
        # existing owner deadline. This probe cannot extend the scene.
        deadline = time.monotonic() + max(0, self.remaining() - 72)

        def budget(seconds):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('OpenCode E1 probe window closed')
            return min(remaining, seconds)

        for stage, timeout in (('cold', 120), ('warm', 30)):
            prefix = 'j1-e1-' + stage
            nonce = os.urandom(16).hex()
            event = self.command(prefix + '-run',
                adopter_journey_command(program, project, model, 'j1-' + nonce, opencode_e1_prompt(nonce)),
                timeout=budget(timeout))
            session = adopter_journey_session(event.read_text(encoding='utf-8'))
            export = self.command(prefix + '-export', [program, 'export', session], timeout=budget(10))
            rows, following = self.audit(cursor, pages=1, deadline=deadline)
            path = self.root / (prefix + '-audit.json')
            write(path, {'before': cursor, 'next': following, 'entries': rows})
            probe = {'nonce': nonce, 'files': {'run': file(event), 'export': file(export), 'audit': file(path)}}
            probes.append(probe)
            cursor = following
            if opencode_e1_probe(self.data, context, self.root, model, stage, probe) is None:
                break
        return cursor

    def _run_opencode(self, context, *, j1=False):
        """Existing native entry, installed official CLI and read-only OA tool."""
        error = None
        try:
            program = checked({key: context['opencode']['program'][key] for key in ('path', 'sha256', 'bytes')})
            configure = Path(context['opencode']['config_tools']['configure']['path'])
            lifecycle = Path(context['opencode']['config_tools']['lifecycle']['path'])
            # The native fixture observes the real profile, with no XDG override.
            profile = Path(self.request['observations']['profile'])
            config_directory = profile / '.config/opencode'
            config_request = self.root / 'opencode-registration-request.json'
            write(config_request, {'operation': 'observe', 'directory': str(config_directory),
                'state': str(Path(context['fixture_owner']['local_app_data']) / 'OpenAbstractionsApps/opencode-installer'),
                'plugin': str(configure.parent)})
            self.app_env = self.isolated_app_environment(config_directory / 'opencode.json')
            self.app_env['BUN_BE_BUN'] = '1'
            registration = self.command('opencode-registration', [program, 'run', '--no-env-file', '--no-install',
                '--no-addons', '--no-macros', '--config=' + str(program.parent.parent / 'installer/empty-bunfig.toml'),
                lifecycle, '--observe-request', config_request])
            del self.app_env['BUN_BE_BUN']
            # Observe never rewrites the installed configuration. Its source-owned
            # descriptor identifies the chosen config (including JSONC precedence).
            registered = json.loads(registration.read_text(encoding='utf-8'))
            self.app_env['OPENCODE_CONFIG'] = registered['configured_provider']['path']
            models = self.command('opencode-models', [program, 'models', 'openabstractions'])
            available = models.read_text(encoding='utf-8').splitlines()
            candidates = context['coding_selection']['policy']['policy']['candidates']
            choices = ['openabstractions/' + row['model'] for row in candidates
                       if row.get('placement') == 'local' and not row.get('credential') and not row.get('provider')]
            model = next((name for name in choices if name in available), None)
            if not model:
                raise ValueError('installed official OpenCode lists no selected local coding model')
            project = self.root / 'opencode-project'; project.mkdir()
            panel_process, _, log = self.start('panel-readback', [self.panel, '-native=false', '-open=false', '-addr', '127.0.0.1:0'])
            self.base, self.key = self.until(lambda: panel_url(log.read_text(errors='replace')))
            _, cursor = self.audit(0)
            if j1:
                instance = context['comfy_components'].get('instance')
                if instance is not None:
                    try:
                        cold = self.collect_comfy_preparation(instance['base'], cold_refusal=True)
                    except (OSError, ValueError):
                        cold = None
                    if cold is not None:
                        self.data['comfy_cold_refusal'] = cold
                self.data['opencode_e1'] = []
                try:
                    cursor = self.collect_opencode_e1(context, program, model, project, cursor)
                except (OSError, ValueError, subprocess.TimeoutExpired) as failure:
                    # Take a new audit boundary for the ordinary run. Never
                    # replay a probe or use the evicted cursor as that boundary.
                    seed = failure.next_cursor if isinstance(failure, InferenceAuditGap) else 0
                    deadline = time.monotonic() + min(12, max(0, self.remaining() - 60))
                    try:
                        _, cursor = self.audit(seed, pages=66, fresh=True, deadline=deadline)
                    except (OSError, ValueError):
                        # The ordinary scenario can still run once. Neither
                        # this partial scan nor the old cursor binds its calls.
                        cursor = None
            nonce = os.urandom(16).hex()
            messages = self.core.get('opencode_messages') is True
            tiny = private_upgrade_completion(self.core,context)
            declaration = opencode_messages_declaration(context, nonce) if messages else None
            if messages:
                if declaration['catalogue'] is None:
                    raise ValueError('installed full OpenCode catalogue is not available for the Messages journey')
                self.start('opencode-native-person', [self.panel, '-native=true', '-page', 'questions'])
                self.progress('opencode-native-messages', {'instruction': 'Review the one native permission request to message Claude. Only the person can allow this direction.',
                    'declaration': declaration})
            prompt=opencode_run_prompt(self.core,context,nonce)
            events = self.command('opencode-run', adopter_journey_command(program, project, model,'j1-' + nonce,prompt))
            session = adopter_journey_session(events.read_text(encoding='utf-8'))
            exported = self.command('opencode-export', [program, 'export', session])
            audit = []
            if cursor is not None:
                audit, cursor = self.audit(cursor)
            write(self.root / 'opencode-audit.json', audit)
            raw = {'model': model, 'nonce': nonce, 'files': {name: file(path) for name, path in {
                'registration': registration, 'models': models, 'events': events, 'export': exported,
                'audit': self.root / 'opencode-audit.json'}.items()}}
            self.data['adopter_journey'] = raw
            observations, missing = opencode_adopter_observations(raw, context, installed=True)
            if tiny:
                questions=self.collect_native_questions('j1-command-questions.json')
                pages=json.loads(checked(questions['records']).read_text(encoding='utf-8'))
                if any(row['answer'].get('Records') for row in pages):
                    raise ValueError('private tiny completion encountered a native permission question; no answer is automated')
                path=self.command('tiny-pending-permissions',[str(self.runtime),'questions','permissions','--json','--timeout','30s',
                    '--endpoint',r'\\.\pipe\openabstractions-user-'+context['account']+'-runtime-v1',
                    '--runtime-program',str(self.runtime)])
                permission=json.loads(path.read_text(encoding='utf-8'))
                if permission.get('outcome')!='page' or permission.get('records')!=[]:
                    raise ValueError('private tiny completion encountered a pending typed permission; no answer is automated')
                self.data['private_pending_permissions']=file(path)
            if messages:
                export = json.loads(exported.read_text(encoding='utf-8'))
                # The common native backend reports permitted only after the
                # current Rights decision and live destination inbox agree.
                # An Answered/Allow tool result is refused by this receiver.
                control = opencode_messages_control(declaration, export, installed=True)
                self.data['opencode_messages'] = {'declaration': declaration, 'observation': control}
                observations.extend({'behavior': name, 'result': 'pass', 'session_id': session} for name in ('tools', 'oa-integration'))
                missing = [name for name in missing if name not in ('tools', 'oa-integration')]
            receiver = self.root / 'opencode-receiver.json'
            write(receiver, opencode_adopter_proof(context, observations, missing))
            self.data.update(receiver_evidence=file(receiver), pending=missing)
            instance = context.get('comfy_components', {}).get('instance')
            if instance is not None:
                self.data['comfy_preparation'] = self.collect_comfy_preparation(instance['base'])
                self.data['comfy_questions'] = self.collect_native_questions('j1-command-questions.json')
                # A verified cold refusal is an observation. Only the ready
                # store can proceed to the separately checked image render.
                if self.data['comfy_preparation']['store'] == 'warm':
                    self.data['comfy_image'] = self.collect_native_image(context, program, model, project, cursor=cursor)
                    if self.data['comfy_image'] is not None:
                        try:
                            self.data['j1_answer_audit'] = self.collect_j1_answer_audit(context, panel_process.pid)
                        except (OSError, ValueError):
                            # No comparison can use a missing or incomplete audit.
                            pass
        except BaseException as caught:
            error = caught; self.data['failure'] = str(caught)
        finally:
            for process, row in reversed(self.children):
                if process.poll() is None:
                    process.kill(); process.wait(timeout=5)
                row.update(exited=True, exit_code=process.returncode, ended_at=row.get('ended_at', now()))
            self.data.update(result='failed' if error else 'partial', ended_at=now(),
                owned_children_exited=all(p.poll() is not None for p, _ in self.children))
            write(self.root / 'adopter-journey-result.json', self.data)
        if error:
            raise error
        return self.root / 'adopter-journey-result.json'

    def collect_native_image(self, context, program, coding_model, project, *, cursor=0):
        """Run the adopted image tool once against the held stock backend."""
        instance = context['comfy_components']['instance']
        preparation = self.data['comfy_preparation']
        if preparation['store'] != 'warm':
            raise ValueError('J1 image needs completed explicit recipe preparation')
        plan_rows = json.loads(checked(preparation['records'], self.root).read_text())
        plan = plan_rows[1]['answer']
        hosts = self.api('/inference/hosts')
        write(self.root/'j1-image-hosts.json', hosts)
        matches = [row for row in hosts.get('Hosts', [])
                   if row.get('Entry', {}).get('Kind') == 'comfyui' and
                   row['Entry'].get('Base', '').rstrip('/') == instance['base'] and row.get('Up') is True]
        if hosts.get('Outcome') != 'page' or len(matches) != 1 or not matches[0]['Entry'].get('Name'):
            raise ValueError('one current registered held Comfy image host required')
        before = self.local(instance['base'], '/history')
        write(self.root/'j1-history-before.json', before)
        if cursor is not None:
            _, cursor = self.audit(cursor)
        nonce = os.urandom(16).hex()
        request = {'model': plan['id'], 'prompt': 'A blue square. Evidence nonce ' + nonce,
                   'size': '512x512', 'save_to': 'j1-image.png'}
        instruction = adopter_journey_image_instruction(request)
        events = self.command('j1-opencode-run', adopter_journey_command(program, project, coding_model, 'j1-' + nonce, instruction))
        session = adopter_journey_session(events.read_text(encoding='utf-8'))
        exported = self.command('j1-opencode-export', [program, 'export', session])
        export = json.loads(exported.read_text(encoding='utf-8'))
        _, image = adopter_journey_image_export(export, session, request)
        after = self.local(instance['base'], '/history')
        write(self.root/'j1-history-after.json', after)
        audit = self.audit(cursor)[0] if cursor is not None else []
        write(self.root/'j1-image-audit.json', audit)
        output_root = Path(instance['output_root']).resolve()
        candidates = []
        for identity in set(after) - set(before):
            for output in after[identity].get('outputs', {}).values():
                for row in output.get('images', []):
                    if row.get('type') != 'output' or not isinstance(row.get('filename'), str):
                        continue
                    path = (output_root/row.get('subfolder', '')/row['filename']).resolve()
                    if (path.is_relative_to(output_root) and path.is_file() and not path.is_symlink()
                            and path.stat().st_size <= 8 * 1024**2 and path.read_bytes() == image):
                        candidates.append(path)
        if len(candidates) != 1:
            raise ValueError('one saved held Comfy output must match the official attachment')
        saved = self.root/'j1-images'/candidates[0].name
        saved.parent.mkdir(); saved.write_bytes(image); ordinary_png(saved)
        project_image = project/request['save_to']
        if project_image.is_symlink() or project_image.read_bytes() != image:
            raise ValueError('official project image differs from returned attachment')
        files = {name: file(path) for name, path in {
            'hosts': self.root/'j1-image-hosts.json', 'events': events, 'export': exported,
            'history_before': self.root/'j1-history-before.json', 'history_after': self.root/'j1-history-after.json',
            'audit': self.root/'j1-image-audit.json', 'saved_image': saved, 'project_image': project_image}.items()}
        if cursor is None:
            return None  # Raw files remain private; no native image correlation is receivable.
        result = {'request': request, 'host': matches[0]['Entry']['Name'], 'files': files}
        result['correlation'] = adopter_journey_image_correlation(export, session, request, before, after, audit,
            str(program), context['account'], result['host'], files['saved_image'], stock_assets=plan['assets'])
        return result

    def _run_prepared(self, prepared):
        # The existing lifecycle authenticates the producer before invoking this
        # child. Expected request fields confer no admission authority.
        if not prepared.get('panel_actor') or not prepared.get('runtime_program'):
            raise ValueError('expected native Panel caller observation and server selection required before execution')
        opencode, comfy = prepared['opencode'], prepared['comfy']
        program = checked(opencode['program'])
        config = checked(opencode['config'], self.root)
        project = Path(opencode['project'])
        if not project.is_absolute() or not project.resolve().is_relative_to(self.root) or project.exists():
            raise ValueError('new owned project required for automated image scene')
        selected = [row for row in prepared.get('selections', []) if row.get('id') == 'opencode' and row.get('target') == 'windows-x64']
        if len(selected) != 1 or selected[0].get('executable_sha256') != opencode['program']['sha256'] or not opencode['coding_model'].startswith('openabstractions/'):
            raise ValueError('exact official executable and OA coding-model route required')
        project.mkdir()
        self.app_env = self.isolated_app_environment(config)
        error = None
        self.data['adopter_journey'] = {'files': {}}
        raw = self.data['adopter_journey']
        try:
            panel_process, _, log = self.start('panel-readback', [self.panel, '-native=false', '-open=false', '-addr', '127.0.0.1:0'])
            self.base, self.key = self.until(lambda: panel_url(log.read_text(errors='replace')))
            identity = self.api('/identity')
            adopter_journey_panel_identity(identity, prepared, self.request['panel'], panel_process.pid)
            write(self.root / 'j1-identity.json', identity)
            _, cursor = self.audit(0)
            before = self.local(comfy['base'], '/history')
            write(self.root / 'j1-history-before.json', before)
            nonce = os.urandom(16).hex()
            image_request = {'model': comfy['model'], 'prompt': 'A blue square. Evidence nonce ' + nonce,
                             'size': '512x512', 'save_to': 'j1-image.png'}
            instruction = ('Use openabstractions_generate_image exactly once with these exact arguments: '
                           + json.dumps(image_request, sort_keys=True) + '. Return the image attachment. Do not use other tools.')
            events = self.command('j1-opencode-run', adopter_journey_command(program, project, opencode['coding_model'], 'j1-' + nonce, instruction))
            session = adopter_journey_session(events.read_text(encoding='utf-8'))
            exported = self.command('j1-opencode-export', [program, 'export', session])
            export = json.loads(exported.read_text(encoding='utf-8'))
            _, attachment = adopter_journey_image_export(export, session, image_request)
            after = self.local(comfy['base'], '/history')
            write(self.root / 'j1-history-after.json', after)
            audit, _ = self.audit(cursor)
            write(self.root / 'j1-audit.json', audit)
            # Find only the newly generated output under the prepared Comfy root.
            candidates = []
            for identity in set(after) - set(before):
                entry = after[identity]
                for output in entry.get('outputs', {}).values():
                    for image in output.get('images', []):
                        if image.get('type') != 'output' or not isinstance(image.get('filename'), str):
                            continue
                        output_root = Path(comfy['output_root']).resolve()
                        source = (output_root / image.get('subfolder', '') / image['filename']).resolve()
                        if source.is_relative_to(output_root) and source.is_file() and not source.is_symlink() and source.stat().st_size <= 8 * 1024 * 1024 and source.read_bytes() == attachment:
                            candidates.append(source)
            if len(candidates) != 1:
                raise ValueError('one actual stock Comfy saved output must match the visible official attachment')
            images = self.root / 'j1-images';images.mkdir()
            saved = images / candidates[0].name;saved.write_bytes(attachment)
            ordinary_png(saved)
            project_image = project / image_request['save_to']
            if project_image.is_symlink() or project_image.read_bytes() != attachment:
                raise ValueError('actual official project image differs from returned attachment')
            raw.update(image_request=image_request, correlation=adopter_journey_image_correlation(export, session, image_request, before, after, audit, str(program), prepared['account'], comfy['host'], file(saved)))
            raw['files'] = {name: file(path) for name, path in {
                'events': events, 'export': exported, 'history_before': self.root / 'j1-history-before.json',
                'history_after': self.root / 'j1-history-after.json', 'audit': self.root / 'j1-audit.json',
                'saved_image': saved, 'project_image': project_image, 'identity': self.root / 'j1-identity.json'}.items()}
            checked(opencode['program']);checked(opencode['config'])
        except BaseException as caught:
            error = caught;self.data['failure'] = str(caught)
        finally:
            for process, row in reversed(self.children):
                if process.poll() is None:
                    process.kill();process.wait(timeout=5)
                row.update(exited=True, exit_code=process.returncode, ended_at=row.get('ended_at', now()))
            self.data.update(result='failed' if error else 'partial', ended_at=now(), owned_children_exited=all(p.poll() is not None for p, _ in self.children))
            self.data['pending'] = 'Full selected-adopter behaviors, GPU execution, negative/store/consent and measured-step cases remain required; this image correlation is not J1 PASS.'
            write(self.root / 'adopter-journey-result.json', self.data)
        if error:
            raise error
        return self.root / 'adopter-journey-result.json'


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument('--ordinary-request')
    modes.add_argument('--ordinary-comfy-request')
    modes.add_argument('--adopter-journey-request')
    parser.add_argument('--ordinary-comfy-port', type=int)
    arguments = parser.parse_args()
    if arguments.ordinary_comfy_request:
        ordinary_comfy_host(arguments.ordinary_comfy_request, arguments.ordinary_comfy_port)
    elif arguments.ordinary_comfy_port is not None:
        parser.error('Comfy port belongs only to its fixed scene entry')
    elif arguments.adopter_journey_request:
        print(AdopterJourneyScene(arguments.adopter_journey_request).run(), flush=True)
    else:
        print(OrdinaryScene(arguments.ordinary_request).run(), flush=True)
