#!/usr/bin/env python3
"""Align a positional translation file to its numbered source range via DP.

Usage: python _subagent_align.py TRANSLATION_FILE START END INDEXED_OUTFILE
Outputs N<TAB>translation lines and prints unmatched source lines.
"""
import re
import sys

ROOT = r"D:/code/mpi/argent-go"


def toks(s):
    return set(re.findall(r"[A-Za-z_][A-Za-z0-9_]*|\d+|`[^`]*`", s))


def main():
    trfile, start, end, outfile = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
    src = []
    with open(ROOT + "/_subagent_numbered.txt", encoding="utf-8") as f:
        for raw in f.read().splitlines():
            if "\t" in raw:
                n, line = raw.split("\t", 1)
                if start <= int(n) <= end:
                    src.append((int(n), line))
    IDX = [n for n, _ in src]
    S = [line for _, line in src]
    tr = open(trfile, encoding="utf-8").read().splitlines()
    n, m = len(S), len(tr)
    st, tt = [toks(x) for x in S], [toks(x) for x in tr]
    GAP = -3
    dp = [[0] * (m + 1) for _ in range(n + 1)]
    for i in range(1, n + 1):
        dp[i][0] = dp[i - 1][0] + GAP
    for j in range(1, m + 1):
        dp[0][j] = dp[0][j - 1] + GAP
    for i in range(1, n + 1):
        for j in range(1, m + 1):
            sc = len(st[i - 1] & tt[j - 1])
            sb = [x for x in st[i - 1] if x.startswith("`")]
            tb = [x for x in tt[j - 1] if x.startswith("`")]
            if sb and sb == tb:
                sc += 2
            dp[i][j] = max(dp[i - 1][j - 1] + sc, dp[i - 1][j] + GAP, dp[i][j - 1] + GAP)
    i, j = n, m
    pairs = []
    while i > 0 and j > 0:
        diag = dp[i - 1][j - 1] + len(st[i - 1] & tt[j - 1])
        if diag >= dp[i - 1][j] + GAP and diag >= dp[i][j - 1] + GAP:
            pairs.append((i - 1, j - 1)); i -= 1; j -= 1
        elif dp[i - 1][j] >= dp[i][j - 1]:
            i -= 1
        else:
            j -= 1
    pairs.reverse()
    matched = set(p[0] for p in pairs)
    missing = [IDX[k] for k in range(n) if k not in matched]
    with open(outfile, "w", encoding="utf-8") as f:
        for si, ti in pairs:
            f.write(f"{IDX[si]}\t{tr[ti]}\n")
    print("src:", n, "translations:", m, "pairs:", len(pairs), "missing:", len(missing))
    for num in missing:
        line = next(x for k, x in src if k == num)
        print("MISSING", num, "|", line[:100])
    # report backtick mismatches among pairs
    bad = 0
    for si, ti in pairs:
        sb = [x for x in st[si] if x.startswith("`")]
        tb = [x for x in tt[ti] if x.startswith("`")]
        if sb != tb:
            bad += 1
    print("BACKTICK_MISMATCHES:", bad)


if __name__ == "__main__":
    main()
