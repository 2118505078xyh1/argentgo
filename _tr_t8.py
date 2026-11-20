import io
BASE = r"D:\code\mpi\argENT-go\internal\session"

def apply(fname, pairs, allpairs=()):
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
    for old, new in allpairs:
        n = text.count(old)
        if n == 0:
            missing.append(("(all)" + old[:70].replace("\n", "\\n"), n))
            continue
        text = text.replace(old, new)
    with io.open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    print(("OK" if not missing else "MISSING"), fname)
    for m, n in missing:
        print("  count=%d %s" % (n, m))

# ===== search_case_ext_test.go =====
apply("search_case_ext_test.go", [
("// TestSearchStripsUppercaseJSONLExtension covers a case-inconsistency bug:",
 "// TestSearchStripsUppercaseJSONLExtension 覆盖一个大小写不一致的 bug："),
("// Search matches the \".jsonl\" suffix case-insensitively but used a",
 "// Search 以不区分大小写的方式匹配 \".jsonl\" 后缀，却用"),
("// case-sensitive strings.TrimSuffix to derive the session id. A hand-copied",
 "// 区分大小写的 strings.TrimSuffix 派生会话 id。因此手工复制的"),
("// transcript named \"SESSION.JSONL\" therefore produced Hit.ID \"SESSION.JSONL\",",
 "// 名为 \"SESSION.JSONL\" 的转录会产生 Hit.ID \"SESSION.JSONL\"，"),
("// which no longer resolves through Store.OpenSession. The id must be stripped",
 "// 再也无法通过 Store.OpenSession 解析。必须按长度剥离 id，"),
("// by length, the same way Container.List does, preserving the original case.",
 "// 与 Container.List 一样，并保留原始大小写。"),
])

# ===== search_test.go =====
apply("search_test.go", [
("// A JSON object is decoded into a Go map, whose iteration order is randomized.",
 "// JSON 对象解码为 Go map，其迭代顺序是随机的。"),
("// When several string values in one record contain the needle, the chosen",
 "// 当一条记录中有多个字符串值包含 needle 时，所选择的"),
("// snippet must still be stable across calls: revisiting the same transcript",
 "// 片段在多次调用间仍必须稳定：重新访问同一转录"),
("// must not yield a different window into the same text. This pins the fix in",
 "// 不得产生关于同一文本的不同窗口。这固化了 collectStrings 中的修复"),
("// collectStrings (keys visited in sorted order).", "// （按排序后的顺序访问键）。"),
("\t// With sorted keys, \"alpha\" is always visited first, so the window must",
 "\t// 键已排序，\"alpha\" 总是最先被访问，因此窗口必须"),
("\t// always come from its value (the leading run of A's).",
 "\t// 始终来自它的值（开头连续的 A）。"),
("// stringValues feeds a payload's strings to snippetFor. Its order must be a",
 "// stringValues 把载荷的字符串提供给 snippetFor。其顺序必须只"),
("// function of the payload alone, never of the Go map's random seed; otherwise",
 "// 取决于载荷本身，绝不取决于 Go map 的随机种子；否则"),
("// the \"first\" matching string is not well defined.",
 "// \"第一个\"匹配的字符串就没有明确定义。"),
("// searchByFullScan is the pre-index Search, used as the semantic baseline: no",
 "// searchByFullScan 是索引出现前的 Search，用作语义基线："),
("// sidecar is consulted and every transcript is parsed from scratch.",
 "// 不查询旁车文件，每份转录都从头解析。"),
("\t// A second pass must use the sidecars and return identical hits.",
 "\t// 第二遍必须使用旁车文件并返回相同的命中。"),
("\t// A corrupt sidecar is ignored: the body is still searched in full.",
 "\t// 损坏的旁车文件会被忽略：正文仍被完整搜索。"),
])

# ===== search_unicode_test.go =====
apply("search_unicode_test.go", [
("// TestSnippetForCaseFoldKeepsRuneAlignment covers session audit #7. Lowercasing",
 "// TestSnippetForCaseFoldKeepsRuneAlignment 覆盖 session 审计 #7。小写化"),
("// a lead character such as İ (2→1 bytes), the Kelvin sign (3→1) or ẞ (3→2)",
 "// 首字符，如 İ（2→1 字节）、开尔文符号（3→1）或 ẞ（3→2），"),
("// shortens the byte string, so using the lowered byte offset to slice the",
 "// 会缩短字节串，因此用小写化后的字节偏移切分"),
("// original value shifts the snippet window. The rune offset is stable because",
 "// 原始值会推移片段窗口。符文偏移是稳定的，因为"),
("// strings.ToLower folds rune by rune.", "// strings.ToLower 按符文逐一折叠。"),
("\t\t{\"dotted-capital-i\", \"İ\"}, // U+0130 -> i: 2 bytes -> 1",
 "\t\t{\"dotted-capital-i\", \"İ\"}, // U+0130 -> i：2 字节 -> 1"),
("\t\t{\"kelvin-sign\", \"\\u212a\"}, // U+212A -> k: 3 bytes -> 1",
 "\t\t{\"kelvin-sign\", \"\\u212a\"}, // U+212A -> k：3 字节 -> 1"),
("\t\t{\"capital-sharp-s\", \"ẞ\"},  // U+1E9E -> ß: 3 bytes -> 2",
 "\t\t{\"capital-sharp-s\", \"ẞ\"},  // U+1E9E -> ß：3 字节 -> 2"),
("\t\t\t// One folded rune up front, 60 x's, the needle, then enough y's",
 "\t\t\t// 开头一个被折叠的符文，60 个 x，needle，然后足够多的 y，"),
("\t\t\t// that the 96-rune tail is fully materialised (and ellipsised).",
 "\t\t\t// 使 96 符文的尾部被完整生成（并被省略）。"),
("// TestSnippetForFoldedNeedle checks the symmetric case: the needle itself",
 "// TestSnippetForFoldedNeedle 检查对称情形：needle 本身"),
("// contains a rune whose lowercase form has a different byte length.",
 "// 包含一个小写形式字节长度不同的符文。"),
])

