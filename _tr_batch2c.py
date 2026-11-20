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
    print(("OK" if not missing else "MISSING"), fname, len(pairs) + len(allpairs))
    for m, n in missing:
        print("  count=%d %s" % (n, m))

def ind(block, n):
    p = "\t" * n
    return "\n".join(p + line for line in block.split("\n"))

# ---------------- session_message.go ----------------
apply("session_message.go", [
(r"""// messageEntry projects one record onto the conversation, or reports false
// when the record is metadata, an event, or a compaction marker.""",
 r"""// messageEntry 将一条记录投影到对话上；当记录是元数据、事件或压缩标记时返回 false。"""),
(ind(r"""// "summary" is the canonical key of a Compaction payload. When a later
// compaction drops an earlier one, retainedForCompaction re-emits its
// system Entry as a KindSummary carrying that original Compaction Data
// (entryKind only sees the role). Reading "summary" here keeps the
// restored message's text identical to the Entry that was dropped;
// otherwise it resurfaces as an empty system message and the earlier
// summary is lost from the conversation view.""", 2),
 ind(r"""// "summary" 是 Compaction 载荷的规范键。当后续压缩丢弃更早的压缩时，
// retainedForCompaction 会把它作为携带原始 Compaction Data 的 KindSummary 重新发出 system Entry
// （entryKind 只能看到角色）。在这里读取 "summary" 可使恢复后的消息文本与被丢弃的 Entry 完全一致；
// 否则它会变成一条空的系统消息，先前的摘要就会从对话视图中丢失。""", 2)),
(r"""// reasoningOf reads the model's thought text from an assistant record. Both
// the neutral "reasoning" key and the OpenAI-compatible "reasoning_content"
// key are accepted so imported transcripts keep their reasoning too. A record
// without either key simply yields an empty string, which keeps legacy
// sessions unchanged.""",
 r"""// reasoningOf 从 assistant 记录中读取模型的思考文本。中性的 "reasoning" 键和
// OpenAI 兼容的 "reasoning_content" 键都被接受，使导入的转录也能保留其推理内容。
// 两个键都不存在的记录只会得到空字符串，从而使旧会话保持不变。"""),
(r"""// AssistantRecord builds the persisted payload for an assistant message that
// carries its reasoning text. Empty fields are omitted so records written by
// earlier builds stay byte-identical. Use it with Session.Append(KindAssistant,
// ...) when a caller has reasoning to preserve; the agent loop's llm.Message
// payload already carries the same "reasoning" key.""",
 r"""// AssistantRecord 为携带推理文本的 assistant 消息构建持久化载荷。空字段会被省略，
// 使较早版本写入的记录保持逐字节一致。当调用方有推理内容需要保留时，
// 可将其与 Session.Append(KindAssistant, ...) 一起使用；agent 循环的 llm.Message
// 载荷已经携带相同的 "reasoning" 键。"""),
("// fieldString reads the first matching string key from a JSON object payload.",
 "// fieldString 从 JSON 对象载荷中读取第一个匹配的字符串键。"),
(r"""// textOf reads a message payload: a bare JSON string, or the usual text keys
// of an object (`content`, `text`, `output`, `result`, `message`).""",
 r"""// textOf 读取消息载荷：裸 JSON 字符串，或对象常用的文本键
// （`content`、`text`、`output`、`result`、`message`）。"""),
("// truncateRunes clips text to limit runes, collapsing it to one line first.",
 "// truncateRunes 将文本裁剪到 limit 个符文，先将其折叠为一行。"),
])

