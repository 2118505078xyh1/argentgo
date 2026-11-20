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
    if missing:
        print("MISSING in", fname)
        for m, n in missing:
            print("  count=%d %s" % (n, m))
    else:
        print("OK", fname, len(pairs) + len(allpairs), "replacements")

def ind(block, n):
    p = "\t" * n
    return "\n".join(p + line for line in block.split("\n"))

# ---------------- store.go ----------------
apply("store.go", [
("// Meta is what a session list shows without loading a transcript into memory.",
 "// Meta 是会话列表无需将转录加载到内存即可显示的内容。"),
(ind(r"""// Modified is the transcript file's mtime, captured during List and used
// only to order sessions with equal in-record timestamps. It is never
// persisted (the sidecar still describes size+mtime elsewhere), so adding
// it does not change the on-disk format.""", 1),
 ind(r"""// Modified 是转录文件的 mtime，在 List 期间捕获，
// 仅用于在记录内时间戳相同时排列会话顺序。它永不被持久化
// （旁车文件仍在别处描述 size+mtime），因此添加它不会改变磁盘格式。""", 1)),
("// Container is a directory that holds session files.",
 "// Container 是存放会话文件的目录。"),
("// Store is the entry point for one session directory.",
 "// Store 是单个会话目录的入口。"),
("// Open creates dir when it is missing and returns a store over it.",
 "// Open 在 dir 不存在时创建它，并返回基于它的 store。"),
("// List reports every session, newest first.\nfunc (s *Store) List()",
 "// List 报告所有会话，最新的在前。\nfunc (s *Store) List()"),
("// Find reports the sessions whose id or title contains query.\nfunc (s *Store) Find",
 "// Find 报告 id 或标题包含 query 的会话。\nfunc (s *Store) Find"),
("// Delete removes one session file.\nfunc (s *Store) Delete",
 "// Delete 删除一个会话文件。\nfunc (s *Store) Delete"),
("// List reports every session under dir, newest first.",
 "// List 报告 dir 下的所有会话，最新的在前。"),
("// Find reports the sessions under dir whose id or title contains query.",
 "// Find 报告 dir 下 id 或标题包含 query 的会话。"),
("// Delete removes one session file under dir.",
 "// Delete 删除 dir 下的一个会话文件。"),
(ind(r"""// List reports every session, newest first. Transcripts are not parsed when a
// matching sidecar exists; a missing, corrupt, or stale one falls back to the
// original full scan and is refreshed on the way out.""", 0),
 ind(r"""// List 报告所有会话，最新的在前。当存在匹配的旁车文件时不会解析转录；
// 旁车文件缺失、损坏或过期时，会回退到原始的全量扫描，并在返回前刷新。""", 0)),
(ind(r"""// Listing never repairs: hard-corrupt transcripts are invisible here,
// and a torn tail stays untouched until an explicit resume.""", 3),
 ind(r"""// 列表操作从不修复：严重损坏的转录在这里不可见，
// 残缺的尾部会保持原样，直到显式恢复时才处理。""", 3)),
(ind(r"""// Sidecars do not store Modified; capture it on every List so a cache
// hit still carries the information needed to break Updated ties.""", 2),
 ind(r"""// 旁车文件不存储 Modified；每次 List 都捕获它，
// 这样即使缓存命中也带有打破 Updated 并列所需的信息。""", 2)),
(ind(r"""// The in-record clock can stamp two sessions identically (e.g. two
// sessions created in the same second). The file's mtime records the
// real order of writes, so a later append moves a session to the top;
// fall back to id only when both timestamps are identical.""", 2),
 ind(r"""// 记录内的时钟可能为两个会话打上相同的时间戳（例如同一秒创建的两个会话）。
// 文件的 mtime 记录了真实的写入顺序，因此较晚的追加会把会话移到顶部；
// 只有当两个时间戳完全相同时才回退到 id。""", 2)),
(ind(r"""// Find reports the sessions whose id or title contains query, newest first.""", 0),
 ind(r"""// Find 报告 id 或标题包含 query 的会话，最新的在前。""", 0)),
(ind(r"""// Delete removes one session file.""", 0),
 ind(r"""// Delete 删除一个会话文件。""", 0)),
(ind(r"""// The sidecar is only a cache; remove it with its transcript so a later
// session with a reused id cannot inherit stale metadata.""", 1),
 ind(r"""// 旁车文件只是缓存；随转录一起删除它，
// 这样之后复用 id 的会话就不会继承过期的元数据。""", 1)),
("// path turns an id into a file path, rejecting anything that could escape dir.",
 "// path 将 id 转换为文件路径，拒绝任何可能逃出 dir 的内容。"),
("// resolvePath accepts an exact id, otherwise a unique case-insensitive prefix.",
 "// resolvePath 接受精确的 id，否则接受唯一的、不区分大小写的前缀。"),
(ind(r"""// transcriptStem strips a trailing transcript extension from a file name
// regardless of case. Discovery paths match the ".jsonl" suffix
// case-insensitively (see List and resolvePath), so the id derived from a
// resolved path must strip it the same way. A case-sensitive strings.TrimSuffix
// would leave "SESSION.JSONL" as the id, making Session.path() point at
// "SESSION.JSONL.jsonl" and every read come back empty on case-sensitive
// filesystems. Names without the suffix are returned unchanged.""", 0),
 ind(r"""// transcriptStem 从文件名中剥离末尾的转录扩展名，不区分大小写。
// 发现路径以不区分大小写的方式匹配 ".jsonl" 后缀（见 List 和 resolvePath），
// 因此从解析后的路径派生 id 时必须以同样方式剥离它。区分大小写的 strings.TrimSuffix
// 会把 "SESSION.JSONL" 当作 id，使 Session.path() 指向 "SESSION.JSONL.jsonl"，
// 在区分大小写的文件系统上每次读取都返回空结果。没有该后缀的名称原样返回。""", 0)),
("// newID mints a timestamped id with a random suffix and its unused file path.",
 "// newID 生成带时间戳和随机后缀的 id 及其未使用的文件路径。"),
(ind(r"""// Sidecars are caches, never the authority for session content. An entry
// stores only the fields List/Search need plus the transcript's size and mtime,
// so an append that bypasses the cache, or any external edit, is detected and
// the transcript is parsed again.""", 0),
 ind(r"""// 旁车文件只是缓存，绝不是会话内容的权威来源。条目只存储 List/Search 需要的字段
// 以及转录的大小和 mtime，因此绕过缓存的追加或任何外部编辑都会被检测到，
// 转录会被重新解析。""", 0)),
(ind(r"""// loadIndex returns the cached meta only when the sidecar is readable, current
// for this version, and describes the transcript's exact size and mtime. Any
// doubt is reported as a miss and handled by a full scan.""", 0),
 ind(r"""// 只有当旁车文件可读、版本为当前版本，且描述的大小和 mtime 与转录完全一致时，
// loadIndex 才返回缓存的 meta。任何疑点都按未命中处理，交由全量扫描处理。""", 0)),
(ind(r"""// saveIndex refreshes a sidecar atomically so a concurrent reader never sees a
// half-written file. Failure is harmless: the next read just parses the file.""", 0),
 ind(r"""// saveIndex 原子地刷新旁车文件，使并发读取者永远不会看到半写入的文件。
// 失败是无害的：下一次读取只会解析该文件。""", 0)),
("// metaFromRecords derives a list entry from the transcript alone.",
 "// metaFromRecords 仅根据转录派生一个列表条目。"),
], [
("// Dir reports the directory that holds the session files.",
 "// Dir 报告存放会话文件的目录。"),
("// OpenSession reopens a stored session by id (an exact id or a unique prefix).",
 "// OpenSession 按 id（精确 id 或唯一前缀）重新打开已存储的会话。"),
("// NewSession creates a session file holding one header record.",
 "// NewSession 创建只含一条 header 记录的会话文件。"),
("// Delete removes one session file.\nfunc (c *Container) Delete",
 "// Delete 删除一个会话文件。\nfunc (c *Container) Delete"),
])

