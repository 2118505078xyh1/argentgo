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

# ===== compact_budget_test.go =====
apply("compact_budget_test.go", [
("\t\t{3, 2}, // first tool result: walk back over the whole tool run",
 "\t\t{3, 2}, // 第一个工具结果：回退跳过整个工具运行段"),
("\t\t{4, 2}, // second result of the same run",
 "\t\t{4, 2}, // 同一运行段的第二个结果"),
("\t\t{6, 5}, // single tool result: back up to its assistant",
 "\t\t{6, 5}, // 单个工具结果：回退到它的 assistant"),
("\t\t{5, 5}, // assistant: unchanged", "\t\t{5, 5}, // assistant：不变"),
("\t\t{7, 7}, // trailing assistant: unchanged", "\t\t{7, 7}, // 末尾的 assistant：不变"),
("\t// Each padded entry costs about 4 + 50 tokens; 120 fits the last two.",
 "\t// 每条填充条目约耗 4 + 50 个 token；120 容得下最后两条。"),
("\t// A compacted history that already fits the budget is a no-op.",
 "\t// 已经符合预算的压缩历史是空操作。"),
])

# ===== compact_exit_case_test.go =====
apply("compact_exit_case_test.go", [
("// TestFailureReasonRecognizesExitMarkerInAnyCase covers a real compaction bug:",
 "// TestFailureReasonRecognizesExitMarkerInAnyCase 覆盖一个真实的压缩 bug："),
("// failureReason located \"[exit N]\" case-insensitively but then stripped the",
 "// failureReason 以不区分大小写的方式定位 \"[exit N]\"，但随后从"),
("// literal lowercase prefix from the ORIGINAL text. A shell that printed",
 "// 原始文本中剥离了字面小写前缀。因此，打印"),
("// \"[EXIT 1]\" (or \"[Exit 2]\") therefore parsed no digits and the failure was",
 "// \"[EXIT 1]\"（或 \"[Exit 2]\"）的 shell 解析不出数字，失败"),
("// dropped from the compaction summary even though the marker had been found.",
 "// 会从压缩摘要中丢失，尽管标记已经被找到了。"),
("// The digits must be read from the lowered string, where case folding cannot",
 "// 数字必须从小写化后的字符串中读取，那里大小写折叠不会"),
("// change ASCII digits, while the returned text keeps the original spelling.",
 "// 改变 ASCII 数字，同时返回的文本保留原始拼写。"),
("// TestFailureReasonStillRejectsSuccessfulAndMalformedExitMarkers guards the",
 "// TestFailureReasonStillRejectsSuccessfulAndMalformedExitMarkers 守护修复的"),
("// negative half of the fix: only a non-zero numeric exit code is a failure,",
 "// 否定面：只有非零数字退出码才算失败，"),
("// regardless of the marker's case.", "// 与标记的大小写无关。"),
("\t\t\"compile done [EXIT 0]\",  // success", "\t\t\"compile done [EXIT 0]\",  // 成功"),
("\t\t\"compile done [Exit 00]\", // zero with leading zeroes",
 "\t\t\"compile done [Exit 00]\", // 带前导零的零"),
("\t\t\"compile done [EXIT x]\",  // non-numeric", "\t\t\"compile done [EXIT x]\",  // 非数字"),
("\t\t\"compile done [EXIT -1]\", // negative", "\t\t\"compile done [EXIT -1]\", // 负数"),
("// TestSummarizeKeepsUppercaseExitFailure is the end-to-end guard: a dropped",
 "// TestSummarizeKeepsUppercaseExitFailure 是端到端的守护：被丢弃的工具结果"),
("// tool result whose output carries an upper-case exit marker must still surface",
 "// 若输出带有大写退出标记，必须仍能作为失败出现在"),
("// as a failure in the compaction summary, so a resumed turn does not forget it.",
 "// 压缩摘要中，使恢复的轮次不会忘记它。"),
])

