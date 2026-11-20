import io, re, os, sys

sys.stdout.reconfigure(encoding='utf-8')

BASE = r"D:\code\mpi\argENT-go\internal\session"
SKIP = {"writer.go","title.go","sync.go","session_read.go","tree.go"}
# prose function words that almost never appear as code identifiers
PROSE = re.compile(r'\b(the|a|an|is|are|was|were|to|of|and|or|that|this|it|its|be|by|from|as|at|not|but|if|when|so|no|must|should|may|can|will|would|each|every|all|any|which|these|those|there|here|into|over|under|out|before|after|then|than|only|also|such|other|more|most|with|for|in|on|while|because|does|did|has|have|had|been|about|between|across|through|without|within|against|another|both|either|since|until|given|takes|allows|needs|reports|ensures|verifies|mirrors|keeps|makes|used|using|added|first|last|new|old|still|never|always|just|same|different|because|whether|there|when|then|once|due|instead|rather)\b', re.I)

total = 0
for name in sorted(os.listdir(BASE)):
    if not name.endswith(".go") or name in SKIP:
        continue
    path = os.path.join(BASE, name)
    with io.open(path, "r", encoding="utf-8", newline="") as f:
        lines = f.read().split("\n")
    hits = []
    for i, line in enumerate(lines, 1):
        if "//" not in line:
            continue
        s = line.strip()
        if s.startswith("//go:") or "nolint:" in s:
            continue
        comment = line[line.index("//"):]
        prose = re.sub(r'`[^`]*`', '', comment)
        pw = PROSE.findall(prose)
        if len(pw) >= 2:
            hits.append((i, comment.strip()[:130]))
    if hits:
        total += len(hits)
        print("=== %s (%d) ===" % (name, len(hits)))
        for i, c in hits:
            print("  %d: %s" % (i, c))
print("TOTAL HITS", total)
