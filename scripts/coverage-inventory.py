#!/usr/bin/env python3
"""Report statement coverage and zero-coverage functions without hiding files."""

import collections
import json
import os
import re
import subprocess
import sys


def run(*args):
    return subprocess.check_output([os.environ.get("GO", "go"), *args], text=True)


def inventory(profile):
    packages = run("list", "-json", "./...")
    decoder = json.JSONDecoder()
    files = {}
    while packages.strip():
        package, end = decoder.raw_decode(packages.lstrip())
        packages = packages.lstrip()[end:]
        path = package["ImportPath"]
        if "/example/" in path or path.endswith("/example"):
            continue
        for name in package.get("GoFiles", []) + package.get("CgoFiles", []):
            files[path + "/" + name] = os.path.join(package["Dir"], name)

    counts = collections.defaultdict(lambda: [0, 0])
    with open(profile, encoding="utf-8") as source:
        if not source.readline().startswith("mode: "):
            raise ValueError("missing coverage mode")
        blocks = {}
        for line in source:
            block, statements, count = line.split()
            statements, count = int(statements), int(count)
            previous = blocks.get(block, (statements, 0))
            blocks[block] = (statements, previous[1] + count)
        for block, (statements, count) in blocks.items():
            path = block.rsplit(":", 1)[0]
            counts[path][1] += statements
            if count:
                counts[path][0] += statements

    functions = []
    for line in run("tool", "cover", "-func=" + profile).splitlines():
        match = re.match(r"^(.+):(\d+):\s+(\S+)\s+([\d.]+)%$", line)
        if match:
            functions.append(match.groups())

    print("# Coverage inventory\n")
    print("Profile: `" + profile + "`. Statements are weighted; files without executable statements have no percentage.\n")
    print("| File | Covered / statements | Coverage |")
    print("|---|---:|---:|")
    for path in sorted(files.keys() | counts.keys()):
        recorded = path in counts
        covered, total = counts[path]
        if total:
            pct = f"{100 * covered / total:.1f}%"
        elif recorded:
            pct = "no executable statements"
        else:
            instrumented = run("tool", "cover", "-mode=set", files[path])
            match = re.search(r"Count\s+\[(\d+)\]uint32", instrumented)
            pct = "not instrumented" if match and int(match[1]) else "no executable statements"
        print(f"| `{path}` | {covered} / {total} | {pct} |")
    print("\n## Zero-coverage functions\n")
    print("Functions reported at 0% by `go tool cover`; declarations without bodies are not functions in this report.\n")
    print("| File:line | Function | Coverage |")
    print("|---|---|---:|")
    for path, line, name, pct in functions:
        if float(pct) == 0:
            print(f"| `{path}:{line}` | `{name}` | {pct}% |")
    print("\n## Function coverage\n")
    print("| File:line | Function | Coverage |")
    print("|---|---|---:|")
    for path, line, name, pct in functions:
        print(f"| `{path}:{line}` | `{name}` | {pct}% |")


if __name__ == "__main__":
    inventory(sys.argv[1] if len(sys.argv) > 1 else "coverage.out")
