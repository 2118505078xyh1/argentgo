#!/usr/bin/env python3
"""Verify translation files align with numbered source lines.

Checks: line counts, comment-marker prefix, and backticked code tokens match.
"""
import glob
import os
import re

ROOT = os.path.dirname(os.path.abspath(__file__))
NUMFILE = os.path.join(ROOT, "_subagent_numbered.txt")
TRANSGLOB = os.path.join(ROOT, "_subagent_trans_*.txt")


def ticks(s):
    return re.findall(r"`[^`]*`", s)


def marker(s):
    m = re.match(r"(\s*)(//|/\*|\*)", s)
    return m.group(2) if m else "<none>"


def main():
    eng = []
    with open(NUMFILE, encoding="utf-8") as f:
        for raw in f.read().splitlines():
            if "\t" in raw:
                eng.append(raw.split("\t", 1)[1])

    idx = 0
    problems = 0
    for path in sorted(glob.glob(TRANSGLOB)):
        with open(path, encoding="utf-8") as f:
            tr = f.read().splitlines()
        base = os.path.basename(path)
        if idx + len(tr) > len(eng):
            print(base, "EXCEEDS total english lines")
            problems += 1
        for j, body in enumerate(tr):
            src = eng[idx + j]
            if marker(body) != marker(src):
                print(base, "MARKER", idx + j + 1, repr(marker(src)), repr(marker(body)))
                problems += 1
            if ticks(src) != ticks(body):
                print(base, "TICKS", idx + j + 1, ticks(src), "!=", ticks(body))
                problems += 1
        print(f"{base}: lines {idx+1}-{idx+len(tr)} count={len(tr)}")
        idx += len(tr)
    print("TOTAL translations:", idx, "of", len(eng))
    print("PROBLEMS:", problems)


if __name__ == "__main__":
    main()
