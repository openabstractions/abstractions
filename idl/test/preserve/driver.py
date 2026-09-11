import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent / "generated/py"))
import rec

try:
    r = rec.decode(Path(sys.argv[1]).read_bytes())
    scope = sys.argv[3]
    if scope == "mutate":
        r.id = "edited"
        if r.child is not None:
            r.child.name = "edited-child"
        for child in r.children:
            child.name = "edited-child"
    elif scope != "read":
        target = r.child if scope == "child" else r.children[0] if scope == "repeated" else r
        key = "\ud800" if scope == "bad-key" else sys.argv[4]
        target.extras[key] = Path(sys.argv[5]).read_bytes()
    output = rec.encode(r)
    rec.decode(output)
    Path(sys.argv[2]).write_bytes(output)
except rec.Refusal as exc:
    print(exc.word, end="")
else:
    print("ok", end="")