# ---------------- retention.go ----------------
apply("retention.go", [
(ind(r"""// retentionKey is the reserved field SetRetention writes into a record's JSON
// data envelope. Keeping retention in the existing Data blob means no schema
// change to Record and no extra JSONL column, and messageEntry already copies
// Data verbatim into Entry.Data so the hint reaches the conversation view.""", 0),
 ind(r"""// retentionKey 是 SetRetention 写入记录 JSON 数据包络的保留字段。
// 将保留策略放在现有的 Data 块中，意味着无需改动 Record 的 schema，也无需增加 JSONL 列；
// 而且 messageEntry 已逐字复制 Data 到 Entry.Data，因此该提示能到达对话视图。""", 0)),
(ind(r"""// Retention is a per-record hint about how long a record must live. It governs
// two independent lifetimes: compaction of the parent thread and propagation
// into child sessions (subagents).""", 0),
 ind(r"""// Retention 是逐条记录的提示，指明记录必须保留多久。它管理两个独立的生命周期：
// 父线程的压缩，以及传播到子会话（子代理）。""", 0)),
(ind(r"""// RetainDefault is normal behaviour: the record may be compacted away and
// is not copied into child sessions. Records that never carried the hint
// also read back as RetainDefault.""", 1),
 ind(r"""// RetainDefault 是默认行为：记录可能被压缩掉，且不会复制到子会话。
// 从未携带该提示的记录读回时也是 RetainDefault。""", 1)),
(ind(r"""// RetainAlways keeps the record across compaction AND propagates it to
// child sessions.""", 1),
 ind(r"""// RetainAlways 既在压缩后保留该记录，又将其传播到子会话。""", 1)),
(ind(r"""// RetainInThread keeps the record across compaction only; it is not
// propagated to child sessions.""", 1),
 ind(r"""// RetainInThread 仅在压缩后保留该记录；不传播到子会话。""", 1)),
(ind(r"""// RetainInChildren propagates the record to child sessions only; it may
// still be compacted away in the parent thread.""", 1),
 ind(r"""// RetainInChildren 只将记录传播到子会话；在父线程中它仍可能被压缩掉。""", 1)),
(ind(r"""// String implements fmt.Stringer. It returns the stable lowercase wire value
// used by SetRetention, or a descriptive form for an out-of-range value.""", 0),
 ind(r"""// String 实现 fmt.Stringer。它返回 SetRetention 使用的稳定小写线格式值，
// 对于超出范围的值则返回描述性形式。""", 0)),
("// SurvivesCompaction reports whether r must be preserved across compaction.",
 "// SurvivesCompaction 报告 r 是否必须在压缩后保留。"),
("// SurvivesChildren reports whether r must be seeded into child sessions.",
 "// SurvivesChildren 报告 r 是否必须植入子会话。"),
(ind(r"""// hasRetentionHint reports whether data is a JSON object carrying a _retention
// field whose value is a string. Presence alone is what matters here: even an
// unrecognised value is an explicit caller choice, and GetRetention already
// maps it to RetainDefault. The automatic kind-based default must never replace
// an explicit hint, or a caller asking for (say) in_thread on a summary would
// silently get always and leak that record into child sessions.""", 0),
 ind(r"""// hasRetentionHint 报告 data 是否为带有字符串值 _retention 字段的 JSON 对象。
// 这里只看字段是否存在：即使是无法识别的值，也是调用方的显式选择，
// 而 GetRetention 已经把它映射为 RetainDefault。基于 kind 的自动默认值绝不能取代显式提示，
// 否则调用方对摘要请求（例如）in_thread 时，会静默得到 always，并把该记录泄漏到子会话。""", 0)),
(ind(r"""// json.Unmarshal(null, &string) succeeds with an empty string; a null hint
// means "no hint", so the automatic default still applies.""", 2),
 ind(r"""// json.Unmarshal(null, &string) 会以空字符串成功返回；null 提示表示"无提示"，
// 因此仍适用自动默认值。""", 2)),
(ind(r"""// parseRetention reads a stored wire value. It is deliberately permissive
// (trimmed, case-folded) so hand-written or older records still read back, and
// it falls back to RetainDefault for anything unrecognised.""", 0),
 ind(r"""// parseRetention 读取存储的线格式值。它刻意宽松（去空白、忽略大小写），
// 使手写或较旧的记录仍能读回，并对任何无法识别的值回退到 RetainDefault。""", 0)),
(ind(r"""// SetRetention injects r into a record's JSON data envelope under retentionKey,
// preserving every other field. An empty data slice is treated as an empty
// object. A non-empty slice that is not a JSON object (or is malformed) is an
// error: retaining a payload whose other fields cannot be preserved would be
// worse than refusing to mark it.""", 0),
 ind(r"""// SetRetention 将 r 注入记录 JSON 数据包络中 retentionKey 对应的位置，
// 保留所有其他字段。空的 data 切片视为空对象。非空但不是 JSON 对象（或格式错误）的切片会报错：
// 保留一个无法保全其他字段的载荷，比拒绝标记它更糟糕。""", 0)),
(r'// JSON "null" unmarshals to a nil map.', r'// JSON "null" 会反序列化为 nil map。'),
(ind(r"""// GetRetention reads the retention hint from a record's JSON data envelope.
// Missing, malformed, non-object, or unrecognised values all mean
// RetainDefault, so records written before this hint existed behave normally.""", 0),
 ind(r"""// GetRetention 从记录的 JSON 数据包络中读取保留提示。
// 缺失、格式错误、非对象或无法识别的值都表示 RetainDefault，
// 因此在该提示出现之前写入的记录表现正常。""", 0)),
(ind(r"""// SelectForCompaction returns the records that must survive compaction: those
// marked RetainAlways or RetainInThread, regardless of where the compactor's
// cut lands. It is a pure helper; the caller decides how to merge the result
// into the kept suffix.
//
// budget caps how many records are returned (it is a count, not a token
// estimate). A non-positive budget means no cap. When the cap forces a choice,
// the most recently occurring flagged records win, mirroring KeepForBudget's
// "newest context survives" rule. The result is in original record order.""", 0),
 ind(r"""// SelectForCompaction 返回必须在压缩后保留的记录：标记为 RetainAlways 或 RetainInThread 的记录，
// 无论压缩器的切分点落在哪里。它是纯辅助函数；调用方决定如何把结果合并到保留的后缀中。
//
// budget 限制返回的记录数量（是条数，不是 token 估算）。非正数表示不设上限。
// 当上限迫使做出选择时，最近出现的带标记记录优先，与 KeepForBudget 的"最新上下文得以保留"规则一致。
// 结果按原始记录顺序排列。""", 0)),
(ind(r"""// SelectForChildren returns the records that must be seeded into child
// sessions: those marked RetainAlways or RetainInChildren, in original record
// order. RetainInThread is intentionally excluded — it survives compaction in
// the parent but must not leak into subagent context.""", 0),
 ind(r"""// SelectForChildren 返回必须植入子会话的记录：标记为 RetainAlways 或 RetainInChildren 的记录，
// 按原始记录顺序排列。RetainInThread 被刻意排除——它在父线程中能熬过压缩，
// 但绝不能泄漏到子代理的上下文中。""", 0)),
])

