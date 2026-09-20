"""Fixture-only loopback HTTP source for the Linux lifecycle scenarios.

Serves deterministic artifacts with a strong ETag, byte ranges and If-Range,
an optional throughput limit and a one-shot hold gate at a byte offset. A held
request notices when its peer disconnects. Every request is logged with its
range, bytes sent, completion and wall-clock interval; overlapping transfer
intervals for one artifact would expose a second writer.

  python3 source.py --ready FILE --artifact NAME:SIZE[:RATE[:HOLD]] ...

Control (loopback GET): /control/status/NAME, /control/release/NAME, /control/log
"""
import argparse
import hashlib
import json
import re
import select
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CHUNK = 16384
LAST_MODIFIED = "Tue, 15 Sep 2026 00:00:00 GMT"


class Artifact:
    def __init__(self, name, size, rate=0, hold=0):
        seed = hashlib.sha256(name.encode()).digest()
        blocks, counter, total = [], 0, 0
        while total < size:
            block = hashlib.sha256(seed + counter.to_bytes(8, "big")).digest() * 64
            blocks.append(block)
            total += len(block)
            counter += 1
        self.name, self.body = name, b"".join(blocks)[:size]
        self.digest = hashlib.sha256(self.body).hexdigest()
        self.etag = '"' + self.digest[:32] + '"'
        self.rate, self.hold = rate, hold
        self.released = threading.Event()
        if not hold:
            self.released.set()
        self.holding = False


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def peer_closed(self):
        try:
            ready, _, _ = select.select([self.connection], [], [], 0)
            if not ready:
                return False
            # Windows has no MSG_DONTWAIT; select already reported the socket
            # readable, so a peek returns at once on either platform.
            return self.connection.recv(1, socket.MSG_PEEK | getattr(socket, "MSG_DONTWAIT", 0)) == b""
        except BlockingIOError:
            return False
        except OSError:
            return True

    def reply_json(self, value):
        body = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def control(self):
        state = self.server.state
        parts = self.path.split("/")
        if parts[2] == "log":
            with state["lock"]:
                return self.reply_json(list(state["log"]))
        art = state["artifacts"].get(parts[3] if len(parts) > 3 else "")
        if art is None:
            return self.send_error(404)
        if parts[2] == "release":
            art.released.set()
        return self.reply_json({"holding": art.holding, "released": art.released.is_set(),
                                "size": len(art.body), "digest": art.digest})

    def do_HEAD(self):
        self.serve(head=True)

    def do_GET(self):
        if self.path.startswith("/control/"):
            return self.control()
        self.serve(head=False)

    def serve(self, head):
        state = self.server.state
        art = state["artifacts"].get(self.path.lstrip("/").split("?")[0])
        if art is None:
            return self.send_error(404)
        size = len(art.body)
        start, end, status = 0, size, 200
        requested = self.headers.get("Range")
        if_range = self.headers.get("If-Range")
        if requested and (if_range is None or if_range == art.etag):
            match = re.fullmatch(r"bytes=(\d*)-(\d*)", requested.strip())
            if match and (match.group(1) or match.group(2)):
                if match.group(1):
                    start = int(match.group(1))
                    end = min(size, int(match.group(2)) + 1) if match.group(2) else size
                else:
                    start, end = max(0, size - int(match.group(2))), size
                if start >= size or start >= end:
                    self.send_response(416)
                    self.send_header("Content-Range", f"bytes */{size}")
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                status = 206
        entry = {"artifact": art.name, "method": self.command, "range": requested, "if_range": if_range,
                 "status": status, "start_offset": start, "end_offset": end, "sent": 0,
                 "started": time.time(), "ended": None, "completed": False, "error": None}
        with state["lock"]:
            state["log"].append(entry)
            active = state["active"].setdefault(art.name, 0) + 1
            state["active"][art.name] = active
            entry["concurrent_at_start"] = active
        try:
            self.send_response(status)
            self.send_header("Accept-Ranges", "bytes")
            self.send_header("ETag", art.etag)
            self.send_header("Last-Modified", LAST_MODIFIED)
            self.send_header("Content-Length", str(end - start))
            if status == 206:
                self.send_header("Content-Range", f"bytes {start}-{end - 1}/{size}")
            self.end_headers()
            if head:
                entry["completed"] = True
                return
            position, begun, rate_origin = start, time.monotonic(), start
            while position < end:
                if not art.released.is_set() and art.hold and position >= art.hold:
                    art.holding = True
                    try:
                        while not art.released.wait(0.05):
                            if self.peer_closed():
                                raise ConnectionAbortedError("peer closed while held")
                    finally:
                        art.holding = False
                    begun, rate_origin = time.monotonic(), position
                chunk = art.body[position:min(end, position + CHUNK)]
                self.wfile.write(chunk)
                position += len(chunk)
                entry["sent"] = position - start
                if art.rate:
                    due = begun + (position - rate_origin) / art.rate
                    delay = due - time.monotonic()
                    if delay > 0:
                        time.sleep(delay)
            self.wfile.flush()
            entry["completed"] = True
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError, OSError) as error:
            entry["error"] = f"{type(error).__name__}: {error}"
            self.close_connection = True
        finally:
            entry["ended"] = time.time()
            with state["lock"]:
                state["active"][art.name] -= 1


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--ready", help="write {port, artifacts} JSON here once listening")
    parser.add_argument("--artifact", action="append", default=[], help="NAME:SIZE[:RATE_BYTES_PER_SECOND[:HOLD_OFFSET]]")
    args = parser.parse_args()
    if not args.ready or not args.artifact:
        parser.print_help()
        return 0
    artifacts = {}
    for spec in args.artifact:
        fields = spec.split(":")
        name, size = fields[0], int(fields[1])
        rate = int(fields[2]) if len(fields) > 2 else 0
        hold = int(fields[3]) if len(fields) > 3 else 0
        artifacts[name] = Artifact(name, size, rate, hold)
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    server.state = {"artifacts": artifacts, "log": [], "active": {}, "lock": threading.Lock()}
    with open(args.ready, "w", encoding="utf-8") as ready:
        json.dump({"port": server.server_address[1],
                   "artifacts": {n: {"size": len(a.body), "digest": a.digest} for n, a in artifacts.items()}}, ready)
    server.serve_forever()


if __name__ == "__main__":
    main()
