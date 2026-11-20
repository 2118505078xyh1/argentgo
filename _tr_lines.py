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

# ===== active_concurrent_test.go =====
apply("active_concurrent_test.go", [
("// TestActiveConcurrentAppendAndRead exercises the s.active data race: many",
 "// TestActiveConcurrentAppendAndRead 检验 s.active 的数据竞争：多个"),
("// goroutines append (which writes s.active under mu) while others read through",
 "// goroutine 执行追加（在 mu 下写 s.active），同时其他 goroutine 通过"),
("// Records/Messages/Path (which must read s.active via activeSnapshot under mu).",
 "// Records/Messages/Path 读取（它们必须通过 activeSnapshot 在 mu 下读 s.active）。"),
("// This machine has no cgo, so the race detector cannot run. The test therefore",
 "// 本机没有 cgo，因此无法运行竞态检测器。所以该测试改为"),
("// asserts logical invariants instead: no panic, the final record count is",
 "// 断言逻辑不变量：不 panic、最终记录数准确，"),
("// exact, and every id/parent link is intact. Running with -race on a cgo host",
 "// 且每个 id/parent 链接完好。在带 cgo 的主机上用 -race 运行"),
("// would additionally catch the original unsynchronised read.",
 "// 还能额外捕获最初那个未同步的读取。"),
("\t// assertChain checks a snapshot a concurrent reader may observe. Because",
 "\t// assertChain 检查并发读取者可能观察到的快照。由于"),
("\t// appends are serialised by mu and only ever extend a single chain, any",
 "\t// 追加由 mu 串行化且只会延长单条链，因此读取者看到的"),
("\t// snapshot the reader sees is a clean prefix: ids are unique and every",
 "\t// 任何快照都是干净的前缀：id 唯一，且每个"),
("\t// non-root parent resolves inside the snapshot.",
 "\t// 非根节点的 parent 都能在快照内解析。"),
("\t\t\t\t// Messages walks the tree via activePath -> activeSnapshot.",
 "\t\t\t\t// Messages 通过 activePath -> activeSnapshot 遍历树。"),
("\t// Authoritative check once all writers are done: exact count and a single",
 "\t// 所有写入方完成后的权威检查：数量准确，且从 header 到活动叶节点"),
("\t// unbroken id chain from the header to the active leaf.",
 "\t// 是一条完整不间断的 id 链。"),
("\twant := 1 + writers*perWriter // header + appended records",
 "\twant := 1 + writers*perWriter // header + 追加的记录"),
])

# ===== append_rollback_test.go =====
apply("append_rollback_test.go", [
("// A write that fails to reach the JSONL file must not leave the in-memory",
 "// 未能写入 JSONL 文件的操作，不能让内存中的"),
("// append index ahead of the disk: the minted id stays unused, the active leaf",
 "// 追加索引领先于磁盘：生成的 id 保持未使用，活动叶节点"),
("// still points at the last record that was actually written, and the next",
 "// 仍指向实际写入的最后一条记录，且下一次"),
("// append can reuse that id and continue the chain without a gap.",
 "// 追加可以复用该 id 并无间隙地延续链条。"),
("\t// Warm the cached append state with one successful append.",
 "\t// 用一次成功的追加预热缓存的追加状态。"),
("\t// On platforms that keep an append fd open, closing it without clearing the",
 "\t// 在保持追加 fd 打开的平台 上，关闭它而不清除"),
("\t// cached stat snapshot keeps the state \"valid\" so the failure happens in",
 "\t// 缓存的 stat 快照会让状态保持\"有效\"，使失败发生在"),
("\t// writeAppend (not in the earlier full-read rebuild). chmod alone would not",
 "\t// writeAppend 中（而不是更早的全量读取重建中）。仅 chmod 不会"),
("\t// stop writes through a handle opened before the file became read-only.",
 "\t// 阻止通过文件变为只读前已打开的句柄写入。"),
("\t// Restore write access. The next append must succeed, reuse the id minted",
 "\t// 恢复写权限。下一次追加必须成功，复用失败追加所生成的"),
("\t// by the failed append (no gap), and link to the same unchanged leaf.",
 "\t// id（无间隙），并链接到同一个未改变的叶节点。"),
])