# ---------------- ordinal.go ----------------
apply("ordinal.go", [
(r"""// seqKey is the reserved field SetSeq writes into a record's JSON data
// envelope. Like retentionKey, it lives in the existing Data blob, so no
// Record schema change and no extra JSONL column are needed, and Data keeps
// reaching the conversation view verbatim.
//
// A record's ordinal is a monotonic append-order counter. Unlike TS it cannot
// collide when several records are written inside one clock tick, which makes
// "newest" and fork points deterministic.""",
 r"""// seqKey 是 SetSeq 写入记录 JSON 数据包络的保留字段。与 retentionKey 一样，
// 它位于现有的 Data 块中，因此无需改动 Record schema，也无需增加 JSONL 列，
// 且 Data 仍逐字到达对话视图。
//
// 记录的序号是单调递增的追加顺序计数器。与 TS 不同，当多条记录在同一时钟周期内写入时，
// 它不会冲突，从而使"最新"和分叉点具有确定性。"""),
(r"""// GetSeq reads the explicit ordinal from a record's JSON data envelope. It
// reports (seq, true) only for a JSON object whose seqKey holds a non-negative
// integer that fits in int64. Missing, malformed, non-object, null, fractional,
// string and negative values all report (0, false), so records written before
// ordinals existed (and hand-written ones) fall back to derived order.""",
 r"""// GetSeq 从记录的 JSON 数据包络中读取显式序号。只有当 JSON 对象的 seqKey 持有
// 一个能放进 int64 的非负整数时，才返回 (seq, true)。缺失、格式错误、非对象、null、
// 小数、字符串和负值都返回 (0, false)，因此序号出现之前写入的记录（以及手写的记录）
// 会回退到派生的顺序。"""),
("// json.Unmarshal(null, &int64) succeeds and leaves zero, so reject it here.",
 "// json.Unmarshal(null, &int64) 会成功并保留零值，因此在这里拒绝它。"),
(r"""// SetSeq injects seq into a record's JSON data envelope under seqKey,
// preserving every other field. An empty data slice is treated as an empty
// object. A non-empty slice that is not a JSON object (or is malformed) is an
// error: replacing a payload whose other fields cannot be preserved would be
// worse than refusing to stamp it. Negative ordinals are rejected because an
// ordinal must be usable as a monotonic replay point.""",
 r"""// SetSeq 将 seq 注入记录 JSON 数据包络中 seqKey 对应的位置，保留所有其他字段。
// 空的 data 切片视为空对象。非空但不是 JSON 对象（或格式错误）的切片会报错：
// 替换一个无法保全其他字段的载荷，比拒绝打标记更糟糕。负序号会被拒绝，
// 因为序号必须可用作单调的重放点。"""),
("// JSON \"null\" unmarshals to a nil map.", "// JSON \"null\" 会反序列化为 nil map。"),
("// SeqOf reports the explicit ordinal carried by r, if any.",
 "// SeqOf 报告 r 携带的显式序号（如果有）。"),
(r"""// RecordSeqs returns one effective ordinal per record, in the same order.
// Records carrying an explicit _seq use it; records without one derive their
// ordinal from their zero-based array index. The derived fallback lets fork and
// replay helpers work on legacy transcripts that predate ordinals.
//
// Mixing explicit and derived ordinals is only meaningful when the explicit
// ones increase in file order, as they do for an append-only session. Writers
// should stamp every new record with SetSeq instead of relying on the fallback.""",
 r"""// RecordSeqs 按相同顺序为每条记录返回一个有效序号。带有显式 _seq 的记录使用它；
// 没有的记录从其在数组中的零基索引派生序号。派生的回退让分叉和重放辅助函数
// 能处理序号出现之前的旧转录。
//
// 只有当显式序号按文件顺序递增时（仅追加会话就是如此），混用显式和派生的序号才有意义。
// 写入方应为每条新记录调用 SetSeq 打标记，而不是依赖回退。"""),
(r"""// NewestBySeq returns the record with the highest ordinal. An explicit ordinal
// is authoritative: any record carrying _seq is newer than a legacy record
// without one, and among stamped records the greater seq wins.
//
// When neither candidate carries a seq (or their seqs tie), the tiebreak is TS
// then ID — the same "later timestamp, then greater id" rule Store.List uses.
// That keeps "newest" deterministic when several records share one second, the
// exact ambiguity that makes timestamp-only resume flaky. An empty input
// returns the zero Record.""",
 r"""// NewestBySeq 返回序号最高的记录。显式序号具有权威性：任何携带 _seq 的记录都比
// 没有它的旧记录更新，而在已打标记的记录中，seq 更大者胜出。
//
// 当两个候选都没有 seq（或 seq 相同）时，用 TS 再以 ID 决胜——与 Store.List 使用的
// "时间戳较晚者优先，再以 id 较大者优先"规则相同。这使多条记录共享同一秒时"最新"仍然确定，
// 而正是这种模糊性让仅基于时间戳的恢复变得不稳定。空输入返回零值 Record。"""),
(ind("// A stamped record always outranks an un-stamped one.", 3),
 ind("// 已打标记的记录始终优先于未打标记的记录。", 3)),
(ind("// Both lack seq, or their seqs tie.", 3),
 ind("// 两者都没有 seq，或它们的 seq 相同。", 3)),
(r"""// newerByTimeAndID breaks ties the way Store.List does: later timestamp wins,
// then the lexicographically greater id.""",
 r"""// newerByTimeAndID 按 Store.List 的方式决胜：时间戳较晚者胜出，
// 然后是字典序较大的 id。"""),
(r"""// ForkBeforeSeq returns the leading records whose effective ordinal is < seq,
// in their original order. It is the replay prefix for a fork at an ordinal:
// everything strictly before the target, nothing from the target onward.
// Effective ordinals come from RecordSeqs, so legacy records without _seq are
// ordered by array index and remain forkable.
//
// The result is a copy, never a slice of the caller's backing array. An empty
// prefix (target at or below the first ordinal) returns nil; a target above
// every ordinal returns a copy of the whole slice. Callers should keep ordinals
// increasing in file order — the prefix stops at the first ordinal >= seq.""",
 r"""// ForkBeforeSeq 返回有效序号 < seq 的前导记录，保持原始顺序。它是在某个序号处分叉的
// 重放前缀：严格位于目标之前的全部内容，不包含目标及其之后的任何内容。
// 有效序号来自 RecordSeqs，因此没有 _seq 的旧记录按数组索引排序，仍可分叉。
//
// 结果是副本，绝不是调用方底层数组的切片。空前缀（目标位于第一个序号处或之下）返回 nil；
// 高于所有序号的目标返回整个切片的副本。调用方应保持序号按文件顺序递增——
// 前缀在第一个 >= seq 的序号处停止。"""),
])

