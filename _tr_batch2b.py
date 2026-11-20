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

# ---------------- stats.go ----------------
apply("stats.go", [
(r"""// Record kinds for statistics and per-request usage. Kinds are intentionally
// open in the session format (see session.go), so these live here rather than
// extending the core kind table.""",
 r"""// 用于统计和每次请求用量的记录 kind。会话格式中 kind 是开放的（见 session.go），
// 因此它们放在这里，而不是扩展核心 kind 表。"""),
(ind(r"""// modelLimit bounds the only free-form string on a usage record so a single
// JSONL line stays bounded. stampLimit does the same for caller timestamps.""", 1),
 ind(r"""// modelLimit 限制用量记录中唯一的自由格式字符串，使单条 JSONL 行保持有界。
// stampLimit 对调用方时间戳做同样的处理。""", 1)),
(r"""// SessionStats is a persisted token/cost roll-up for one run. Storing it as a
// `kind:"stats"` record makes session state survive resume: the JSONL
// transcript is the single source of truth, not in-memory run state.""",
 r"""// SessionStats 是一次运行的持久化 token/成本汇总。将其存储为 `kind:"stats"` 记录
// 使会话状态能在恢复后存活：JSONL 转录是唯一的事实来源，而不是内存中的运行状态。"""),
(r"""// UsageRecord is one billable request's usage, kept for cost provenance. It is
// deliberately distinct from SessionStats: usage records are raw per-request
// facts, while stats records are the roll-up callers display.""",
 r"""// UsageRecord 是一次计费请求的用量，用于成本溯源。它刻意区别于 SessionStats：
// 用量记录是原始的逐请求事实，而统计记录是调用方展示的汇总。"""),
(r"""// AppendStats persists one stats record through the normal append path, so it
// inherits ids, branching and the durability guarantees of every other record.""",
 r"""// AppendStats 通过普通追加路径持久化一条统计记录，
// 因此它继承 id、分支以及每条记录的持久性保证。"""),
(r"""// AppendUsage persists one per-request usage record. The model string and the
// caller timestamp are scrubbed and bounded before they reach disk; token
// counts are clamped non-negative.""",
 r"""// AppendUsage 持久化一条逐请求的用量记录。模型字符串和调用方时间戳在写入磁盘前
// 会被清理和限制；token 计数被限制为非负。"""),
(r"""// LatestStats returns the newest stats record on the active branch, falling
// back to the newest record anywhere in the file when the branch has none.
// The bool reports whether any stats record was found.""",
 r"""// LatestStats 返回活动分支上最新的统计记录，当该分支没有统计记录时，
// 回退到文件中任意位置最新的记录。布尔值报告是否找到任何统计记录。"""),
(r"""// AggregateStats merges every stats record in file order into one roll-up.
// Each record is a per-run delta, so token counts, calls and cost are summed;
// CostSource and Updated come from the newest record that carried them. This
// is what recovers a total across resumes.""",
 r"""// AggregateStats 按文件顺序将所有统计记录合并为一个汇总。每条记录是一次运行的增量，
// 因此 token 计数、调用次数和成本被相加；CostSource 和 Updated 来自携带它们的最新记录。
// 这就是跨恢复恢复总数的方式。"""),
("// UsageRecords returns every usage record in append order, for cost auditing.",
 "// UsageRecords 按追加顺序返回所有用量记录，用于成本审计。"),
("// NaN/Inf cannot be encoded as JSON and would break the whole append.",
 "// NaN/Inf 无法编码为 JSON，会破坏整个追加。"),
(ind(r"""// A cost source is a closed set; dropping anything else also drops secrets
// a caller may have put in this otherwise free-form field.""", 1),
 ind(r"""// 成本来源是封闭集合；丢弃其他内容也会丢弃调用方可能放在这个自由格式字段中的秘密。""", 1)),
])

