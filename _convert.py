#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Convert line-number-keyed _t*.txt translations into exact-text-keyed _canonical.txt."""
import os, re, glob, sys

BS = chr(92)
DIRS = ["internal/tools", "internal/tui"]

def norm(p):
    return p.replace(BS, "/")

def make_fresh_dump():
    out = {}
    for d in DIRS:
        for root, _, files in os.walk(d):
            for fn in sorted(files):
                if not fn.endswith(".go"):
                    continue
                p = os.path.join(root, fn)
                with open(p, encoding="utf-8", newline="") as f:
                    lines = f.read().split("\n")
                for i, l in enumerate(lines, 1):
                    s = l.lstrip()
                    if s.startswith("//") and re.search(r"[A-Za-z]", s[2:]) and not s.startswith("//go:"):
                        out[(norm(p), i)] = l
    return out

def load_line_trans():
    trans = {}
    for tf in sorted(glob.glob("_t*.txt")):
        cur = None
        for raw in open(tf, encoding="utf-8"):
            line = raw.rstrip("\n")
            if line.startswith("====="):
                cur = norm(line[5:].strip()); continue
            if cur is None or not line.strip():
                continue
            num, zh = line.split("\t", 1)
            trans[(cur, int(num.strip()))] = zh.lstrip(" \t")
    return trans

def load_canonical():
    data = {}
    if not os.path.exists("_canonical.txt"):
        return data
    cur = None
    for raw in open("_canonical.txt", encoding="utf-8"):
        line = raw.rstrip("\n")
        if line.startswith("====="):
            cur = norm(line[5:].strip()); continue
        if cur is None or " => " not in line:
            continue
        txt, zh = line.split(" => ", 1)
        data[(cur, txt)] = zh
    return data

def main():
    fresh = make_fresh_dump()
    trans = load_line_trans()
    canon = load_canonical()
    bad = []
    added = 0
    for (p, num), zh in trans.items():
        txt = fresh.get((p, num))
        if txt is None:
            bad.append((p, num, "NO FRESH LINE")); continue
        key = (p, txt)
        if key in canon and canon[key] == zh:
            continue
        canon[key] = zh
        added += 1
    print("line-trans:", len(trans), "added/updated:", added, "bad:", len(bad))
    for b in bad[:50]:
        print("BAD", b)
    byfile = {}
    for (p, txt), zh in canon.items():
        byfile.setdefault(p, []).append((txt, zh))
    with open("_canonical.txt", "w", encoding="utf-8", newline="") as f:
        for p in sorted(byfile):
            f.write("===== " + p + "\n")
            for txt, zh in sorted(byfile[p]):
                f.write(txt + " => " + zh + "\n")
    print("canonical entries:", len(canon))

if __name__ == "__main__":
    main()