# ---------------- compact_activity.go ----------------
apply("compact_activity.go", [
(r"""// This file implements the deterministic, zero-LLM "tool activity" part of a
// compaction summary. When a span of the conversation is dropped, the next turn
// still needs to know which files were read (and which lines), which were
// changed, and which commands were run — otherwise the agent re-reads the same
// files or forgets a change it already made. Unlike the goal / failure / next
// step extraction, nothing here calls a model: it reads the recorded tool calls
// directly from the dropped entries.""",
 r"""// 本文件实现压缩摘要中确定性的、无需 LLM 的"工具活动"部分。当一段对话被丢弃后，
// 下一轮仍需要知道读取了哪些文件（以及哪些行）、修改了哪些文件、运行了哪些命令——
// 否则 agent 会重复读取相同的文件，或忘记它已经做过的改动。与目标/失败/下一步的提取不同，
// 这里没有调用模型：它直接从被丢弃的条目中读取记录下来的工具调用。"""),
("// cline-style truncation for a command line", "// 对命令行进行类似 cline 的截断"),
("// keep a path from eating the whole bullet", "// 防止路径占满整个项目符号"),
("// hard cap on one rendered activity line", "// 单条渲染活动行的硬上限"),
("// keep the summary bounded regardless of transcript size",
 "// 无论转录多大都保持摘要有界"),
(r"""// activityReadTools are tools whose arguments describe reading a file. The
// recorded arguments carry one of path/file/file_path plus offset/limit or a
// line-centred window (line/before/after), from which a path:start-end range is
// reconstructed.""",
 r"""// activityReadTools 是其参数描述读取文件的工具。记录下来的参数携带 path/file/file_path
// 之一，再加上 offset/limit 或以行号为中心的窗口（line/before/after），
// 从中重建出 path:start-end 范围。"""),
(r"""// activityEditTools are tools that change files. Only the path is recoverable
// from a recorded edit: the tool result is a bare "edited <path>" line with no
// diff, so no reliable line range can be derived without touching the file (a
// pure function must not).""",
 r"""// activityEditTools 是修改文件的工具。从记录下来的编辑中只能恢复路径：
// 工具结果只是一行裸的 "edited <path>"，没有 diff，因此不触碰文件就无法得出可靠的行范围
// （纯函数不应触碰文件）。"""),
("// activityCommandTools are tools whose arguments carry a shell command.",
 "// activityCommandTools 是参数中携带 shell 命令的工具。"),
(r"""// activityCall is one tool invocation projected out of a transcript record: a
// name and its decoded arguments. It is built only from map lookups, never from
// map iteration, so extraction is deterministic.""",
 r"""// activityCall 是从转录记录中投影出的一次工具调用：一个名称及其解码后的参数。
// 它只通过 map 查找构建，绝不通过 map 迭代，因此提取是确定性的。"""),
(r"""// summarizeToolActivity returns a markdown "Tool activity" section built from
// the read/edit/command calls in dropped, or "" when there is no such activity.
// It is a pure function: deterministic, deduplicated and bounded in size.""",
 r"""// summarizeToolActivity 根据 dropped 中的读取/编辑/命令调用返回一个 markdown
// "Tool activity" 章节；没有此类活动时返回 ""。它是纯函数：确定、去重且大小有界。"""),
(ind(r"""// Chronological order is kept; when there are too many, the most recent
// activities survive (the ones a resumed turn is most likely to need).""", 1),
 ind(r"""// 保持时间顺序；当活动过多时，保留最近的活动
// （恢复的轮次最可能需要这些）。""", 1)),
(r"""// callsFromData extracts tool invocations from one record's payload. Two
// layouts occur in a session file:
//   - a tool result record with top-level "name" and "arguments";
//   - an assistant record with a "tool_calls" (or "toolCalls") array whose
//     items carry function.name and function.arguments.""",
 r"""// callsFromData 从一条记录的载荷中提取工具调用。会话文件中会出现两种布局：
//   - 工具结果记录，顶层带有 "name" 和 "arguments"；
//   - assistant 记录，带有 "tool_calls"（或 "toolCalls"）数组，
//     其元素携带 function.name 和 function.arguments。"""),
(r"""// decodedArgs accepts arguments serialized either as a JSON string (the usual
// case in a tool record) or as an already-decoded JSON object.""",
 r"""// decodedArgs 接受以 JSON 字符串序列化的参数（工具记录中的常见情况），
// 或已解码的 JSON 对象。"""),
(r"""// activityBullet renders one tool call as a single activity line. Unknown tools
// and calls without the expected argument are ignored.""",
 r"""// activityBullet 将一次工具调用渲染为单行活动记录。未知工具以及缺少预期参数的调用会被忽略。"""),
(r"""// activityPath returns the file a tool call refers to, preferring the keys the
// built-in tools actually use.""",
 r"""// activityPath 返回工具调用所指的文件，优先使用内置工具实际使用的键。"""),
(r"""// activityPathRange appends a line range to a read path. Explicit window
// arguments win; otherwise a line selector already embedded in the path
// (":10", ":10:20", "#L10", "#L10-L20") is normalised to "path:start-end".""",
 r"""// activityPathRange 在读取路径后附加行范围。显式的窗口参数优先；
// 否则把路径中已嵌入的行选择器（":10"、":10:20"、"#L10"、"#L10-L20"）
// 规范化为 "path:start-end"。"""),
(r"""// activityInt reads an integer argument. encoding/json decodes numbers into
// float64 when unmarshalling into any, which is the common case here.""",
 r"""// activityInt 读取整数参数。encoding/json 在反序列化到 any 时把数字解码为 float64，
// 这里正是这种常见情况。"""),
(r"""// splitTrailingRange recognises a line selector embedded in a path and returns
// the path without the selector plus the inclusive start/end lines. It is
// careful not to mistake "C:\dir" for a selector (a colon must be followed by
// digits) and returns ok=false when no selector is present.""",
 r"""// splitTrailingRange 识别路径中嵌入的行选择器，返回去掉选择器的路径以及含端点的起始/结束行。
// 它小心避免把 "C:\dir" 误认为选择器（冒号后面必须跟数字），
// 没有选择器时返回 ok=false。"""),
(r"""// activityRangeToken parses the part after "#", e.g. "L10", "10-20" or
// "L10-L20", into an inclusive line range.""",
 r"""// activityRangeToken 将 "#" 之后的部分（例如 "L10"、"10-20" 或 "L10-L20"）
// 解析为含端点的行范围。"""),
(ind(r"// Each component may carry its own \"L\" prefix, e.g. \"L10-L20\".", 1),
 ind(r"// 每个组件都可以自带 \"L\" 前缀，例如 \"L10-L20\"。", 1)),
])