# ---------------- repair.go ----------------
apply("repair.go", [
("// DecodeKind says whether one physical JSONL line can be trusted.",
 "// DecodeKind 表示一条物理 JSONL 行是否可信。"),
(r"""// ErrHardCorruption marks a transcript that cannot be safely repaired: a
// schema error anywhere, or a syntax error on a non-final line.""",
 r"""// ErrHardCorruption 标记无法安全修复的转录：任意位置出现 schema 错误，
// 或非最后一行出现语法错误。"""),
(r"""// A session transcript is bounded so a damaged or hostile file cannot force an
// unbounded read during validation.""",
 r"""// 会话转录有大小上限，因此损坏或恶意文件无法在校验期间强制无界读取。"""),
("// RepairReport summarizes one validation pass over a session transcript.",
 "// RepairReport 汇总对会话转录的一次校验。"),
(r"""// ValidateAndRepair checks a JSONL transcript before a lenient readRecords
// rebuilds the session tree on truncated history. Only a syntax error on the
// LAST physical line is a recoverable torn tail; with repair set it is dropped
// by publishing the valid prefix atomically. A schema error anywhere, or a
// syntax error on a non-final line, is hard corruption and leaves the file
// untouched. A valid file that merely lacks its final newline gets one appended
// in a separate small repair.""",
 r"""// ValidateAndRepair 在宽松的 readRecords 于历史被截断时重建会话树之前，检查 JSONL 转录。
// 只有最后一条物理行上的语法错误才是可恢复的残缺尾部；设置 repair 时，
// 会通过原子发布有效前缀来丢弃它。任意位置的 schema 错误，或非最后一条物理行上的语法错误，
// 都属于严重损坏，文件保持不动。仅仅缺少末尾换行符的有效文件，会通过一次单独的小修复补上。"""),
(ind(r"""// Drop exactly one trailing empty string so the final physical line is the
// last real line, while an extra blank line remains visible as a torn tail.""", 1),
 ind(r"""// 恰好丢弃一个末尾空字符串，使最后一条物理行就是最后一条真实行，
// 而额外的空行仍作为残缺尾部可见。""", 1)),
(ind(r"""// Terminate the kept prefix only when it actually holds a record.
// A torn first line leaves no prefix; writing a bare "\n" here would
// publish a blank line that decodeLine classifies as another torn
// tail, so every later resume would "repair" the file again forever.""", 3),
 ind(r"""// 仅当保留的前缀确实包含记录时才为其添加结束换行。
// 第一行残缺时不会留下任何前缀；在这里写入一个裸 "\n" 会发布一个空行，
// decodeLine 会把它归类为又一处残缺尾部，于是以后每次恢复都会永远"修复"该文件。""", 3)),
(ind(r"""// An empty file has no unterminated record to fix. Leaving it empty
// keeps a repaired transcript clean instead of introducing the blank
// line that would be reported as a torn tail on the next validation.""", 2),
 ind(r"""// 空文件没有未结束的记录需要修复。保持它为空可使修复后的转录保持干净，
// 而不是引入一条会在下次校验时被报告为残缺尾部的空行。""", 2)),
(r"""// decodeLine classifies one physical line and explains any rejection. Absent
// id, parent and ts fields are legal because readRecords synthesizes an id and
// leaves the other zero values untouched.""",
 r"""// decodeLine 对一条物理行进行分类，并解释拒绝原因。缺少 id、parent 和 ts 字段是合法的，
// 因为 readRecords 会合成 id，并让其他零值保持原样。"""),
(r"""// jsonString rejects null and non-string scalars, which encoding/json would
// otherwise silently accept into a Go string.""",
 r"""// jsonString 拒绝 null 和非字符串标量，否则 encoding/json 会静默把它们接受为 Go 字符串。"""),
(r"""// atomicPublishJSONL stages a complete transcript next to its target, syncs it
// before the rename, and cleans up the staged file on any failure. On Windows
// os.Rename replaces the existing target (MOVEFILE_REPLACE_EXISTING).""",
 r"""// atomicPublishJSONL 在目标文件旁边暂存一份完整的转录，在重命名前同步它，
// 并在任何失败时清理暂存文件。在 Windows 上，os.Rename 会替换已有目标
// （MOVEFILE_REPLACE_EXISTING）。"""),
(r"""// syncRepairDir is best-effort: Windows reports EINVAL for directory handles
// and some filesystems reject Sync outright, neither of which undoes a rename.""",
 r"""// syncRepairDir 是尽力而为的：Windows 对目录句柄返回 EINVAL，某些文件系统直接拒绝 Sync，
// 这两种情况都不会撤销重命名。"""),
(r"""// RecoverProgress returns the latest durable assistant-progress checkpoint for
// crash recovery, after rejecting checkpoints that are behind the session's
// last committed assistant message.
//
// Two guards keep recovery from rewinding the conversation:
//   - Position: an assistant message recorded at or after the checkpoint means
//     that turn already completed, so its mid-turn progress is obsolete.
//   - Turn: a checkpoint whose Turn is not greater than the number of committed
//     assistant messages on the active branch belongs to a turn that is already
//     durable. Progress from the current, in-flight turn passes.
//
// The boolean reports whether a valid checkpoint was found. This is advisory:
// the agent decides whether to resume, replay or discard the returned text and
// tool calls. ValidateAndRepair itself is unchanged.""",
 r"""// RecoverProgress 返回用于崩溃恢复的最新持久 assistant 进度检查点，
// 但会先拒绝落后于会话最后一条已提交 assistant 消息的检查点。
//
// 两道防线防止恢复把对话倒退：
//   - 位置：在检查点处或之后记录的 assistant 消息意味着该轮次已经完成，
//     其中途进度已过时。
//   - 轮次：Turn 不大于活动分支上已提交 assistant 消息数量的检查点，
//     属于已经持久化的轮次。来自当前进行中轮次的进度可以通过。
//
// 布尔值报告是否找到有效检查点。这是建议性的：
// 由 agent 决定是恢复、重放还是丢弃返回的文本和工具调用。ValidateAndRepair 本身不变。"""),
(ind(r"""// A committed assistant message follows this checkpoint: the turn
// it belonged to is complete, so do not resume from mid-turn state.""", 3),
 ind(r"""// 一条已提交的 assistant 消息位于此检查点之后：它所属的轮次已完成，
// 因此不要从中途状态恢复。""", 3)),
(r"""// appendRepairNewline fixes an unterminated final record without rewriting the
// rest of the transcript. It is kept separate from torn-tail repair so a schema
// error can never be masked by a cosmetic rewrite.""",
 r"""// appendRepairNewline 修复未结束的最后一条记录，而不重写转录的其余部分。
// 它与残缺尾部修复分开，因此 schema 错误永远不会被一次表面性的重写所掩盖。"""),
])

