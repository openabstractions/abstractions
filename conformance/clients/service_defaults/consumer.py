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
try:
    Machine(sys.argv[2]+"-absent", timeout=.2).resolve_config(scope="local")
except OSError:
    pass
else:
    raise AssertionError("missing resolver selected local provider")
print("PASS: installed Python reader/editor, existing records, change, conflict, absent resolver")