# ---------------- retention_write.go ----------------
apply("retention_write.go", [
(ind(r"""// retentionDataLimit leaves headroom under maxLineLen for the record envelope
// and the small _retention field SetRetention adds. A payload above the limit
// is refused rather than producing a JSONL line the reader would skip as
// oversized.""", 0),
 ind(r"""// retentionDataLimit 在 maxLineLen 之下为记录信封和 SetRetention 添加的小 _retention 字段
// 留出余量。超过限制的载荷会被拒绝，而不是生成一条会被读取器视为过大而跳过的 JSONL 行。""", 0)),
(ind(r"""// RetentionFromKind maps a record kind to its default retention hint. The
// policy is deliberately small and automatic, so callers do not have to
// remember to stamp each important record:
//
//   - checkpoint, summary, compaction: durable context anchors. They carry the
//     state needed to resume or explain a run, so they survive compaction and
//     are seeded into child sessions (RetainAlways).
//   - title: cheap orientation for a subagent, but not worth pinning in the
//     parent thread; propagate to children only (RetainInChildren).
//   - everything else (messages, tools, attempts, progress, stats, usage,
//     header, unknown kinds): RetainDefault. Read and write behaviour stays
//     exactly as it was before these hints existed.
//
// Matching is case-insensitive and trims surrounding whitespace, so kinds
// authored outside this package still get the sensible default.""", 0),
 ind(r"""// RetentionFromKind 将记录 kind 映射到其默认保留提示。该策略刻意简单且自动，
// 这样调用方不必记得为每条重要记录打标记：
//
//   - checkpoint、summary、compaction：持久的上下文锚点。它们携带恢复或解释一次运行所需的状态，
//     因此在压缩后保留，并植入子会话（RetainAlways）。
//   - title：为子代理提供廉价的定位信息，但不值得固定在父线程中；只传播到子会话（RetainInChildren）。
//   - 其他所有类型（消息、工具、尝试、进度、统计、用量、header、未知 kind）：RetainDefault。
//     读写行为与这些提示出现之前完全一致。
//
// 匹配不区分大小写并去除首尾空白，因此在本包之外定义的 kind 也能得到合理的默认值。""", 0)),
(ind(r"""// StampIfImportant applies RetentionFromKind to an already-encoded payload and
// returns the bytes to write. Default kinds are returned byte-identical, so
// ordinary message/tool writes are unchanged. If an important kind's payload is
// not a JSON object, SetRetention refuses to mark it and the original bytes are
// returned rather than sacrificing the payload to attach a hint. A payload that
// already carries an explicit _retention hint is returned unchanged: the kind
// default is only a fallback for callers that did not choose one.""", 0),
 ind(r"""// StampIfImportant 将 RetentionFromKind 应用于已编码的载荷，并返回要写入的字节。
// 默认 kind 的字节原样返回，因此普通消息/工具写入不受影响。如果重要 kind 的载荷不是 JSON 对象，
// SetRetention 会拒绝标记它，并返回原始字节，而不是为了附加提示而牺牲载荷本身。
// 已经带有显式 _retention 提示的载荷原样返回：kind 默认值只是给未做选择的调用方兜底。""", 0)),
(ind(r"""// An explicit hint wins over the kind's automatic default.
// AppendWithRetention stamps its own value before calling AppendRecord, and
// compaction re-emits records that already carry a hint, so overwriting it
// here would silently change retention semantics on either path.""", 1),
 ind(r"""// 显式提示优先于 kind 的自动默认值。AppendWithRetention 在调用 AppendRecord 前
// 已打上自己的值，压缩也会重新发出已带提示的记录，因此在这里覆盖它会
// 悄然改变这两条路径上的保留语义。""", 1)),
(ind(r"""// AppendWithRetention writes a pre-serialized record with an explicit retention
// hint. The payload is bounded and passed through the shared JSON redactor
// before stamping, then written through AppendRecord, so ids, branching and
// durability behave exactly like any other append.
//
// data must be a JSON object envelope (or empty/null): SetRetention rejects
// other shapes rather than rewriting a payload it cannot safely preserve.""", 0),
 ind(r"""// AppendWithRetention 写入一条带有显式保留提示的预序列化记录。载荷会先被限制大小
// 并通过共享的 JSON 脱敏器，然后打上标记，最后经 AppendRecord 写入，
// 因此 id、分支和持久性与任何其他追加完全一致。
//
// data 必须是 JSON 对象信封（或为空/null）：SetRetention 会拒绝其他形状，
// 而不是重写一个它无法安全保留的载荷。""", 0)),
])

