"""Outside Python client: argv contains installed package root and bootstrap only."""
import sys
import time
sys.path.insert(0, sys.argv[1])
from abstraction.facade.client import Machine
from abstraction.config.rec import RunOverrides

machine = Machine(sys.argv[2], deadline=time.monotonic() + 8)
reader = machine.resolve_config(scope="local")
value = reader.Read(RunOverrides())
assert value.store == "existing-store" and value.nas_store == "existing-nas"
assert value.off == {"nas": "maintenance"}
assert value.origins.store.rung == "user"
editor = machine.resolve_config_editor(scope="local")
original = editor.ReadUser()
original.values.log_sink = "updated-by-service"
updated = editor.ReplaceUser(original.revision, original.values)
assert updated.outcome == "applied"
conflict = editor.ReplaceUser(original.revision, original.values)
assert conflict.outcome == "conflict"
assert reader.Read(RunOverrides()).log_sink == "updated-by-service"

observer = machine.resolve_config_observer(scope="local")
first = observer.Observe(RunOverrides(), "", 0)
assert first.outcome == "snapshot" and first.cursor and first.snapshot.log_sink == "updated-by-service", first.outcome
unchanged = observer.Observe(RunOverrides(), first.cursor, 0)
assert unchanged.outcome == "unchanged" and unchanged.snapshot is None and unchanged.cursor == first.cursor, unchanged.outcome
observed_edit = editor.ReadUser()
observed_edit.values.log_sink = "observed-by-python"
assert editor.ReplaceUser(observed_edit.revision, observed_edit.values).outcome == "applied"
changed = observer.Observe(RunOverrides(), first.cursor, 3000)
assert changed.outcome == "snapshot" and changed.cursor != first.cursor and changed.snapshot.log_sink == "observed-by-python", changed.outcome
try:
    observer.Observe(RunOverrides(), changed.cursor, 30001)
except ValueError:
    pass
else:
    raise AssertionError("unbounded config wait reached the service")
restore = editor.ReadUser()
restore.values.log_sink = "updated-by-service"
assert editor.ReplaceUser(restore.revision, restore.values).outcome == "applied"


def edit_policy(mode):
    with open(sys.argv[3], "w", encoding="utf-8") as f:
        f.write(mode)


current = editor.ReadUser()
edit_policy("forbidden")
refused = editor.ReplaceUser(current.revision, current.values)
assert refused.outcome == "forbidden", refused
assert refused.snapshot.revision == "" and refused.snapshot.values.off == {} and refused.snapshot.values.store == ""
edit_policy("unavailable")
assert editor.ReplaceUser(current.revision, current.values).outcome == "unavailable"
assert editor.ReadUser().revision == current.revision, "refused edits changed settings"
edit_policy("permit")

from abstraction.asks.api.rec import ApplicationQuestion
questions = machine.resolve_asks(scope="local")
operator = machine.resolve_asks_operator(scope="local")
admitted = questions.Ask(ApplicationQuestion(request_key="python-retired", key="download.reach", slots={"host": "python.example"}))
assert admitted.outcome == "pending", admitted
retired = operator.RetireQuestion(admitted.answer.id)
assert retired.outcome == "retired" and retired.record.id == admitted.answer.id and not retired.record.option, retired
replay = operator.RetireQuestion(admitted.answer.id)
assert replay.outcome == "retired" and replay.record is None, replay
assert questions.Observe("python-retired", 0).outcome == "gone"
assert questions.Ask(ApplicationQuestion(request_key="python-retired", key="download.reach", slots={"host": "python.example"})).outcome == "gone"
assert operator.RetireQuestion("python-never-admitted").outcome == "unknown"
page = operator.ListQuestions("", 16)
assert page.outcome == "page" and page.records == [], page
from abstraction.model.api.rec import Ref
lookup = machine.resolve_model(scope="local")
resolved = lookup.Resolve(Ref(registry="fixture", repo="weights"))
assert resolved.outcome == "resolved", resolved
assert resolved.request.artifact.digest == "sha256:" + "b" * 64 and resolved.request.artifact.size == 7
assert [s.locator for s in resolved.request.sources] == ["http://127.0.0.1/python-weights"]
edit_policy("model-forbidden")
refused = lookup.Resolve(Ref(registry="fixture", repo="weights"))
assert refused.outcome == "forbidden" and refused.request is None, refused
edit_policy("model-unavailable")
refused = lookup.Resolve(Ref(registry="fixture", repo="weights"))
assert refused.outcome == "unavailable" and refused.request is None, refused
edit_policy("permit")
assert lookup.Resolve(Ref(registry="fixture", repo="weights")).outcome == "resolved"

