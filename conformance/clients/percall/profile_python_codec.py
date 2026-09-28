"""Profile a synthetic generated Python caller reply without launching a runtime."""

import argparse
import cProfile
import io
from pathlib import Path
import pstats
import statistics
import subprocess
import sys
import time
import types

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "openabstractions-flat" / "abstraction-facade" / "py"))
from abstraction.facade import _codec as wire  # noqa: E402


def sample():
    observation = wire.CallerObservation(
        outcome=wire.CallerOutcome.OBSERVED,
        mechanism="synthetic/windows",
        account="synthetic-account",
        program="C:\\synthetic\\python.exe",
        pid=12345,
        attributes=[
            wire.CallerAttribute(attribute="account", proof="synthetic", ceiling="synthetic"),
            wire.CallerAttribute(attribute="program", proof="synthetic", ceiling="synthetic"),
            wire.CallerAttribute(attribute="code", proof="synthetic", ceiling="synthetic"),
        ],
        platform="windows", transport="named_pipe", bindable=True, stronger="",
    )
    result = wire._CallerObserveResult(value=observation)
    return wire._service_encode(wire._write_oa_caller_observe_result, result, 1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--calls", type=int, default=10000)
    parser.add_argument("--profile", action="store_true")
    comparison = parser.add_mutually_exclusive_group()
    comparison.add_argument("--compare-ref", metavar="REV",
                            help="compare the worktree codec with an immutable resolved commit")
    comparison.add_argument("--compare-head", action="store_true",
                            help="alias for --compare-ref HEAD")
    args = parser.parse_args()
    if args.calls < 1:
        parser.error("--calls must be positive")
    payload = sample()

    for encoded, expected in ((b'"plain"', "plain"), (b'""', ""),
                              (b'"a\\nb"', "a\nb"),
                              (b'"\\uD83D\\uDE00"', "😀"),
                              ('"é"'.encode("utf-8"), "é")):
        reader = wire._Reader(encoded)
        if reader.string() != expected or reader.pos != len(encoded):
            raise AssertionError("string decode changed")
    for encoded, word in ((b'"a\x01b"', "bad_string"),
                          (b'"\xff"', "bad_string"),
                          (b'"unterminated', "malformed"),
                          (b'"\\uD800"', "bad_string")):
        try:
            wire._Reader(encoded).string()
        except wire.Refusal as error:
            if error.word != word:
                raise AssertionError(f"{encoded!r}: {error.word} != {word}") from error
        else:
            raise AssertionError(f"{encoded!r} was accepted")

    def decoder(module):
        def decode():
            value = module._service_decode(module._read_oa_caller_observe_result, payload, 1).value
            if value.outcome is not module.CallerOutcome.OBSERVED or len(value.attributes) != 3:
                raise AssertionError("decoded observation changed")
        return decode

    decode = decoder(wire)

    def measure(fn):
        began = time.perf_counter_ns()
        for _ in range(args.calls):
            fn()
        return (time.perf_counter_ns() - began) / args.calls / 1_000_000

    if args.compare_ref or args.compare_head:
        reference = args.compare_ref or "HEAD"
        commit = subprocess.check_output(
            ["git", "rev-parse", "--verify", reference + "^{commit}"], cwd=ROOT, text=True
        ).strip()
        relative = "openabstractions-flat/abstraction-facade/py/abstraction/facade/_codec.py"
        source = subprocess.check_output(["git", "show", commit + ":" + relative], cwd=ROOT)
        baseline = types.ModuleType("_head_facade_codec")
        sys.modules[baseline.__name__] = baseline
        exec(compile(source, commit + ":" + relative, "exec"), baseline.__dict__)
        old = decoder(baseline)
        for _ in range(1000):
            old()
            decode()
        pairs = []
        for index in range(5):
            if index % 2:
                new_ms, old_ms = measure(decode), measure(old)
            else:
                old_ms, new_ms = measure(old), measure(decode)
            pairs.append((old_ms, new_ms))
        print(f"synthetic python={sys.version.split()[0]} baseline={commit} bytes={len(payload)} "
              f"paired baseline/worktree ms={pairs} "
              f"median_baseline={statistics.median(p[0] for p in pairs):.4f} "
              f"median_worktree={statistics.median(p[1] for p in pairs):.4f}")
        return

    for _ in range(1000):
        decode()
    batches = []
    for _ in range(5):
        batches.append(measure(decode))
    print(f"synthetic python={sys.version.split()[0]} codec_bytes={len(payload)} calls={args.calls} "
          f"batch_median_ms={statistics.median(batches):.4f} batches_ms={batches}")
    if args.profile:
        profiler = cProfile.Profile()
        profiler.enable()
        for _ in range(args.calls):
            decode()
        profiler.disable()
        output = io.StringIO()
        pstats.Stats(profiler, stream=output).sort_stats("cumulative").print_stats(25)
        print(output.getvalue())


if __name__ == "__main__":
    main()