# ===== compact_pairing_test.go =====
apply("compact_pairing_test.go", [
("// assistantWithCalls builds an assistant Entry whose Data carries the full",
 "// assistantWithCalls 构建 assistant Entry，其 Data 携带 agent 循环持久化的"),
("// llm.Message payload shape persisted by the agent loop: \"tool_calls\" is an",
 "// 完整 llm.Message 载荷形态：\"tool_calls\" 是一个"),
("// array of objects with an \"id\".", "// 带 \"id\" 的对象数组。"),
("// toolResult builds a tool Entry with the result id recorded under the same",
 "// toolResult 构建工具 Entry，其结果 id 记录在与转录相同的"),
("// key the transcript uses (\"id\"), which messageEntry maps to ToolCallID.",
 "// 键（\"id\"）下，messageEntry 会把它映射为 ToolCallID。"),
("\t\t\tstart: 4, // points at the third result",
 "\t\t\tstart: 4, // 指向第三个结果"),
("\t\t\twant:  1, // whole batch + assistant moved to the kept side",
 "\t\t\twant:  1, // 整批 + assistant 移到保留侧"),
("\t\t\tstart: 4, // points at the second result; both calls are at 1",
 "\t\t\tstart: 4, // 指向第二个结果；两个调用都在 1"),
("\t\t\twant:  1, // back up over the first call too, so no pair is split",
 "\t\t\twant:  1, // 也回退跳过第一个调用，使任何配对都不被拆分"),
("\t\t\t// The legacy helper only looks at a trailing run of tool results.",
 "\t\t\t// 旧版辅助函数只看末尾连续的工具结果。"),
("\t\t\t// Here start points at a user message, so the old cut would leave",
 "\t\t\t// 这里 start 指向一条用户消息，因此旧切分会留下"),
("\t\t\t// assistant's call in the summary while keeping its result.",
 "\t\t\t// assistant 的调用在摘要中，却保留它的结果。"),
])

# ===== compact_retention_summary_test.go =====
apply("compact_retention_summary_test.go", [
("// TestCompactionRestoresOldSummaryText covers the compaction+retention",
 "// TestCompactionRestoresOldSummaryText 覆盖压缩+保留的交互："),
("// interaction: a compaction record is stamped RetainAlways, so when a later",
 "// 压缩记录被标记为 RetainAlways，因此当后续"),
("// compaction drops it, retainedForCompaction re-emits it. entryKind maps the",
 "// 压缩丢弃它时，retainedForCompaction 会重新发出它。entryKind 把"),
("// compaction's system Entry to KindSummary, but the re-emitted record keeps the",
 "// 压缩的 system Entry 映射为 KindSummary，但重新发出的记录仍保留"),
("// Compaction payload (which has a \"summary\" key, not \"text\"/\"content\"). Without",
 "// Compaction 载荷（它有 \"summary\" 键，而不是 \"text\"/\"content\"）。如果"),
("// \"summary\" in messageEntry's KindSummary keys, the restored message loses all",
 "// messageEntry 的 KindSummary 键中没有 \"summary\"，恢复的消息会丢失"),
("// its text and resurfaces as an empty system entry.",
 "// 全部文本，变成一条空的系统消息。"),
("\t// Grow the conversation past the first compaction and compact again. The",
 "\t// 让对话增长到第一次压缩之后，再压缩一次。此时"),
("\t// cut now drops the first compaction's system Entry, which is flagged for",
 "\t// 切分会丢弃第一次压缩的 system Entry，它被标记为"),
("\t// retention because KindCompaction is stamped RetainAlways.",
 "\t// 保留，因为 KindCompaction 被标记为 RetainAlways。"),
("\t// The re-emitted summary must still carry the original text, not an empty",
 "\t// 重新发出的摘要仍必须携带原始文本，而不是空的"),
("\t// system message. There are now two system entries: the new compaction's",
 "\t// 系统消息。现在有两条系统条目：新压缩的"),
("\t// summary and the restored one; the earlier one must survive verbatim.",
 "\t// 摘要和被恢复的那条；较早的那条必须逐字保留。"),
("// TestMessageEntrySummaryKey documents that a KindSummary carrying the",
 "// TestMessageEntrySummaryKey 记录这样一个事实：携带规范 \"summary\" 载荷键的"),
("// canonical \"summary\" payload key projects onto the same Entry as one carrying",
 "// KindSummary 与携带 \"text\" 的条目投影到相同的 Entry——"),
("// \"text\" — the invariant retainedForCompaction relies on.",
 "// 这正是 retainedForCompaction 所依赖的不变量。"),
])

