"""Complete local lifecycle for one unchanged installed C++ application on Linux.

Run as root on a disposable Linux host with systemd as PID 1 (WSL Ubuntu here):

  python3 conformance/clients/linux_lifecycle/run.py --run \\
      --candidate abstraction-<version>-linux-amd64.tar.gz \\
      --predecessor abstraction-0.1.6-linux-amd64.tar.gz [--predecessor-sums SHA256SUMS] \\
      --prefix <installed abstraction_facade/abstraction_ipc/abstraction_download_request prefix> \\
      --python-packages <installed identity/facade/job/download-request packages> \\
      --ipc-shared-prefix <installed shared abstraction_ipc prefix> \\
      --go-client <built go_client/main.go> \\
      --out <evidence directory> --source-revision REV --predecessor-revision TEXT \\
      [--scenarios candidate,python,go,upgrade]

conformance/clients/linux_lifecycle/drive.py builds every input from a git archive
and fetches the published predecessor; scripts/check.sh --linux-lifecycle uses it.

The application (client.cpp) is built once against the installed C++ prefix and
used unchanged by every scenario. It names no provider, private path, daemon or
endpoint. This fixture owns the temporary accounts and user managers (through
installer/posix/qualify_linux.py), the loopback HTTP source, the shared lost-reply
fault (conformance/faults/lostreply) and every private path it inspects.

Scenario groups:
  candidate  account A: absence, installation and activation, resolution,
             acceptance, caller exit, lost acceptance reply, reconnect, refusal,
             host crash (SIGKILL)
  python     account A: the installed Python job client loses its Submit reply
             through the shared fault helper and reconciles; the C++ application,
             another program in the same account, cannot reconcile that receipt
  go         account A: the installed Go job client loses its Submit reply
             through the shared Go fault helper and reconciles; the C++
             application cannot reconcile that receipt
  upgrade    account B: install the predecessor, accept work, upgrade to the
             candidate mid-download; one writer, same receipt, exact result
Account A always runs absence, installation and resolution when candidate,
python or go is selected. Accounts and user managers are removed and verified absent.
Exit status is zero only when every selected scenario passed and cleanup completed.
"""
import argparse
import hashlib
import importlib.util
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import time
import types
import urllib.request
import uuid
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from workspace import source_revision, dry_run_stop, DRY_RUN_HELP

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
SHIM_SOURCE = ROOT / "conformance/faults/lostreply/lostreply.c"
MIB = 1 << 20
GROUPS = ("candidate", "python", "go", "upgrade")
ACCOUNT_A_GROUPS = {"candidate", "python", "go"}
# Whole-word patterns: a provider, daemon, unit, command or private location name.
FORBIDDEN_TOKENS = (r"\bopenabstractions\b", r"\bjobd\b", r"\bruntime-v1\b", r"\.local/", r"\bsystemctl\b",
                    r"\babstraction-runtime\b", r"/run/user", r"\bserve\s+runtime\b", r"\.sock\b")


