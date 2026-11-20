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
    if missing:
        print("MISSING in", fname)
        for m, n in missing:
            print("  count=%d %s" % (n, m))
    else:
        print("OK", fname, len(pairs), "replacements")

def ind(block, n):
    p = "\t" * n
    return "\n".join(p + line for line in block.split("\n"))

apply("reconcile.go", [
(ind(r"""// abortedResultText is the content of a synthetic tool result inserted at
// replay time for a tool call that never produced output.""", 1),
 ind(r"""// abortedResultText 是在重放时从未产生输出的工具调用插入的合成工具结果的内容。""", 1)),

(ind(r"""// abortedIDPrefix namespaces deterministic synthetic ids so they cannot
// collide with ids minted for real records.""", 1),
 ind(r"""// abortedIDPrefix 为确定性的合成 id 提供命名空间，使其不会与为真实记录生成的 id 冲突。""", 1)),

(ind(r"""// maxSyntheticResults bounds how many aborted results a single replay may
// insert, keeping a corrupted transcript from ballooning the projection.""", 1),
 ind(r"""// maxSyntheticResults 限制单次重放可以插入多少条中止结果，
// 防止损坏的转录使投影结果膨胀。""", 1)),

(r"""// abortedResultID derives a stable synthetic result id from a tool call id.
// Replaying the same session twice yields the same id, which matters because
// these ids travel to the model as the partner of a real tool call.""",
 r"""// abortedResultID 从工具调用 id 派生一个稳定的合成结果 id。
// 对同一会话重放两次会得到相同的 id，这很重要，因为这些 id 会作为真实工具调用的配对项传给模型。"""),

(r"""// ReconcileToolPairs repairs tool call/result pairing in a projected
// conversation. It drops tool results whose call is absent and inserts a
// synthetic aborted result for every tool call that has no result, mirroring
// the replay-time reconciliation performed when a JSONL session is resumed.
//
// The input is never mutated: existing entries are value-copied into a fresh
// slice and synthetic results are appended to that copy. Entries without any
// tool-call metadata pass through unchanged (the input slice is returned
// directly in that case), so clean sessions keep a byte-identical projection.""",
r"""// ReconcileToolPairs 修复投影对话中工具调用/结果的配对。它丢弃调用不存在的结果条目，
// 并为每个没有结果的调用插入一条合成的中止结果，与恢复 JSONL 会话时执行的重放期协调一致。
//
// 输入永远不会被修改：现有条目按值复制到新的切片，合成结果追加到该副本上。
// 没有任何工具调用元数据的条目原样通过（这种情况下直接返回输入切片），
// 因此干净的会话保持逐字节相同的投影。"""),

(r"// Pass 1: collect the ids declared by assistant calls and by tool results.",
 r"// 第 1 轮：收集 assistant 调用和工具结果声明的 id。"),

(ind(r"// No tool metadata at all: nothing to reconcile.", 2),
 ind(r"// 完全没有工具元数据：无需协调。", 2)),

(ind(r"""// Pass 2: keep results whose call exists, synthesize results for calls that
// have none. A synthetic result follows the assistant entry that declares
// the call (in declaration order) and is inserted at most once per id.""", 1),
 ind(r"""// 第 2 轮：保留调用存在的结果，为没有结果的调用合成结果。合成结果紧跟在声明该调用的
// assistant 条目之后（按声明顺序），且每个 id 最多插入一次。""", 1)),

(r"// Orphaned result: the call it answers is not in the transcript.",
 r"// 孤立结果：它所响应的调用不在转录中。"),

(r"""// RecoveryCategory is the state a transcript was found in when a session is
// resumed. The four values are ordered by how much operator oversight the
// state needs: clean needs none, degraded_history needs a human decision.""",
r"""// RecoveryCategory 表示恢复会话时发现转录所处的状态。四个值按所需的人工介入程度排序：
// clean 无需介入，degraded_history 需要人工决策。"""),

(r"""// Visible announcements carried by a RecoveryPlan. They are plain, stable
// strings so this stdlib-only package needs no i18n dependency; a frontend is
// free to localize them by keying off Category instead.""",
r"""// RecoveryPlan 携带的可见公告。它们是普通且稳定的字符串，
// 因此这个仅依赖标准库的包不需要 i18n 依赖；前端可以自由地根据 Category 进行本地化。"""),

(r"""// RecoveryPlan is a structured, read-only account of the state a conversation
// is in when it is resumed. It deliberately changes nothing: tool call/result
// pairing is still repaired at replay time by ReconcileToolPairs, and physical
// damage is still handled by ValidateAndRepair. A caller uses the plan only to
// decide whether to continue automatically, ask the user, and what to announce.
//
// The detail fields are deduplicated and in first-seen order so two callers
// drawing conclusions from the same transcript see the same values.""",
r"""// RecoveryPlan 是对恢复对话时所在状态的结构化只读描述。它刻意不做任何更改：
// 工具调用/结果的配对仍由 ReconcileToolPairs 在重放时修复，物理损坏仍由 ValidateAndRepair 处理。
// 调用方只使用该计划来决定是否自动继续、是否询问用户以及公告什么内容。
//
// 详情字段已去重并保持首次出现的顺序，因此从同一转录得出结论的两个调用方看到相同的值。"""),

(ind(r"""// CanAutoContinue reports whether the run may proceed without a fresh
// operator decision. It is false only for degraded_history.""", 1),
 ind(r"""// CanAutoContinue 报告运行是否可以在无需新的人工决策的情况下继续。
// 仅当状态为 degraded_history 时为 false。""", 1)),

(ind(r"""// RequiresUserConfirmation reports whether an operator must explicitly
// approve continuation. It is true only for degraded_history.""", 1),
 ind(r"""// RequiresUserConfirmation 报告是否必须由操作员显式批准才能继续。
// 仅当状态为 degraded_history 时为 true。""", 1)),

(ind(r"""// Announcement is the user-visible notice for this plan. It is empty for
// clean sessions, where there is nothing to report.""", 1),
 ind(r"""// Announcement 是该计划对用户可见的通知。对于干净的会话，它为空，因为没有什么可报告的。""", 1)),

(ind(r"""// OrphanToolCalls lists assistant tool calls that have no real result and
// are therefore answered by ReconcileToolPairs with a synthetic aborted
// result. Synthetic results already present in the input do not count as
// answers, so a replayed transcript classifies the same as a raw one.""", 1),
 ind(r"""// OrphanToolCalls 列出没有真实结果的 assistant 工具调用，因此会被 ReconcileToolPairs
// 用合成的中止结果来应答。输入中已存在的合成结果不算作应答，
// 因此重放后的转录与原始转录的分类结果相同。""", 1)),

(ind(r"""// OrphanToolResults lists tool results whose call is absent from the
// transcript; unlike a missing result, a missing call means lost history.""", 1),
 ind(r"""// OrphanToolResults 列出调用在转录中缺失的工具结果；与缺失结果不同，缺失调用意味着历史丢失。""", 1)),

(ind(r"""// BrokenLinks counts parent links that do not point at the preceding
// message on the linear path.""", 1),
 ind(r"""// BrokenLinks 统计没有指向线性路径上一条消息的 parent 链接数量。""", 1)),

(ind(r"""// PendingPrompt is true when the transcript ends with an unanswered user
// prompt.""", 1),
 ind(r"""// 当转录以未得到应答的用户提示结束时，PendingPrompt 为 true。""", 1)),

(ind(r"""// InterruptedAssistant is true when the transcript ends in the middle of
// an assistant turn (a dangling call or a result with no final answer).""", 1),
 ind(r"""// 当转录在 assistant 轮次中途结束时（悬空调用或没有最终应答的结果），InterruptedAssistant 为 true。""", 1)),

(r"""// ClassifyRecovery inspects a projected conversation and returns the recovery
// plan for resuming it. It is pure: the input slice and its entries are never
// mutated. Classification is conservative — a history that cannot be trusted
// is reported as degraded_history so automatic continuation is disabled.
//
// Decision order, most severe first:
//   - a broken parent link or an orphaned tool result => degraded_history;
//   - a trailing unanswered user prompt             => interrupted_prompt;
//   - a trailing tool result or an unanswered call   => interrupted_turn;
//   - anything else                                 => clean.
//
// Synthetic aborted results produced by ReconcileToolPairs are skipped when
// checking parent links (they carry no parent of their own) and never count
// as real answers, so classifying a replayed transcript is equivalent to
// classifying the raw history before replay.""",
r"""// ClassifyRecovery 检查投影对话并返回用于恢复它的计划。它是纯函数：输入切片及其条目永远不会被修改。
// 分类是保守的——无法信任的历史会被报告为 degraded_history，从而禁用自动继续。
//
// 判定顺序，从最严重开始：
//   - parent 链接断开或存在孤立的工具结果 => degraded_history；
//   - 末尾有未应答的用户提示              => interrupted_prompt；
//   - 末尾有工具结果或未应答的调用        => interrupted_turn；
//   - 其他情况                            => clean。
//
// ReconcileToolPairs 生成的合成中止结果在检查 parent 链接时会被跳过
// （它们自身不携带 parent），也从不算作真实应答，因此对重放后的转录进行分类
// 等价于对重放前的原始历史进行分类。"""),

(ind(r"""// Index declarations and real answers. A synthetic aborted result is not
// an answer: it is the recovery mechanism itself, not something the model
// produced. Deduplicate declarations while keeping first-seen order.""", 1),
 ind(r"""// 为声明和真实应答建立索引。合成的中止结果不是应答：它本身就是恢复机制，
// 而不是模型产生的东西。对声明去重，同时保持首次出现的顺序。""", 1)),

(r"""// isSyntheticAborted recognizes the aborted results ReconcileToolPairs inserts
// at replay time. They are bookkeeping, not user-visible history.""",
r"""// isSyntheticAborted 识别 ReconcileToolPairs 在重放时插入的中止结果。
// 它们是内部记账信息，不是用户可见的历史。"""),

(r"""// lastRealEntry returns the newest non-synthetic entry, or nil if the
// conversation consists only of synthetic results.""",
r"""// lastRealEntry 返回最新的非合成条目；如果对话仅由合成结果组成，则返回 nil。"""),

(r"""// countBrokenLinks checks that each message's parent id is the id of the
// message before it on the linear path. The first entry is a boundary (its
// parent may be a header or a compacted-away record) and is skipped, as is an
// entry following a system compaction summary. Entries with an empty id or
// parent carry no evidence either way and are skipped rather than guessed at.
// Synthetic results are skipped entirely so the real message before them stays
// the link target.""",
r"""// countBrokenLinks 检查每条消息的 parent id 是否为线性路径上一条消息的 id。
// 第一个条目是边界（其 parent 可能是 header 或已被压缩掉的记录），会被跳过；
// 紧跟在系统压缩摘要之后的条目同样跳过。id 或 parent 为空的条目无法提供任何证据，
// 因此跳过而不是猜测。合成结果被完全跳过，使其前面的真实消息保持为链接目标。"""),
])
