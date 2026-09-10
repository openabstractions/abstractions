import pathlib
import sys

sys.path.insert(0, sys.argv[1])
import rec

OUT = pathlib.Path(sys.argv[2])
lines = []
for p in sorted(pathlib.Path(sys.argv[3]).glob("*.json")):
    try:
        v = rec.decode(p.read_bytes())
    except rec.Refusal as refusal:
        lines.append("%s\t%s\t%d\n" % (p.stem, refusal.word, refusal.offset))
        continue
    lines.append("%s\tok\n" % p.stem)
    (OUT / ("py-rt-" + p.stem + ".json")).write_bytes(rec.encode(v))
(OUT / "py-corpus.txt").write_bytes("".join(lines).encode("utf-8"))
