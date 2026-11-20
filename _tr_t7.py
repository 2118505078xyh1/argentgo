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

# ===== retention_test.go =====
apply("retention_test.go", [
("// markedRecord builds a message record whose Data carries the retention hint.",
 "// markedRecord 构建一条 Data 中带有保留提示的消息记录。"),
("// plainRecord builds a message record with no retention metadata at all.",
 "// plainRecord 构建一条完全没有保留元数据的消息记录。"),
("// normalizedObject decodes a JSON object for semantic comparison, ignoring the",
 "// normalizedObject 解码 JSON 对象以进行语义比较，忽略"),
("// reserved retention key and any insignificant formatting differences.",
 "// 保留的保留策略键以及任何无关紧要的格式差异。"),
("\t// The hint adds only a small field; the payload is not copied or truncated.",
 "\t// 该提示只增加一个很小的字段；载荷不会被复制或截断。"),
])

# ===== retention_write_test.go =====
apply("retention_write_test.go", [
("// TestAppendWithRetentionExplicitHintBeatsKindDefault covers the case where a",
 "// TestAppendWithRetentionExplicitHintBeatsKindDefault 覆盖调用方请求的提示"),
("// caller asks for a hint that differs from the kind's automatic default. The",
 "// 与 kind 的自动默认值不同的情形。显式值必须能"),
("// explicit value must survive AppendRecord's StampIfImportant step; otherwise",
 "// 经受住 AppendRecord 的 StampIfImportant 步骤；否则"),
("// e.g. a summary marked in_thread silently becomes always and leaks into child",
 "// 例如标记为 in_thread 的摘要会静默变成 always，并违背调用方意图"),
("// sessions against the caller's intent.", "// 泄漏到子会话中。"),
("\t\t{KindSummary, RetainInChildren},  // automatic default: RetainAlways",
 "\t\t{KindSummary, RetainInChildren},  // 自动默认值：RetainAlways"),
("\t\t{KindCheckpoint, RetainInThread}, // automatic default: RetainAlways",
 "\t\t{KindCheckpoint, RetainInThread}, // 自动默认值：RetainAlways"),
("\t\t{KindTitle, RetainAlways},        // automatic default: RetainInChildren",
 "\t\t{KindTitle, RetainAlways},        // 自动默认值：RetainInChildren"),
("\t// An important kind with a non-object payload keeps its bytes: a hint must",
 "\t// 载荷非对象的重要 kind 保留其字节：提示绝不能"),
("\t// never cost the payload itself.", "\t// 以牺牲载荷本身为代价。"),
("\t\t// The compaction summary also quotes the dropped text; only count",
 "\t\t// 压缩摘要也会引用被丢弃的文本；只统计"),
("\t\t// conversation records, not the summary record itself.",
 "\t\t// 对话记录，不统计摘要记录本身。"),
("\t\t// entryKind maps a \"user\" role back to KindUser when the compactor",
 "\t\t// 压缩器重新发出带标记的记录时，entryKind 会把 \"user\" 角色"),
("\t\t// re-emits the flagged record, so the restored copy has that kind.",
 "\t\t// 映射回 KindUser，因此恢复的副本具有该 kind。"),
])

# ===== schema_test.go =====
apply("schema_test.go", [
("// TestMigrateRecordsLegacyV0AddsHeader: a transcript with no schema header is",
 "// TestMigrateRecordsLegacyV0AddsHeader：没有 schema 头的转录是 v0，"),
("// v0 and gains exactly one header on top; nothing else changes.",
 "// 升级时只在顶部增加一个头；其他内容不变。"),
("// TestMigrateRecordsExplicitV1Unchanged: a transcript already at the current",
 "// TestMigrateRecordsExplicitV1Unchanged：已处于当前版本的转录"),
("// version passes through byte-for-byte.", "// 逐字节原样通过。"),
("// TestMigrateRecordsIdempotent: migrating twice yields the same result as once,",
 "// TestMigrateRecordsIdempotent：迁移两次与迁移一次结果相同，"),
("// for both a legacy (v0) and an already-current (v1) input.",
 "// 无论输入是旧版（v0）还是已处于当前版本（v1）。"),
("// TestMigrateRecordsV0HeaderUpdatedInPlace: an explicit v0 header is bumped in",
 "// TestMigrateRecordsV0HeaderUpdatedInPlace：显式的 v0 头被原地提升，"),
("// place rather than duplicated.", "// 而不是被复制。"),
("// TestMigrateRecordsNewerVersionRejected: a header from a newer build must be a",
 "// TestMigrateRecordsNewerVersionRejected：来自更新版本的头必须产生"),
("// clear error, never a silent downgrade.", "// 明确的错误，绝不能静默降级。"),
("// TestMigrateRecordsEmpty: an empty transcript is a no-op.",
 "// TestMigrateRecordsEmpty：空转录是空操作。"),
("// TestMigrateRecordsPreservesUnknownKinds: kinds are open; an unrecognised kind",
 "// TestMigrateRecordsPreservesUnknownKinds：kind 是开放的；无法识别的 kind"),
("// survives migration untouched.", "// 在迁移中保持原样。"),
("// TestMigrateRecordsCJK: non-ASCII payloads are carried through unchanged.",
 "// TestMigrateRecordsCJK：非 ASCII 载荷原样通过。"),
("// TestMigrateRecordsDoesNotMutateInput: the caller's slice is never modified.",
 "// TestMigrateRecordsDoesNotMutateInput：调用方的切片永不被修改。"),
("// TestMigrateRecordsMalformedHeader: a broken header is reported, not silently",
 "// TestMigrateRecordsMalformedHeader：损坏的头会被报告，而不是"),
("// treated as v0.", "// 被静默当作 v0。"),
("// TestMigrateRecordsConflictingHeaders: two headers disagreeing on the version",
 "// TestMigrateRecordsConflictingHeaders：两个版本不一致的头会被报告，"),
("// are reported rather than guessed at.", "// 而不是靠猜测处理。"),
])

