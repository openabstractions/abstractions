"""What the hand-written Python peer makes of the corpus and of stored records.

    python verdicts.py <repo root> <corpus dir> <record path or dir> ...

One tab-separated line per input, read by scripts/peers/corpus.sh:

    peer       python
    toolchain  CPython 3.12.10 win32
    verdict    <fixture>  ok | refused | unknown-model | escaped
    roundtrip  <fixture>  same | differs          (accepted fixtures only)
    wire       <record>   read | moved | refused

`escaped` is not a verdict this layer offers. A refusal that leaves
Record.from_json as anything but a JobError is one no caller can catch, so it is
counted apart from a refusal rather than folded into it.
"""

import os
import platform
import sys

ROOT = os.path.abspath(sys.argv[1])
# The module finds its cas sibling by walking up from its own file, which a copy
# taken out of the tree for a baseline cannot do.
sys.path.insert(0, os.path.join(ROOT, "cas", "python"))
sys.path.insert(0, os.environ.get("ABSTRACTION_JOB_PY", os.path.join(ROOT, "job", "python")))
import abstraction_job as aj


def jsons(path):
    if os.path.isdir(path):
        return [os.path.join(path, n) for n in sorted(os.listdir(path)) if n.endswith(".json")]
    return [path]


# A fixture whose canonical form is not its own bytes says so in a sibling file:
# `<fixture>.roundtrip` holds what a decode and re-encode must produce. That is
# how a payload's insignificant whitespace is tested — [JOB-E7] deliberately does
# not carry it, so the input and the expectation are different bytes on purpose.
def wanted(path, raw):
    expectation = path + ".roundtrip"
    if not os.path.exists(expectation):
        return raw
    with open(expectation, "rb") as f:
        return f.read()


def report(raw):
    try:
        record = aj.Record.from_json(raw)
    except aj.UnknownSchema:
        return "unknown-model", None
    except aj.JobError:
        return "refused", None
    except Exception:
        return "escaped", None
    return "ok", record


def main():
    print("peer\tpython")
    print("toolchain\t%s %s %s" % (platform.python_implementation(),
                                   platform.python_version(), sys.platform))

    for path in jsons(sys.argv[2]):
        with open(path, "rb") as f:
            raw = f.read()
        word, record = report(raw)
        name = os.path.basename(path)
        print("verdict\t%s\t%s" % (name, word))
        if record is not None:
            print("roundtrip\t%s\t%s" % (name, "same" if record.to_json() == wanted(path, raw) else "differs"))

    for arg in sys.argv[3:]:
        for path in jsons(arg):
            with open(path, "rb") as f:
                raw = f.read()
            name = os.path.basename(path)
            try:
                record = aj.Record.from_json(raw)
            except Exception as e:
                print("wire\t%s\trefused\t%s: %s" % (name, type(e).__name__, e))
                continue
            print("wire\t%s\t%s" % (name, "read" if record.to_json() == raw else "moved"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