# ---------------- compact_structured.go ----------------
apply("compact_structured.go", [
(r"""// StructuredSummary is the machine-readable projection of a compaction summary.
//
// Compaction.Summary is historically one free-text markdown string (see
// Summarize). This DTO is an additive, opt-in view of that text: it lets a later
// UI or search index consume progress, uncertainties and user actions without
// reimplementing a markdown parser. The compaction write path does not use it —
// Summarize keeps emitting byte-for-byte the same free text as before.""",
 r"""// StructuredSummary 是压缩摘要的机器可读投影。
//
// Compaction.Summary 历来是一个自由格式的 markdown 字符串（见 Summarize）。
// 这个 DTO 是对该文本的附加、可选视图：它让后续的 UI 或搜索索引能够消费进度、
// 不确定事项和用户操作，而无需重新实现 markdown 解析器。压缩写入路径不使用它——
// Summarize 继续输出与以前逐字节相同的自由文本。"""),
("// structuredBucket is the internal target of one markdown section heading.",
 "// structuredBucket 是一个 markdown 章节标题的内部目标。"),
(r"""// summaryHeadingBuckets maps a normalized section title to one of the three
// StructuredSummary fields. It deliberately covers three families:
//
//   - the canonical structured titles emitted by RenderStructuredSummary;
//   - the legacy titles Summarize has always written ("Goal", "Failures",
//     "Next steps", ...), so existing free text can be parsed as-is;
//   - common CJK equivalents, since summaries are often written in zh-CN.
//
// Titles not in this table are treated as structure, not content: their section
// is skipped and does not by itself make ParseStructuredSummary succeed. That
// is what lets a format drift fall back to "not structured" instead of
// producing a misleading partial DTO.""",
 r"""// summaryHeadingBuckets 将归一化后的章节标题映射到 StructuredSummary 的三个字段之一。
// 它刻意覆盖三类：
//
//   - RenderStructuredSummary 输出的规范结构化标题；
//   - Summarize 一直写入的旧标题（"Goal"、"Failures"、"Next steps" 等），
//     使现有的自由文本可以原样解析；
//   - 常见的中文等价标题，因为摘要常常以简体中文书写。
//
// 不在表中的标题被视为结构而非内容：其章节会被跳过，本身不会让 ParseStructuredSummary 成功。
// 这使格式漂移能回退到"非结构化"，而不是产生一个具有误导性的部分 DTO。"""),
("// Canonical structured titles.", "// 规范的结构化标题。"),
("// Legacy Summarize sections.", "// Summarize 的旧版章节。"),
("// CJK equivalents.", "// 中文等价标题。"),
(r"""// normalizeSummaryHeading recognizes a markdown heading line and returns its
// normalized title. A line is a heading only when its first non-space character
// is '#'; plain labels ("Progress:") and bold text ("**Progress**") are not —
// a format drift must be detectable rather than silently accepted.""",
 r"""// normalizeSummaryHeading 识别 markdown 标题行并返回其归一化标题。
// 只有当一行的第一个非空白字符是 '#' 时，它才是标题；普通标签（"Progress:"）和粗体文本
// （"**Progress**"）都不是——格式漂移必须能被检测到，而不是被静默接受。"""),
("// collapse whitespace, incl. U+3000", "// 折叠空白，包括 U+3000"),
(r"""// summaryBullet extracts the text of a "- ", "* " or "• " bullet. Empty and
// "(none)"-style placeholder bullets are not content and are dropped. Skipping
// placeholders is what makes Render -> Parse a clean round trip, since the
// renderer uses them to keep empty sections visible to a human reader.""",
 r"""// summaryBullet 提取 "- "、"* " 或 "• " 项目符号的文本。空条目和 "(none)" 形式的
// 占位条目不是内容，会被丢弃。跳过占位条目使 Render -> Parse 成为干净的往返，
// 因为渲染器用它们来让空章节对人类读者保持可见。"""),
(r"""// ParseStructuredSummary is a best-effort, pure parser that projects a
// free-text compaction summary onto StructuredSummary. The bool reports whether
// any *recognized* section heading was found: false means the text carries no
// structured information (empty, prose only, or a drifted format) and the
// caller should keep using the original free text. A true result may contain
// empty fields when a section exists but has no bullets.
//
// Bullets are collected in appearance order and deduplicated. Unrecognized
// sections (including the leading "## Compaction" metadata block) are ignored.""",
 r"""// ParseStructuredSummary 是一个尽力而为的纯解析器，把自由文本的压缩摘要投影到
// StructuredSummary。布尔值报告是否找到了任何*可识别的*章节标题：false 表示文本不包含
// 结构化信息（为空、只有散文或格式已漂移），调用方应继续使用原始自由文本。
// 当某个章节存在但没有条目时，true 的结果中可能包含空字段。
//
// 条目按出现顺序收集并去重。无法识别的章节（包括开头的 "## Compaction" 元数据块）会被忽略。"""),
(ind("// unknown title -> bucketNone", 1), ind("// 未知标题 -> bucketNone", 1)),
(r"""// RenderStructuredSummary renders a StructuredSummary in the canonical
// markdown shape understood by ParseStructuredSummary. Empty fields are emitted
// as "- (none)" so the human-readable text keeps the familiar Summarize layout
// while ParseStructuredSummary still round-trips them to empty slices.""",
 r"""// RenderStructuredSummary 以 ParseStructuredSummary 能理解的规范 markdown 形式
// 渲染 StructuredSummary。空字段输出为 "- (none)"，使人类可读文本保持熟悉的 Summarize 布局，
// 同时 ParseStructuredSummary 仍能将它们往返为空切片。"""),
(ind(r"""// Keep one bullet per line: an embedded newline would look like a
// new section body and break the round trip.""", 3),
 ind(r"""// 每行保持一个项目符号：内嵌的换行看起来会像新的章节正文，从而破坏往返。""", 3)),
(r"""// Structured is a read-only, opt-in projection of c.Summary. It does not
// mutate the record and is not called by any compaction write path.""",
 r"""// Structured 是 c.Summary 的只读、可选投影。它不修改记录，也不被任何压缩写入路径调用。"""),
])