# ===== schema_read_test.go =====
apply("schema_read_test.go", [
("// This file wires schema migration into the shared read path and guards the",
 "// 本文件把 schema 迁移接入共享读取路径，并端到端守护该行为："),
("// behaviour end to end: legacy transcripts gain a header on read, explicit",
 "// 旧转录在读取时获得一个头，显式版本被尊重，"),
("// versions are respected, and a newer version is rejected rather than guessed",
 "// 而更新的版本被拒绝而不是靠猜测"),
("// at.", "// 处理。"),
("// withoutSchemaHeader drops a leading, read-time schema header so the existing",
 "// withoutSchemaHeader 去掉开头的读取时 schema 头，使现有的"),
("// tree/chain tests can keep asserting on the transcript's own records. The",
 "// 树/链测试仍能对转录自身的记录进行断言。注入的头是元数据，"),
("// injected header is metadata with no parent and an empty id; it never joins",
 "// 没有 parent 且 id 为空；它永远不会加入"),
("// the id/parent chain.", "// id/parent 链。"),
("// withoutSchemaBranches is the Branch counterpart of withoutSchemaHeader.",
 "// withoutSchemaBranches 是 withoutSchemaHeader 的 Branch 对应版本。"),
("// TestReadRecordsLegacyV0GainsHeader: a v0 transcript (no schema header) read",
 "// TestReadRecordsLegacyV0GainsHeader：通过共享读取器读取的 v0 转录"),
("// through the shared reader comes back upgraded to v1, with the original",
 "// （没有 schema 头）会升级到 v1 返回，原始记录"),
("// records preserved after the injected metadata header.",
 "// 保留在注入的元数据头之后。"),
("\t// The injected header is metadata with an empty id: it never enters the",
 "\t// 注入的头是 id 为空的元数据：它永远不会进入"),
("\t// parent chain and must not consume an id slot used by appends.",
 "\t// parent 链，也不能占用追加所使用的 id 槽位。"),
("// TestReadRecordsExplicitV1Unchanged: a transcript that already carries an",
 "// TestReadRecordsExplicitV1Unchanged：已经带有显式 v1 schema 头的转录"),
("// explicit v1 schema header is returned unchanged (no duplicate header).",
 "// 原样返回（不产生重复头）。"),
("// TestReadRecordsNewerVersionErrors: a transcript from a newer build is a",
 "// TestReadRecordsNewerVersionErrors：来自更新构建的转录是明确的错误，"),
("// clear error, not a silent downgrade. readRecords must propagate ErrNewerSchemaVersion.",
 "// 而不是静默降级。readRecords 必须传播 ErrNewerSchemaVersion。"),
("// TestMigratedSessionBranchesAndMessages: after a v0 transcript is migrated on",
 "// TestMigratedSessionBranchesAndMessages：v0 转录在读取时迁移后，"),
("// read, the conversation view is unaffected — messages follow the real parent",
 "// 对话视图不受影响——消息跟随真实的 parent"),
("// links and the schema header never appears as a message.",
 "// 链接，schema 头永远不会作为消息出现。"),
("\t// The injected schema header is metadata: it shows up as a branch but never",
 "\t// 注入的 schema 头是元数据：它作为分支出现，但永远不会"),
("\t// as a message, and the real records keep their ids/parents/depths.",
 "\t// 作为消息出现，真实记录保留其 id/parent/depth。"),
("\t// Messages walks from the active leaf and skips metadata records.",
 "\t// Messages 从活动叶节点遍历并跳过元数据记录。"),
])
