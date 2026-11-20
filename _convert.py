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

def load_dump_file(fn):
    d = {}
    if not os.path.exists(fn):
        return d
    for l in open(fn, encoding="utf-8"):
        l = l.rstrip("\n")
        m = re.match(r"^(.*?):(\d+): (.*)" + chr(36), l)
        if m:
            d[(norm(m.group(1)), int(m.group(2)))] = m.group(3)
    return d

def load_snapshots():
    """Multiple (file,line)->text snapshots from different points in time."""
    return [load_dump_file(fn) for fn in
            ["_comment_dump.txt", "_missed_dump.txt", "_fresh_dump.txt"]]

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

def current_texts():
    """Map (file, exact-line-text) -> True for all comment lines in current files."""
    out = {}
    for (p, num), txt in make_fresh_dump().items():
        out[(p, txt)] = True
    return out

def main():
    snaps = load_snapshots()
    cur = current_texts()
    trans = load_line_trans()
    canon = load_canonical()
    bad = []
    added = 0
    for (p, num), zh in trans.items():
        txt = None
        for snap in snaps:
            t = snap.get((p, num))
            if t is not None and (p, t) in cur:
                txt = t
                break
        if txt is None:
            bad.append((p, num, "NO VALID SNAPSHOT TEXT")); continue
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