# ---------------- session_append.go ----------------
apply("session_append.go", [
(r"""// Append writes one record as a child of the active leaf and makes it the new
// active leaf. The payload is encoded as JSON; nil is stored as `null`.""",
 r"""// Append 写入一条记录作为活动叶节点的子节点，并使其成为新的活动叶节点。
// 载荷编码为 JSON；nil 存储为 `null`。"""),
(r"""// AppendRecord writes one record like Append but also returns the stored
// record, so callers can link later records (checkpoints, retries, attempts)
// to the exact id that was minted.""",
 r"""// AppendRecord 像 Append 一样写入一条记录，但还返回存储的记录，
// 使调用方可以把后续记录（检查点、重试、尝试）链接到刚生成的确切 id。"""),
(ind(r"""// Auto-stamp durable kinds (checkpoint/summary/compaction/title) so the
// compaction and child-context selectors can find them. Default kinds stay
// byte-identical, so ordinary message/tool writes are unchanged.""", 1),
 ind(r"""// 自动为持久 kind（checkpoint/summary/compaction/title）打标记，
// 使压缩和子上下文选择器能找到它们。默认 kind 保持逐字节一致，
// 因此普通消息/工具写入不受影响。""", 1)),
(ind(r"""// prepareAppend mints an id and bumps nextSeq before anything reaches the
// disk. Keep the pre-append value so a failed write can undo that drift and
// leave the index describing exactly what is in the file.""", 1),
 ind(r"""// prepareAppend 在任何内容写入磁盘之前先生成 id 并递增 nextSeq。
// 保留追加前的值，使写入失败时可以撤销这一偏移，让索引精确描述文件中的内容。""", 1)),
(ind(r"""// Records() is always a fresh read from disk, so `active`, `lastID` and the
// `ids` entry for this record are only advanced after a successful write.
// On failure the only in-memory change left behind is the bumped nextSeq;
// drop the minted id from the index as well in case a writer added it
// before reporting an error. writeAppend already drops the fd/cache when
// the write itself failed, so the next append safely rebuilds from disk.""", 1),
 ind(r"""// Records() 始终从磁盘重新读取，因此 `active`、`lastID` 以及该记录的 `ids` 条目
// 只在写入成功后才推进。失败时留下的唯一内存改动是递增后的 nextSeq；
// 也要从索引中删除已生成的 id，以防写入方在报错前把它加了进去。
// writeAppend 在写入本身失败时已经丢弃 fd/缓存，因此下一次追加会安全地从磁盘重建。""", 1)),
(r"""// prepareAppend returns the parent and a fresh id for the next record. It
// lazily builds the in-memory index, and rebuilds it whenever the append fd is
// missing or stale. The id rule is the same as nextID: the lowest unused
// n%06d, so external records with gaps cannot cause a collision.""",
 r"""// prepareAppend 返回下一条记录的 parent 和新的 id。它惰性构建内存索引，
// 并在追加 fd 缺失或过期时重建。id 规则与 nextID 相同：最小的未使用 n%06d，
// 因此有间隙的外部记录不会导致冲突。"""),
(ind(r"""// Same fallback as the old full-read path: an active id that is not
// in the file (forks, external rewrites) links to the last record.""", 2),
 ind(r"""// 与旧的全量读取路径相同的回退：不在文件中的活动 id（分叉、外部重写）
// 链接到最后一条记录。""", 2)),
(r"""// appendStateValid checks that the cached index still describes the file on
// disk: same inode/volume, same size, same modtime. A replacement by rename, a
// truncation, or an edit by another writer fails one of these checks, so the
// caller falls back to the full-read rebuild path. On Windows there is no held
// fd, so the path identity recorded at last sync is used for the inode check.""",
 r"""// appendStateValid 检查缓存索引是否仍描述磁盘上的文件：相同的 inode/卷、大小和修改时间。
// 通过重命名替换、截断或其他写入方的编辑都会让其中一项检查失败，
// 因此调用方回退到全量读取的重建路径。Windows 上没有持有的 fd，
// 因此用上次同步时记录的路径标识来做 inode 检查。"""),
(r"""// rebuildAppendState is the full-read fallback: read the transcript once,
// rebuild the id index, then open a fresh append fd. It retries when the file
// changes between the read and the fstat, so the index can never lag behind
// bytes already in the file.""",
 r"""// rebuildAppendState 是全量读取回退路径：读取一次转录，重建 id 索引，
// 然后打开一个新的追加 fd。当文件在读取和 fstat 之间发生变化时会重试，
// 因此索引永远不会落后于文件中已有的字节。"""),
(ind(r"""// Another writer moved the file between the read and the open;
// start over instead of trusting a stale index.""", 3),
 ind(r"""// 另一个写入方在读取和打开之间移动了文件；
// 重新开始，而不是信任过期的索引。""", 3)),
(ind(r"""// Stat the path after the open: O_CREATE may have just created the
// file, so this is the first reliable identity/modtime snapshot.""", 2),
 ind(r"""// 在打开之后对路径执行 Stat：O_CREATE 可能刚刚创建了文件，
// 因此这是第一个可靠的标识/修改时间快照。""", 2)),
(ind(r"// See keepAppendFD: never leave a Windows handle open.", 3),
 ind(r"// 见 keepAppendFD：绝不让 Windows 句柄保持打开。", 3)),
(r"""// writeAppend writes one JSONL line and updates the index in memory. On Unix
// the long-lived fd is reused; on Windows a short-lived fd is opened per append
// (see keepAppendFD). Any anomaly drops the cached state so the next append
// takes the safe full-read path.""",
 r"""// writeAppend 写入一行 JSONL 并更新内存索引。在 Unix 上复用长生命周期的 fd；
// 在 Windows 上每次追加都打开短生命周期的 fd（见 keepAppendFD）。
// 任何异常都会丢弃缓存状态，使下一次追加走安全的全量读取路径。"""),
(ind(r"""// One stat per append is enough to catch a file replaced behind our back.
// The size check proves the write landed on the file currently at path;
// the inode comparison against lastInfo (captured when the fd was opened
// and refreshed on every append since) proves that file is still the one
// the fd writes through. The former fd.Stat() cross-check added no case the
// pair above misses — any replacement of the path shows up as a size or
// inode mismatch — and it doubled the syscall cost of every append.""", 1),
 ind(r"""// 每次追加做一次 stat 就足以发现背后被替换的文件。
// 大小检查证明写入落在了当前位于该路径的文件上；
// 与 lastInfo（在 fd 打开时捕获，此后每次追加都刷新）的 inode 比较，
// 证明该文件仍是 fd 所写入的那个。之前的 fd.Stat() 交叉检查没有覆盖上面这对检查遗漏的任何情况
// ——路径的任何替换都会表现为大小或 inode 不匹配——反而让每次追加的系统调用成本翻倍。""", 1)),
(ind(r"""// The file was replaced or written to behind our back during the
// append. The line reached a handle that is no longer current; drop
// the cache so the next append rebuilds from what is really on disk.""", 2),
 ind(r"""// 在追加期间，文件在我们背后被替换或写入。这一行写入了一个已不再有效的句柄；
// 丢弃缓存，使下一次追加根据磁盘上的真实内容重建。""", 2)),
("// invalidateAppendState drops both the fd and the id index.",
 "// invalidateAppendState 丢弃 fd 和 id 索引。"),
("// readAll goes through the overridable reader so tests can count full reads.",
 "// readAll 通过可覆盖的读取器，使测试可以统计全量读取次数。"),
(r"""// invalidateAppendCacheAfterRewrite drops cached append state. Code that
// rewrites the session file (compaction, repair tools) should call this so the
// next append re-reads the file. Validation normally detects such rewrites via
// inode/size/modtime, but this makes it explicit and immediate.""",
 r"""// invalidateAppendCacheAfterRewrite 丢弃缓存的追加状态。重写会话文件的代码
// （压缩、修复工具）应调用它，使下一次追加重新读取文件。
// 校验通常能通过 inode/大小/修改时间检测到此类重写，但这让处理变得明确且即时。"""),
])

