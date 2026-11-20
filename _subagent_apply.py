#!/usr/bin/env python3
"""Apply comment-only translations to a fixed set of Go directories.

_subagent_numbered.txt: one line per target English comment, format N<TAB>line.
_subagent_indexed_*.txt: N<TAB>translated comment, keyed by numbered-line index.
Each value is the translated comment WITHOUT leading indentation, starting with
//, /* or *. Leading whitespace/code is preserved from source.
"""
import glob
import os

ROOT = os.path.dirname(os.path.abspath(__file__))
NUMFILE = os.path.join(ROOT, "_subagent_numbered.txt")
TRANSGLOB = os.path.join(ROOT, "_subagent_indexed_*.txt")

DIRS = [
    "internal/commands", "internal/update", "internal/memory", "internal/hooks",
    "internal/workflow", "internal/eval", "internal/lsp", "internal/auth",
    "internal/automation", "internal/skills", "internal/provider", "internal/jobs",
    "internal/pty", "internal/plugins", "internal/i18n", "internal/tokenize",
    "internal/archguard", "native", "cmd",
]


def main():
    eng_lines = []
    with open(NUMFILE, "r", encoding="utf-8") as f:
        for raw in f.read().splitlines():
            if "\t" not in raw:
                continue
            eng_lines.append(raw.split("\t", 1)[1])

    # index -> translated comment (without leading indentation)
    by_index = {}
    for path in sorted(glob.glob(TRANSGLOB)):
        with open(path, "r", encoding="utf-8") as f:
            for raw in f.read().splitlines():
                if "\t" not in raw:
                    continue
                num, body = raw.split("\t", 1)
                by_index[num.strip()] = body

    print("english lines:", len(eng_lines), "translations:", len(by_index))
    by_line = {}
    for i, line in enumerate(eng_lines):
        num = str(i + 1)
        if num in by_index:
            by_line[line] = by_index[num]

    changed = 0
    files_changed = 0
    unmatched = set(by_line.keys())
    for d in DIRS:
        base = os.path.join(ROOT, d)
        for dirpath, _, filenames in os.walk(base):
            for fn in filenames:
                if not fn.endswith(".go"):
                    continue
                p = os.path.join(dirpath, fn)
                with open(p, "r", encoding="utf-8", newline="") as f:
                    text = f.read()
                nl = "\r\n" if "\r\n" in text else "\n"
                lines = text.split(nl)
                out = []
                modified = False
                for line in lines:
                    if line in by_line and ("//" in line or "/*" in line):
                        prefix = ""
                        for ch in line:
                            if ch in " \t":
                                prefix += ch
                            else:
                                break
                        new = prefix + by_line[line]
                        if new != line:
                            out.append(new)
                            modified = True
                            changed += 1
                            unmatched.discard(line)
                            continue
                    out.append(line)
                if modified:
                    with open(p, "w", encoding="utf-8", newline="") as f:
                        f.write(nl.join(out))
                    files_changed += 1

    print("lines replaced:", changed)
    print("files changed:", files_changed)
    if unmatched:
        print("WARN translations not found in source (count):", len(unmatched))
        for x in list(sorted(unmatched))[:15]:
            print("  MISS:", repr(x[:90]))


if __name__ == "__main__":
    main()
