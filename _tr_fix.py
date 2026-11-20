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

def ind(block):
    return "\n".join("\t" + line for line in block.split("\n"))

apply("compact.go", [
(ind(r"""// Index tool pairs inside the dropped prefix so a flagged half pulls in
// the other half rather than leaving an orphan in the survived context."""),
 ind(r"""// 为被丢弃前缀内的工具对建立索引，这样带标记的一半会把另一半一起带入，
// 而不会在保留的上下文中留下孤立的一半。""")),

(ind(r"""// Map every tool-call id to the assistant indices that declared it. The
// transcript may contain retries with the same id, so keep all positions."""),
 ind(r"""// 将每个工具调用 id 映射到声明它的 assistant 索引。转录中可能包含使用相同 id 的重试，
// 因此保留所有位置。""")),

(ind(r"""// For each pair (call, result) the cut must not fall strictly between them,
// i.e. in (call, result]. Lowering start to the call index resolves that
// split; repeat because the new start may sit inside another pair's range."""),
 ind(r"""// 对于每一对（调用、结果），切分点不得严格落在两者之间，即不能落在 (call, result] 内。
// 将 start 降低到调用的索引即可解决这种拆分；重复此过程，因为新的 start 可能位于另一对的范围之内。""")),

(ind(r"""// files are collected in chronological order; keep the most recent twelve
// before sorting, otherwise "recent" would mean lexicographically last."""),
 ind(r"""// files 按时间顺序收集；在排序前保留最近十二个，否则"最近"就会变成字典序最后的那些。""")),

(ind(r"""// Deterministic, zero-LLM account of the reads/edits/commands that were
// dropped, so a resumed turn does not repeat them or forget a change."""),
 ind(r"""// 对被丢弃的读取/编辑/命令进行确定性的、无需 LLM 的记录，
// 使恢复的轮次不会重复这些操作或遗忘某项改动。""")),

(ind(r"""// A previous compaction's summary is projected as a system Entry and
// fed back through Summarize when a later compaction drops it. Its own
// markdown section headers ("## Next steps", "# TODO", ...) contain
// the markers below but are structure, not a plan: matching them turns
// a header into a bullet like "- ## Next steps" in the new summary.
// Heading lines never carry an actionable next step, so skip them."""),
 ind(r"""// 上一次压缩的摘要会被投影为 system Entry，并在后续压缩丢弃它时再次送入 Summarize。
// 它自身的 markdown 章节标题（"## Next steps"、"# TODO" 等）包含下面的标记，
// 但那是结构而非计划：匹配它们会把标题变成新摘要中类似 "- ## Next steps" 的项目符号。
// 标题行从不包含可执行的下一步，因此跳过它们。""")),

(ind(r"""// lower was searched case-insensitively, so the original text may spell
// the marker "[EXIT " or "[Exit ". Read the digits from lower (ASCII
// digits are unchanged by case folding) instead of stripping a
// lowercase-only prefix from tail; the latter silently loses every
// failure whose shell printed the marker in another case."""),
 ind(r"""// lower 是在忽略大小写的情况下搜索的，因此原始文本中的标记可能写作 "[EXIT " 或 "[Exit "。
// 应从 lower 读取数字（ASCII 数字不受大小写折叠影响），而不是从 tail 中剥离仅小写的
// 前缀；后者会静默丢失所有 shell 以其他大小写打印标记的失败。""")),

(ind(r"""// index is a byte offset into lower; map it back to trimmed before
// slicing so a preceding rune whose lowercase is shorter cannot shift
// the window into the middle of a code point (or lose the failure)."""),
 ind(r"""// index 是 lower 中的字节偏移；在切分前将其映射回 trimmed，
// 这样前面某个小写形式更短的符文就不会把窗口推移到码点中间（或丢失失败信息）。""")),

(ind(r"""// A JSON object decodes into a Go map, whose iteration order is
// randomized. pathsFromData feeds Summarize, which keeps only the most
// recent twelve paths before sorting (see tailItems(files, 12)). If the
// cutoff lands inside a single payload that carries several paths,
// map order would decide which subset survives — the same transcript
// could produce a different "Files changed" section on every run.
// Visit keys in sorted order so appearance order is deterministic;
// collectStrings in search.go applies the same rule."""),
 ind(r"""// JSON 对象解码为 Go map，其迭代顺序是随机的。pathsFromData 为 Summarize 提供数据，
// 后者在排序前只保留最近的十二条路径（见 tailItems(files, 12)）。如果截断点恰好落在
// 某个携带多条路径的载荷内部，map 的顺序就会决定哪些子集得以保留——同一份转录
// 每次运行都可能产生不同的 "Files changed" 部分。按排序后的顺序访问键，
// 使出现顺序具有确定性；search.go 中的 collectStrings 也采用同样的规则。""")),

(r"// keep original text byte-faithful (no \u003c etc.)", r"// 保持原始文本逐字节忠实（不产生 \u003c 等转义）"),

(ind(r"""// os.Rename does not overwrite an existing file on Windows, so drop a
// previous archive of the same id first. The id is expected to be unique
// per compaction; this only matters when a caller deliberately rewrites."""),
 ind(r"""// os.Rename 在 Windows 上不会覆盖已存在的文件，因此先删除同一 id 的旧归档。
// id 预期在每次压缩中都是唯一的；只有在调用方刻意重写时此处理才有意义。""")),

(ind(r"""// The path may come from a persisted record, so never let it escape the
// store directory (matching the invariant for every other filesystem path)."""),
 ind(r"""// 该路径可能来自持久化记录，因此绝不能让它逃出存储目录
// （与处理其他所有文件系统路径的不变量一致）。""")),
])