# ---------------- session_branch.go ----------------
apply("session_branch.go", [
("// Path returns the records from the session root down to entryID, oldest first.",
 "// Path 返回从会话根到 entryID 的记录，最早的在前。"),
(r"""// Fork copies the path to entryID into a new session and returns it. The
// original session is untouched, and the copy keeps the original entry ids so
// the history it points at is unchanged.""",
 r"""// Fork 将到 entryID 的路径复制到一个新会话并返回。原会话不受影响，
// 副本保留原有的条目 id，因此它所指向的历史不变。"""),
("// Fork copies the path to entryID into a new session in the same directory.",
 "// Fork 将到 entryID 的路径复制到同一目录下的新会话。"),
(ind(r"""// A fork taken from a mid-turn record would otherwise lose the header and
// its explicit title. Copy the original header when the path omits it, and
// carry the effective title across as a fresh title record.""", 1),
 ind(r"""// 从轮次中途的记录分叉会丢失 header 及其显式标题。
// 当路径不包含 header 时，复制原始 header，并把有效标题作为一条新的标题记录带过去。""", 1)),
(ind(r"""// Presence of a title record on the path is not enough: a newer override
// may have been appended after the fork point, leaving the path with an
// older title. Compare the source's effective title with the title the
// copied path would show, and carry the former across when they differ so
// the fork resumes under the same name the session had when it was forked.""", 1),
 ind(r"""// 路径上存在标题记录还不够：更新的覆盖可能是在分叉点之后追加的，
// 使路径上留下的是较旧的标题。把源会话的有效标题与复制路径所显示的标题进行比较，
// 不同时将前者带过去，这样分叉恢复时使用的名字与会话被分叉时的名字相同。""", 1)),
(r"""// path is the session file location. A session opened through ResolvePath keeps
// the exact spelling of the file it found (which may use an upper-case
// extension); only sessions built directly from dir+id need the canonical
// id+fileExt spelling. Rebuilding the path from the id would lose the original
// case and break every read/write on a case-sensitive filesystem.""",
 r"""// path 是会话文件的位置。通过 ResolvePath 打开的会话会保留它所找到文件的确切拼写
// （可能使用大写扩展名）；只有直接由 dir+id 构造的会话才需要规范的 id+fileExt 拼写。
// 从 id 重建路径会丢失原始大小写，并在区分大小写的文件系统上破坏每一次读写。"""),
(r"""// activeSnapshot returns the active leaf under the mutex. Every read path that
// walks the tree goes through this helper so the s.active field is never read
// outside the lock, while the caller still does its own file I/O lock-free.""",
 r"""// activeSnapshot 在互斥锁下返回活动叶节点。每条遍历树的读取路径都经过这个辅助函数，
// 使 s.active 字段永远不会在锁外被读取，而调用方仍可在无锁状态下执行自己的文件 I/O。"""),
(r"""// activePath walks from the active leaf to the root and returns the records
// oldest first. Records that are not reachable from the leaf are branches and
// stay out of the conversation.""",
 r"""// activePath 从活动叶节点走到根节点，并返回记录（最早的在前）。
// 从叶节点不可达的记录属于分支，不会出现在对话中。"""),
("// nextID mints the lowest id that is not already used.",
 "// nextID 生成未被使用的最小 id。"),
])
