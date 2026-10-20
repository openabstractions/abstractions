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
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import socket
import struct
import subprocess
import sys
import time
import urllib.parse
import urllib.error
import urllib.request
import zlib


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


def checked(row, root=None):
    if not isinstance(row, dict) or set(row) != {"path", "sha256", "bytes"}:
        raise ValueError("exact core input file binding required")
    path = Path(row["path"])
    if (not path.is_absolute() or path.is_symlink() or not path.is_file()
            or (root is not None and not path.resolve().is_relative_to(root.resolve()))
            or file(path) != row):
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
        self.app = checked(self.core["go"], inputs_root)
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

    def call(self, name, argv, *, input=None, acceptable=(0,)):
        process, row, path = self.start(name, argv)
        try:
            process.communicate(input=input, timeout=min(10, self.remaining()))
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
            for process, row in reversed(self.children):
                if process.poll() is None:
                    process.kill()  # This helper owns this exact Popen handle/PID.
                    process.wait(timeout=5)
                row.update(exit_code=process.returncode, exited=True, ended_at=row.get("ended_at", now()), log=file(row["log_path"]))
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
            if (part.get('type') != 'tool' or part.get('tool') != 'openabstractions_authorization_questions'
                    or part.get('sessionID') != session_id or part.get('messageID') != info.get('id')
                    or state.get('status') != 'completed' or given != {'action': 'answer_choice', 'id': choice_id, 'option': option}):
                continue
            try:
                result = json.loads(state.get('output', ''))
            except (TypeError, ValueError):
                continue
            if (result.get('kind') == 'shared_question_answer' and result.get('outcome') == 'answered'
                    and result.get('id') == choice_id and result.get('option') == option and result.get('shownToPerson')):
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

    def audit(self, cursor):
        rows = []
        for _ in range(32):
            page = self.api('/inference/audit?cursor=' + str(cursor))
            if page.get('Outcome') != 'page' or page.get('Next', -1) < cursor:
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
                    'start_guidance': 'Start ComfyUI yourself. OpenAbstractions does not start it.'}:
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


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument('--ordinary-request')
    modes.add_argument('--ordinary-comfy-request')
    parser.add_argument('--ordinary-comfy-port', type=int)
    arguments = parser.parse_args()
    if arguments.ordinary_comfy_request:
        ordinary_comfy_host(arguments.ordinary_comfy_request, arguments.ordinary_comfy_port)
    elif arguments.ordinary_comfy_port is not None:
        parser.error('Comfy port belongs only to its fixed scene entry')
    else:
        print(OrdinaryScene(arguments.ordinary_request).run(), flush=True)
