import io, sys

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

apply("compact.go", [
(r"""// ArchiveRange is a half-open byte range [Start, End) locating one message in
// a compaction archive. A reader can seek to Start and read End-Start bytes to
// recover that message verbatim, without loading the whole archive. ID/Role are
// copied from the original entry so a range can be matched to a summary bullet
// without decoding the payload.""",
r"""// ArchiveRange 是一个半开字节区间 [Start, End)，用于定位压缩归档中的一条消息。
// 读取器可以定位到 Start 并读取 End-Start 个字节，逐字还原该消息，而无需加载整个归档。
// ID/Role 从原始条目复制而来，因此无需解码载荷即可将区间与摘要中的项目符号匹配。"""),

(r"""// ArchiveRef is an optional pointer to the lossless archive of the messages a
// compaction dropped. Path is relative to the session store directory (the
// same dir that holds the <id>.jsonl transcripts), so a record can carry it
// without embedding a machine-specific absolute path. Bytes is the size of the
// archive file; Ranges maps each dropped message, in order, to its byte range
// inside that file.""",
r"""// ArchiveRef 是指向压缩所丢弃消息的无损归档的可选指针。Path 相对于会话存储目录
// （即存放 <id>.jsonl 转录的同一目录），因此记录可以携带它而无需嵌入特定于机器的绝对路径。
// Bytes 是归档文件的大小；Ranges 按顺序将每条被丢弃的消息映射到其在文件中的字节区间。"""),

(r"""// Compaction is the payload of a kind:"compaction" record. The summary
// replaces the dropped part of the conversation; Keep is how many of the most
// recent messages still follow it verbatim.
//
// Archive is additive and optional (omitempty): older records have no archive
// field and are read exactly as before. Existing compaction behaviour never
// sets it; writers opt in by calling WriteCompactionArchive / WriteCompactionArchiveRef.""",
r"""// Compaction 是 kind:"compaction" 记录的载荷。summary 替换对话中被丢弃的部分；
// Keep 表示仍逐字保留在其后的最近消息数量。
//
// Archive 是附加且可选的字段（omitempty）：较旧的记录没有 archive 字段，读取方式与以前完全相同。
// 现有的压缩行为从不设置它；写入方通过调用 WriteCompactionArchive / WriteCompactionArchiveRef 来选择启用。"""),

(r"""// Compact replaces the older part of the conversation with one summary record
// and keeps the last keep messages. It is a no-op when the history already fits.""",
r"""// Compact 用一条摘要记录替换对话中较旧的部分，并保留最后 keep 条消息。
// 当历史记录已经放得下时，此操作为空操作。"""),

(r"""// CompactBudget compacts the older part of the conversation so the kept suffix
// fits an estimated token budget. The original Compact(keep) signature is
// unchanged; this is the token-budget overload used by mid-turn compaction,
// where a fixed message count is meaningless for variable-length tool output.""",
r"""// CompactBudget 压缩对话中较旧的部分，使保留的后缀能够容纳在估算的 token 预算内。
// 原有的 Compact(keep) 签名保持不变；这是用于轮中压缩的 token 预算重载，
// 在工具输出长度可变的场景下，固定的消息数量没有意义。"""),

(r"""// appendCompaction writes the summary record and then re-emits any
// retention-flagged records from the dropped prefix so they survive
// (SelectForCompaction). A flagged tool call/result is re-emitted together with
// its partner, so compaction never reintroduces an orphaned result or a call
// without its result. When nothing is flagged this is byte-for-byte the old
// behaviour: one compaction record and nothing else.""",
r"""// appendCompaction 写入摘要记录，然后重新发出被丢弃前缀中任何带有保留标记的记录，
// 使其得以保留（SelectForCompaction）。带标记的工具调用/结果会与其配对项一起重新发出，
// 因此压缩绝不会重新引入孤立的结果，也不会留下没有结果的调用。
// 当没有任何记录被标记时，此行为与旧版逐字节一致：只写入一条压缩记录，别无其他。"""),

(r"""// retainedForCompaction returns the flagged records from the dropped prefix in
// original order, expanded to whole tool pairs. Only Record.Data is inspected
// by SelectForCompaction, so projecting Entry onto a minimal Record is enough.""",
r"""// retainedForCompaction 按原始顺序返回被丢弃前缀中带标记的记录，并扩展为完整的工具对。
// SelectForCompaction 只检查 Record.Data，因此将 Entry 投影为最小化的 Record 就已足够。"""),

(r"// No cap: a retention hint must win over keeping compaction cheap.",
 r"// 不设上限：保留提示必须优先于保持压缩的低成本。"),

(r"""// Index tool pairs inside the dropped prefix so a flagged half pulls in
// the other half rather than leaving an orphan in the survived context.""",
r"""// 为被丢弃前缀内的工具对建立索引，这样带标记的一半会把另一半一起带入，
// 而不会在保留的上下文中留下孤立的一半。"""),

(r"""// entryKind maps a conversation role back to the record kind used to re-emit
// it, so messageEntry projects the restored record onto the same Entry.""",
r"""// entryKind 将对话角色映射回用于重新发出该记录的记录 kind，
// 以便 messageEntry 将恢复的记录投影为相同的 Entry。"""),

(r"""// CutForBudget returns the first index to keep for a conversation whose kept
// suffix should fit budget. Zero means there is nothing worth compacting: the
// whole history already fits, or the budget is too small to keep any pair.""",
r"""// CutForBudget 返回对话中应保留的第一个索引，使保留的后缀能够容纳在预算内。
// 返回零表示没有值得压缩的内容：整个历史已经放得下，或者预算太小，连一对消息都放不下。"""),

(r"""// KeepForBudget reports how many trailing entries fit within budget. It walks
// from the newest entry backwards, so the most recent context survives.""",
r"""// KeepForBudget 报告有多少末尾条目能够容纳在预算内。它从最新的条目向前遍历，
// 因此最近的上下文得以保留。"""),

(r"""// adjustCut makes a proposed cut tool-pairing safe: a tool call and its result
// must end up on the same side of the cut. It first preserves the original
// behaviour of backing up over a trailing run of tool messages, then uses the
// tool-call ids carried in assistant payloads and tool results to back up over
// any remaining call/result pair straddling the cut. Backing up keeps more
// messages verbatim (the pair moves into the kept suffix); it never moves the
// cut forward, so a call can never be summarised while its result is kept (or
// vice versa). The same helper is used by Compact(keep) and CompactBudget.""",
r"""// adjustCut 使提议的切分点对工具配对安全：工具调用及其结果必须落在切分点的同一侧。
// 它首先保留原有行为，即向前回退以跳过末尾连续的工具消息，然后利用 assistant 载荷和工具结果中
// 携带的工具调用 id，继续回退以跳过任何横跨切分点的调用/结果对。回退会逐字保留更多消息
// （该对移入保留的后缀）；它从不将切分点向前移动，因此绝不会出现调用被摘要而其结果被保留
// （或反之）的情况。Compact(keep) 和 CompactBudget 都使用这个辅助函数。"""),

(r"// Legacy, id-less case: never cut inside a trailing run of tool results.",
 r"// 遗留的无 id 情况：绝不在末尾连续的工具结果中间切分。"),

(r"""// Map every tool-call id to the assistant indices that declared it. The
// transcript may contain retries with the same id, so keep all positions.""",
r"""// 将每个工具调用 id 映射到声明它的 assistant 索引。转录中可能包含使用相同 id 的重试，
// 因此保留所有位置。"""),

(r"""// For each pair (call, result) the cut must not fall strictly between them,
// i.e. in (call, result]. Lowering start to the call index resolves that
// split; repeat because the new start may sit inside another pair's range.""",
r"""// 对于每一对（调用、结果），切分点不得严格落在两者之间，即不能落在 (call, result] 内。
// 将 start 降低到调用的索引即可解决这种拆分；重复此过程，因为新的 start 可能位于另一对的范围之内。"""),

(r"""// toolCallIDs extracts the ids of the tool calls declared by an assistant
// entry. Assistant records persist the full llm.Message payload, whose key is
// "tool_calls" (some producers use "toolCalls"); older records may instead
// carry a single "tool_call_id". Entries without any id contribute no pairs,
// which preserves the id-less trailing-run behaviour of adjustCut.""",
r"""// toolCallIDs 提取 assistant 条目声明的工具调用 id。assistant 记录持久化完整的 llm.Message 载荷，
// 其键为 "tool_calls"（某些生产者使用 "toolCalls"）；较旧的记录可能只携带单个 "tool_call_id"。
// 没有任何 id 的条目不产生配对，从而保留 adjustCut 的无 id 末尾连续段行为。"""),

(r"""// lastCallBefore returns the greatest call index strictly before before, or -1
// when the id has no call on that side of the transcript.""",
r"""// lastCallBefore 返回严格位于 before 之前的最大调用索引；如果该 id 在转录的这一侧没有调用，则返回 -1。"""),

(r"""// estimateEntryTokens is a conservative, allocation-free estimate used only
// for compaction cuts. Wide (CJK) runes cost one token each; other runes follow
// the usual four-chars-per-token rule.""",
r"""// estimateEntryTokens 是仅用于压缩切分点的保守、无分配估算。宽字符（CJK）每个算一个 token；
// 其他字符遵循通常的每四字符一个 token 规则。"""),

(r"""// Summarize turns the dropped entries into a compaction payload that keeps the
// structure a later turn needs: goal, touched files, failures and next steps.""",
r"""// Summarize 将被丢弃的条目转换为压缩载荷，保留后续轮次所需的结构：目标、涉及的文件、失败和下一步。"""),

(r"""// files are collected in chronological order; keep the most recent twelve
// before sorting, otherwise "recent" would mean lexicographically last.""",
r"""// files 按时间顺序收集；在排序前保留最近十二个，否则"最近"就会变成字典序最后的那些。"""),

(r"""// Deterministic, zero-LLM account of the reads/edits/commands that were
// dropped, so a resumed turn does not repeat them or forget a change.""",
r"""// 对被丢弃的读取/编辑/命令进行确定性的、无需 LLM 的记录，
// 使恢复的轮次不会重复这些操作或遗忘某项改动。"""),

(r"""// nextStepMarkers are the short explicit cues a model uses to record
// unfinished work. Matching is intentionally loose (substring, case-folded):
// a false positive costs one extra bullet in the summary, while a miss would
// drop the plan a resumed run needs.""",
r"""// nextStepMarkers 是模型用来记录未完成工作的简短显式提示。匹配故意宽松（子串、忽略大小写）：
// 误报只会在摘要中多出一个项目符号，而漏报则会丢失恢复运行所需的计划。"""),

(r"""// nextStepLines keeps only the lines that actually carry a next-step marker.
// A plan is often written after a summary line, so inspecting just the first
// line of an entry loses it.""",
r"""// nextStepLines 只保留真正带有下一步标记的行。计划通常写在摘要行之后，
// 因此只检查条目的第一行会漏掉它。"""),

(r"""// A previous compaction's summary is projected as a system Entry and
// fed back through Summarize when a later compaction drops it. Its own
// markdown section headers ("## Next steps", "# TODO", ...) contain
// the markers below but are structure, not a plan: matching them turns
// a header into a bullet like "- ## Next steps" in the new summary.
// Heading lines never carry an actionable next step, so skip them.""",
r"""// 上一次压缩的摘要会被投影为 system Entry，并在后续压缩丢弃它时再次送入 Summarize。
// 它自身的 markdown 章节标题（"## Next steps"、"# TODO" 等）包含下面的标记，
// 但那是结构而非计划：匹配它们会把标题变成新摘要中类似 "- ## Next steps" 的项目符号。
// 标题行从不包含可执行的下一步，因此跳过它们。"""),

(r"// tailItems keeps the most recent limit entries.",
 r"// tailItems 保留最近的 limit 个条目。"),

(r"""// lowerByteOffset maps a byte offset in strings.ToLower(s) back to the
// corresponding byte offset in s. strings.ToLower folds rune-by-rune, so the
// rune index is identical in both strings even when a rune's lowercase form has
// a different byte length (e.g. U+0130 2->1, U+1E9E 3->2, U+212A 3->1 bytes).
// Slicing the original at a lowercased byte offset would otherwise start
// mid-rune and corrupt the text -- the alignment rule snippetFor uses too.""",
r"""// lowerByteOffset 将 strings.ToLower(s) 中的字节偏移映射回 s 中对应的字节偏移。
// strings.ToLower 按符文逐一折叠，因此即使某个符文的小写形式字节长度不同（例如 U+0130 2->1、
// U+1E9E 3->2、U+212A 3->1 字节），两个字符串中的符文索引也相同。
// 否则按小写后的字节偏移切分原字符串会从符文中间开始并破坏文本——snippetFor 也使用同样的对齐规则。"""),

(r"// failureReason recognizes a tool result that represents a failure.",
 r"// failureReason 识别表示失败的工具结果。"),

(r"""// lower was searched case-insensitively, so the original text may spell
// the marker "[EXIT " or "[Exit ". Read the digits from lower (ASCII
// digits are unchanged by case folding) instead of stripping a
// lowercase-only prefix from tail; the latter silently loses every
// failure whose shell printed the marker in another case.""",
r"""// lower 是在忽略大小写的情况下搜索的，因此原始文本中的标记可能写作 "[EXIT " 或 "[Exit "。
// 应从 lower 读取数字（ASCII 数字不受大小写折叠影响），而不是从 tail 中剥离仅小写的
// 前缀；后者会静默丢失所有 shell 以其他大小写打印标记的失败。"""),

(r"""// index is a byte offset into lower; map it back to trimmed before
// slicing so a preceding rune whose lowercase is shorter cannot shift
// the window into the middle of a code point (or lose the failure).""",
r"""// index 是 lower 中的字节偏移；在切分前将其映射回 trimmed，
// 这样前面某个小写形式更短的符文就不会把窗口推移到码点中间（或丢失失败信息）。"""),

(r"""// failureMarkers are the cues that identify a failed tool result. Matching is
// case-folded substring matching on a single line so a failure buried after
// progress output ("compiling...", "building") is still captured.""",
r"""// failureMarkers 是识别失败工具结果的提示。匹配是在单行上进行的忽略大小写子串匹配，
// 因此即使失败被埋在进度输出（"compiling..."、"building"）之后也能被捕获。"""),

(r"""// failureLine returns the first line of content that reports a failure. The
// whole result is scanned rather than just its first line, then the offending
// line is clipped to limit runes.""",
r"""// failureLine 返回内容中第一行报告失败的文本。它扫描整个结果而非仅第一行，
// 然后将出错的行裁剪到 limit 个符文。"""),

(r"""// pathsFromData collects file paths mentioned by a record payload, including
// tool-call arguments that are themselves JSON strings.""",
r"""// pathsFromData 收集记录载荷中提到的文件路径，包括本身就是 JSON 字符串的工具调用参数。"""),

(r"""// A JSON object decodes into a Go map, whose iteration order is
// randomized. pathsFromData feeds Summarize, which keeps only the most
// recent twelve paths before sorting (see tailItems(files, 12)). If the
// cutoff lands inside a single payload that carries several paths,
// map order would decide which subset survives — the same transcript
// could produce a different "Files changed" section on every run.
// Visit keys in sorted order so appearance order is deterministic;
// collectStrings in search.go applies the same rule.""",
r"""// JSON 对象解码为 Go map，其迭代顺序是随机的。pathsFromData 为 Summarize 提供数据，
// 后者在排序前只保留最近的十二条路径（见 tailItems(files, 12)）。如果截断点恰好落在
// 某个携带多条路径的载荷内部，map 的顺序就会决定哪些子集得以保留——同一份转录
// 每次运行都可能产生不同的 "Files changed" 部分。按排序后的顺序访问键，
// 使出现顺序具有确定性；search.go 中的 collectStrings 也采用同样的规则。"""),

(r"""// ---------------------------------------------------------------------------
// Lossless compaction archive.
//
// A compaction only adds a summary record; the original JSONL transcript is
// never rewritten, so "lossless" already holds at the file level. These helpers
// add the second half of the gptme audit item: a model-friendly, byte-addressable
// copy of the dropped messages. They are deliberately pure (no wiring into
// Compact/appendCompaction) so existing compaction stays byte-for-byte identical.
// Archives live under <session-store-dir>/compactions/, a subdirectory that
// Container.List already skips because it only considers *.jsonl files at the
// top level. Nothing is written under .argent/.
// ---------------------------------------------------------------------------""",
r"""// ---------------------------------------------------------------------------
// 无损压缩归档。
//
// 压缩只添加一条摘要记录；原始 JSONL 转录从不被重写，因此"无损"在文件层面已经成立。
// 这些辅助函数补上了 gptme 审计项的后半部分：为模型友好、可按字节寻址的被丢弃消息副本。
// 它们被刻意设计为纯函数（不接入 Compact/appendCompaction），
// 因此现有压缩行为保持逐字节一致。归档存放在 <session-store-dir>/compactions/ 下，
// Container.List 已跳过该子目录，因为它只考虑顶层的 *.jsonl 文件。
// 不会在 .argent/ 下写入任何内容。
// ---------------------------------------------------------------------------"""),

(r"""// compactionArchiveID validates an archive id the same way Container.path
// validates a session id, so an id can never escape the compactions directory.
// A trailing ".jsonl" is accepted and stripped for convenience.""",
r"""// compactionArchiveID 以与 Container.path 验证会话 id 相同的方式验证归档 id，
// 确保 id 永远不会逃出 compactions 目录。为方便起见，结尾的 ".jsonl" 会被接受并剥离。"""),

(r"""// CompactionArchivePath returns the on-disk path of the archive for id:
// <dir>/compactions/<id>.jsonl. The id is validated for path safety.""",
r"""// CompactionArchivePath 返回 id 对应归档在磁盘上的路径：<dir>/compactions/<id>.jsonl。
// id 会经过路径安全性校验。"""),

(r"""// compactionArchiveRelPath is the path stored in an ArchiveRef: relative to the
// session store directory, with forward slashes for stable records on any OS.""",
r"""// compactionArchiveRelPath 是存储在 ArchiveRef 中的路径：相对于会话存储目录，
// 使用正斜杠，以便在任何操作系统上生成稳定的记录。"""),

(r"""// writeCompactionArchive is the shared implementation. It writes messages as
// JSONL atomically (temp file + rename), computes a byte range for every
// message, and returns both the absolute path and an ArchiveRef whose Path is
// relative to dir. No temp file is left behind on any error path.""",
r"""// writeCompactionArchive 是共享实现。它以原子方式（临时文件 + 重命名）将消息写入 JSONL，
// 为每条消息计算字节区间，并返回绝对路径和一个 Path 相对于 dir 的 ArchiveRef。
// 任何错误路径都不会留下临时文件。"""),

(r"// already validated by CompactionArchivePath", r"// 已由 CompactionArchivePath 校验"),

(r"// keep original text byte-faithful (no \\u003c etc.)", r"// 保持原始文本逐字节忠实（不产生 \\u003c 等转义）"),

(r"""// os.Rename does not overwrite an existing file on Windows, so drop a
// previous archive of the same id first. The id is expected to be unique
// per compaction; this only matters when a caller deliberately rewrites.""",
r"""// os.Rename 在 Windows 上不会覆盖已存在的文件，因此先删除同一 id 的旧归档。
// id 预期在每次压缩中都是唯一的；只有在调用方刻意重写时此处理才有意义。"""),

(r"""// WriteCompactionArchive writes messages to the lossless archive
// <dir>/compactions/<id>.jsonl and returns the on-disk path. The write is
// atomic and leaves no temporary files behind.""",
r"""// WriteCompactionArchive 将消息写入无损归档 <dir>/compactions/<id>.jsonl 并返回磁盘路径。
// 写入是原子性的，不会留下临时文件。"""),

(r"""// WriteCompactionArchiveRef is WriteCompactionArchive plus the byte ranges and
// store-relative path needed to embed a reference in a Compaction record.""",
r"""// WriteCompactionArchiveRef 在 WriteCompactionArchive 的基础上，额外返回在 Compaction 记录中
// 嵌入引用所需的字节区间和相对于存储目录的路径。"""),

(r"""// resolveArchivePath turns a stored ref.Path into a filesystem path. Absolute
// paths are used as-is; relative paths are resolved under dir. A ref.Path using
// forward slashes (as written by compactionArchiveRelPath) works on Windows too.""",
r"""// resolveArchivePath 将存储的 ref.Path 转换为文件系统路径。绝对路径原样使用；
// 相对路径在 dir 下解析。使用正斜杠的 ref.Path（由 compactionArchiveRelPath 写入）
// 在 Windows 上同样有效。"""),

(r"// Normalize a stored store-relative path (forward slashes) for this OS.",
 r"// 将存储的相对于存储目录的路径（正斜杠）规范化为当前操作系统的格式。"),

(r"""// The path may come from a persisted record, so never let it escape the
// store directory (matching the invariant for every other filesystem path).""",
r"""// 该路径可能来自持久化记录，因此绝不能让它逃出存储目录
// （与处理其他所有文件系统路径的不变量一致）。"""),

(r"""// ReadCompactionArchive reads an archive back into messages, preserving every
// field (including the original json.RawMessage Data). Blank and malformed
// lines are skipped, matching the leniency of readRecords. path may be the
// absolute path returned by WriteCompactionArchive or the store-relative Path
// stored in an ArchiveRef.""",
r"""// ReadCompactionArchive 将归档读回为消息，保留每个字段（包括原始的 json.RawMessage Data）。
// 空白行和格式错误的行会被跳过，与 readRecords 的宽松策略一致。path 可以是
// WriteCompactionArchive 返回的绝对路径，也可以是 ArchiveRef 中存储的相对于存储目录的 Path。"""),

(r"""// ReadArchiveRange reads exactly one byte range from an archive, the primitive
// that turns an ArchiveRange in a Compaction record into the original message
// bytes. Bounds are checked against the file so a corrupt offset cannot read
// past the archive.""",
r"""// ReadArchiveRange 从归档中精确读取一个字节区间，这是将 Compaction 记录中的 ArchiveRange
// 还原为原始消息字节的基础操作。边界会对照文件进行检查，
// 因此损坏的偏移量无法读取到归档之外。"""),

(r"""// CompactionArchiveNotice renders a short, model-readable reference that can
// be appended to a compaction summary/notice so a resumed turn knows the exact
// original text is recoverable and how to address it. It is a pure helper:
// compression does not call it unless a caller opts in.""",
r"""// CompactionArchiveNotice 生成一段简短、模型可读的引用文本，可附加到压缩摘要/通知中，
// 使恢复的轮次知道确切的原文是可恢复的以及如何寻址它。它是纯辅助函数：
// 除非调用方显式选择启用，否则压缩不会调用它。"""),
])