# ---------------- attempt.go ----------------
apply("attempt.go", [
(ind(r"""// Attempt reasons recorded as append-only audit records. They never enter the
// model-visible message history.""", 0),
 ind(r"""// Attempt 原因记录为仅追加的审计记录。它们永远不会进入模型可见的消息历史。""", 0)),
(ind(r"""// Attempt is the payload of a kind:"attempt" record. Error text is redacted
// before it is written; only the committed messages are reconstructed as model
// history.""", 0),
 ind(r"""// Attempt 是 kind:"attempt" 记录的载荷。错误文本在写入前会被脱敏；
// 只有已提交的消息会被重建为模型历史。""", 0)),
(ind(r"""// NewAttempt builds an attempt record from an error. A nil error records just
// the reason; a non-nil error contributes a redacted message and its Go type as
// a stable error class.""", 0),
 ind(r"""// NewAttempt 根据错误构建一条尝试记录。nil 错误只记录原因；
// 非 nil 错误会贡献一条脱敏后的消息及其 Go 类型作为稳定的错误类别。""", 0)),
("// AppendAttempt records one attempt and returns the stored record.",
 "// AppendAttempt 记录一次尝试并返回存储的记录。"),
(ind(r"""// RecordAttemptError is the error-shaped convenience wrapper around
// AppendAttempt, used by callers that only have an error to report.""", 0),
 ind(r"""// RecordAttemptError 是 AppendAttempt 的错误形式便捷包装，
// 供只有错误需要报告的调用方使用。""", 0)),
("// Attempts lists the attempt records on the active branch, oldest first.",
 "// Attempts 列出活动分支上的尝试记录，最早的在前。"),
("// Snippet is the short, redacted text used by tree and export views.",
 "// Snippet 是树视图和导出视图使用的简短脱敏文本。"),
(ind(r"""// kindProgress is the record kind for durable assistant-progress checkpoints.
// The other record Kind constants live in session.go, which this change does
// not own, so the progress kind is deliberately kept local to this file.""", 0),
 ind(r"""// kindProgress 是持久的 assistant 进度检查点所对应的记录 kind。
// 其他记录 Kind 常量位于 session.go，本次改动不拥有该文件，
// 因此进度 kind 被刻意保留在本文件内。""", 0)),
(ind(r"""// Bounds for one progress checkpoint. A checkpoint is written after every
// streamed chunk or tool batch, so records must stay small. Limits are in runes
// (not bytes) so a multi-byte UTF-8 rune is never cut in half.""", 0),
 ind(r"""// 单个进度检查点的上限。每收到一个流式块或工具批次后都会写入检查点，
// 因此记录必须保持很小。限制以符文（而非字节）为单位，
// 确保多字节 UTF-8 符文永远不会被从中间切断。""", 0)),
("// 8 KiB of assistant text per checkpoint", "// 每个检查点保留 8 KiB 的 assistant 文本"),
("// tool calls kept per checkpoint", "// 每个检查点保留的工具调用数"),
("// characters kept per tool call name/argument", "// 每个工具调用名称/参数保留的字符数"),
(ind(r"""// Progress is the payload of a kind:"progress" record: committed progress
// WITHIN a turn, so a crash mid-run can resume from the last checkpoint rather
// than only from the last complete assistant message. Like Attempt, it is a
// durability/audit record and is never rebuilt into model-visible history.
//
// Turn is 1-based and should match the turn the assistant reply belongs to.
// TS is filled from the enclosing record on read; the stored value is just a
// convenience for callers that construct a Progress directly.""", 0),
 ind(r"""// Progress 是 kind:"progress" 记录的载荷：记录一个轮次之内已提交的进度，
// 使运行中途崩溃时可以从最后一个检查点恢复，而不只是从最后一条完整的 assistant 消息恢复。
// 与 Attempt 一样，它是持久性/审计记录，永远不会被重建为模型可见的历史。
//
// Turn 从 1 开始，应与 assistant 回复所属的轮次匹配。
// TS 在读取时由外层记录填充；存储的值只是为直接构造 Progress 的调用方提供便利。""", 0)),
("// capProgressRunes clips text to limit runes without otherwise changing it.",
 "// capProgressRunes 将文本裁剪到 limit 个符文，其他方面不做改动。"),
(ind(r"""// sanitizeProgress bounds and redacts a progress payload before it reaches the
// transcript. Redaction runs before truncation so a credential that straddles
// the cap can never survive as a partially-cut secret. Tool calls are bounded
// in both count and length to keep a single record small.""", 0),
 ind(r"""// sanitizeProgress 在进度载荷写入转录之前限制大小并脱敏。脱敏在截断之前进行，
// 这样跨越上限的凭据就不会以被切断的部分形式残留。工具调用在数量和长度上都受限，
// 以保持单条记录很小。""", 0)),
(ind(r"""// AppendProgress appends one durable progress checkpoint via the normal append
// path and returns an error only when the record could not be written. The
// payload is redacted and bounded first, so callers may pass raw streamed text.""", 0),
 ind(r"""// AppendProgress 通过普通追加路径追加一个持久的进度检查点，
// 仅在记录无法写入时返回错误。载荷会先被脱敏和限制大小，
// 因此调用方可以传入原始的流式文本。""", 0)),
(ind(r"""// progressScope returns the records a progress query should scan: the active
// branch when one is resolvable, otherwise every record in file order.""", 0),
 ind(r"""// progressScope 返回进度查询应扫描的记录：可解析时返回活动分支，
// 否则按文件顺序返回所有记录。""", 0)),
(ind(r"""// latestProgressRecord returns the last progress record in a file-ordered
// slice together with its index, so callers can compare its position with a
// committed assistant message. The boolean reports whether one was found.""", 0),
 ind(r"""// latestProgressRecord 返回按文件排序的切片中最后一个进度记录及其索引，
// 使调用方可以把它与已提交的 assistant 消息比较位置。布尔值报告是否找到了记录。""", 0)),
("// the record timestamp is authoritative on read",
 "// 记录的时间戳在读取时具有权威性"),
(ind(r"""// LatestProgress returns the most recent progress checkpoint on the active
// branch (or the newest record when no active branch can be resolved). The
// boolean reports whether any checkpoint was found. It is the raw query; use
// RecoverProgress for staleness-checked crash recovery.""", 0),
 ind(r"""// LatestProgress 返回活动分支上最新的进度检查点
// （当无法解析活动分支时返回最新的记录）。布尔值报告是否找到任何检查点。
// 这是原始查询；如需带过期检查的崩溃恢复，请使用 RecoverProgress。""", 0)),
(ind(r"""// ProgressCount reports how many progress checkpoints are stored, including
// ones on inactive branches. It is intended for tests and metrics.""", 0),
 ind(r"""// ProgressCount 报告存储了多少个进度检查点，包括非活动分支上的检查点。
// 它用于测试和指标统计。""", 0)),
])

