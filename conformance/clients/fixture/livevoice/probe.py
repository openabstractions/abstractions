"""Measure 200 ms audio Append/Observe with a concurrent production chat stream.
Uses generated codecs and the shared native IPC library. See run.py --help.
"""
import argparse
import concurrent.futures
import json
import math
import threading
import time

from abstraction.ipc import FrameTransport, Library
from abstraction.inference import api as inference
from oa.test import livevoice as wire


def measure(endpoint, chat_endpoint, frames, warmup):
    library = Library()
    def transport(ep):
        return FrameTransport(library, ep, timeout=5.0)
    chat = inference.ChatClient(transport(chat_endpoint))
    admission = chat.start(inference.Request(model="probe-model", credential="probe",
        guarantees=[inference.RequestGuarantee.HOSTED_ALLOWED], messages=[inference.Message(
            role=inference.Role.USER, parts=[inference.Part(kind=inference.PartKind.TEXT, text="stream")])]))
    if admission.outcome != inference.StartOutcome.ACCEPTED:
        raise RuntimeError(f"chat admission: {admission}")
    stop = threading.Event()
    audio = bytes([1, 2]) * 3200

    def chat_stream():
        cursor = 0
        while not stop.is_set():
            page = chat.observe(admission.operation, cursor, 32, 65536, 200)
            if page.outcome != inference.PageOutcome.PAGE or page.at_end:
                raise RuntimeError(f"chat ended early: {page.outcome}")
            cursor = page.next

    def observe():
        client = wire.VoiceProbeClient(transport(endpoint))
        samples = []
        for i in range(frames + warmup):
            began = time.perf_counter_ns()
            sample = client.observe(i)
            elapsed = time.perf_counter_ns() - began - sample.wait_ns
            if sample.sequence != i or sample.audio != audio:
                raise RuntimeError("observe frame mismatch")
            if i >= warmup:
                samples.append(elapsed / 1e6)
        return samples

    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        stream = pool.submit(chat_stream)
        reader = pool.submit(observe)
        appends = []
        try:
            client = wire.VoiceProbeClient(transport(endpoint))
            start = time.perf_counter()
            for i in range(frames + warmup):
                time.sleep(max(0, start + i * 0.2 - time.perf_counter()))
                began = time.perf_counter_ns()
                sample = client.append(wire.Frame(sequence=i, audio=audio))
                elapsed = time.perf_counter_ns() - began - sample.wait_ns
                if sample.sequence != i:
                    raise RuntimeError("append sequence mismatch")
                if i >= warmup:
                    appends.append(elapsed / 1e6)
            observed = reader.result(timeout=5)
        finally:
            stop.set()
            stream.result(timeout=5)
            chat.cancel(admission.operation)
    result = dict(language="python", frames=frames, frame_ms=200, chat_concurrent=True)
    for name, values in (("append", appends), ("observe", observed)):
        values.sort()
        result[name + "_p99_ms"] = values[math.ceil(len(values) * 0.99) - 1]
        result[name + "_max_ms"] = values[-1]
        if result[name + "_p99_ms"] >= 200:
            raise RuntimeError(f"{name} p99 exceeds 200 ms: {result}")
    print(json.dumps(result), flush=True)


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--endpoint", required=True)
    p.add_argument("--chat-endpoint", required=True)
    p.add_argument("--frames", type=int, default=100)
    p.add_argument("--warmup", type=int, default=5)
    a = p.parse_args()
    if a.frames < 1 or a.warmup < 0 or a.frames + a.warmup > 9999:
        p.error("invalid frame count")
    measure(a.endpoint, a.chat_endpoint, a.frames, a.warmup)
