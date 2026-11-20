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

# ===== durable_test.go =====
apply("durable_test.go", [
("\t// Progress is a durability record and must not become model history.",
 "\t// Progress 是持久性记录，绝不能变成模型历史。"),
("\t// Committing that turn's assistant message makes its progress obsolete.",
 "\t// 提交该轮次的 assistant 消息会使其进度过时。"),
("\t// A checkpoint for the next turn is valid again.",
 "\t// 下一轮次的检查点重新有效。"),
("\t// The no-argument form is a durability barrier and must not add records.",
 "\t// 无参数形式是持久性屏障，绝不能添加记录。"),
("\t// Simulate a crash: drop every in-memory handle without a clean shutdown",
 "\t// 模拟崩溃：不执行干净关闭就丢弃所有内存句柄，"),
("\t// and rebuild the session from the transcript on disk.",
 "\t// 并从磁盘上的转录重建会话。"),
])

# ===== export_test.go =====
apply("export_test.go", [
("// exportDirSession writes records to <dir>/<id>.jsonl and returns both, so the",
 "// exportDirSession 把记录写入 <dir>/<id>.jsonl 并返回两者，使测试"),
("// tests exercise the directory-based ExportProject entry point.",
 "// 能覆盖基于目录的 ExportProject 入口。"),
("// Sanitizing only rewrites text fields, so numeric payload values must survive",
 "// 脱敏只重写文本字段，因此数字载荷值必须"),
("// byte-for-byte. Decoding into any would round integers above 2^53 through",
 "// 逐字节保留。解码到 any 会把超过 2^53 的整数经"),
("// float64 and silently corrupt ids/counters in an otherwise faithful export.",
 "// float64 舍入，从而在本应忠实的导出中静默破坏 id/计数器。"),
("\tconst big = \"9007199254740993\" // 2^53+1, not exactly representable as float64",
 "\tconst big = \"9007199254740993\" // 2^53+1，无法用 float64 精确表示"),
("\t\t{branch: \"\", want: \"branch B\", absent: \"branch A\"}, // active leaf",
 "\t\t{branch: \"\", want: \"branch B\", absent: \"branch A\"}, // 活动叶节点"),
("\t\t\t// JSON must stay parsable: the marker is a structured record, not",
 "\t\t\t// JSON 必须保持可解析：标记是一条结构化记录，而不是"),
("\t\t\t// bare text appended after the closing bracket.",
 "\t\t\t// 追加在右方括号之后的裸文本。"),
("// A truncated JSON transcript must remain a valid, unmarshalable JSON array,",
 "// 被截断的 JSON 转录必须仍是有效、可反序列化的 JSON 数组，"),
("// with the truncation explanation carried by a final structured record.",
 "// 截断说明由最后一条结构化记录携带。"),
("// CJK payloads are multi-byte per rune: truncation at entry boundaries must",
 "// 中文载荷每符文多字节：在条目边界截断时绝不能"),
("// never cut a rune, and the JSON artifact must still parse and show the note.",
 "// 切断符文，且 JSON 产物仍必须可解析并显示说明。"),
("\thuge := strings.Repeat(\"漢字\", maxExportBytes) // 3 bytes per rune, well over 2 MiB",
 "\thuge := strings.Repeat(\"漢字\", maxExportBytes) // 每符文 3 字节，远超 2 MiB"),
("// Below the cap the JSON export must be byte-for-byte identical to the plain",
 "// 在上限之下，JSON 导出必须与普通渲染器逐字节一致"),
("// renderer (no marker added, CJK kept as raw UTF-8 rather than escaped).",
 "// （不添加标记，中文保留为原始 UTF-8 而非转义）。"),
])

# ===== fork_title_test.go =====
apply("fork_title_test.go", [
("// TestForkInheritsNewestTitleWhenOlderTitleIsOnPath covers a real fork bug:",
 "// TestForkInheritsNewestTitleWhenOlderTitleIsOnPath 覆盖一个真实的分叉 bug："),
("// the fork point can sit after an older title record but before a newer title",
 "// 分叉点可能位于较旧标题记录之后、较新标题覆盖之前。"),
("// override. Checking only whether *any* title record is on the copied path",
 "// 只检查复制路径上是否*存在*标题记录，"),
("// made the fork inherit the stale older title instead of the title the source",
 "// 会让分叉继承过时的旧标题，而不是源会话在分叉时"),
("// session actually showed when the fork was taken.",
 "// 实际显示的标题。"),
("\t// Freeze the fork point before the newer override lands. The copied path",
 "\t// 在较新覆盖生效之前固定分叉点。复制的路径"),
("\t// still contains the \"First\" title record.",
 "\t// 仍包含 \"First\" 标题记录。"),
("\t// The fork's newest title record must be the carried-over title, not the",
 "\t// 分叉中最新的标题记录必须是带过来的标题，而不是"),
("\t// stale one already on the copied path.",
 "\t// 复制路径上已有的过时标题。"),
("// TestForkCarriesDerivedTitleWhenOverrideClearedAfterForkPoint is the mirror",
 "// TestForkCarriesDerivedTitleWhenOverrideClearedAfterForkPoint 是过时标题情形的"),
("// image of the stale-title case: clearing an override after the fork point",
 "// 镜像情形：在分叉点之后清除覆盖，"),
("// makes the source fall back to its prompt-derived title, and the fork must",
 "// 使源回退到从 prompt 派生的标题，而分叉必须"),
("// show that derived title rather than the now-cleared explicit one.",
 "// 显示该派生标题，而不是已被清除的显式标题。"),
("// TestForkDoesNotDuplicateTitleWhenSourceTitleUnchanged guards the common",
 "// TestForkDoesNotDuplicateTitleWhenSourceTitleUnchanged 守护常见路径："),
("// path: when the copied path already shows the source's effective title, no",
 "// 当复制路径已显示源的有效标题时，不会"),
("// extra title record is appended and the fork is a faithful copy.",
 "// 追加额外的标题记录，分叉是忠实的副本。"),
])

# ===== import_test.go =====
apply("import_test.go", [
("// The assertions below intentionally inspect Entry's public shape rather than",
 "// 下面的断言刻意检查 Entry 的公开形态，而不是"),
("// the importers' private helpers, so they describe the import contract without",
 "// 导入器的私有辅助函数，因此它们描述导入契约，而不"),
("// depending on how the Data payload is organized internally.",
 "// 依赖 Data 载荷在内部如何组织。"),
])
