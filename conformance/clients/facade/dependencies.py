"""Check production Go facade dependency closures using the Go package graph.

No arguments prints help. --check inspects primary facade and /client; /legacy
is an explicit provider adopter and is outside these guarded entrypoints.
"""
import argparse
from collections import deque
import json
from pathlib import Path
import subprocess

PREFIX = "github.com/openabstractions/"
FACADE = PREFIX + "abstraction-facade/go"
TARGETS = (FACADE, FACADE + "/client")
# Capability modules combine protocol/client packages with concrete providers.
# Their root and implementation subpackages are provider code. Generated
# protocol namespaces and explicit client subtrees remain valid dependencies.
PROVIDERS = tuple(PREFIX + "abstraction-" + name + "/go" for name in
                  ("asks", "cas", "config", "download", "job", "logging",
                   "model", "rights", "router", "storage", "watch"))


def forbidden(path):
    if path.startswith(FACADE + "/"):
        return path.split("/")[4] in {"legacy", "runtime", "service", "serve"}
    for root in PROVIDERS:
        if path == root:
            return True
        if path.startswith(root + "/"):
            suffix = path[len(root) + 1:]
            return not (suffix == "client" or suffix.startswith("client/") or
                        suffix.startswith("abstraction/"))
    return False


def packages(root, targets=TARGETS, go="go"):
    result = subprocess.run([go, "list", "-deps", "-json", *targets], cwd=root,
                            text=True, encoding="utf-8", capture_output=True, timeout=90)
    if result.returncode:
        raise RuntimeError("go list failed: " + result.stderr.strip())
    decoder, offset, values = json.JSONDecoder(), 0, {}
    while offset < len(result.stdout):
        if result.stdout[offset].isspace():
            offset += 1
            continue
        value, offset = decoder.raw_decode(result.stdout, offset)
        if value.get("Error") or value.get("DepsErrors"):
            raise RuntimeError("incomplete Go dependency graph: " + value["ImportPath"])
        values[value["ImportPath"]] = value
    return values


def violations(graph, targets=TARGETS):
    findings = []
    for target in targets:
        if target not in graph:
            raise RuntimeError("missing entrypoint in Go graph: " + target)
        queue, visited = deque([(target, [target])]), set()
        while queue:
            path, chain = queue.popleft()
            if path in visited:
                continue
            visited.add(path)
            if forbidden(path):
                findings.append(" -> ".join(chain))
                continue
            package = graph.get(path)
            if package is None:
                if path == "C":
                    continue
                raise RuntimeError("missing dependency in Go graph: " + path)
            aliases = package.get("ImportMap", {})
            for imported in package.get("Imports", []):
                imported = aliases.get(imported, imported)
                queue.append((imported, chain + [imported]))
    return sorted(findings)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[3])
    parser.add_argument("--go", default="go", help="Go executable (no tool installation)")
    args = parser.parse_args(argv)
    if not args.check:
        parser.print_help()
        return 0
    try:
        found = violations(packages(args.root, go=args.go))
    except (RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        print("FAIL:", error)
        return 1
    if found:
        print("FAIL: facade client imports provider implementation")
        for chain in found:
            print(chain)
        return 1
    print("PASS: primary facade and /client dependency closures contain no provider implementation")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
