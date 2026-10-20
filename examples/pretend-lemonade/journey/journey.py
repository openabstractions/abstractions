"""Run the five-abstraction journey against a started example folder.

The folder is one that scripts/panel_review.ps1 -Examples started without
-Background: its own runtime, state directory, Panel and loopback source. Each
step does what the README asks the person to do, through the same Panel
routes the Panel page calls, and records what the runtime, the apps and the
source showed. Evidence goes to <run>/journey.json, one entry per check.

    py -3.12 journey.py --run DIR [--python-path PATHS --ipc-prefix DIR]

Steps that only a person can take in the native presenter are named in the
evidence under "human"; the controller takes the Panel's equivalent where one
exists and says so. It answers nothing on the owner's screen: a folder whose
pids.txt records a presenter is refused.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

SECRET_NAME = "mirror-token"
SECRET_VALUE = "journey-dummy-value"  # a harmless fixture, never a real secret
OLD_OUTPUT = b"previous local output; the completed runtime job stays independent\n"


class Journey:
    def __init__(self, args):
        self.run = args.run.resolve()
        self.connection = dict(line.split("=", 1) for line in (self.run / "connection.txt").read_text(encoding="utf-8").splitlines() if "=" in line)
        pids = (self.run / "pids.txt").read_text(encoding="utf-8-sig")
        if "presenter.pid=" in pids:
            raise SystemExit("this folder runs the native presenter; start it without -Background for the controller")
        self.pack = Path(self.connection["RUNTIME_PROGRAM"]).parent
        self.app = self.pack / "pretend-lemonade.exe"
        self.logging_app = self.pack / "logging-quickstart.exe"
        self.panel_review = args.panel_review or self.pack / "panel_review.ps1"
        self.python_path, self.ipc_prefix, self.python = args.python_path, args.ipc_prefix, args.python
        url = re.search(r"^service control panel: (.+)$", (self.run / "panel.stdout.log").read_text(), re.M).group(1)
        parts = urllib.parse.urlsplit(url)
        self.key = urllib.parse.parse_qs(parts.query)["k"][0]
        self.base = urllib.parse.urlunsplit((parts.scheme, parts.netloc, "", "", ""))
        self.env = dict(os.environ, ABSTRACTION_RUNTIME_ENDPOINT=self.connection["ABSTRACTION_RUNTIME_ENDPOINT"])
        self.evidence = self.run / "journey.json"
        self.data = {"run": str(self.run), "pack": str(self.pack), "endpoint": self.connection["ABSTRACTION_RUNTIME_ENDPOINT"],
                     "binaries": {}, "checks": {}, "receipts": {}, "human": {}, "children": []}
        for name in ("openabstractions.exe", "Abstraction Panel.exe", "pretend-lemonade.exe", "logging-quickstart.exe"):
            self.data["binaries"][name] = hashlib.sha256((self.pack / name).read_bytes()).hexdigest()
        self.files = self.run / "files"
        self.size = int(self.connection["FIXTURE_SIZE"])
        self.digest = self.connection["FIXTURE_DIGEST"]

    # -- recording -------------------------------------------------------
    def save(self):
        self.evidence.write_text(json.dumps(self.data, indent=2), encoding="utf-8")

    def check(self, name, condition, receipt):
        self.data["checks"][name] = bool(condition)
        self.data["receipts"][name] = receipt
        self.save()
        print(("PASS " if condition else "FAIL ") + name, flush=True)
        if not condition:
            raise AssertionError(name + ": " + json.dumps(receipt, default=str)[:4000])

    def human(self, name, text):
        self.data["human"][name] = text
        self.save()

    # -- Panel routes ----------------------------------------------------
    def api(self, path, body=None):
        payload = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, payload, headers={"X-Panel-Key": self.key, "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            return {"http_error": error.code, "body": error.read().decode(errors="replace")}

    def rules(self):
        rules, cursor = [], ""
        while True:
            page = self.api("/rights" + ("?cursor=" + urllib.parse.quote(cursor, safe="") if cursor else ""))
            rules.extend(page.get("Rules", []))
            if page.get("Complete", True):
                return page, rules
            cursor = page["Next"]

    def app_rules(self, program=None):
        program = str(program or self.app).lower()
        return [r for r in self.rules()[1] if r["Subject"]["Program"].lower() == program]

    def edit_rule(self, action, resource, permit, program=None):
        page, _ = self.rules()
        body = {"revision": page["Revision"], "account": self.account, "program": str(program or self.app), "action": action, "resource": resource}
        body.update({"edit": "set", "permit": permit} if permit is not None else {"edit": "revoke"})
        return self.api("/rights", body)

    def questions(self):
        page = self.api("/questions")
        return page.get("Records", []), page

    def pending(self, key, about=None):
        records, _ = self.questions()
        return [r for r in records if r["Key"] == key and not r["Option"] and (about is None or r["About"].lower() == str(about).lower())]

    def snapshot(self, operation):
        return next((s for s in self.api("/account-work").get("Snapshots", []) if s["Receipt"]["OperationID"] == operation), None)

    def source(self):
        with urllib.request.urlopen(self.connection["SOURCE_URL"] + "/stats", timeout=5) as response:
            return json.load(response)

    def logs(self, program):
        records, cursor = [], ""
        for _ in range(64):
            page = self.api("/logging?count=256&program=" + urllib.parse.quote(program) + ("&cursor=" + urllib.parse.quote(cursor, safe="") if cursor else ""))
            if page.get("outcome") != "page":
                return records, page
            records.extend(page["records"])
            if page["atEnd"]:
                return records, page
            cursor = page["next"]
        return records, page

    # -- apps ------------------------------------------------------------
    def command(self, url=None, *options):
        argv = [str(self.app), "--runtime-program", self.connection["RUNTIME_PROGRAM"], "--out", str(self.files), *options]
        return argv + ([url, str(self.size), self.digest] if url else [])

    def child(self, argv, role, timeout=60):
        process = subprocess.Popen(argv, env=self.env, text=True, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.data["children"].append({"role": role, "pid": process.pid, "exe": argv[0]})
        try:
            out, err = process.communicate(timeout=timeout)
        finally:
            if process.poll() is None:
                process.kill()  # Only this owned child PID.
                process.wait(timeout=5)
        return {"argv": argv[3:], "exit": process.returncode, "stdout": out, "stderr": err}

    def background(self, argv, role):
        out, err = self.run / (role + ".stdout.log"), self.run / (role + ".stderr.log")
        with out.open("w") as o, err.open("w") as e:
            process = subprocess.Popen(argv, env=self.env, text=True, stdin=subprocess.DEVNULL, stdout=o, stderr=e)
        self.data["children"].append({"role": role, "pid": process.pid, "exe": argv[0]})
        return process, out, err

    def submitted(self, path, process, seconds=15):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            match = re.search(r"^submitted (.+)$", path.read_text(), re.M)
            if match:
                return match.group(1)
            if process.poll() is not None:
                return None
            time.sleep(0.1)
        return None

    def running(self, operation, seconds=15):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            s = self.snapshot(operation)
            if s and s["State"] == "running" and s["Progress"]["Done"] > 0:
                return s
            time.sleep(0.2)
        return self.snapshot(operation)

    def runtime(self, verb):
        # Output goes to a file: the runtime it starts inherits the helper's
        # handles, and a pipe held open by the runtime would never reach EOF.
        log = self.run / ("runtime-" + verb + ".helper.log")
        with log.open("w") as out:
            done = subprocess.run(["powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(self.panel_review), "-Dir", str(self.run), "-Runtime", verb],
                                  stdout=out, stderr=subprocess.STDOUT, timeout=60, stdin=subprocess.DEVNULL)
        return subprocess.CompletedProcess(done.args, done.returncode, log.read_text(), "")

    def companion(self, interpreter, role):
        """Run python/companion.py with interpreter; return its result, the
        records attributed to that exact program and the questions' script-host
        sentences."""
        env = dict(self.env, PYTHONPATH=self.python_path, ABSTRACTION_IPC_PREFIX=str(self.ipc_prefix))
        script = Path(__file__).resolve().parent.parent / "python" / "companion.py"
        argv = [interpreter, str(script), "--runtime-program", self.connection["RUNTIME_PROGRAM"],
                self.connection["FIXTURE_URL"] + "?request=" + role, str(self.size), self.digest]
        result = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL)
        self.data["children"].append({"role": role, "exe": interpreter, "exit": result.returncode})
        records, _ = self.logs(Path(interpreter).name)
        own = [r for r in records if any(h.get("verified") and h.get("exe", "").lower() == interpreter.lower() for h in r["hops"])]
        programs = self.api("/questions").get("programs") or {}
        return {"result": result, "messages": [r["message"] for r in own], "attributed": len(own) >= 2,
                "script_host": {k: v.get("scriptHost", "") for k, v in programs.items()}}

    def file_digest(self, name):
        path = self.files / name
        return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest() if path.exists() else ""

    # -- the journey -----------------------------------------------------
    def identity(self):
        view = self.api("/identity")
        explicit = view.get("explicit", {})
        self.account = explicit.get("account", "")
        self.check("panel_binds_this_runtime", explicit.get("program", "").lower() == self.connection["RUNTIME_PROGRAM"].lower() and self.account, explicit)

    def register(self):
        result = self.child(self.command(None, "--register"), "register")
        asked = self.pending("application.self_registered", self.app)
        self.check("register_self_under_default", result["exit"] == 0 and "registered itself" in result["stdout"] and len(asked) == 1, {"app": result, "question": asked})
        self.human("register_keep", "The native presenter shows '" + asked[0]["Text"] + "'; the person clicks Keep. The controller answers Keep through Permissions → Questions waiting.")
        answer = self.api("/questions", {"action": "answer", "id": asked[0]["ID"], "option": "keep"})
        apps = self.api("/apps/registrations?source=applications&wait_ms=0")
        entry = next((e for e in apps.get("entries", []) if e["name"] == "pretend-lemonade"), None)
        self.check("keep_confirms_entry", answer.get("Outcome") == "answered" and entry is not None and entry["application"]["SelfRegistered"] is False and entry["title"] == "Pretend Lemonade",
                   {"answer": answer, "entry": entry})

    def default_download(self):
        before_q, before_s = len(self.questions()[0]), self.source()
        result = self.child(self.command(self.connection["FIXTURE_URL"]), "download")
        operation = re.search(r"^submitted (.+)$", result["stdout"], re.M)
        rules = self.app_rules()
        self.check("download_under_default_no_question", result["exit"] == 0 and operation and self.file_digest("fixture.bin") == self.digest
                   and self.source()["bytes"] - before_s["bytes"] == self.size and len(self.questions()[0]) == before_q
                   and not any(r["Action"] == "abstraction.job/acceptance.submit" for r in rules),
                   {"app": result, "digest": self.file_digest("fixture.bin"), "source_before": before_s, "source_after": self.source(), "app_rules": rules})
        self.operation = operation.group(1)
        before = self.source()
        replay = self.child(self.command(self.connection["FIXTURE_URL"]), "download-again")
        self.check("rerun_reconciles_same_operation", replay["exit"] == 0 and ("submitted " + self.operation) in replay["stdout"] and self.source() == before,
                   {"app": replay, "source": self.source()})

    def revocation(self):
        url = self.connection["SLOW_URL"]  # Slow-download.cmd
        process, out, err = self.background(self.command(url), "revocation")
        operation = self.submitted(out, process)
        running = self.running(operation) if operation else None
        self.check("slow_download_running_under_default", running is not None and running["State"] == "running", {"operation": operation, "snapshot": running})
        block = self.edit_rule("abstraction.job/acceptance.submit", "job:download", False)
        blocked_at = time.monotonic()
        try:
            process.wait(timeout=40)
        finally:
            if process.poll() is None:
                process.kill()  # Only this owned child PID.
        ended_after = round(time.monotonic() - blocked_at, 2)
        snap = self.snapshot(operation)
        moved = self.source()["bytes"]
        time.sleep(2)
        self.check("block_ends_running_download", block.get("Outcome") == "applied" and process.returncode != 0 and "rights:denied" in err.read_text()
                   and snap["State"] == "failed" and snap["Failure"]["Message"] == "rights:denied" and ended_after < 35 and self.source()["bytes"] == moved,
                   {"block": block, "seconds_to_end": ended_after, "stdout": out.read_text(), "stderr": err.read_text(), "snapshot": snap, "source": self.source()})
        self.revoked = (url, operation)
        before_q = len(self.questions()[0])
        refused = self.child(self.command(self.connection["FIXTURE_URL"] + "?request=while-blocked"), "while-blocked")
        self.check("blocked_app_refused_without_question", refused["exit"] != 0 and "forbidden" in refused["stderr"] and len(self.questions()[0]) == before_q, refused)

    def unblock(self):
        url, failed = self.revoked
        unblock = self.edit_rule("abstraction.job/acceptance.submit", "job:download", None)
        before = self.source()
        result = self.child(self.command(url), "after-unblock", timeout=90)
        operation = re.search(r"^submitted (.+)$", result["stdout"], re.M)
        snap = self.snapshot(operation.group(1)) if operation else None
        self.check("unblock_reruns_as_next_attempt", unblock.get("Outcome") == "applied" and result["exit"] == 0 and "attempt 0 failed: rights:denied; trying again as attempt 1" in result["stdout"]
                   and operation and operation.group(1) != failed and snap["Receipt"]["Identity"]["Attempt"] == 1 and snap["State"] == "complete"
                   and self.file_digest("slow.bin") == self.digest and self.source()["bytes"] - before["bytes"] >= self.size,
                   {"unblock": unblock, "app": result, "snapshot": snap, "failed_operation": failed, "source_before": before, "source_after": self.source()})

    def first_use(self):
        stored = self.api("/credentials", {"action": "add", "name": SECRET_NAME, "kind": "bearer", "targets": ["127.0.0.1"],
                                           "consumers": ["abstraction.download/http-execution@1"], "secret": SECRET_VALUE})
        refused = self.child(self.command(None, "--read-secret", SECRET_NAME), "read-refused")
        requests = [r for r in self.permission_requests() if not r["Option"]]
        in_panel = [r for r in self.questions()[0] if r["About"].lower() == str(self.app).lower() and r["Key"] == "rights.permission@2"]
        self.check("raw_read_refused_and_asked", stored.get("Outcome") == "stored" and refused["exit"] != 0 and "read mirror-token: forbidden" in refused["stderr"] and len(requests) == 1,
                   {"stored": {k: stored.get(k) for k in ("Outcome", "Revision")}, "app": refused, "permission_requests": requests, "panel_pending_lists_it": bool(in_panel)})
        self.human("first_use_allow", "The native presenter shows the typed permission request " + requests[0]["ID"] + " (raw Read of " + SECRET_NAME +
                   "); the person clicks Allow. Permissions → Questions waiting lists no typed permission request, so the controller saves the same "
                   "Allow as a Saved permissions rule (read saved secret value, one saved secret name, " + SECRET_NAME + ").")
        page, _ = self.rules()
        allow = self.api("/rights", {"edit": "set", "revision": page["Revision"], "account": self.account, "program": str(self.app),
                                     "action": "abstraction.credentials/secret.read", "resource_profile": "abstraction.credentials/named-secret@1",
                                     "scope": "one", "credential_name": SECRET_NAME, "permit": True})
        read = self.child(self.command(None, "--read-secret", SECRET_NAME), "read-allowed")
        still = [r["ID"] for r in self.permission_requests() if not r["Option"]]
        self.check("allow_takes_effect", allow.get("Outcome") == "applied" and read["exit"] == 0 and "read mirror-token: %d bytes" % len(SECRET_VALUE) in read["stdout"],
                   {"allow": allow, "app": read, "requests_pending_after_rule": still})

    def permission_requests(self):
        listed = subprocess.run([self.connection["RUNTIME_PROGRAM"], "questions", "permissions", "--endpoint", self.connection["ABSTRACTION_RUNTIME_ENDPOINT"],
                                 "--runtime-program", self.connection["RUNTIME_PROGRAM"], "--json"], capture_output=True, text=True, timeout=30, stdin=subprocess.DEVNULL)
        return [r for r in json.loads(listed.stdout or "{}").get("records", []) if r["Asker"].lower() == str(self.app).lower()]

    def ordinary_choice(self):
        result_file = self.files / "fixture.bin"
        completed = result_file.read_bytes()
        rules_before = self.app_rules()
        for option in ("keep", "replace"):
            result_file.write_bytes(OLD_OUTPUT)
            process, out, err = self.background(self.command(self.connection["FIXTURE_URL"], "--ask-before-replace", "--wait-output-choice"), "choice-" + option)
            record, deadline = None, time.monotonic() + 15
            while record is None and time.monotonic() < deadline and process.poll() is None:
                record = next((r for r in self.api("/choices").get("Records") or [] if str(r.get("State")) == "pending"), None)
                time.sleep(0.1)
            answer = self.api("/choices", {"action": "answer", "shown": record, "option": option}) if record else None
            try:
                process.wait(timeout=20)
            finally:
                if process.poll() is None:
                    process.kill()  # Only this owned child PID.
            want = completed if option == "replace" else OLD_OUTPUT
            self.check("ordinary_choice_" + option, record is not None and record["Asker"].lower() == "pretend-lemonade.exe" and process.returncode == 0
                       and result_file.read_bytes() == want and ("submitted " + self.operation) in out.read_text() and self.app_rules() == rules_before,
                       {"shown": record, "answer": answer, "stdout": out.read_text(), "stderr": err.read_text(), "app_rules": self.app_rules()})

    def notify(self):
        url = self.connection["SLOW_URL"] + "?request=notify"
        result = self.child(self.command(url, "--notify-when-done"), "notify-when-done")
        match = re.search(r"^notification (\S+): (\w+); this app can exit$", result["stdout"], re.M)
        self.check("notification_posted_app_exits", result["exit"] == 0 and match is not None and match.group(2) == "scheduled", result)
        self.notification = (url, match.group(1))

    def notification_after_exit(self):
        url, nid = self.notification
        deadline, state = time.monotonic() + 60, None
        while time.monotonic() < deadline:
            entry = next((n for n in self.api("/notifications").get("notifications", []) if n["id"] == nid), None)
            state = entry and entry["state"]
            if state == "posted":
                break
            time.sleep(1)
        read = self.child(self.command(url, "--notification"), "notification-read", timeout=90)
        self.check("notification_held_until_download_ends", state == "posted" and read["exit"] == 0 and "notification: posted" in read["stdout"],
                   {"notification": entry, "app": read})
        self.human("notification_click", "With the presenter running, Windows shows 'Download finished' from Pretend Lemonade with Copy now and Show in Activity. "
                   "The person clicks Copy now with no app running; Notification.cmd then copies slow.bin into run/files (a file copy, not the clipboard) and acknowledges it. "
                   "No Panel route acts on a notification, so the controller stops at 'posted'.")

    def reconnect_and_cancel(self):
        url = self.connection["SLOW_URL"] + "?request=reconnect"
        process, out, _ = self.background(self.command(url), "reconnect-first")
        operation = self.submitted(out, process)
        process.terminate()  # The app closes; only this owned child PID.
        process.wait(timeout=10)
        time.sleep(0.3)
        live = self.snapshot(operation)
        self.check("work_outlives_app", live is not None and live["State"] in ("pending", "running"), {"operation": operation, "snapshot": live})
        again, out2, err2 = self.background(self.command(url), "reconnect-again")
        same = self.submitted(out2, again)
        cancel = self.api("/account-work", {"operation": operation})
        try:
            again.wait(timeout=20)
        finally:
            if again.poll() is None:
                again.kill()  # Only this owned child PID.
        snap = self.snapshot(operation)
        self.check("reconnect_then_cancel", same == operation and again.returncode != 0 and "download cancelled" in err2.read_text() and snap["State"] == "cancelled",
                   {"cancel": cancel, "stdout": out2.read_text(), "stderr": err2.read_text(), "snapshot": snap})

    def restart(self):
        url = self.connection["SLOW_URL"] + "?request=restart"
        process, out, _ = self.background(self.command(url), "restart-first")
        operation = self.submitted(out, process)
        partial = self.running(operation) if operation else None
        process.terminate()  # Only this owned child PID.
        process.wait(timeout=10)
        stopped = self.runtime("stop")
        down = self.child(self.command(self.connection["FIXTURE_URL"]), "runtime-down", timeout=60)
        self.check("unavailable_runtime_reported", stopped.returncode == 0 and down["exit"] != 0 and "no download service" in down["stderr"] and partial and 0 < partial["Progress"]["Done"] < self.size,
                   {"stop": stopped.stdout + stopped.stderr, "app": down, "partial": partial})
        started = self.runtime("start")
        before = self.source()
        replay = self.child(self.command(self.connection["FIXTURE_URL"]), "replay-after-restart")
        after_replay = self.source()
        resumed = self.child(self.command(url), "restart-again", timeout=90)
        snap = self.snapshot(operation)
        same_key = [s for s in self.api("/account-work").get("Snapshots", []) if s["Receipt"]["Identity"]["Key"] == snap["Receipt"]["Identity"]["Key"]]
        self.check("restart_reconciles", started.returncode == 0 and replay["exit"] == 0 and ("submitted " + self.operation) in replay["stdout"]
                   and after_replay == before and resumed["exit"] == 0 and ("submitted " + operation) in resumed["stdout"] and snap["State"] == "complete" and len(same_key) == 1
                   and self.file_digest("slow.bin") == self.digest,
                   {"start": started.stdout + started.stderr, "replay": replay, "resumed": resumed, "snapshot": snap, "source_before": before,
                    "source_after_replay": after_replay, "source_after": self.source()})

    def logging(self):
        records, _ = self.logs("pretend-lemonade.exe")
        messages = [r["message"] for r in records]
        verified = all(any(h.get("verified") and h.get("exe", "").lower() == str(self.app).lower() for h in r["hops"]) for r in records)
        wanted = ["download requested", "download accepted", "download delivered", "download ended: failed: rights:denied", "download ended: cancelled"]
        self.check("panel_reads_attributed_app_logs", verified and all(w in messages for w in wanted) and any(m.startswith("download not accepted: forbidden") for m in messages),
                   {"count": len(records), "messages": sorted(set(messages))})
        self.history_reader()

    def python_clients(self):
        """The second client language, from the app's own interpreter copy and
        from the shared interpreter."""
        if self.python_path and self.python:
            # The app's own interpreter copy, inside the app's folder: its
            # records, questions and rules name that copy alone.
            own = str(self.python)
            first = self.companion(own, "python-own")
            asked = [r for r in self.pending("rights.first_use") if r["About"].lower() == own.lower() and "job:download" in r["Text"]]
            # Decided (VISION 2026-10-07): an app's own interpreter copy is a
            # script host and receives no default; it is asked at first use
            # with the script-host warning.
            sentence = first["script_host"].get(asked[0]["ID"]) if len(asked) == 1 else None
            self.check("python_own_interpreter_is_its_own_program", first["result"].returncode == 0 and "history: refused" in first["result"].stdout
                       and {"companion started"} <= set(first["messages"]) and first["attributed"]
                       and "download: forbidden" in first["result"].stdout and len(asked) == 1 and sentence and "runs scripts" in sentence,
                       {"stdout": first["result"].stdout, "stderr": first["result"].stderr, "messages": first["messages"], "download_question": asked,
                        "script_host_sentence": sentence})
            self.human("python_own_allow", "The native presenter asks whether " + own + " may download; the person clicks Allow. "
                       "The controller answers Allow through Permissions → Questions waiting.")
            answer = self.api("/questions", {"action": "answer", "id": asked[0]["ID"], "option": "allow"})
            again = self.companion(own, "python-own-allowed")
            rule = [r for r in self.app_rules(own) if r["Action"] == "abstraction.job/acceptance.submit"]
            shared_rule = [r for r in self.app_rules(sys.executable) if r["Action"] == "abstraction.job/acceptance.submit"]
            self.check("python_own_allow_covers_that_copy", again["result"].returncode == 0 and "download: accepted" in again["result"].stdout
                       and len(rule) == 1 and rule[0]["Permit"] and not shared_rule,
                       {"answer": answer.get("Rule") or answer, "stdout": again["result"].stdout, "rule": rule, "shared_interpreter_rule": shared_rule})
        if self.python_path:
            # The shared interpreter: every script it runs would act as it.
            shared = self.companion(sys.executable, "python-shared")
            asked = [r for r in self.pending("rights.first_use") if r["About"].lower() == sys.executable.lower() and "job:download" in r["Text"]]
            sentence = shared["script_host"].get(asked[0]["ID"]) if asked else None
            self.check("python_shared_interpreter_warned", shared["result"].returncode == 0 and "download: forbidden" in shared["result"].stdout
                       and shared["attributed"] and len(asked) == 1 and sentence and "runs scripts" in sentence,
                       {"stdout": shared["result"].stdout, "stderr": shared["result"].stderr, "messages": shared["messages"], "question": asked, "script_host_sentence": sentence})

    def history_reader(self):
        program = str(self.logging_app)
        denied = self.child([program, "--runtime-program", self.connection["RUNTIME_PROGRAM"]], "logging-denied", timeout=20)
        grant = self.edit_rule("abstraction.logging/history.read", "abstraction.logging/history", True, program)
        allowed = self.child([program, "--runtime-program", self.connection["RUNTIME_PROGRAM"]], "logging-allowed", timeout=20)
        self.check("history_reader_refused_then_authorized", denied["exit"] != 0 and "not permitted" in denied["stderr"] and grant.get("Outcome") == "applied"
                   and allowed["exit"] == 0 and "read back: worker started" in allowed["stdout"], {"denied": denied, "grant": grant, "allowed": allowed})

    def default_off(self):
        page = self.api("/rights/defaults")
        off = self.api("/rights/defaults", {"revision": page["Revision"], "name": "download", "on": False})
        url = self.connection["FIXTURE_URL"] + "?request=default-off"
        refused = self.child(self.command(url), "default-off")
        asked = [r for r in self.pending("rights.first_use", self.app) if "job:download" in r["Text"]]
        self.check("default_off_refuses_and_asks", off.get("Outcome") == "applied" and off["Current"]["On"] is False and refused["exit"] != 0
                   and "forbidden" in refused["stderr"] and len(asked) == 1, {"switch": off, "app": refused, "question": asked})
        answer = self.api("/questions", {"action": "answer", "id": asked[0]["ID"], "option": "allow"})
        allowed = self.child(self.command(url), "default-off-allowed")
        rule = [r for r in self.app_rules() if r["Action"] == "abstraction.job/acceptance.submit" and r["Resource"] == "job:download"]
        self.check("allow_writes_app_rule_and_downloads", allowed["exit"] == 0 and len(rule) == 1 and rule[0]["Permit"], {"answer": answer, "app": allowed, "rule": rule})
        page = self.api("/rights/defaults")
        on = self.api("/rights/defaults", {"revision": page["Revision"], "name": "download", "on": True})
        self.check("default_switched_back_on", on.get("Outcome") == "applied" and on["Current"]["On"] is True, on)


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], epilog=__doc__.split("\n\n", 1)[1], formatter_class=argparse.RawDescriptionHelpFormatter)
    entry = parser.add_mutually_exclusive_group(required=True)
    entry.add_argument("--run", type=Path, help="the folder panel_review.ps1 started")
    entry.add_argument("--native-request", type=Path, help="the fixed installed owner's hash-bound native acceptance request")
    parser.add_argument("--native-python", choices=("post", "resume"), help=argparse.SUPPRESS)
    parser.add_argument("--panel-review", type=Path, help="panel_review.ps1 for the runtime restart; default: the copy in the pack")
    parser.add_argument("--python-path", default="", help="PYTHONPATH with the OA Python packages; empty skips the Python companion")
    parser.add_argument("--ipc-prefix", type=Path, help="install prefix of the native abstraction_ipc library")
    parser.add_argument("--python", type=Path, help="the companion's own interpreter copy inside its own folder; the shared interpreter runs the companion too")
    parser.add_argument("--only", default="", help="comma-separated steps to run again on a folder an earlier run left, after identity: logging,default_off")
    args = parser.parse_args()
    if args.native_request:
        import native_core
        native_core.main(args.native_request, args.native_python)
        return
    if args.native_python:
        parser.error("--native-python requires --native-request")
    if args.python_path and not args.ipc_prefix:
        parser.error("--python-path needs --ipc-prefix")
    journey = Journey(args)
    if args.only and journey.evidence.exists():
        journey.data = dict(json.loads(journey.evidence.read_text(encoding="utf-8")), binaries=journey.data["binaries"])
    journey.save()
    steps = [journey.identity, journey.register, journey.default_download, journey.revocation, journey.unblock, journey.first_use,
             journey.ordinary_choice, journey.notify, journey.notification_after_exit, journey.reconnect_and_cancel, journey.restart,
             journey.logging, journey.python_clients, journey.default_off]
    if args.only:
        wanted = {"identity", *args.only.split(",")}
        unknown = wanted - {step.__name__ for step in steps}
        if unknown:
            parser.error("unknown steps: " + ", ".join(sorted(unknown)))
        steps = [step for step in steps if step.__name__ in wanted]
    for step in steps:
        journey.data.setdefault("steps", []).append({"step": step.__name__, "started": time.strftime("%Y-%m-%dT%H:%M:%S%z")})
        step()
    print(journey.evidence)


if __name__ == "__main__":
    main()
