#!/usr/bin/env python3
"""Convert positionally-authored verified translation files to indexed form.

Each _subagent_trans_XX.txt covers a known contiguous [start, end] range;
line i maps to index start+i. Output: _subagent_indexed_XX.txt (N<TAB>translation).
"""
import os

ROOT = r"D:/code/mpi/argent-go"
RANGES = {
    "_subagent_trans_01.txt": 1,
    "_subagent_trans_02.txt": 101,
    "_subagent_trans_03.txt": 201,
    "_subagent_trans_04.txt": 301,
    "_subagent_trans_05.txt": 401,
    "_subagent_trans_06.txt": 601,
    "_subagent_trans_07.txt": 901,
}
for fn, start in RANGES.items():
    tr = open(os.path.join(ROOT, fn), encoding="utf-8").read().splitlines()
    outfn = fn.replace("_trans_", "_indexed_")
    with open(os.path.join(ROOT, outfn), "w", encoding="utf-8") as f:
        for i, line in enumerate(tr):
            f.write(f"{start + i}\t{line}\n")
    print(outfn, len(tr), "lines, start", start)
