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

# ---------------- session.go ----------------
apply("session.go", [
(r"""// Package session stores a conversation as JSONL files whose records form a tree.
//
// One file lives under a directory per session. Every line is a record
// `{"id","parent","ts","kind","data"}`; a record's parent is the id of the
// record it follows, so branching never rewrites history. The conversation the
// model sees is the path from the root to the active leaf, and a compaction
// record replaces the older part of that path with a summary.""",
 r"""// Package session 将对话存储为 JSONL 文件，其记录构成一棵树。
//
// 每个会话有一个目录，其中存放一个文件。每一行都是一条记录
// `{"id","parent","ts","kind","data"}`；记录的 parent 是它所跟随记录的 id，
// 因此分支永远不会重写历史。模型看到的对话是从根到活动叶节点的路径，
// 而压缩记录会用摘要替换该路径中较旧的部分。"""),
(r"""// Record kinds written by the store. Kinds are open: an unknown kind is kept
// verbatim and ignored by the conversation view.""",
 r"""// store 写入的记录 kind。kind 是开放的：未知 kind 会被逐字保留，并被对话视图忽略。"""),
(ind(r"""// keepAppendFD is false on Windows: os.OpenFile there does not request
// FILE_SHARE_DELETE, so a held handle blocks os.Remove/os.Rename of the
// session file (and breaks t.TempDir cleanup). The id index still removes
// the O(n^2) re-read on every platform; Windows just repeats the cheap
// open/write/close trio per append.""", 1),
 ind(r"""// keepAppendFD 在 Windows 上为 false：那里的 os.OpenFile 不请求 FILE_SHARE_DELETE，
// 因此持有的句柄会阻塞对会话文件的 os.Remove/os.Rename（并破坏 t.TempDir 清理）。
// id 索引在所有平台上仍然消除了 O(n^2) 的重复读取；Windows 只是每次追加重复执行
// 开销很小的 open/write/close 三步骤。""", 1)),
("// Record is one line of a session file.", "// Record 是会话文件中的一行。"),
("// Entry is one message on the linear conversation.", "// Entry 是线性对话中的一条消息。"),
(ind(r"""// Reasoning is the model's thought text on an assistant turn. It is
// transcript-only metadata: empty on legacy records and on non-assistant
// turns, and omitted from archives when unset so old bytes stay identical.""", 1),
 ind(r"""// Reasoning 是 assistant 轮次中模型的思考文本。它是仅存在于转录中的元数据：
// 在旧记录和非 assistant 轮次上为空，未设置时从归档中省略，以便旧字节保持一致。""", 1)),
("// tool name, when the record carries one", "// 记录携带时的工具名称"),
("// Header is the first record of every session.", "// Header 是每个会话的第一条记录。"),
("// Session is an append-only handle to one session file.", "// Session 是单个会话文件的仅追加句柄。"),
(ind(r"""// file is the exact on-disk path resolved when the session was opened or
// created. Discovery matches the ".jsonl" suffix case-insensitively, so a
// transcript may be named "Mixed.JSONL"; deriving the path from id+fileExt
// would point at "Mixed.jsonl" and read nothing on case-sensitive files.
// Empty only for sessions constructed directly from dir+id in tests.""", 1),
 ind(r"""// file 是会话打开或创建时解析出的精确磁盘路径。发现过程以不区分大小写的方式
// 匹配 ".jsonl" 后缀，因此转录文件可能命名为 "Mixed.JSONL"；从 id+fileExt
// 派生路径会指向 "Mixed.jsonl"，在区分大小写的文件系统上什么也读不到。
// 仅对于测试中直接由 dir+id 构造的会话为空。""", 1)),
(ind(r"""// Append state is built lazily on the first AppendRecord and reused while
// the backing file is untouched. All fields below are guarded by mu.
// Rebuilding it means one full JSONL read; steady-state appends only write
// one line and update these fields, avoiding O(n^2) re-parsing.""", 1),
 ind(r"""// 追加状态在第一次 AppendRecord 时惰性构建，并在支撑文件未被改动时复用。
// 以下所有字段都由 mu 保护。重建它意味着一次完整的 JSONL 读取；
// 稳态追加只写一行并更新这些字段，避免 O(n^2) 的重复解析。""", 1)),
("// ids of the records currently in the file", "// 当前文件中记录的 id"),
("// id of the last record in file order", "// 按文件顺序最后一条记录的 id"),
("// next n%06d candidate to mint", "// 下一个待生成的 n%06d 候选 id"),
("// long-lived O_CREATE|O_WRONLY|O_APPEND handle (nil on Windows)",
 "// 长生命周期的 O_CREATE|O_WRONLY|O_APPEND 句柄（Windows 上为 nil）"),
("// file size at last sync", "// 上次同步时的文件大小"),
("// modtime at last sync", "// 上次同步时的修改时间"),
("// path identity at last sync (inode/volume on Windows)",
 "// 上次同步时的路径标识（Windows 上为 inode/卷）"),
(ind(r"""// readRecordsFn, when set by tests, overrides the full read used to build
// or rebuild the index. Records() deliberately stays on readRecords.""", 1),
 ind(r"""// readRecordsFn 由测试设置时，会覆盖用于构建或重建索引的全量读取。
// Records() 刻意始终使用 readRecords。""", 1)),
("// ID is the session id, which is also its file name without the extension.",
 "// ID 是会话 id，也就是去掉扩展名后的文件名。"),
("// Dir reports the directory that holds the session file.", "// Dir 报告存放会话文件的目录。"),
])

