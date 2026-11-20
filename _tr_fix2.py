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
            missing.append((old[:60].replace("\n", "\\n"), n))
            continue
        text = text.replace(old, new, 1)
    with io.open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    if missing:
        print("MISSING in", fname)
        for m, n in missing:
            print("  count=%d %s" % (n, m))
    else:
        print("OK", fname, len(pairs), "replacements")

def ind(block, n):
    p = "\t" * n
    return "\n".join(p + line for line in block.split("\n"))

apply("compact.go", [
(ind(r"""// A previous compaction's summary is projected as a system Entry and
// fed back through Summarize when a later compaction drops it. Its own
// markdown section headers ("## Next steps", "# TODO", ...) contain
// the markers below but are structure, not a plan: matching them turns
// a header into a bullet like "- ## Next steps" in the new summary.
// Heading lines never carry an actionable next step, so skip them.""", 2),
 ind(r"""// 上一次压缩的摘要会被投影为 system Entry，并在后续压缩丢弃它时再次送入 Summarize。
// 它自身的 markdown 章节标题（"## Next steps"、"# TODO" 等）包含下面的标记，
// 但那是结构而非计划：匹配它们会把标题变成新摘要中类似 "- ## Next steps" 的项目符号。
// 标题行从不包含可执行的下一步，因此跳过它们。""", 2)),

(ind(r"""// lower was searched case-insensitively, so the original text may spell
// the marker "[EXIT " or "[Exit ". Read the digits from lower (ASCII
// digits are unchanged by case folding) instead of stripping a
// lowercase-only prefix from tail; the latter silently loses every
// failure whose shell printed the marker in another case.""", 2),
 ind(r"""// lower 是在忽略大小写的情况下搜索的，因此原始文本中的标记可能写作 "[EXIT " 或 "[Exit "。
// 应从 lower 读取数字（ASCII 数字不受大小写折叠影响），而不是从 tail 中剥离仅小写的
// 前缀；后者会静默丢失所有 shell 以其他大小写打印标记的失败。""", 2)),

(ind(r"""// index is a byte offset into lower; map it back to trimmed before
// slicing so a preceding rune whose lowercase is shorter cannot shift
// the window into the middle of a code point (or lose the failure).""", 3),
 ind(r"""// index 是 lower 中的字节偏移；在切分前将其映射回 trimmed，
// 这样前面某个小写形式更短的符文就不会把窗口推移到码点中间（或丢失失败信息）。""", 3)),

(ind(r"""// A JSON object decodes into a Go map, whose iteration order is
// randomized. pathsFromData feeds Summarize, which keeps only the most
// recent twelve paths before sorting (see tailItems(files, 12)). If the
// cutoff lands inside a single payload that carries several paths,
// map order would decide which subset survives — the same transcript
// could produce a different "Files changed" section on every run.
// Visit keys in sorted order so appearance order is deterministic;
// collectStrings in search.go applies the same rule.""", 2),
 ind(r"""// JSON 对象解码为 Go map，其迭代顺序是随机的。pathsFromData 为 Summarize 提供数据，
// 后者在排序前只保留最近的十二条路径（见 tailItems(files, 12)）。如果截断点恰好落在
// 某个携带多条路径的载荷内部，map 的顺序就会决定哪些子集得以保留——同一份转录
// 每次运行都可能产生不同的 "Files changed" 部分。按排序后的顺序访问键，
// 使出现顺序具有确定性；search.go 中的 collectStrings 也采用同样的规则。""", 2)),
])
