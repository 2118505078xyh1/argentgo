#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Apply line-number-keyed Chinese comment translations to .go files."""
import os, sys, glob

DIRS = ["internal/tools", "internal/tui"]
TRANS_FILES = sorted(glob.glob("_t*.txt"))

def load_trans():
    data = {}
    for tf in TRANS_FILES:
        cur = None
        with open(tf, encoding="utf-8") as f:
            for raw in f:
                line = raw.rstrip("\n")
                if line.startswith("====="):
                    cur = line[5:].strip().replace("/", "\\")
                    continue
                if cur is None or not line.strip():
                    continue
                if "\t" not in line:
                    print("BAD LINE in", tf, ":", repr(line)); sys.exit(1)
                num, zh = line.split("\t", 1)
                num = int(num.strip())
                zh = zh.lstrip(" \t")
                data[(cur, num)] = zh
    return data

def apply():
    trans = load_trans()
    # group by file
    byfile = {}
    for (path, num), zh in trans.items():
        byfile.setdefault(path, {})[num] = zh
    for path, mapping in byfile.items():
        full = os.path.join(os.getcwd(), path)
        if not os.path.exists(full):
            print("MISSING FILE:", path); sys.exit(1)
        with open(full, "r", encoding="utf-8", newline="") as f:
            lines = f.readlines()
        for num, zh in mapping.items():
            if num < 1 or num > len(lines):
                print("BAD LINE NUM", path, num); sys.exit(1)
            line = lines[num-1]
            body = line.rstrip("\r\n")
            ending = line[len(body):]
            idx = body.find("//")
            if idx < 0:
                print("NO // ON LINE", path, num, repr(body)); sys.exit(1)
            prefix = body[:idx]
            after = body[idx+2:]
            # preserve separator whitespace after //
            sep = ""
            j = 0
            while j < len(after) and after[j] in " \t":
                sep += after[j]; j += 1
            newbody = prefix + "//" + sep + zh
            lines[num-1] = newbody + ending
        with open(full, "w", encoding="utf-8", newline="") as f:
            f.writelines(lines)
        print("applied", path, len(mapping), "comments")

if __name__ == "__main__":
    apply()
