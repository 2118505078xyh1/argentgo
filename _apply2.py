#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Apply exact-text-keyed Chinese comment translations from _canonical.txt."""
import os, sys

BS = chr(92)

def load_canonical():
    data = {}
    cur = None
    for raw in open("_canonical.txt", encoding="utf-8"):
        line = raw.rstrip("\n")
        if line.startswith("====="):
            cur = line[5:].strip().replace("/", BS); continue
        if cur is None or " => " not in line:
            continue
        txt, zh = line.split(" => ", 1)
        data[(cur, txt)] = zh
    return data

def apply():
    canon = load_canonical()
    byfile = {}
    for (p, txt), zh in canon.items():
        byfile.setdefault(p, {})[txt] = zh
    changed_files = 0
    total = 0
    missing = []
    for p, mapping in byfile.items():
        full = os.path.join(os.getcwd(), p)
        if not os.path.exists(full):
            print("MISSING FILE:", p); sys.exit(1)
        with open(full, "r", encoding="utf-8", newline="") as f:
            lines = f.readlines()
        hits = 0
        for i, line in enumerate(lines):
            body = line.rstrip("\r\n")
            if body in mapping:
                zh = mapping[body]
                idx = body.find("//")
                prefix = body[:idx]
                after = body[idx+2:]
                sep = ""
                j = 0
                while j < len(after) and after[j] in " \t":
                    sep += after[j]; j += 1
                ending = line[len(body):]
                lines[i] = prefix + "//" + sep + zh + ending
                hits += 1
        if hits:
            with open(full, "w", encoding="utf-8", newline="") as f:
                f.writelines(lines)
            changed_files += 1
            total += hits
    print("applied", total, "comments across", changed_files, "files")

if __name__ == "__main__":
    apply()