# ---------------- export.go ----------------
apply("export.go", [
("// ExportOptions controls how ExportProject renders a transcript.",
 "// ExportOptions 控制 ExportProject 如何渲染转录。"),
("// record id (or unique prefix) whose path to export; empty follows the active leaf",
 "// 要导出其路径的记录 id（或唯一前缀）；为空则跟随活动叶节点"),
("// redact content, paths, tool payloads and credentials before rendering",
 "// 渲染前对内容、路径、工具载荷和凭据进行脱敏"),
(ind(r"""// maxExportBytes bounds the size of a share file so a very large session cannot
// produce an unbounded artifact. Text formats (HTML/Markdown) append
// exportTruncatedMarker; the JSON format caps structurally (see capExportJSON)
// so the artifact stays a valid JSON array.""", 0),
 ind(r"""// maxExportBytes 限制分享文件的大小，使非常大的会话不会产出无界的文件。
// 文本格式（HTML/Markdown）追加 exportTruncatedMarker；
// JSON 格式则从结构上限制（见 capExportJSON），使产物始终是有效的 JSON 数组。""", 0)),
(ind(r"""// sensitiveExportKeys maps a JSON key to the "kind" placed in its redaction
// placeholder. Fields with these names carry content, filesystem paths,
// commands or tool payloads and must never leave the process in clear text in
// a portable share file. The map is intentionally exhaustive: an unknown key
// still gets the credential-pattern pass from security.Redact.""", 0),
 ind(r"""// sensitiveExportKeys 将 JSON 键映射到其脱敏占位符中使用的"kind"。
// 具有这些名称的字段携带内容、文件系统路径、命令或工具载荷，
// 绝不能以明文形式出现在可移植的分享文件中。该映射刻意做到详尽：
// 未知的键仍会经过 security.Redact 的凭据模式检查。""", 0)),
("// exportEntry is one record of a structured (JSON/Markdown) transcript.",
 "// exportEntry 是结构化（JSON/Markdown）转录中的一条记录。"),
("// only set on the synthetic truncation record",
 "// 仅在合成的截断记录上设置"),
(ind(r"""// exportTruncatedNote is the note carried by the synthetic record appended when
// a JSON transcript is truncated, so the note is machine-readable and the
// artifact stays a valid JSON array.""", 0),
 ind(r"""// exportTruncatedNote 是 JSON 转录被截断时追加的合成记录所携带的说明，
// 使该说明可被机器读取，且产物仍是有效的 JSON 数组。""", 0)),
(ind(r"""// ExportProject renders one branch of a session as a portable, optionally
// redacted share file. project is either a *Session or a session directory
// (string); id identifies the session when a directory is given. branch in the
// argument list takes precedence over opts.Branch.""", 0),
 ind(r"""// ExportProject 将会话的一个分支渲染为可移植的、可选择脱敏的分享文件。
// project 可以是 *Session 或会话目录（字符串）；给定目录时，id 用于标识会话。
// 参数列表中的 branch 优先于 opts.Branch。""", 0)),
(ind(r"""// A plain-text marker appended after the closing bracket would leave an
// unparsable artifact, so cap the JSON structurally instead.""", 2),
 ind(r"""// 在右方括号之后追加纯文本标记会使产物无法解析，因此改为从结构上限制 JSON。""", 2)),
("// loadExportRecords resolves project into records, its backing path and id.",
 "// loadExportRecords 将 project 解析为记录、其支撑路径和 id。"),
(ind(r"""// exportBranch selects the root-to-leaf record chain for branch. It mirrors
// RenderHTML's walk: an empty branch follows the active (last) record, and an
// unknown branch is an error rather than a silent fallback.""", 0),
 ind(r"""// exportBranch 选择 branch 从根到叶的记录链。它镜像 RenderHTML 的遍历：
// 空 branch 跟随活动（最后一条）记录，未知 branch 会报错，而不是静默回退。""", 0)),
("// Accept a full record id or a unique prefix, mirroring ResolvePath.",
 "// 接受完整的记录 id 或唯一前缀，与 ResolvePath 一致。"),
("// ambiguous", "// 不唯一"),
(ind(r"""// sanitizeRecords returns a deep copy of records whose Data payloads have all
// sensitive fields replaced with "[redacted:<kind>:<id>]" placeholders and
// whose remaining strings have passed through security.Redact.""", 0),
 ind(r"""// sanitizeRecords 返回记录的深拷贝，其 Data 载荷中所有敏感字段都被替换为
// "[redacted:<kind>:<id>]" 占位符，其余字符串都经过 security.Redact。""", 0)),
(ind(r"""// Decode with UseNumber so integer payloads survive the round trip exactly.
// json.Unmarshal into any would turn every number into float64, silently
// rounding values above 2^53 (large ids, timestamps in microseconds, token
// counters) and corrupting an otherwise faithful sanitized export.""", 1),
 ind(r"""// 使用 UseNumber 解码，使整数载荷在往返中精确保留。
// json.Unmarshal 到 any 会把每个数字变成 float64，静默舍入超过 2^53 的值
// （大 id、微秒级时间戳、token 计数器），从而破坏原本忠实的脱敏导出。""", 1)),
(ind(r"// A bare string payload is message text with no key to classify it.", 2),
 ind(r"// 裸字符串载荷就是消息文本，没有可用于分类的键。", 2)),
(ind(r"""// Array elements inherit the parent key: `args: ["cmd", ...]`
// must redact every element, not just the array itself.""", 3),
 ind(r"""// 数组元素继承父键：`args: ["cmd", ...]`
// 必须对每个元素脱敏，而不只是对数组本身。""", 3)),
(ind(r"// Numbers, booleans and null carry no free-form text.", 2),
 ind(r"// 数字、布尔值和 null 不携带自由格式文本。", 2)),
(ind(r"""// renderExportHTML delegates to RenderHTML. HTML cannot be built from records
// in memory without editing tree.go, so a sanitized export writes the already
// redacted chain to a temporary JSONL file and renders that instead. For an
// unsanitized export the original file is rendered directly, but the leaf of
// the already-resolved chain must still be passed as the branch: RenderHTML
// with an empty branch follows the active (last) record, which would silently
// export the wrong path when the caller selected a non-active branch.""", 0),
 ind(r"""// renderExportHTML 委托给 RenderHTML。若不修改 tree.go，就无法从内存中的记录构建 HTML，
// 因此脱敏导出会把已脱敏的记录链写入临时 JSONL 文件再渲染它。对于未脱敏的导出，
// 直接渲染原文件，但仍必须把已解析链的叶节点作为 branch 传入：
// RenderHTML 在 branch 为空时跟随活动（最后一条）记录，
// 当调用方选择了非活动分支时，这会静默导出错误的路径。""", 0)),
(ind(r"""// capExportJSON bounds a JSON transcript at maxExportBytes without breaking its
// structure. Unlike capExport (used by the text formats), it never appends bare
// text after the closing bracket: it keeps the largest prefix of complete
// entries that fits and appends a synthetic
// {"kind":"truncated","note":"..."} record instead. The result is always a
// valid JSON array that json.Unmarshal can read.""", 0),
 ind(r"""// capExportJSON 在不破坏结构的前提下将 JSON 转录限制在 maxExportBytes 之内。
// 与 capExport（用于文本格式）不同，它从不在右方括号后追加裸文本：
// 它保留能容纳的最长完整条目前缀，并追加一条合成的
// {"kind":"truncated","note":"..."} 记录。结果始终是 json.Unmarshal 可读的有效 JSON 数组。""", 0)),
(ind(r"""// Fast path: an export already under the cap must be byte-for-byte identical
// to the plain renderer (no marker, no schema change).""", 1),
 ind(r"""// 快速路径：已经在上限之内的导出必须与普通渲染器逐字节一致
// （没有标记，也没有 schema 变化）。""", 1)),
(ind(r"""// Marshal a prefix of n real entries plus the truncation record. Adding an
// entry only grows the array, so its size is monotonic in n and we can
// binary-search the largest prefix that still fits under the cap.""", 1),
 ind(r"""// 序列化 n 条真实条目的前缀再加上截断记录。增加条目只会让数组变大，
// 因此其大小随 n 单调，我们可以二分查找仍能容纳在上限内的最长前缀。""", 1)),
(ind(r"""// best == 0 still yields an array holding only the tiny truncation record,
// which is well under the 2 MiB cap.""", 1),
 ind(r"""// best == 0 仍会得到一个只包含极小截断记录的数组，
// 它远低于 2 MiB 的上限。""", 1)),
(ind(r"""// exportEntryFromRecord projects one record onto the export schema. Metadata
// records (header, attempt, compaction, ...) are rendered with a best-effort
// text so the share file is a complete transcript rather than messages only.""", 0),
 ind(r"""// exportEntryFromRecord 将一条记录投影到导出 schema。
// 元数据记录（header、attempt、compaction 等）以尽力而为的文本渲染，
// 使分享文件是完整的转录，而不只是消息。""", 0)),
(ind(r"""// capExport bounds the artifact at maxExportBytes, backing up to a UTF-8 rune
// boundary so a truncated share file never ends in the middle of a code point.""", 0),
 ind(r"""// capExport 将产物限制在 maxExportBytes 之内，并回退到 UTF-8 符文边界，
// 使截断的分享文件永远不会停在某个码点的中间。""", 0)),
])