# ===== compact_summarize_test.go =====
apply("compact_summarize_test.go", [
("// pathEntry builds a tool entry whose payload carries a file path, matching",
 "// pathEntry 构建工具条目，其载荷携带文件路径，匹配"),
("// the \"path\" key that pathsFromData collects.",
 "// pathsFromData 收集的 \"path\" 键。"),
("\t// Appearance order: the three lexically largest paths are oldest, the",
 "\t// 出现顺序：字典序最大的三个路径最早，"),
("\t// three lexically smallest are newest. Sorting before tailItems (the old",
 "\t// 字典序最小的三个最新。在 tailItems 之前排序（旧"),
("\t// behaviour) would keep the wrong twelve.",
 "\t// 行为）会保留错误的十二条。"),
("\t// Display order is still sorted for a stable summary.",
 "\t// 显示顺序仍然排序，以保持摘要稳定。"),
("// Summarize keeps only the most recent twelve file paths before sorting. When",
 "// Summarize 在排序前只保留最近十二条文件路径。当"),
("// the twelve-path cutoff falls inside a single payload that carries several",
 "// 十二条路径的截断点恰好落在携带多条路径的单个载荷内部时，"),
("// paths, the chosen subset must not depend on Go's randomized map iteration:",
 "// 所选择的子集不得依赖 Go 随机化的 map 迭代："),
("// the same transcript must always produce the same \"Files changed\" section.",
 "// 同一份转录必须始终产生相同的 \"Files changed\" 章节。"),
("// This mirrors the deterministic-key rule collectStrings uses in search.go.",
 "// 这镜像了 search.go 中 collectStrings 使用的确定性键规则。"),
("\t// One payload carrying ten paths in random map order; combined with the",
 "\t// 一个以随机 map 顺序携带十条路径的载荷；与下面的条目组合后，"),
("\t// entries below, tailItems(files, 12) cuts through this very block.",
 "\t// tailItems(files, 12) 恰好切过这个块。"),
("\t// Whatever the internal appearance order, the five oldest paths must be",
 "\t// 无论内部出现顺序如何，最早的五条路径必须"),
("\t// the ones dropped and the eight newest ones the ones kept.",
 "\t// 被丢弃，最新的八条必须被保留。"),
("// TestSummarizeDoesNotPromoteSummaryHeadersToNextSteps covers repeated",
 "// TestSummarizeDoesNotPromoteSummaryHeadersToNextSteps 覆盖重复压缩："),
("// compaction: a compaction record is stamped RetainAlways, so a later",
 "// 压缩记录被标记为 RetainAlways，因此后续"),
("// compaction sees the previous summary as a system Entry (its Data is the old",
 "// 压缩会把先前的摘要视为 system Entry（其 Data 是旧的"),
("// Compaction payload) and re-runs the next-step heuristics on its markdown",
 "// Compaction 载荷），并对其 markdown 文本重新运行下一步启发式。"),
("// text. The old summary's own \"## Next steps\" header matches the \"next step\"",
 "// 旧摘要自身的 \"## Next steps\" 标题匹配 \"next step\""),
("// marker, which used to surface as a literal \"- ## Next steps\" bullet in the",
 "// 标记，过去常在新摘要中变成字面量 \"- ## Next steps\" 的项目符号。"),
("// new summary. Markdown headings are structure, never a plan.",
 "// markdown 标题是结构，绝不是计划。"),
("// TestSummarizeStillKeepsGenuineNextStepAfterHeader guards the fix above from",
 "// TestSummarizeStillKeepsGenuineNextStepAfterHeader 防止上面的修复"),
("// dropping real next steps that follow a heading in the same content.",
 "// 丢弃同一内容中跟在标题之后的真实下一步。"),
("\tlongStep := \"下一步: \" + strings.Repeat(\"补中文注释并跑测试\", 60) // >200 runes",
 "\tlongStep := \"下一步: \" + strings.Repeat(\"补中文注释并跑测试\", 60) // >200 符文"),
])