from abstraction.router.rec import HostAllowance, PickRequest, ServiceError as RouterError
routes = machine.resolve_router(scope="local")
# Live fake hosts: resident Lemonade, cold LM Studio, unreachable Ollama.
models = routes.Models(False)
hosts = routes.Hosts(False)
assert len(models.models) == 2 and len(hosts.hosts) == 3, (len(models.models), len(hosts.hosts))
assert any(a.host == "lemonade" and a.resident and a.servable for f in models.models for a in f.names), "resident alias lost"
down = [h for h in hosts.hosts if h.host == "ollama"]
assert down and not down[0].up and down[0].why and down[0].installed == 0 and down[0].resident == [], "host failure hidden"
audited = len(hosts.asked)
live = "qwen/qwen3.6-35b-a3b"
resident = routes.Pick(PickRequest(model=live, fresh=False)).decision
assert resident.verdict == "resident" and resident.loads == 0 and resident.endpoint and resident.authorised is None, resident.verdict
refused = routes.Pick(PickRequest(model=live, fresh=False, allowed=HostAllowance(hosts=[]))).decision
assert refused.verdict == "unauthorised" and not refused.endpoint and refused.authorised.hosts == [] and refused.withheld, refused.verdict
cold = routes.Pick(PickRequest(model=live, fresh=False, allowed=HostAllowance(hosts=["lmstudio"]))).decision
assert cold.verdict == "would-load" and cold.loads == 1 and cold.host == "lmstudio" and cold.endpoint, cold.verdict
after = routes.Hosts(False)
assert len(after.asked) == audited + 3, (audited, len(after.asked))
assert all(a.caller == after.observation.caller.path_description and a.user == after.observation.caller.user_description
           for a in after.asked[audited:]), "audit not bound to the Python caller"
picked = routes.Pick(PickRequest(model="python-model", fresh=False))
assert picked.decision.asked == "python-model", picked.decision.asked


def router_code(call):
    try:
        call()
    except RouterError as e:
        return e.code
    return "ok"


for mode, code in (("router-forbidden", "forbidden"), ("router-unavailable", "policy_unavailable")):
    edit_policy(mode)
    assert router_code(lambda: routes.Models(False)) == code, mode
    assert router_code(lambda: routes.Hosts(False)) == code, mode
    assert router_code(lambda: routes.Pick(PickRequest(model="python-model", fresh=False))) == code, mode
edit_policy("permit")
assert router_code(lambda: routes.Pick(PickRequest(model="python-model", fresh=False))) == "ok"
import hashlib
import threading
from abstraction.logging.rec import Record, ServiceError as LogError

sink = machine.resolve_log(scope="local")
observer = machine.resolve_log_observer(scope="local")
log_cursor = ""
while True:
    page = observer.Observe(log_cursor, 256, 65536, 0)
    assert page.outcome == "page", page.outcome
    log_cursor = page.next
    if page.at_end:
        break
late = Record(schema=1, time="2026-09-15T10:00:00.000000Z", level=2, msg="python observed after waiting", attrs={"fixture": "python-observer"})
threading.Timer(0.3, lambda: sink.Write(late)).start()
began = time.monotonic()
page = observer.Observe(log_cursor, 16, 65536, 5000)
assert page.outcome == "page" and time.monotonic() - began >= 0.25, (page.outcome, time.monotonic() - began)
assert [r.msg for r in page.records] == ["python observed after waiting"], [r.msg for r in page.records]
assert page.records[0].attrs["fixture"] == "python-observer" and page.next != log_cursor
log_cursor = page.next
for mode, code in (("history-forbidden", "forbidden"), ("history-unavailable", "policy_unavailable")):
    edit_policy(mode)
    try:
        observer.Observe(log_cursor, 16, 65536, 0)
    except LogError as e:
        assert e.code == code, (mode, e.code)
    else:
        raise AssertionError(mode + " observation returned records")
edit_policy("permit")
try:
    observer.Observe(log_cursor, 16, 65536, 30001)
except ValueError:
    pass
else:
    raise AssertionError("unbounded wait reached the service")
assert observer.Observe(log_cursor, 16, 65536, 0).outcome == "page"

from abstraction.storage.content.client import new_request_id
changes = machine.resolve_storage_changes(scope="local")
writer = machine.resolve_storage_writer(scope="local")


def store(text):
    data = text.encode()
    digest = "sha256:" + hashlib.sha256(data).hexdigest()
    stored = writer.Write(new_request_id(), digest, data)
    assert stored.digest == digest
    return digest


for mode, outcome in (("changes-forbidden", "forbidden"), ("changes-unavailable", "unavailable")):
    edit_policy(mode)
    refused = changes.Observe("", 16, 0)
    assert refused.outcome == outcome and not refused.changes and refused.next == "", refused.outcome
    listed = changes.List("", 16)
    assert listed.outcome == outcome and not listed.objects and not listed.cursor, listed.outcome
edit_policy("permit")
objects, cursor = changes.Snapshot(16)
assert objects == [] and cursor
first = store("python observed object")
page = changes.Observe(cursor, 16, 5000)
assert page.outcome == "page" and [(c.kind, c.digest) for c in page.changes] == [("added", first)], page.outcome
cursor = page.next

