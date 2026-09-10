import pathlib
import sys

sys.path.insert(0, sys.argv[1])
import rec

OUT = pathlib.Path(sys.argv[2])
TS = "2026-09-08T05:07:14.951609Z"


def cp(*points):
    return "".join(chr(p) for p in points)


def awkward():
    by = ("Ada L" + cp(0x016B) + "velace <ada@" + cp(0x4F8B, 0x3048)
          + ".jp> & co " + cp(0x2702, 0xFE0F) + " " + cp(0x1F9FF))
    err = ("line1" + cp(0x0A) + "line2" + cp(0x09) + "tabbed" + cp(0x01)
           + "ctrl " + cp(0x22) + "quoted" + cp(0x22) + " back" + cp(0x5C) + "slash")
    return rec.Record(
        content=["abstraction.job/base@1", "abstraction.job/intent@1",
                 "abstraction.job/envelope@1", "abstraction.job/step@1"],
        critical=["abstraction.job/base@1"],
        id="1787202430967-a752f9a9c2c77b123ffd",
        kind="download",
        envelope=rec.Envelope(
            schema="nas.example/transfer@2",
            actions=["cancel", "nas.example/transfer@2#re-mirror"],
        ),
        state="pending",
        spec='{"artifact":{"bytes":9223372036854775807,"empty_obj":{},"empty_arr":[]},'
             '"note":"a<b&c>d","nested":{"deep":{"x":1.50,"neg":-0.0}}}',
        progress=rec.Progress(
            done=0,
            total=9223372036854775807,
            updated_at=TS,
            step=rec.Step(name="", ordinal=1, of=0, done=0, total=-9223372036854775808),
        ),
        lease=rec.Lease(owner="", epoch=0, expires_at=TS),
        error=err,
        intent=rec.Intent(want="cancel", by=by, at=TS),
        extensions={
            "zz.example/v1": '{"k":"v"}',
            "aa.example/v1": "[1,2,3]",
            cp(0x00E9) + ".example": "null",
            cp(0xFFFD) + ".example": "true",
            cp(0x1D11E) + ".example": "{}",
        },
        created_at=TS,
        updated_at=TS,
    )


def ranges():
    return rec.Record(
        content=["abstraction.job/base@1", "abstraction.download/ranges@1"],
        critical=["abstraction.job/base@1"],
        id="1787202430967-a752f9a9c2c77b123ffd",
        kind="download",
        state="running",
        spec='{"artifact":{"bytes":23068672}}',
        checkpoint='{"verified_prefix":4194304,"verified":'
                   "[[0,4194304],[8388608,12582912],[20971520,23068672]]}",
        progress=rec.Progress(done=10485760, total=23068672,
                              updated_at="2026-08-20T05:07:14.951609Z"),
        lease=rec.Lease(owner="go-worker", epoch=2,
                        expires_at="2026-08-20T05:08:14.635068Z"),
        created_at="2026-08-20T05:07:10.967343Z",
        updated_at="2026-08-20T05:07:15.134811Z",
    )


def terminal():
    return rec.Record(
        content=["abstraction.job/base@1", "abstraction.job/step@1",
                 "abstraction.job/terminal@1", "abstraction.job/recall@1"],
        critical=["abstraction.job/base@1", "abstraction.job/terminal@1",
                  "abstraction.job/recall@1"],
        id="1787202430967-a752f9a9c2c77b123ffd",
        kind="download",
        state="failed",
        spec='{"artifact":{"bytes":64}}',
        checkpoint='{"verified_prefix":8}',
        progress=rec.Progress(done=8, total=64,
                              updated_at="2026-09-09T17:21:08.958178Z",
                              step=rec.Step(name="fetch", ordinal=1, of=2, done=8, total=64)),
        lease=rec.Lease(owner="alpha", epoch=3,
                        expires_at="2026-09-09T17:21:09.958178Z",
                        recall=rec.Recall(reason="yield", by="broker",
                                          at="2026-09-09T17:21:08.958178Z",
                                          until="2026-09-09T17:21:09.958178Z")),
        error="source closed the connection",
        created_at="2026-09-09T17:21:06.998457Z",
        updated_at="2026-09-09T17:21:08.967883Z",
    )


def verdict(data):
    try:
        v = rec.decode(data)
    except rec.Refusal as refusal:
        return "%s\t%d" % (refusal.word, refusal.offset)
    return "ok\t" + v.spec.decode("utf-8")


for name, r in (("awkward", awkward()), ("ranges", ranges()), ("terminal", terminal())):
    encoded = rec.encode(r)
    (OUT / ("py-" + name + ".json")).write_bytes(encoded)
    (OUT / ("py-rt-" + name + ".json")).write_bytes(rec.encode(rec.decode(encoded)))

lines = ["%s\t%s\n" % (p.stem, verdict(p.read_bytes()))
         for p in sorted(pathlib.Path(sys.argv[3]).glob("*.json"))]
(OUT / "py-corpus.txt").write_bytes("".join(lines).encode("utf-8"))