# ===== session_bench_test.go =====
apply("session_bench_test.go", [
("// This file records performance baselines for the session hot paths: JSONL",
 "// 本文件记录会话热路径的性能基线：JSONL"),
("// append, full transcript parsing, and the List/sidecar index. All inputs are",
 "// 追加、完整转录解析以及 List/旁车索引。所有输入都是"),
("// fixed in-memory fixtures written to b.TempDir(); no network, model or real",
 "// 写入 b.TempDir() 的固定内存夹具；不涉及网络、模型或真实的"),
("// wall-clock dependency is involved. No wall-clock thresholds are asserted —",
 "// 时钟依赖。不断言任何时钟阈值——"),
("// the numbers exist only so later runs can be compared.",
 "// 这些数字的存在只是为了让后续运行可以比较。"),
("// benchWriteTranscript writes a JSONL file with n well-formed records. The",
 "// benchWriteTranscript 写入一个包含 n 条格式良好记录的 JSONL 文件。"),
("// timestamp is fixed so repeated runs produce byte-identical files.",
 "// 时间戳是固定的，因此重复运行产生逐字节相同的文件。"),
("// benchTranscriptPath returns the path of a freshly written fixture transcript.",
 "// benchTranscriptPath 返回新写入的夹具转录的路径。"),
("// BenchmarkAppendRecord measures steady-state AppendRecord throughput: the",
 "// BenchmarkAppendRecord 测量稳态 AppendRecord 吞吐量：header 之后"),
("// index is warm after the header, so each iteration exercises payload encoding,",
 "// 索引已预热，因此每次迭代覆盖载荷编码、"),
("// id minting, one JSONL write and the post-write stat/sync.",
 "// id 生成、一次 JSONL 写入以及写入后的 stat/sync。"),
("// BenchmarkReadRecords measures the full JSONL parse used by every history",
 "// BenchmarkReadRecords 测量每次历史读取所用的完整 JSONL 解析，"),
("// read, across the three history sizes called out in the performance brief.",
 "// 覆盖性能简报中提到的三种历史规模。"),
("\t\t\t\t// readRecords prepends one synthetic schema header for v0",
 "\t\t\t\t// readRecords 会为 v0 转录在前面加一条合成 schema 头，"),
("\t\t\t\t// transcripts, so a fixture of n records parses to n+1.",
 "\t\t\t\t// 因此 n 条记录的夹具解析为 n+1 条。"),
("// BenchmarkSessionRecords measures the public Records() wrapper, which reads",
 "// BenchmarkSessionRecords 测量公开的 Records() 包装器，它读取"),
("// through the Session's own path rather than a raw file path.",
 "// 的是 Session 自身的路径，而不是原始文件路径。"),
("\t\t\t\t// Records() exposes the same synthetic schema header readRecords",
 "\t\t\t\t// Records() 暴露与 readRecords 相同的合成 schema 头，"),
("\t\t\t\t// prepends, so a fixture of n records yields n+1.",
 "\t\t\t\t// 因此 n 条记录的夹具产生 n+1 条。"),
("// BenchmarkList measures the store listing after sidecars are warm: each call",
 "// BenchmarkList 测量旁车文件预热后的 store 列表：每次调用"),
("// still validates every transcript but reads metadata from the .idx cache",
 "// 仍会校验每份转录，但从 .idx 缓存读取元数据，"),
("// instead of parsing the whole file.", "// 而不是解析整个文件。"),
("\t\t\t// One warm-up List builds the sidecars; the timed loop is the hot path.",
 "\t\t\t// 一次预热 List 构建旁车文件；计时循环是热路径。"),
("// BenchmarkMetaFromRecords isolates the index builder that a cold List runs on",
 "// BenchmarkMetaFromRecords 隔离冷 List 在没有可用旁车文件时，"),
("// every transcript without a usable sidecar.", "// 对每份转录运行的索引构建器。"),
])