# ===== checkpoint_test.go =====
apply("checkpoint_test.go", [
("// TestUndoRefusesWhenAnyManifestFileAdvanced covers the git-less equivalent of",
 "// TestUndoRefusesWhenAnyManifestFileAdvanced 覆盖无 git 环境下等价于"),
("// \"HEAD moved past the checkpoint base\": a later change to any manifest file",
 "// \"HEAD 移过了检查点基线\"的情形：清单中任何文件在之后发生更改，"),
("// aborts the whole undo, and no file -- including untouched ones -- is written.",
 "// 都会中止整个撤销，且不会写入任何文件——包括未被触碰的文件。"),
("\t// A user commit advances only b.txt after the checkpoint.",
 "\t// 用户的提交在检查点之后只推进了 b.txt。"),
("// TestUndoRestoresWhenFilesUnchangedAndRechecksEachWrite verifies the normal",
 "// TestUndoRestoresWhenFilesUnchangedAndRechecksEachWrite 验证正常路径"),
("// path still restores every manifest file and that the CAS recheck runs once",
 "// 仍会还原每个清单文件，且 CAS 复查对每个"),
("// per restored file.", "// 被还原的文件只运行一次。"),
("// TestUndoCASRefusesConcurrentChangeBetweenValidationAndWrite injects an edit",
 "// TestUndoCASRefusesConcurrentChangeBetweenValidationAndWrite 在校验与首次写入"),
("// in the window between validation and the first write, and asserts the CAS",
 "// 之间注入一次编辑，并断言 CAS 复查会在"),
("// recheck aborts before clobbering concurrent work.",
 "// 覆盖并发工作之前中止。"),
("// TestUndoIsTransactionalAcrossFiles pins the all-or-nothing guarantee when a",
 "// TestUndoIsTransactionalAcrossFiles 固化当较早文件已经写入、"),
("// CAS recheck fails after earlier files were already written. Before the fix,",
 "// 而 CAS 复查失败时的全有或全无保证。修复之前，"),
("// a conflict on a later file left earlier files half-reverted even though the",
 "// 后续文件上的冲突会让较早的文件处于半还原状态，即使"),
("// call reported an error and no files were reported as restored.",
 "// 调用报告了错误且没有任何文件被报告为已还原。"),
("\t\tconflictAt string // file sabotaged by the before-write hook; \"\" = none",
 "\t\tconflictAt string // 被写前钩子破坏的文件；\"\" 表示无"),
("// TestUndoRollsBackCreatedAndDeletedFiles covers the non-overwrite action kinds:",
 "// TestUndoRollsBackCreatedAndDeletedFiles 覆盖非覆盖类的操作："),
("// a conflict on a later file must also undo the earlier removal and recreation.",
 "// 后续文件上的冲突也必须撤销较早的删除和重建。"),
("\t// The manifest sorts paths: a.txt (modify), del.txt (recreate), new.txt",
 "\t// 清单按路径排序：a.txt（修改）、del.txt（重建）、new.txt"),
("\t// (remove). Sabotaging new.txt forces rollback of the first two actions.",
 "\t// （删除）。破坏 new.txt 会强制回滚前两个操作。"),
("// TestVerifyRestoreTargetRejectsDivergence covers every branch of the CAS",
 "// TestVerifyRestoreTargetRejectsDivergence 覆盖 CAS 辅助函数的每个分支："),
("// helper: files the turn created, modified, or deleted, and a concurrent change",
 "// 轮次创建、修改或删除的文件，以及对其中每种状态的"),
("// to each of those states.", "// 并发更改。"),
("// countingCheckpointReader counts file reads while delegating to os.ReadFile.",
 "// countingCheckpointReader 在委托给 os.ReadFile 的同时统计文件读取次数。"),
("// It is injected via captureCheckpoint so tests can observe cache hits directly.",
 "// 它通过 captureCheckpoint 注入，使测试可以直接观察缓存命中。"),
("// TestCaptureCheckpointHashCacheReusesUnchangedFiles asserts the core win: a",
 "// TestCaptureCheckpointHashCacheReusesUnchangedFiles 断言核心收益："),
("// second checkpoint over an unchanged file serves hash and bytes from the",
 "// 对未更改文件的第二次检查点从缓存提供哈希和字节，"),
("// cache without re-reading, and the result is identical to the uncached path.",
 "// 无需重新读取，且结果与无缓存路径完全相同。"),
("// TestCaptureCheckpointHashCacheRecomputesAfterChange asserts a changed file",
 "// TestCaptureCheckpointHashCacheRecomputesAfterChange 断言更改后的文件"),
("// gets a new stat-metadata key, so the stale hash is never served.",
 "// 会得到新的 stat 元数据键，因此过期哈希永不会被提供。"),
("\t// Force a distinct mtime so the metadata key is guaranteed to differ.",
 "\t// 强制一个不同的 mtime，确保元数据键一定不同。"),
("// TestCaptureCheckpointHashCacheConcurrentCalls exercises the cache from many",
 "// TestCaptureCheckpointHashCacheConcurrentCalls 让许多 goroutine 同时使用缓存，"),
("// goroutines at once and asserts every caller sees a correct, complete manifest.",
 "// 并断言每个调用方都能看到正确、完整的清单。"),
("\t// Concurrent misses may duplicate reads, but never more than one per file",
 "\t// 并发未命中可能重复读取，但每个调用方对每个文件"),
("\t// per caller, and at least the three distinct files must have been read.",
 "\t// 最多读取一次，且三个不同文件至少都被读取过。"),
("// TestClearAndInvalidateCheckpointHashCache covers both invalidation entry",
 "// TestClearAndInvalidateCheckpointHashCache 覆盖两个失效入口："),
("// points: targeted invalidation re-reads only the named file, while a full",
 "// 定向失效只重新读取指定文件，而完全"),
("// clear forces every file to be read again.", "// 清空会强制重新读取每个文件。"),
("\t// Invalidation on an unknown or unsafe path is a harmless no-op.",
 "\t// 对未知或不安全路径的失效操作是无害的空操作。"),
("// TestCaptureCheckpointHashCacheIsolatedPerWorkspace guards cross-workspace",
 "// TestCaptureCheckpointHashCacheIsolatedPerWorkspace 守护跨工作区"),
("// correctness: identical relative paths with different content must not share",
 "// 正确性：内容不同但相对路径相同的条目不得共享"),
("// a cache entry.", "// 同一个缓存条目。"),
])
