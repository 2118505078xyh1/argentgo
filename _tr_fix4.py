import io
BASE = r"D:\code\mpi\argENT-go\internal\session"

def apply(fname, pairs):
    path = BASE + "\\" + fname
    with io.open(path, "r", encoding="utf-8", newline="") as f:
        text = f.read()
    missing = []
    for old, new in pairs:
        n = text.count(old)
        if n != 1:
            missing.append((old[:70].replace("\n", "\\n"), n))
            continue
        text = text.replace(old, new, 1)
    with io.open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    print(("OK" if not missing else "MISSING"), fname)
    for m, n in missing:
        print("  count=%d %s" % (n, m))

apply("compact_activity.go", [
("\t// Each component may carry its own \"L\" prefix, e.g. \"L10-L20\".",
 "\t// 每个组件都可以自带 \"L\" 前缀，例如 \"L10-L20\"。"),
])

apply("compact_structured.go", [
("// unknown title -> bucketNone", "// 未知标题 -> bucketNone"),
])
