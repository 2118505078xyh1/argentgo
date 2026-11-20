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

apply("checkpoint.go", [
(r"""// ErrCheckpointConflict reports that a file changed after the checkpoint was
// completed. Undo refuses to overwrite such a manual edit.""",
 r"""// ErrCheckpointConflict 表示文件在检查点完成之后发生了更改。
// Undo 拒绝覆盖这样的手动编辑。"""),

(r"// ErrNoCheckpoint reports that a session has no checkpoint record.",
 r"// ErrNoCheckpoint 表示会话没有检查点记录。"),

(r"""// CheckpointFile holds one pre-turn snapshot. Path is always a slash-separated
// path relative to the workspace; SHA256 is the hash of the pre-turn bytes and
// Before carries those bytes so undo is self-contained. Absent means the file
// did not exist before the turn; the After fields describe the state recorded
// when the turn finished (including an aborted turn).""",
r"""// CheckpointFile 保存一个轮次前的快照。Path 始终是相对于工作区、以斜杠分隔的路径；
// SHA256 是轮次前字节的哈希，Before 携带这些字节，使撤销可以自包含。
// Absent 表示该轮次之前文件不存在；After 系列字段描述轮次结束时（包括中止的轮次）记录的状态。"""),

(r"""// Checkpoint is the payload of a kind:"checkpoint" record. Parent is the id of
// the record that was the active leaf before the mutating turn, so /retry can
// fork from exactly that point.""",
r"""// Checkpoint 是 kind:"checkpoint" 记录的载荷。Parent 是执行变更轮次之前活动叶节点的记录 id，
// 这样 /retry 可以恰好从该点分叉。"""),

(r"""// hashCacheKey identifies one version of a file for the checkpoint hash cache.
// It combines the resolved target path (which encodes both the workspace and
// the relative path) with the stat metadata captured by CaptureCheckpoint. A
// cached entry is reused only while size, mtime and mode stay unchanged.""",
r"""// hashCacheKey 为检查点哈希缓存标识文件的一个版本。它把解析后的目标路径
// （其中编码了工作区和相对路径）与 CaptureCheckpoint 捕获的 stat 元数据组合在一起。
// 只有当大小、mtime 和模式都保持不变时，才会复用缓存条目。"""),

(r"""// hashCacheEntry holds the pre-turn bytes and their hash together, because a
// checkpoint stores the bytes in Before so undo stays self-contained. Skipping
// the read therefore requires caching the bytes as well as the digest.""",
r"""// hashCacheEntry 将轮次前的字节及其哈希保存在一起，因为检查点把字节存放在 Before 中，
// 使撤销保持自包含。因此要跳过读取，就必须同时缓存字节和摘要。"""),

(r"""// lookupCheckpointHash returns a copy of the cached entry for key. Copying the
// bytes keeps a caller mutating CheckpointFile.Before from corrupting the cache.""",
r"""// lookupCheckpointHash 返回 key 对应缓存条目的副本。复制字节可防止调用方修改
// CheckpointFile.Before 时破坏缓存。"""),

(r"// rememberCheckpointHash caches the hash and bytes of a freshly read file.",
 r"// rememberCheckpointHash 缓存新读取文件的哈希和字节。"),

(r"""// ClearCheckpointHashCache drops every cached checkpoint hash and byte slice.
// The cache is a pure speed-up: clearing it never changes a checkpoint result,
// it only makes the next CaptureCheckpoint re-read and re-hash its files.""",
r"""// ClearCheckpointHashCache 丢弃所有缓存的检查点哈希和字节切片。
// 缓存只是纯粹的加速手段：清除它绝不会改变检查结果，
// 只会让下一次 CaptureCheckpoint 重新读取并重新计算其文件的哈希。"""),

(r"""// InvalidateCheckpointHashCache drops all cached versions of rel under
// workspace. Call it when a file is known to have changed outside a checkpoint
// turn and a stat-metadata-only key would not notice (for example a same-size
// edit with a restored mtime). Unknown or unresolvable paths are a no-op.""",
r"""// InvalidateCheckpointHashCache 丢弃 workspace 下 rel 的所有缓存版本。
// 当已知文件在检查点轮次之外发生了更改，而仅基于 stat 元数据的键无法察觉时
// （例如大小不变且恢复了 mtime 的编辑），应调用此函数。未知或无法解析的路径为空操作。"""),

(r"""// checkpointFileReader is the injectable file reader used by CaptureCheckpoint.
// Production callers use os.ReadFile; tests substitute a counting reader.""",
r"""// checkpointFileReader 是 CaptureCheckpoint 使用的可注入文件读取器。
// 生产调用方使用 os.ReadFile；测试则替换为计数读取器。"""),

(r"""// CaptureCheckpoint snapshots the pre-turn bytes of paths, which must be
// relative to workspace. Paths are deduplicated and sorted for a stable
// manifest. Unchanged files are served from the in-process hash cache; the
// cache is strictly an optimization and is never required for correctness.""",
r"""// CaptureCheckpoint 对 paths 的轮次前字节进行快照，这些路径必须相对于 workspace。
// 路径会去重并排序，以生成稳定的清单。未更改的文件由进程内哈希缓存提供；
// 该缓存严格来说只是优化，正确性从不依赖它。"""),

(r"""// CompleteCheckpoint records the post-turn state of every manifest entry. It
// must run even when the turn was cancelled, with Partial set on the manifest.""",
r"""// CompleteCheckpoint 记录清单中每一项的轮次后状态。即使轮次被取消，也必须运行它，
// 并在清单上设置 Partial。"""),

(r"""// RecordCheckpoint appends a checkpoint to s and returns its stored record.
// Call CaptureCheckpoint and RecordCheckpoint before the mutating tool batch.""",
r"""// RecordCheckpoint 向 s 追加一个检查点并返回其存储记录。
// 在执行变更的工具批次之前，先调用 CaptureCheckpoint 和 RecordCheckpoint。"""),

(r"""// LatestCheckpoint returns the last checkpoint on the active branch and its
// record. It returns ErrNoCheckpoint when the session has none.""",
r"""// LatestCheckpoint 返回活动分支上最后一个检查点及其记录。
// 当会话没有检查点时返回 ErrNoCheckpoint。"""),

(r"""// CheckpointBack returns the Nth checkpoint on the active branch, counting back
// from the leaf (1 is the most recent), together with its record. It powers
// "undo the last N turns" and "retry from N turns ago" without exposing the
// private path walker to frontends.""",
r"""// CheckpointBack 返回活动分支上倒数第 N 个检查点（从叶节点往回数，1 表示最近的一个）及其记录。
// 它支撑"撤销最近 N 个轮次"和"从 N 个轮次前重试"，而无需向前端暴露私有的路径遍历器。"""),

(r"""// UndoCheckpoint restores only the files named in cp. A file whose current
// bytes match neither the recorded post-turn state nor the pre-turn state is a
// conflicting manual edit and is refused with ErrCheckpointConflict.
//
// argent-go checkpoints are not git-based: there is no HEAD to reset and no
// update-ref to compare against. The "refuse to destroy history" guarantee is
// expressed per file instead. Before any write, every manifest entry must
// still be at the post-turn state recorded when the checkpoint completed. If
// any file has advanced (a manual edit or a later turn), undo is refused in
// full instead of silently overwriting that work. Each write re-verifies the
// expected state immediately before mutating the file -- the equivalent of
// `git update-ref HEAD new old` -- which closes the time-of-check/time-of-use
// window against a concurrent writer. If a later file fails that recheck, the
// files already restored in this pass are rolled back, so a failed undo never
// leaves the workspace half-reverted.""",
r"""// UndoCheckpoint 只还原 cp 中指定的文件。如果某个文件当前的字节既不匹配记录的轮次后状态，
// 也不匹配轮次前状态，则视为冲突的手动编辑，并返回 ErrCheckpointConflict 拒绝操作。
//
// argent-go 的检查点不基于 git：没有可重置的 HEAD，也没有可比较的 update-ref。
// "拒绝破坏历史"的保证改为按文件表达。在任何写入之前，清单中的每一项都必须
// 仍处于检查点完成时记录的轮次后状态。如果任何文件已经前进（手动编辑或后续轮次），
// 则整体拒绝撤销，而不是静默覆盖那些工作。每次写入都会在修改文件前立即重新验证预期状态
// ——相当于 `git update-ref HEAD new old`——从而关闭针对并发写入者的检查时/使用时
// （time-of-check/time-of-use）窗口。如果后续文件未通过该复查，本次已还原的文件会回滚，
// 因此失败的撤销绝不会让工作区处于半还原状态。"""),

(r"""// undoAction is one validated file mutation. preUndo/preUndoMode capture the
// state validation accepted, so a later failure can roll back earlier writes
// and keep a failed undo all-or-nothing.""",
r"""// undoAction 是一次经过校验的文件变更。preUndo/preUndoMode 记录校验所接受的状态，
// 这样后续失败时可以回滚较早的写入，使失败的撤销保持全有或全无。"""),

(r"""// undoCheckpoint implements UndoCheckpoint. beforeWrite, when non-nil, runs
// immediately before each file is mutated; tests use it to simulate a
// concurrent edit and assert that the CAS recheck rejects it. Production
// callers reach it only through UndoCheckpoint with a nil hook.""",
r"""// undoCheckpoint 实现 UndoCheckpoint。beforeWrite 非空时，会在每次修改文件前立即运行；
// 测试使用它模拟并发编辑，并断言 CAS 复查会拒绝该编辑。生产调用方只能通过
// 传入 nil 钩子的 UndoCheckpoint 到达这里。"""),

(r"// already at the pre-turn state", r"// 已处于轮次前状态"),

(ind(r"""// CAS: re-verify the file is still at the post-turn state the manifest
// recorded (the "old" value). A concurrent edit that landed after the
// validation pass must not be overwritten.""", 2),
 ind(r"""// CAS：重新验证文件仍处于清单记录的轮次后状态（即"旧"值）。
// 在校验通过之后到达的并发编辑不得被覆盖。""", 2)),

(r"""// rollbackUndoCheckpoint restores actions that were already applied back to
// the state validation saw. It runs in reverse order and is best-effort: a
// secondary cleanup failure is reported to the caller but never replaces the
// primary conflict/write error that triggered the rollback.""",
r"""// rollbackUndoCheckpoint 将已经执行的操作还原到校验时看到的状态。它按逆序运行，且是尽力而为的：
// 二次清理失败会报告给调用方，但绝不会取代触发回滚的主冲突/写入错误。"""),

(r"// Undo deleted a file the turn had created; put it back.",
 r"// 撤销删除了该轮次创建的文件；把它放回去。"),

(r"// Undo recreated a file the turn had deleted; remove it again.",
 r"// 撤销重新创建了该轮次删除的文件；再次将其删除。"),

(r"// Undo overwrote the file; write the post-turn bytes back.",
 r"// 撤销覆盖了该文件的字节；把轮次后的字节写回。"),

(r"""// verifyRestoreTarget re-checks that target is still in the post-turn state
// recorded in file -- the "old" value of the conceptual compare-and-swap. It
// mirrors the first-pass validation in undoCheckpoint and runs immediately
// before each mutation so a concurrent edit cannot be silently clobbered.""",
r"""// verifyRestoreTarget 重新检查 target 是否仍处于 file 中记录的轮次后状态，
// 即概念上的比较并交换（compare-and-swap）中的"旧"值。它镜像 undoCheckpoint 中的首轮校验，
// 并在每次修改前立即运行，使并发编辑不会被静默覆盖。"""),

(ind(r"""// The turn created this file and undo removes it; it must still be the
// exact file the turn left behind.""", 2),
 ind(r"""// 该轮次创建了这个文件，而撤销会删除它；它必须仍是该轮次留下的那个文件。""", 2)),

(ind(r"// The turn deleted this file and undo recreates it; it must still be absent.", 2),
 ind(r"// 该轮次删除了这个文件，而撤销会重新创建它；它必须仍然不存在。", 2)),

(ind(r"""// The turn modified this file and undo restores the pre-turn bytes; it
// must still match the post-turn hash recorded at checkpoint completion.""", 2),
 ind(r"""// 该轮次修改了这个文件，而撤销会还原轮次前的字节；它必须仍匹配检查点完成时记录的轮次后哈希。""", 2)),

(r"""// RetryFromCheckpoint forks s at the checkpoint's parent and records a retried
// attempt on the fork. The source session file is never modified. An empty id
// selects the latest checkpoint on the active branch; a checkpoint record id
// or its parent record id are both accepted.""",
r"""// RetryFromCheckpoint 在检查点的父节点处分叉 s，并在分叉上记录一次重试尝试。
// 源会话文件永远不会被修改。空的 id 会选择活动分支上最新的检查点；
// 检查点记录 id 或其父记录 id 均可接受。"""),

(r"// Retry is the store-scoped form of RetryFromCheckpoint.",
 r"// Retry 是 RetryFromCheckpoint 的 store 作用域版本。"),

(r"""// normalizeRelativePath converts a stored path to a slash-separated relative
// path. Absolute paths and .. segments are refused before any file is touched.""",
r"""// normalizeRelativePath 将存储的路径转换为以斜杠分隔的相对路径。
// 在触碰任何文件之前，绝对路径和 .. 段都会被拒绝。"""),

(r"""// resolveWorkspacePath maps a normalized relative path to an absolute path,
// rejecting symlinked components so a checkpoint can never restore through a
// link that points outside the workspace.""",
r"""// resolveWorkspacePath 将规范化的相对路径映射为绝对路径，
// 拒绝符号链接组件，使检查点永远无法通过指向工作区之外的链接进行还原。"""),
])