def load_qualification():
    spec = importlib.util.spec_from_file_location("qualify_linux", ROOT / "installer/posix/qualify_linux.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def leaked_tokens(source):
    code = re.sub(r"//[^\n]*", "", source)
    code = re.sub(r"#[^\n]*", "", code) if source.lstrip().startswith(('"""', "import", "#")) else code
    return [token for token in FORBIDDEN_TOKENS if re.search(token, code)]


class Lifecycle:
    def __init__(self, args):
        self.args = args
        self.groups = set(args.scenarios.split(","))
        self.q = load_qualification()
        self.out = Path(args.out).resolve()
        self.report = {"scenarios": [], "cleanup": {}, "selected": sorted(self.groups)}
        self.source = None
        self.app = self.shim = self.go_client = None

    # -- evidence ------------------------------------------------------------
    def verdict(self, scenario, checks, **detail):
        passed = bool(checks) and all(checks.values())
        entry = {"scenario": scenario, "verdict": "PASS" if passed else "FAIL", "checks": checks, **detail}
        self.report["scenarios"].append(entry)
        print(f"{entry['verdict']} {scenario} {json.dumps(checks, sort_keys=True)}", flush=True)
        return passed

    def failure(self, scenario, error):
        self.report["scenarios"].append({"scenario": scenario, "verdict": "FAIL", "checks": {}, "error": str(error)})
        print(f"FAIL {scenario} error={error}", flush=True)

    # -- application and helpers -------------------------------------------------
    def build_application(self):
        source = (HERE / "client.cpp").read_text(encoding="utf-8")
        python_source = (HERE / "python_client.py").read_text(encoding="utf-8")
        leaked = leaked_tokens(source)
        python_leaked = [t for t in FORBIDDEN_TOKENS if re.search(t, re.sub(r'"""[\s\S]*?"""', "", python_source))]
        go_source = (HERE / "go_client/main.go").read_text(encoding="utf-8")
        # Go import paths carry the publishing organization's name; the scan reads the code.
        go_leaked = leaked_tokens(re.sub(r'"github\.com/openabstractions/[^"\n]*"', '""', go_source))
        build = self.out / "app-build"
        self.run(["cmake", "-S", HERE, "-B", build, f"-DCMAKE_PREFIX_PATH={self.args.prefix}", "-DCMAKE_BUILD_TYPE=Release"], 300)
        self.run(["cmake", "--build", build, "-j4"], 600)
        app_dir = self.out / "app"
        app_dir.mkdir(parents=True, exist_ok=True)
        self.app = app_dir / "lifecycle_consumer"
        shutil.copy2(build / "lifecycle_consumer", self.app)
        self.python_client = app_dir / "python_client.py"
        shutil.copy2(HERE / "python_client.py", self.python_client)
        self.shim = app_dir / "lostreply.so"
        self.run(["gcc", "-shared", "-fPIC", "-O2", "-o", self.shim, SHIM_SOURCE, "-ldl"], 120)
        executables = [self.out, app_dir, self.app, self.shim, self.python_client]
        if self.args.go_client:
            self.go_client = app_dir / "lifecycle_go_client"
            shutil.copy2(self.args.go_client, self.go_client)
            executables.append(self.go_client)
        for path in executables:
            os.chmod(path, 0o755)
        needed = self.run(["readelf", "-d", self.app], 30).stdout
        self.report["application"] = {
            "source_sha256": hashlib.sha256(source.encode()).hexdigest(),
            "binary_sha256": sha256_file(self.app),
            "python_client_sha256": hashlib.sha256(python_source.encode()).hexdigest(),
            "fault_helper_sha256": sha256_file(SHIM_SOURCE),
            "go_client_source_sha256": hashlib.sha256(go_source.encode()).hexdigest(),
            "go_client_binary_sha256": sha256_file(self.go_client) if self.go_client else None,
            "forbidden_tokens_in_source": leaked, "forbidden_tokens_in_python_client": python_leaked,
            "forbidden_tokens_in_go_client": go_leaked,
            "dynamic_needed": re.findall(r"\[(.+?)\]", "\n".join(l for l in needed.splitlines() if "NEEDED" in l)),
            "prefix_packages": sorted(p.name for p in (Path(self.args.prefix) / "lib/cmake").iterdir()),
        }
        self.verdict("application: one installed-package build names no provider, private path or daemon",
                     {"no_forbidden_tokens_in_source": not leaked, "no_forbidden_tokens_in_python_client": not python_leaked,
                      "no_forbidden_tokens_in_go_client": not go_leaked,
                      "links_only_installed_prefix_packages": set(self.report["application"]["prefix_packages"]) <=
                      {"abstraction_facade", "abstraction_facade_resolution", "abstraction_ipc", "abstraction_download_request", "abstraction_job_acceptance"}},
                     **self.report["application"])

    def run(self, argv, timeout):
        result = subprocess.run([str(a) for a in argv], capture_output=True, text=True, timeout=timeout)
        if result.returncode:
            raise RuntimeError(f"{argv[0]} failed: {(result.stdout + result.stderr)[-2000:]}")
        return result

    @staticmethod
    def parse(result):
        lines, parsed = [], {}
        for line in result.stdout.splitlines():
            words = line.split(" ")
            if words[0] == "RESULT" and len(words) >= 3:
                body = bytes.fromhex(words[2])
                parsed["RESULT"] = {"size": int(words[1]), "bytes": len(body), "sha256": hashlib.sha256(body).hexdigest()}
                lines.append(f"RESULT {words[1]} <{len(body)} bytes sha256 {parsed['RESULT']['sha256']}>")
                continue
            parsed.setdefault(words[0], words[1:])
            lines.append(line)
        return {"rc": result.returncode, "lines": lines, "stderr": result.stderr.strip()[-500:], **parsed}

    def call(self, account, *args, extra=None, timeout=120):
        return self.parse(account.user([self.app, *args], timeout=timeout, check=False, extra=extra))

    def python(self, account, *args, extra=None, timeout=120):
        env = {"ABSTRACTION_IPC_PREFIX": str(self.args.ipc_shared_prefix), "PYTHONDONTWRITEBYTECODE": "1", **(extra or {})}
        return self.parse(account.user(["/usr/bin/python3", "-I", self.python_client, self.args.python_packages, *args],
                                       timeout=timeout, check=False, extra=env))

    def go(self, account, *args, timeout=120):
        return self.parse(account.user([self.go_client, *args], timeout=timeout, check=False))

    def fault_env(self, marker, method="Submit"):
        return {"LD_PRELOAD": str(self.shim), "OA_LOST_REPLY_MARKER": str(marker), "OA_LOST_REPLY_METHOD": method}

    # -- fixture source ----------------------------------------------------------
    def start_source(self):
        ready = self.out / "source-ready.json"
        specs = ["plain-accept:262147", "plain-lost:262147", "plain-python:262147", "plain-go:262147", "refused:1024", "conflict:1024",
                 f"gated-exit:{3 * MIB}:{MIB}:{MIB}", f"gated-crash:{3 * MIB}:{MIB}:{MIB}", f"gated-upgrade:{3 * MIB}:{MIB}:{MIB}"]
        argv = [sys.executable, HERE / "source.py", "--ready", ready] + [a for s in specs for a in ("--artifact", s)]
        self.source = subprocess.Popen([str(a) for a in argv], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        for _ in range(200):
            if ready.exists() and ready.stat().st_size:
                break
            time.sleep(0.05)
        info = json.loads(ready.read_text())
        self.port, self.artifacts = info["port"], info["artifacts"]

    def url(self, name, credentials=""):
        return f"http://{credentials}127.0.0.1:{self.port}/{name}"

    def control(self, *path):
        with urllib.request.urlopen(f"http://127.0.0.1:{self.port}/control/" + "/".join(path), timeout=10) as response:
            return json.load(response)

    def transfers(self, name):
        return [e for e in self.control("log") if e["artifact"] == name and e["method"] == "GET" and e["range"] != "bytes=0-0"]

    @staticmethod
    def overlapping(entries):
        spans = sorted((e["started"], e["ended"] or time.time()) for e in entries)
        return any(later[0] < earlier[1] for earlier, later in zip(spans, spans[1:]))

    def wait_holding(self, name, seconds=60):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if self.control("status", name)["holding"]:
                return True
            time.sleep(0.1)
        return False

    def wait_transferred(self, name, seconds=60):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if any(e["completed"] and e["end_offset"] == self.artifacts[name]["size"] for e in self.transfers(name)):
                return True
            time.sleep(0.1)
        return False

    def exact(self, name, output):
        result = output.get("RESULT") or {}
        return result.get("sha256") == self.artifacts[name]["digest"] and result.get("bytes") == self.artifacts[name]["size"]

    def request_args(self, name, credentials=""):
        art = self.artifacts[name]
        return [self.url(name, credentials), str(art["size"]), "sha256:" + art["digest"]]

    def submit(self, account, name, key, credentials="", extra=None):
        return self.call(account, "submit", *self.request_args(name, credentials), key, extra=extra)

    # -- accounts --------------------------------------------------------------
    def account(self, name):
        args = types.SimpleNamespace(account=name, python="", ipc_prefix="", archive="", evidence="", source_revision="")
        account = self.q.Qualification(args)
        account.root_before = account.root_abstraction_units()
        account.create_account()
        work = Path(account.home) / "lifecycle"
        account.user(["mkdir", "-p", work])
        return account, work

    def extract(self, account, archive, destination):
        account.user(["mkdir", "-p", destination])
        staged = destination / Path(archive).name
        shutil.copyfile(archive, staged)
        os.chown(staged, account.uid, account.gid)
        account.user(["tar", "-xzf", staged, "-C", destination], timeout=120)
        packages = [p for p in destination.iterdir() if p.is_dir()]
        if len(packages) != 1:
            raise RuntimeError(f"expected one package directory in {destination}")
        return packages[0]

    def install(self, account, package, log_name):
        started = time.time()
        result = account.user([package / "install.sh"], timeout=300, check=False)
        log = self.out / log_name
        log.write_text(f"$ {package}/install.sh (exit {result.returncode}, {time.time() - started:.1f}s)\n"
                       f"--- stdout ---\n{result.stdout}--- stderr ---\n{result.stderr}", encoding="utf-8")
        return result

    def runtime(self, account):
        return account.show("abstraction-runtime.service", "LoadState", "ActiveState", "MainPID", "NRestarts",
                            "UnitFileState", "ExecMainStartTimestamp")

    def image(self, pid):
        return sha256_file(f"/proc/{pid}/exe")

    def job_records(self, account):
        root = Path(account.home) / ".local/share/openabstractions/runtime-v1/jobs"
        return sorted(p.name for p in (root / "jobs").glob("*.json")) if (root / "jobs").is_dir() else []

    # -- account A ----------------------------------------------------------------
    def account_a(self, a, work):
        absent = self.call(a, "absent")
        self.verdict("absence: typed refusal before installation",
                     {"typed_refusal": "REFUSED" in absent, "no_runtime_unit": self.runtime(a).get("LoadState") == "not-found",
                      "no_installed_program": not (Path(a.home) / ".local/bin/openabstractions").exists()},
                     output=absent)

        package = self.extract(a, self.args.candidate, work / "candidate")
        installed = self.install(a, package, "install-candidate-account-a.log")
        unit = self.runtime(a)
        ready, status = a.ready_status(20)
        self.verdict("installation and activation without manually starting daemons",
                     {"install_exit_zero": installed.returncode == 0, "runtime_active": unit.get("ActiveState") == "active",
                      "runtime_enabled_for_login": unit.get("UnitFileState") == "enabled",
                      "status_all_contracts_resolved": ready,
                      "process_image_is_candidate": unit.get("MainPID", "0") != "0" and self.image(unit["MainPID"]) == self.candidate_hash},
                     unit=unit, status=status, log="install-candidate-account-a.log")

        resolved = self.call(a, "resolve")
        self.verdict("resolution through default bootstrap and installation-selected trust",
                     {"resolved": "RESOLVED" in resolved and len(resolved["RESOLVED"]) == 2}, output=resolved)
        if "candidate" in self.groups:
            self.candidate_scenarios(a, work)
        if "python" in self.groups:
            self.python_scenario(a, work)
        if "go" in self.groups:
            self.go_scenario(a, work)

    def candidate_scenarios(self, a, work):
        key = "lifecycle-accept-" + uuid.uuid4().hex[:8]
        accepted = self.submit(a, "plain-accept", key)
        receipt = accepted.get("ACCEPTED", [])
        result = self.call(a, "result", key, receipt[2], receipt[0], "60") if len(receipt) == 4 else {}
        again = self.submit(a, "plain-accept", key)
        self.accept_receipt = (key, receipt)
        self.verdict("acceptance and exact result",
                     {"accepted": len(receipt) == 4, "fresh_process_reconciles_same_operation": result.get("RECONCILED", [None])[0] == receipt[0] if receipt else False,
                      "exact_result_bytes": self.exact("plain-accept", result),
                      "duplicate_submit_returns_original_receipt": again.get("ACCEPTED", [None])[:3] == receipt[:3],
                      "one_source_transfer": len(self.transfers("plain-accept")) == 1},
                     submit=accepted, result=result, duplicate=again, transfers=self.transfers("plain-accept"))

        key = "lifecycle-exit-" + uuid.uuid4().hex[:8]
        accepted = self.submit(a, "gated-exit", key)
        receipt = accepted.get("ACCEPTED", [])
        exited = accepted["rc"] == 0 and len(receipt) == 4
        holding = self.wait_holding("gated-exit")
        during = self.call(a, "observe", key, receipt[2]) if exited else {}
        self.control("release", "gated-exit")
        transferred = self.wait_transferred("gated-exit")
        after = self.call(a, "observe", key, receipt[2]) if exited else {}
        result = self.call(a, "result", key, receipt[2], receipt[0], "60") if exited else {}
        transfers = self.transfers("gated-exit")
        self.verdict("caller exit while work runs; work continues and a new process gets the same receipt and result",
                     {"caller_exited_after_acceptance": exited, "work_in_progress_after_caller_exit": holding and during.get("STATE", [""])[0] in ("pending", "running"),
                      "work_completed_without_any_caller": transferred and after.get("STATE", [""])[0] == "complete",
                      "same_operation": result.get("RECONCILED", [None])[0] == receipt[0] if receipt else False,
                      "exact_result_bytes": self.exact("gated-exit", result),
                      "single_uninterrupted_transfer": len(transfers) == 1 and transfers[0]["completed"]},
                     submit=accepted, during=during, after=after, result=result, transfers=transfers)

        key = "lifecycle-lost-" + uuid.uuid4().hex[:8]
        marker = work / "lost-reply.marker"
        lost = self.submit(a, "plain-lost", key, extra=self.fault_env(marker))
        receipt = lost.get("RECONCILED", [])
        discarded = marker.read_text().strip() if marker.exists() else ""
        body = re.search(r"and (\d+) reply body bytes", discarded)
        result = self.call(a, "result", key, receipt[2], receipt[0], "60") if len(receipt) == 4 else {}
        duplicate = self.submit(a, "plain-lost", key)
        self.verdict("lost acceptance reply: committed reply discarded, application reconciles the same receipt",
                     {"reply_was_sent_and_discarded": bool(body) and int(body.group(1)) > 0,
                      "application_saw_unknown_outcome": "UNKNOWN" in lost and "ACCEPTED" not in lost,
                      "reconciled_accepted_receipt": len(receipt) == 4,
                      "duplicate_submit_returns_same_operation": duplicate.get("ACCEPTED", [None])[0] == (receipt[0] if receipt else "missing"),
                      "exact_result_bytes": self.exact("plain-lost", result),
                      "one_source_transfer": len(self.transfers("plain-lost")) == 1},
                     submit=lost, marker=discarded, result=result, duplicate=duplicate)

        before = self.runtime(a)
        a.user(["systemctl", "--user", "restart", "abstraction-runtime.service"], timeout=60)
        restarted = a.wait(lambda: (lambda s: s.get("ActiveState") == "active" and s.get("MainPID") not in ("0", before["MainPID"]) and s)(self.runtime(a)), 30, 0.2)
        key, receipt = self.accept_receipt
        ready, status = a.ready_status(20)
        result = self.call(a, "result", key, receipt[2], receipt[0], "60")
        self.verdict("reconnect after runtime restart: same receipt and result",
                     {"runtime_process_replaced": bool(restarted), "status_ready_again": ready,
                      "same_operation": result.get("RECONCILED", [None])[0] == receipt[0],
                      "exact_result_bytes": self.exact("plain-accept", result),
                      "no_new_source_transfer": len(self.transfers("plain-accept")) == 1},
                     before=before, after=restarted, result=result)

        guarantee = self.call(a, "refuse-guarantee", "abstraction.job/not-provided@1")
        credentialed = self.submit(a, "refused", "lifecycle-refused-" + uuid.uuid4().hex[:8], credentials="user:secret@")
        conflict_key, _ = self.accept_receipt
        conflict = self.submit(a, "conflict", conflict_key)
        self.verdict("refusal: typed denials without work or source access",
                     {"unprovided_guarantee_refused_at_resolution": guarantee.get("REFUSED", [""])[0] == "resolution",
                      "credentialed_request_not_accepted": credentialed.get("NOT_ACCEPTED", [""])[0] in ("invalid", "definitely_not_accepted"),
                      "reused_identity_with_different_request_is_key_conflict": conflict.get("NOT_ACCEPTED", [""])[0] == "key_conflict",
                      "refused_requests_reached_no_source": not self.transfers("refused") and not self.transfers("conflict")},
                     guarantee=guarantee, credentialed=credentialed, conflict=conflict)

        key = "lifecycle-crash-" + uuid.uuid4().hex[:8]
        accepted = self.submit(a, "gated-crash", key)
        receipt = accepted.get("ACCEPTED", [])
        holding = self.wait_holding("gated-crash")
        before = self.runtime(a)
        image_matches = before.get("MainPID", "0") != "0" and self.image(before["MainPID"]) == self.candidate_hash
        a.user(["systemctl", "--user", "kill", "--kill-whom=main", "--signal=KILL", "abstraction-runtime.service"], timeout=30)
        restarted = a.wait(lambda: (lambda s: s.get("ActiveState") == "active" and s.get("MainPID") not in ("0", before["MainPID"])
                                    and int(s.get("NRestarts", "0")) == int(before["NRestarts"]) + 1 and s)(self.runtime(a)), 40, 0.2)
        self.control("release", "gated-crash")
        result = self.call(a, "result", key, receipt[2], receipt[0], "90") if len(receipt) == 4 else {}
        transfers = self.transfers("gated-crash")
        self.verdict("host crash (SIGKILL): runtime recovers and returns the same receipt and result",
                     {"accepted_before_crash": len(receipt) == 4, "transfer_in_progress_at_crash": holding and image_matches,
                      "manager_restarted_runtime_once": bool(restarted),
                      "same_operation": result.get("RECONCILED", [None])[0] == (receipt[0] if receipt else "missing"),
                      "exact_result_bytes": self.exact("gated-crash", result),
                      "interrupted_then_resumed_transfer": len(transfers) >= 2 and not transfers[0]["completed"] and transfers[-1]["completed"],
                      "one_writer_no_overlapping_transfers": not self.overlapping(transfers)},
                     before=before, after=restarted, result=result, transfers=transfers)

    def python_scenario(self, a, work):
        key = "lifecycle-python-lost-" + uuid.uuid4().hex[:8]
        marker = work / "python-lost-reply.marker"
        lost = self.python(a, "submit", *self.request_args("plain-python"), key, extra=self.fault_env(marker))
        receipt = lost.get("RECONCILED", [])
        discarded = marker.read_text().strip() if marker.exists() else ""
        body = re.search(r"and (\d+) reply body bytes", discarded)
        result = self.python(a, "result", key, receipt[2], receipt[0], "60") if len(receipt) == 4 else {}
        duplicate = self.python(a, "submit", *self.request_args("plain-python"), key)
        # Acceptance scope is the authenticated caller program. The C++ application
        # is another program of the same account: its Reconcile of the Python
        # identity must not see that receipt or its bytes.
        other_program = self.call(a, "result", key, receipt[2], receipt[0], "60") if len(receipt) == 4 else {}
        self.verdict("lost acceptance reply through the shared fault helper with the installed Python job client",
                     {"reply_was_sent_and_discarded": bool(body) and int(body.group(1)) > 0,
                      "python_client_saw_unknown_outcome": "UNKNOWN" in lost and "ACCEPTED" not in lost,
                      "python_client_reconciled_accepted_receipt": len(receipt) == 4,
                      "duplicate_python_submit_returns_same_operation": duplicate.get("ACCEPTED", [None])[0] == (receipt[0] if receipt else "missing"),
                      "python_exact_result_bytes": self.exact("plain-python", result),
                      "another_program_cannot_reconcile_the_python_receipt": "NOT_ACCEPTED" in other_program
                      and "RECONCILED" not in other_program and "RESULT" not in other_program,
                      "one_source_transfer": len(self.transfers("plain-python")) == 1},
                     submit=lost, marker=discarded, result=result, duplicate=duplicate, other_program=other_program)

    def go_scenario(self, a, work):
        key = "lifecycle-go-lost-" + uuid.uuid4().hex[:8]
        lost = self.go(a, "submit-lost", *self.request_args("plain-go"), key)
        # UNKNOWN go <errors.Is ErrLostReply> <fired> <discarded reply bytes>
        unknown = lost.get("UNKNOWN", [])
        fault = len(unknown) == 4 and unknown[0] == "go"
        receipt = lost.get("RECONCILED", [])
        result = self.go(a, "result", key, receipt[2], receipt[0], "60") if len(receipt) == 4 else {}
        duplicate = self.go(a, "submit", *self.request_args("plain-go"), key)
        # The C++ application is another program of the same account.
        other_program = self.call(a, "result", key, receipt[2], receipt[0], "60") if len(receipt) == 4 else {}
        self.verdict("lost acceptance reply through the shared Go fault helper with the installed Go job client",
                     {"reply_was_sent_and_discarded": fault and unknown[2] == "true" and unknown[3].isdigit() and int(unknown[3]) > 0,
                      "go_client_saw_lost_reply_error": fault and unknown[1] == "true" and "ACCEPTED" not in lost,
                      "go_client_reconciled_accepted_receipt": len(receipt) == 4,
                      "duplicate_go_submit_returns_same_operation": duplicate.get("ACCEPTED", [None])[0] == (receipt[0] if receipt else "missing"),
                      "go_exact_result_bytes": self.exact("plain-go", result),
                      "another_program_cannot_reconcile_the_go_receipt": "NOT_ACCEPTED" in other_program
                      and "RECONCILED" not in other_program and "RESULT" not in other_program,
                      "one_source_transfer": len(self.transfers("plain-go")) == 1},
                     submit=lost, result=result, duplicate=duplicate, other_program=other_program)

    # -- account B ----------------------------------------------------------------
    def upgrade_scenario(self, b, work):
        predecessor = self.extract(b, self.args.predecessor, work / "predecessor")
        installed = self.install(b, predecessor, "install-predecessor-account-b.log")
        unit = self.runtime(b)
        ready, status = b.ready_status(20)
        resolved = self.call(b, "resolve")
        key = "lifecycle-upgrade-" + uuid.uuid4().hex[:8]
        accepted = self.submit(b, "gated-upgrade", key)
        receipt = accepted.get("ACCEPTED", [])
        holding = self.wait_holding("gated-upgrade")
        during = self.call(b, "observe", key, receipt[2]) if len(receipt) == 4 else {}
        old = self.runtime(b)
        old_image = self.image(old["MainPID"]) if old.get("MainPID", "0") != "0" else ""
        candidate = self.extract(b, self.args.candidate, work / "candidate")
        upgrade_started = time.time()
        upgraded = self.install(b, candidate, "upgrade-candidate-account-b.log")
        new = self.runtime(b)
        old_gone = not Path(f"/proc/{old.get('MainPID', '0')}").exists() or old.get("MainPID") == "0"
        new_image = self.image(new["MainPID"]) if new.get("MainPID", "0") != "0" else ""
        self.control("release", "gated-upgrade")
        result = self.call(b, "result", key, receipt[2], receipt[0], "120") if len(receipt) == 4 else {}
        transfers = self.transfers("gated-upgrade")
        before_upgrade = [t for t in transfers if t["started"] < upgrade_started]
        after_upgrade = [t for t in transfers if t["started"] >= upgrade_started]
        records = self.job_records(b)
        checks = {"predecessor_installed_and_ready": installed.returncode == 0 and unit.get("ActiveState") == "active" and ready,
                  "predecessor_runtime_image": old_image == self.predecessor_hash,
                  "application_resolved_predecessor": "RESOLVED" in resolved,
                  "work_accepted_by_predecessor_and_in_progress": len(receipt) == 4 and holding and during.get("STATE", [""])[0] in ("pending", "running"),
                  "candidate_install_over_live_predecessor_exit_zero": upgraded.returncode == 0,
                  "predecessor_process_stopped": old_gone, "candidate_runtime_image": new_image == self.candidate_hash,
                  "same_operation_owner_and_epoch": result.get("RECONCILED", [None])[:3] == receipt[:3] if receipt else False,
                  "exact_result_bytes": self.exact("gated-upgrade", result),
                  "predecessor_transfer_interrupted_before_candidate_resumed": bool(before_upgrade) and bool(after_upgrade)
                  and all(not t["completed"] for t in before_upgrade) and after_upgrade[-1]["completed"],
                  "one_writer_no_overlapping_transfers": not self.overlapping(transfers),
                  "one_job_record": len(records) == 1}
        if self.args.predecessor_sums:
            checks["predecessor_is_published_asset_verified_by_sha256sums"] = self.report["inputs"]["predecessor"]["published_checksum_verified"]
        self.verdict("upgrade from the released predecessor during accepted work: one writer, same receipt, exact result", checks,
                     predecessor_unit=unit, predecessor_status=status, submit=accepted, during=during, old_runtime=old, new_runtime=new,
                     result=result, transfers=transfers, job_records=records,
                     logs=["install-predecessor-account-b.log", "upgrade-candidate-account-b.log"])

    # -- driver ----------------------------------------------------------------
    def verify_sums(self):
        name = Path(self.args.predecessor).name
        actual = sha256_file(self.args.predecessor)
        for line in Path(self.args.predecessor_sums).read_text(encoding="utf-8").splitlines():
            fields = line.split()
            if len(fields) == 2 and fields[1].lstrip("*") == name:
                return fields[0] == actual
        return False

    def execute(self):
        self.out.mkdir(parents=True, exist_ok=True)
        os.chmod(self.out, 0o755)
        self.candidate_hash = self.payload_hash(self.args.candidate)
        self.predecessor_hash = self.payload_hash(self.args.predecessor)
        self.report["inputs"] = {
            "source_revision": self.args.source_revision, "predecessor_revision": self.args.predecessor_revision,
            "candidate": {"archive": Path(self.args.candidate).name, "sha256": sha256_file(self.args.candidate),
                          "bytes": Path(self.args.candidate).stat().st_size, "runtime_sha256": self.candidate_hash},
            "predecessor": {"archive": Path(self.args.predecessor).name, "sha256": sha256_file(self.args.predecessor),
                            "bytes": Path(self.args.predecessor).stat().st_size, "runtime_sha256": self.predecessor_hash,
                            "published_checksum_verified": self.verify_sums() if self.args.predecessor_sums else None},
            "platform": {"kernel": os.uname().release, "systemd": subprocess.run(["systemctl", "--version"], capture_output=True, text=True).stdout.splitlines()[0]}}
        accounts = []
        try:
            self.build_application()
            self.start_source()
            suffix = uuid.uuid4().hex[:6]
            plan = []
            if self.groups & ACCOUNT_A_GROUPS:
                plan.append(("oalca" + suffix, self.account_a))
            if "upgrade" in self.groups:
                plan.append(("oalcb" + suffix, self.upgrade_scenario))
            for label, scenarios in plan:
                account = None
                try:
                    account, work = self.account(label)
                    accounts.append(account)
                    scenarios(account, work)
                except Exception as error:  # record and continue to cleanup and the next account
                    self.failure(f"{label}: scenario execution stopped", error)
                    if account is None:
                        break
        finally:
            for account in accounts:
                try:
                    account.cleanup(account.root_before)
                finally:
                    self.report["cleanup"][account.account] = account.evidence["cleanup"]
            if self.source:
                try:
                    (self.out / "source-log.json").write_text(json.dumps(self.control("log"), indent=2) + "\n", encoding="utf-8")
                    self.report["source_log"] = "source-log.json"
                except Exception as error:
                    self.report["source_log"] = f"unavailable: {error}"
                self.source.terminate()
                self.source.wait(timeout=10)
            self.report["passed"] = (bool(self.report["scenarios"]) and all(s["verdict"] == "PASS" for s in self.report["scenarios"])
                                     and bool(self.report["cleanup"]) and all(c.get("complete") for c in self.report["cleanup"].values()))
            (self.out / "evidence.json").write_text(json.dumps(self.report, indent=2, sort_keys=True, default=str) + "\n", encoding="utf-8")
        print(("PASS" if self.report["passed"] else "FAIL") + f" Linux complete lifecycle; evidence {self.out / 'evidence.json'}", flush=True)
        return 0 if self.report["passed"] else 1

    def payload_hash(self, archive):
        with tarfile.open(archive) as tar:
            member = next(m for m in tar.getmembers() if m.name.endswith("payload/.local/bin/openabstractions"))
            return hashlib.sha256(tar.extractfile(member).read()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--run", action="store_true", help="create temporary accounts and run the selected scenarios")
    parser.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
    parser.add_argument("--candidate", help="current Linux package tarball")
    parser.add_argument("--predecessor", help="released predecessor Linux package tarball")
    parser.add_argument("--predecessor-sums", help="published SHA256SUMS the predecessor tarball must match")
    parser.add_argument("--prefix", help="installed C++ prefix with abstraction_facade, abstraction_ipc and abstraction_download_request")
    parser.add_argument("--python-packages", help="installed identity, facade, job and download-request Python packages (python group)")
    parser.add_argument("--ipc-shared-prefix", help="installed shared abstraction_ipc prefix for the Python client (python group)")
    parser.add_argument("--go-client", help="Linux build of go_client/main.go against the workspace with the lost-reply helper (go group)")
    parser.add_argument("--out", help="evidence directory (created)")
    parser.add_argument("--source-revision", default="unrecorded")
    parser.add_argument("--predecessor-revision", default="unrecorded")
    parser.add_argument("--scenarios", default=",".join(GROUPS), help="comma list of " + ", ".join(GROUPS))
    args = parser.parse_args()
    if not (args.run or args.dry_run):
        parser.print_help()
        return 0
    source_revision()
    if args.dry_run: dry_run_stop('linux_lifecycle', args)
    groups = set(args.scenarios.split(","))
    if not groups or not groups <= set(GROUPS):
        parser.error("--scenarios takes a comma list of " + ", ".join(GROUPS))
    required = [("--candidate", args.candidate), ("--prefix", args.prefix), ("--out", args.out)]
    if "upgrade" in groups:
        required.append(("--predecessor", args.predecessor))
    if "python" in groups:
        required += [("--python-packages", args.python_packages), ("--ipc-shared-prefix", args.ipc_shared_prefix)]
    if "go" in groups:
        required.append(("--go-client", args.go_client))
    missing = [flag for flag, value in required if not value]
    if missing:
        parser.error("--run requires " + ", ".join(missing))
    if not args.predecessor:
        args.predecessor = args.candidate
    if not sys.platform.startswith("linux") or os.geteuid() != 0:
        parser.error("run as root on Linux; the fixture creates and removes temporary accounts")
    return Lifecycle(args).execute()


if __name__ == "__main__":
    sys.exit(main())