edit_policy("storage-unreadable")
hidden = store("python object without read permission")
page = changes.Observe(cursor, 16, 300)
assert page.outcome == "page" and hidden not in [c.digest for c in page.changes], "unreadable object reported"
assert page.next != cursor, "skipped change did not advance the cursor"
edit_policy("permit")
cursor = page.next

stale = cursor
burst = [store("python burst object %d" % i) for i in range(6)]
gap = changes.Observe(stale, 16, 0)
assert gap.outcome == "gap" and not gap.changes and gap.next == stale, gap.outcome
objects, recovered = changes.Snapshot(2)
assert {o.digest for o in objects} == {first, hidden, *burst}, [o.digest for o in objects]
settled = changes.Observe(recovered, 16, 0)
assert settled.outcome == "page" and not settled.changes and settled.at_end
try:
    changes.Observe(recovered, 257, 0)
except ValueError:
    pass
else:
    raise AssertionError("oversized change request reached the service")

import os
from abstraction.rights.api.rec import Subject, PolicyRule

decisions = machine.resolve_rights(scope="local")
administration = machine.resolve_rights_operator(scope="local")
me = Subject(account=os.environ["OA_RIGHTS_ACCOUNT"], program=os.environ["OA_RIGHTS_PROGRAM"])
right, target = "fixture.read", "python-resource"


def rule(permit):
    return PolicyRule(subject=me, action=right, resource=target, permit=permit)


assert decisions.Decide(right, target).outcome == "not_granted"
unknown = decisions.Decide("fixture.absent", target)
assert unknown.outcome == "unknown_action" and unknown.policy_revision, unknown.outcome
page = administration.ListPolicy("", 64)
assert page.outcome == "page" and page.catalog == [right] and page.rules == [] and page.complete, page.outcome
granted = administration.SetRule(page.revision, rule(True))
assert granted.outcome == "applied" and granted.current.permit and granted.revision != page.revision, granted.outcome
permitted = decisions.Decide(right, target)
assert permitted.outcome == "permitted" and permitted.policy_revision, permitted.outcome
assert decisions.DecideFor(me, right, target).outcome == "permitted"
stale = administration.SetRule(page.revision, rule(False))
assert stale.outcome == "conflict" and stale.revision == granted.revision and stale.current.permit, stale.outcome
assert decisions.Decide(right, target).outcome == "permitted", "conflicting deny changed the decision"
denied_rule = administration.SetRule(granted.revision, rule(False))
assert denied_rule.outcome == "applied" and not denied_rule.current.permit, denied_rule.outcome
assert decisions.Decide(right, target).outcome == "denied"
listed = administration.ListPolicy("", 64)
assert [(r.subject.program, r.permit) for r in listed.rules] == [(me.program, False)], listed.rules
revoked = administration.RevokeRule(denied_rule.revision, me, right, target)
assert revoked.outcome == "applied" and revoked.current is None, revoked.outcome
assert decisions.Decide(right, target).outcome == "not_granted"
late = administration.RevokeRule(denied_rule.revision, me, right, target)
assert late.outcome == "conflict" and late.current is None, late.outcome
for mode, outcome in (("operator-forbidden", "forbidden"), ("operator-unavailable", "unavailable")):
    edit_policy(mode)
    refused = administration.ListPolicy("", 64)
    assert refused.outcome == outcome and not refused.revision and not refused.rules, (mode, refused.outcome)
    refused_edit = administration.SetRule(revoked.revision, rule(True))
    assert refused_edit.outcome == outcome and not refused_edit.revision, (mode, refused_edit.outcome)
edit_policy("permit")
assert decisions.Decide(right, target).outcome == "not_granted", "refused edits changed policy"
try:
    administration.SetRule("", rule(True))
except ValueError:
    pass
else:
    raise AssertionError("edit without revision reached the service")
policy_file = os.environ["OA_RIGHTS_POLICY_FILE"]
saved = open(policy_file, "rb").read() if os.path.exists(policy_file) else None
with open(policy_file, "wb") as f:
    f.write(b"{ not a policy")
outage = decisions.Decide(right, target)
assert outage.outcome == "unavailable" and not outage.policy_revision, outage.outcome
if saved is None:
    os.remove(policy_file)
else:
    with open(policy_file, "wb") as f:
        f.write(saved)
assert decisions.Decide(right, target).outcome == "not_granted"

try:
    Machine(sys.argv[2]+"-absent", timeout=.2).resolve_config(scope="local")
except OSError:
    pass
else:
    raise AssertionError("missing resolver selected local provider")
print("PASS: installed Python reader/editor, edit-policy forbidden/unavailable, question retirement, model lookup forbidden/unavailable, router inventory/pick forbidden/policy_unavailable, existing records, change, conflict, absent resolver")