# ---------------- import.go ----------------
apply("import.go", [
(r"""// Package session — foreign transcript import.
//
// These helpers convert the JSONL transcripts written by other coding agents
// into argent-go's native Entry projection, the same shape Session.Messages()
// returns. They are pure readers: nothing here touches the store, mints record
// ids in the store's namespace, or wires a CLI subcommand.
//
// The strategy mirrors goose's session/import_formats: parse each line leniently
// (blank and malformed lines are skipped), map the foreign events onto messages,
// then run the package's canonical tool-pair reconciliation so the result is a
// valid conversation a store can replay.""",
 r"""// Package session —— 外部转录导入。
//
// 这些辅助函数把其他编码 agent 写入的 JSONL 转录转换为 argent-go 原生的 Entry 投影，
// 即 Session.Messages() 返回的相同形态。它们是纯读取器：这里没有任何代码触碰 store、
// 在 store 的命名空间中生成记录 id，或接线 CLI 子命令。
//
// 该策略参照 goose 的 session/import_formats：宽松解析每一行（跳过空白和格式错误的行），
// 把外部事件映射为消息，然后运行本包规范的 tool-pair 协调，
// 使结果成为 store 可以重放的有效对话。"""),
(r"""// Import id prefixes are deliberately distinct from the store's "n%06d" ids so
// imported provenance stays recognizable and cannot collide with store ids.""",
 r"""// 导入 id 的前缀刻意区别于 store 的 "n%06d" id，
// 使导入来源保持可识别，且不会与 store 的 id 冲突。"""),
(r"""// claudeCall is the shape stored in an assistant Entry's `tool_calls` array.
// It is exported to tests indirectly through Entry.Data; keeping it flat makes
// the payload both readable and usable by toolCallIDs, which only needs `id`.""",
 r"""// claudeCall 是存储在 assistant Entry 的 `tool_calls` 数组中的形态。
// 它通过 Entry.Data 间接暴露给测试；保持扁平结构使载荷既易读，
// 又能被只需要 `id` 的 toolCallIDs 使用。"""),
(r"""// claudeUsage is the token accounting Claude Code attaches to assistant
// events. It is preserved in Entry.Data under `usage`.""",
 r"""// claudeUsage 是 Claude Code 附加在 assistant 事件上的 token 统计。
// 它被保留在 Entry.Data 的 `usage` 键下。"""),
(r"""// ImportClaudeCodeJSONL converts a Claude Code `.jsonl` transcript into the
// linear conversation argent-go projects from its own records.
//
// Claude Code writes one typed event per line. Only `user` and `assistant`
// events carry conversation content; every other line (attachments, progress,
// queue operations, titles, ...) is ignored. A message's `content` is either a
// plain string (typical for user prompts) or an array of blocks:
//
//   - `text`        — message text, joined with newlines;
//   - `tool_use`    — an assistant tool call, preserved in Entry.Data under
//     `tool_calls` so toolCallIDs/ReconcileToolPairs can pair it;
//   - `tool_result` — delivered inside a `user` event, emitted as a `tool`
//     Entry whose ToolCallID is the originating tool_use id;
//   - anything else (image, thinking, ...) is skipped.
//
// Assistant `message.usage` is preserved verbatim in Entry.Data under `usage`.
// Timestamps are parsed as RFC3339 and normalized to UTC; an unparseable or
// missing timestamp leaves Entry.TS zero.
//
// The source is read leniently: malformed lines are skipped rather than
// failing the whole import. An empty (or entirely non-message) transcript
// yields an empty, non-nil slice. Only a nil reader is an error.""",
 r"""// ImportClaudeCodeJSONL 把 Claude Code 的 `.jsonl` 转录转换为
// argent-go 从其自身记录投影出的线性对话。
//
// Claude Code 每行写入一个带类型的事件。只有 `user` 和 `assistant` 事件携带对话内容；
// 其他每一行（附件、进度、队列操作、标题等）都会被忽略。消息的 `content` 要么是普通字符串
// （用户提示的典型情况），要么是块数组：
//
//   - `text`        —— 消息文本，用换行符连接；
//   - `tool_use`    —— assistant 工具调用，保留在 Entry.Data 的 `tool_calls` 下，
//     以便 toolCallIDs/ReconcileToolPairs 配对；
//   - `tool_result` —— 在 `user` 事件中传递，输出为一个 `tool` Entry，
//     其 ToolCallID 是发起它的 tool_use id；
//   - 其他任何内容（图片、思考等）都会被跳过。
//
// assistant 的 `message.usage` 原样保留在 Entry.Data 的 `usage` 下。
// 时间戳按 RFC3339 解析并规范化为 UTC；无法解析或缺失的时间戳使 Entry.TS 保持零值。
//
// 源数据以宽松方式读取：格式错误的行会被跳过，而不是让整个导入失败。
// 空的（或完全不含消息的）转录会返回空的非 nil 切片。只有 nil 读取器才是错误。"""),
(r"""// ImportCodexJSONL converts a Codex (OpenAI Responses) `.jsonl` rollout into
// the native Entry projection. Codex transcripts begin with a `session_meta`
// line and then carry conversation items as `response_item` lines whose payload
// type is `message`, `function_call` or `function_call_output`. `event_msg`
// lines (token accounting) and all other event types are metadata and skipped.
//
// Developer/system messages are harness prompts and are skipped. The `cwd`,
// `id` and usage totals found in session_meta/event_msg do not belong on an
// Entry and are intentionally not surfaced here.""",
 r"""// ImportCodexJSONL 把 Codex（OpenAI Responses）的 `.jsonl` 运行记录转换为
// 原生 Entry 投影。Codex 转录以一行 `session_meta` 开始，随后以 `response_item` 行
// 携带对话条目，其载荷类型为 `message`、`function_call` 或 `function_call_output`。
// `event_msg` 行（token 统计）和所有其他事件类型都是元数据，会被跳过。
//
// developer/system 消息是框架提示，会被跳过。session_meta/event_msg 中的 `cwd`、
// `id` 和用量总数不属于 Entry，刻意不在这里呈现。"""),
(ind("// Codex stores arguments as a JSON-encoded string.", 3),
 ind("// Codex 将参数存储为 JSON 编码的字符串。", 3)),
(r"""// importEntryBuilder chains imported entries by parent and mints fallback ids
// only when a source event does not carry one.""",
 r"""// importEntryBuilder 用 parent 把导入的条目链接起来，
// 仅在源事件没有携带 id 时才生成回退 id。"""),
(r"""// readImportLines parses every non-blank line that is a JSON object. Bad lines
// are skipped so a torn or foreign export cannot make the whole import fail.""",
 r"""// readImportLines 解析每一个非空且为 JSON 对象的行。坏行会被跳过，
// 使残缺或外部来源的导出不会让整个导入失败。"""),
(ind(r"""// A scanner error (line too long or read failure) ends the stream; whatever
// was decoded so far is still a usable, lenient import.""", 1),
 ind(r"""// 扫描器错误（行过长或读取失败）会结束流；至此已解码的内容仍是一个可用的宽松导入结果。""", 1)),
(r"""// importTimestamp parses an RFC3339 timestamp, accepting the fractional
// seconds Claude Code and Codex both emit. A bad timestamp stays zero so callers
// can distinguish it from the Unix epoch.""",
 r"""// importTimestamp 解析 RFC3339 时间戳，接受 Claude Code 和 Codex 都会输出的小数秒。
// 无效时间戳保持零值，使调用方可以将其与 Unix 纪元区分开。"""),
(r"""// importBlockID gives each block within one event a distinct id while keeping
// the source uuid as provenance. A missing uuid falls back to a minted id.""",
 r"""// importBlockID 为同一事件内的每个块赋予不同的 id，同时保留源 uuid 作为来源标记。
// 缺少 uuid 时回退到生成的 id。"""),
(r"""// importedToolResultText flattens a Claude Code tool_result `content`, which is
// either a string or an array of text/tool_reference blocks.""",
 r"""// importedToolResultText 把 Claude Code 的 tool_result `content` 展平，
// 它要么是字符串，要么是 text/tool_reference 块的数组。"""),
("// codexMessageText joins the text-bearing blocks of a Codex message payload.",
 "// codexMessageText 连接 Codex 消息载荷中携带文本的块。"),
])
