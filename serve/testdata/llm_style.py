"""An llm-style client of the gateway window.

It sends what the llm command's OpenAI plugin sends to a configured api_base:
POST <base>/chat/completions with a bearer key, stream true and
stream_options.include_usage, and reads the server-sent events to [DONE].

    llm_style.py <base URL ending in /v1> <key> <model>

Prints one JSON line: text, finish_reason and usage. Exits 3 with the status
and body when the window answers anything but 200.
"""
import http.client
import json
import sys
import urllib.parse

if len(sys.argv) != 4:
    print(__doc__)
    raise SystemExit(2)
base, key, model = sys.argv[1:]
url = urllib.parse.urlsplit(base)
conn = http.client.HTTPConnection(url.hostname, url.port, timeout=30)
body = {"model": model, "messages": [{"role": "user", "content": "Say hello"}], "stream": True,
        "stream_options": {"include_usage": True}}
conn.request("POST", url.path.rstrip("/") + "/chat/completions", json.dumps(body),
             {"Authorization": "Bearer " + key, "Content-Type": "application/json", "Accept": "application/json",
              "User-Agent": "OpenAI/Python llm-style"})
resp = conn.getresponse()
if resp.status != 200:
    print(json.dumps({"status": resp.status, "body": resp.read().decode("utf-8", "replace")}))
    raise SystemExit(3)
text, finish, usage = "", None, None
for raw in resp:
    line = raw.decode("utf-8").strip()
    if not line.startswith("data: "):
        continue
    data = line[len("data: "):]
    if data == "[DONE]":
        break
    chunk = json.loads(data)
    if "error" in chunk:
        print(json.dumps({"error": chunk["error"]}))
        raise SystemExit(4)
    for choice in chunk.get("choices", []):
        text += choice.get("delta", {}).get("content") or ""
        finish = choice.get("finish_reason") or finish
    if chunk.get("usage"):
        usage = chunk["usage"]
print(json.dumps({"text": text, "finish_reason": finish, "usage": usage}))