# ===== compact_structured_test.go =====
apply("compact_structured_test.go", [
("\t// Existing free text from Summarize: legacy headings must be recognized",
 "\t// Summarize 产生的现有自由文本：旧标题必须能被识别，"),
("\t// without changing what Summarize writes.", "// 而不改变 Summarize 写入的内容。"),
("\t// None of these carry a recognized markdown heading, so the caller must",
 "\t// 这些都不带有可识别的 markdown 标题，因此调用方必须"),
("\t// keep treating the summary as free text instead of a trusted DTO.",
 "\t// 继续把摘要当作自由文本，而不是可信的 DTO。"),
("\t\t{}, // empty fields round-trip through \"- (none)\" placeholders",
 "\t\t{}, // 空字段通过 \"- (none)\" 占位符往返"),
("\t// The projection is read-only: the underlying free text is untouched.",
 "\t// 该投影是只读的：底层自由文本不受影响。"),
])

# ===== compact_utf8_test.go =====
apply("compact_utf8_test.go", [
("// TestFailureReasonKeepsExitCodeAfterLowercaseLengthChange covers a real",
 "// TestFailureReasonKeepsExitCodeAfterLowercaseLengthChange 覆盖一个真实的"),
("// compaction bug: failureReason lowercased the whole tool output to locate the",
 "// 压缩 bug：failureReason 将整个工具输出小写化以定位"),
("// \"[exit N]\" marker case-insensitively, then used that LOWERCASED byte offset to",
 "// 不区分大小写的 \"[exit N]\" 标记，然后用那个小写化后的字节偏移去"),
("// slice the ORIGINAL text. strings.ToLower folds rune-by-rune, so a rune whose",
 "// 切分原始文本。strings.ToLower 按符文逐一折叠，因此标记之前"),
("// lowercase form is fewer bytes (İ 2→1, ẞ 3→2, K 3→1) before the marker shifts",
 "// 小写形式字节更少的符文（İ 2→1、ẞ 3→2、K 3→1）会把偏移推离"),
("// the offset off a rune boundary. The result was a garbled tail, a failed prefix",
 "// 符文边界。结果是乱码尾部、前缀匹配失败，"),
("// match, and — when no other failure marker existed — the failure being dropped",
 "// 以及——当没有其他失败标记时——失败被完全"),
("// from the compaction summary entirely. The offset must be mapped back through",
 "// 从压缩摘要中丢弃。在切分原始字符串之前，偏移必须通过"),
("// rune indices before slicing the original string.", "// 符文索引映射回去。"),
("\t\t{\"dotted-capital-i\", \"İ done [exit 1]\"}, // U+0130 -> i: 2 bytes -> 1",
 "\t\t{\"dotted-capital-i\", \"İ done [exit 1]\"}, // U+0130 -> i：2 字节 -> 1"),
("\t\t{\"capital-sharp-s\", \"ẞ done [exit 1]\"},  // U+1E9E -> ß: 3 bytes -> 2",
 "\t\t{\"capital-sharp-s\", \"ẞ done [exit 1]\"},  // U+1E9E -> ß：3 字节 -> 2"),
("\t\t{\"kelvin-sign\", \"K done [exit 1]\"},      // U+212A -> k: 3 bytes -> 1",
 "\t\t{\"kelvin-sign\", \"K done [exit 1]\"},      // U+212A -> k：3 字节 -> 1"),
("\t\t{\"cjk-unchanged\", \"编译完成 [exit 1]\"},      // CJK lowercases to itself",
 "\t\t{\"cjk-unchanged\", \"编译完成 [exit 1]\"},      // 中文小写化后不变"),
("// TestSummarizeKeepsFailureWithUnicodeBeforeExitCode is the end-to-end guard:",
 "// TestSummarizeKeepsFailureWithUnicodeBeforeExitCode 是端到端守护："),
("// a dropped tool result whose output carries a length-changing rune before an",
 "// 被丢弃的工具结果若在退出码之前带有长度会变化的符文，"),
("// exit code must still surface as a failure in the compaction summary, rather",
 "// 仍必须作为失败出现在压缩摘要中，而不是"),
("// than silently losing the evidence a resumed turn needs.",
 "// 静默丢失恢复轮次所需的证据。"),
])