# ---------------- schema.go ----------------
apply("schema.go", [
(r"""// CurrentSchemaVersion is the JSONL schema version this build writes and reads.
// It is bumped only when the *meaning* of an existing record changes: adding a
// new, unknown kind needs no bump because kinds are open (see Record).""",
 r"""// CurrentSchemaVersion 是本版本写入和读取的 JSONL schema 版本。
// 仅当现有记录的*含义*发生变化时才会提升它：添加新的未知 kind 无需提升，
// 因为 kind 是开放的（见 Record）。"""),
(r"""// KindSchema marks the explicit schema header record. Like KindHeader it is
// metadata: the conversation view ignores it, so inserting one never changes
// which messages a session yields.""",
 r"""// KindSchema 标记显式的 schema 头记录。与 KindHeader 一样，它是元数据：
// 对话视图会忽略它，因此插入一条永远不会改变会话产生的消息。"""),
(r"""// ErrNewerSchemaVersion reports a transcript written by a newer build than this
// one. MigrateRecords refuses to guess how to downgrade such a file.""",
 r"""// ErrNewerSchemaVersion 表示转录由比本版本更新的构建写入。
// MigrateRecords 拒绝猜测如何降级这样的文件。"""),
("// SchemaPayload is the data carried by a KindSchema header record.",
 "// SchemaPayload 是 KindSchema 头记录携带的数据。"),
(r"""// SchemaRecord builds the explicit schema header record for version. It is a
// pure constructor: the returned Record carries only Kind and Data, leaving
// ID/Parent/TS for the caller (or the append path) to fill in.""",
 r"""// SchemaRecord 为 version 构建显式的 schema 头记录。它是纯构造函数：
// 返回的 Record 只携带 Kind 和 Data，ID/Parent/TS 留给调用方（或追加路径）填充。"""),
(r"""// migration upgrades records from one schema version to the next. A step must
// be pure: it returns a new slice and never mutates its input. Register a step
// for source version N to upgrade N -> N+1 when CurrentSchemaVersion is bumped.""",
 r"""// migration 将记录从一个 schema 版本升级到下一个版本。每一步必须是纯函数：
// 返回新切片，永不修改输入。当 CurrentSchemaVersion 提升时，为源版本 N 注册一步，
// 将 N 升级到 N+1。"""),
(r"""// migrations is the upgrade hook table, keyed by source version. Today the only
// step is v0 -> v1 (give the legacy, header-less transcript an explicit header).
// A future v1 -> v2 bump adds a `1: migrateV1ToV2` entry here without touching
// MigrateRecords.""",
 r"""// migrations 是按源版本索引的升级钩子表。目前唯一的一步是 v0 -> v1
// （给旧的无头转录加上显式头）。将来 v1 -> v2 的提升会在这里添加
// `1: migrateV1ToV2` 条目，而无需改动 MigrateRecords。"""),
(r"""// migrateV0ToV1 upgrades the legacy format to v1 by giving the transcript an
// explicit schema header. If a v0 header is already present its version field
// is updated in place; otherwise a new header is prepended. Every other record
// keeps its order, ids, parents and payload verbatim.""",
 r"""// migrateV0ToV1 通过给转录添加显式 schema 头，将旧格式升级到 v1。
// 如果已存在 v0 头，则原地更新其 version 字段；否则在前面加一个新头。
// 其他每条记录都逐字保留其顺序、id、parent 和载荷。"""),
(r"""// MigrateRecords upgrades records to CurrentSchemaVersion. It is pure (the
// input is never mutated) and idempotent: a transcript already at the current
// version is returned unchanged, so re-running it is a no-op. A transcript with
// no schema header is treated as v0; one carrying a newer version is rejected
// with ErrNewerSchemaVersion rather than being silently downgraded. Unknown
// kinds are always preserved verbatim.""",
 r"""// MigrateRecords 将记录升级到 CurrentSchemaVersion。它是纯函数（输入永不被修改）且幂等：
// 已处于当前版本的转录原样返回，因此重运行是空操作。没有 schema 头的转录按 v0 处理；
// 携带更新版本的转录会被拒绝并返回 ErrNewerSchemaVersion，而不是被静默降级。
// 未知 kind 始终逐字保留。"""),
(r"""// schemaVersion reports the version declared by the schema header record, or 0
// when the transcript has none. headerIndex is -1 in the latter case. A
// malformed or conflicting header is an error so it is never silently treated
// as v0.""",
 r"""// schemaVersion 报告 schema 头记录声明的版本；当转录没有头时返回 0。
// 后一种情况下 headerIndex 为 -1。格式错误或冲突的头会被报错，
// 绝不会被静默当作 v0。"""),
])

# ---------------- page.go ----------------
apply("page.go", [
(r"""// defaultPageMaxBytes bounds a page when the caller passes maxBytes <= 0.
// It keeps a single read well below the size of a large transcript while still
// returning a useful amount of history.""",
 r"""// defaultPageMaxBytes 在调用方传入 maxBytes <= 0 时限制页大小。
// 它使单次读取远低于大转录的大小，同时仍返回有用的历史量。"""),
(r"""// Page is one byte-bounded slice of a transcript, in file order.
//
// Bytes is the sum of the raw line lengths (newline excluded) of Records: the
// number of transcript bytes the page carries, not decoded payload sizes.
// HasMore reports whether more records exist on the unread side of the page.
// Next is the cursor for the next call: with fromEnd=false it is the last
// returned record's ID (resume AFTER it); with fromEnd=true it is the first
// returned record's ID (resume BEFORE it). Next is empty when HasMore is false.""",
 r"""// Page 是按文件顺序、受字节限制的转录片段。
//
// Bytes 是 Records 原始行长度之和（不含换行符）：该页携带的转录字节数，
// 不是解码后载荷的大小。HasMore 报告页的未读一侧是否还有更多记录。
// Next 是下一次调用的游标：fromEnd=false 时是最后一条返回记录的 ID（在其后恢复）；
// fromEnd=true 时是第一条返回记录的 ID（在其前恢复）。HasMore 为 false 时 Next 为空。"""),
("// parsedRecord pairs a decoded record with the size of its line on disk.",
 "// parsedRecord 将解码后的记录与其磁盘上的行大小配对。"),
(r"""// ReadPage returns one page of at most maxBytes of transcript data. When
// maxBytes <= 0, defaultPageMaxBytes is used.
//
// afterID is an exclusive cursor. With fromEnd=false, records strictly after
// that ID are returned; with fromEnd=true, records strictly before it are
// returned, so a caller can walk backwards by feeding Next straight back in.
// If the cursor ID is absent, the page starts at the beginning (forward) or
// at the end of the file (backward). Records on the far side of the cursor are
// still parsed to locate it but are never counted toward maxBytes or returned.
//
// Lines are decoded leniently, exactly like readRecords: blank and malformed
// lines are skipped. A record whose encoded line alone exceeds maxBytes is
// never split — it is returned as the page's only record — so every page holds
// whole records and no decoded record is ever truncated.""",
 r"""// ReadPage 返回一页最多 maxBytes 字节的转录数据。当 maxBytes <= 0 时，使用 defaultPageMaxBytes。
//
// afterID 是排他游标。fromEnd=false 时返回严格位于该 ID 之后的记录；
// fromEnd=true 时返回严格位于其之前的记录，因此调用方可以把 Next 直接传回以向前（往回）遍历。
// 如果游标 ID 不存在，页从文件开头（正向）或文件末尾（反向）开始。
// 游标另一侧的记录仍会被解析以定位它，但从不计入 maxBytes，也不会返回。
//
// 行的解码是宽松的，与 readRecords 完全一致：空白行和格式错误的行会被跳过。
// 仅编码后的一行就超过 maxBytes 的记录永远不会被拆分——它作为该页唯一的记录返回——
// 因此每页都包含完整的记录，任何解码后的记录都不会被截断。"""),
(r"""// parseRecordsForPage decodes the whole file in file order. No index exists
// yet, so locating a cursor requires a full lenient pass; the returned page
// itself is still bounded by the byte limit.""",
 r"""// parseRecordsForPage 按文件顺序解码整个文件。目前还没有索引，
// 因此定位游标需要一次完整的宽松扫描；返回的页本身仍受字节上限约束。"""),
(ind(r"""// Defer synthetic ids until every explicit id is known, then use the same
// helper as readRecords so cursors minted by one read API resolve in the
// other (session audit #5).""", 1),
 ind(r"""// 延迟生成合成 id，直到所有显式 id 都已知，然后使用与 readRecords 相同的辅助函数，
// 使一个读取 API 生成的游标能在另一个中解析（session 审计 #5）。""", 1)),
("// pageFromStart returns the leading page of records, bounded by limit.",
 "// pageFromStart 返回开头的记录页，受 limit 限制。"),
(r"""// pageFromEnd returns the trailing page of records, bounded by limit. Next is
// the first returned record's ID so a caller can step further backwards.""",
 r"""// pageFromEnd 返回末尾的记录页，受 limit 限制。Next 是第一条返回记录的 ID，
// 使调用方可以继续往回翻页。"""),
], [
("// An oversized record cannot be split: return it whole and alone.",
 "// 超大记录不能拆分：整条单独返回。"),
])

# ---------------- search.go ----------------
apply("search.go", [
("// Hit is one matching record inside a transcript.",
 "// Hit 是转录中一条匹配的记录。"),
(r"""// Search scans every JSONL transcript under dir for query, case-insensitively.
// Hits are ordered by session (newest first) and then by line number (ascending).
// limit caps the total number of hits; a value <= 0 means no cap.
//
// Snippets are passed through security.Redact before being returned. The
// session package can import internal/security without a cycle (security only
// depends on the standard library), which keeps redaction consistent with the
// audit log instead of maintaining a second regex set here.""",
 r"""// Search 以不区分大小写的方式在 dir 下的每个 JSONL 转录中搜索 query。
// 命中按会话排序（最新的在前），再按行号排序（升序）。
// limit 限制命中总数；值 <= 0 表示不设上限。
//
// 片段在返回前会经过 security.Redact。session 包可以导入 internal/security 而不产生循环
// （security 只依赖标准库），这使脱敏与审计日志保持一致，而无需在此维护第二套正则。"""),
(ind(r"""// The suffix match above is case-insensitive, so strip by length just
// like Container.List does. A case-sensitive TrimSuffix would leave the
// extension on the id for "SESSION.JSONL", producing a Hit.ID that no
// longer resolves through Store.OpenSession.""", 2),
 ind(r"""// 上面的后缀匹配不区分大小写，因此像 Container.List 一样按长度剥离。
// 区分大小写的 TrimSuffix 会在 "SESSION.JSONL" 的 id 上留下扩展名，
// 产生一个再也无法通过 Store.OpenSession 解析的 Hit.ID。""", 2)),
(ind(r"""// A hit can live in any record's payload, so every transcript still has
// to be scanned. The sidecar removes the meta recomputation (title and
// turn counting) and is only trusted while its stat still matches.""", 2),
 ind(r"""// 命中可能位于任何记录的载荷中，因此仍必须扫描每一份转录。
// 旁车文件省去了元数据的重算（标题和轮次计数），且只在其 stat 仍匹配时才被信任。""", 2)),
("// Search is also available on an open store for callers that already own one.",
 "// 对于已经拥有 store 的调用方，Search 也可在打开的 store 上调用。"),
(r"""// numberedRecord keeps the physical JSONL line number: readRecords deliberately
// skips malformed lines, but a search hit must point at the line a user sees.""",
 r"""// numberedRecord 保留物理 JSONL 行号：readRecords 刻意跳过格式错误的行，
// 但搜索命中必须指向用户看到的那一行。"""),
(ind(r"""// Same deferred, conflict-free synthetic ids as readRecords and ReadPage
// (session audit #5); the physical line numbers stay untouched.""", 1),
 ind(r"""// 与 readRecords 和 ReadPage 相同的延迟、无冲突合成 id
// （session 审计 #5）；物理行号保持不变。""", 1)),
(r"""// snippetFor finds the first string value in a record payload containing the
// needle and returns a redacted, single-line window around it. Tool output is
// included because it is stored as a plain string value in the payload.""",
 r"""// snippetFor 在记录载荷中查找第一个包含 needle 的字符串值，
// 并返回其周围经过脱敏的单行窗口。工具输出也包含在内，因为它在载荷中是作为普通字符串值存储的。"""),
(ind(r"""// strings.Index returns a byte offset into lowered, but lowercasing
// can change byte lengths (e.g. İ→i, ẞ→ß). Convert it to a rune
// offset first: strings.ToLower folds rune-by-rune, so the rune
// index is identical in lowered and in the original value.""", 3),
 ind(r"""// strings.Index 返回的是 lowered 中的字节偏移，但小写化可能改变字节长度
// （例如 İ→i、ẞ→ß）。先将其转换为符文偏移：strings.ToLower 按符文逐一折叠，
// 因此符文索引在 lowered 和原始值中完全相同。""", 3)),
("// stringValues walks a JSON payload and returns every string it contains.",
 "// stringValues 遍历 JSON 载荷并返回其中包含的所有字符串。"),
(ind(r"""// A JSON object is decoded into a Go map, whose iteration order is
// randomized. Walking it directly would make "the first string value
// containing the needle" (see snippetFor) depend on the map's random
// seed, so two searches of the same transcript could return different
// snippets. Visit keys in sorted order instead: the set of strings is
// unchanged, but the order — and therefore the chosen snippet — is
// deterministic for a given record.""", 2),
 ind(r"""// JSON 对象解码为 Go map，其迭代顺序是随机的。直接遍历它会使"第一个包含 needle 的
// 字符串值"（见 snippetFor）依赖于 map 的随机种子，因此同一转录的两次搜索可能返回不同的片段。
// 改为按排序后的顺序访问键：字符串集合不变，但顺序——以及所选择的片段——对给定记录是确定的。""", 2)),
(r"""// contextWindow slices a snippet around a rune index without breaking UTF-8,
// then redacts credentials before collapsing whitespace. Redaction happens
// before truncation so a token placeholder is never cut in half.""",
 r"""// contextWindow 在符文索引周围切出片段而不破坏 UTF-8，然后在折叠空白之前对凭据脱敏。
// 脱敏发生在截断之前，因此 token 占位符永远不会被切成两半。"""),
])
