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

# ===== recovery_plan_test.go =====
apply("recovery_plan_test.go", [
("// recoveryLink attaches a stable id/parent pair to an entry so parent-chain",
 "// recoveryLink 给条目附加稳定的 id/parent 对，使 parent 链检查"),
("// checks have something to compare.", "// 有可比较的对象。"),
("// recoveryCall builds an assistant entry that declares one or more tool calls.",
 "// recoveryCall 构建声明一个或多个工具调用的 assistant 条目。"),
("// recoveryResult builds a real (non-synthetic) tool result entry.",
 "// recoveryResult 构建真实的（非合成）工具结果条目。"),
("\t// The tool batch finished but the final assistant answer was never written.",
 "\t// 工具批次已完成，但最终的 assistant 应答从未写入。"),
("\t// The same call id is declared twice (a retry) and two results refer to a",
 "\t// 同一个调用 id 被声明了两次（重试），两个结果指向一个"),
("\t// call that never existed. Both lists must be deduplicated and stable.",
 "\t// 从未存在的调用。两个列表都必须去重且稳定。"),
("\t// The kept suffix after compaction points at pre-compaction parents, not",
 "\t// 压缩后保留的后缀指向压缩前的 parent，而不是"),
("\t// at the compaction summary that now precedes it. That boundary is not a",
 "\t// 现在位于其前的压缩摘要。这个边界不是"),
("\t// broken link.", "// 断开的链接。"),
])

# ===== recovery_fuzz_test.go =====
apply("recovery_fuzz_test.go", [
("// FuzzSessionRecordRoundtrip drives arbitrary payloads through AppendRecord and",
 "// FuzzSessionRecordRoundtrip 把任意载荷送入 AppendRecord，"),
("// then through the lenient read/repair path used on resume. The properties",
 "// 再送入恢复时使用的宽松读取/修复路径。检查的属性有："),
("// checked are: no panic on any input, every line that survives repair is a",
 "// 任何输入都不 panic、修复后幸存的每一行都是"),
("// well-formed JSON record, and the known-good sentinel record is never lost to",
 "// 格式良好的 JSON 记录，且已知的哨兵记录绝不会因"),
("// a torn or malformed tail. Hard corruption is an accepted (non-panicking)",
 "// 残缺或格式错误的尾部而丢失。严重损坏是可接受的（不 panic）"),
("// outcome and is reported as such by ValidateAndRepair/OpenSession.",
 "// 结果，并由 ValidateAndRepair/OpenSession 如实报告。"),
("\t// Well-formed object payload, clean tail.", "\t// 格式良好的对象载荷，干净的尾部。"),
("\t// Truncated payload and torn final line: payload falls back to a JSON",
 "\t// 截断的载荷和残缺的最后一行：载荷回退为 JSON"),
("\t// string, tail is dropped by torn-tail repair.", "\t// 字符串，尾部由残缺尾部修复丢弃。"),
("\t// Valid non-object JSON payload plus a bare newline tail.",
 "\t// 有效的非对象 JSON 载荷加上一个裸换行尾部。"),
("\t// Nested payload and a valid extra record appended as raw JSON.",
 "\t// 嵌套载荷以及作为原始 JSON 追加的一条有效额外记录。"),
("\t// Empty payload (stored as null) and non-JSON garbage tail.",
 "\t// 空载荷（存为 null）和非 JSON 垃圾尾部。"),
("\t// Combining marks and a structurally valid extra tail record.",
 "\t// 组合字符和一条结构上有效的额外尾部记录。"),
("\t// A syntax error on a non-final physical line is hard corruption.",
 "\t// 非最后一条物理行上的语法错误属于严重损坏。"),
("\t// A schema error (kind is not a string) is hard corruption.",
 "\t// schema 错误（kind 不是字符串）属于严重损坏。"),
("\t// Keep each fuzz case small and bounded; the seed corpus already stays well",
 "\t// 让每个 fuzz 用例保持小且有界；种子语料本身已远"),
("\t// under this limit.", "\t// 低于这个限制。"),
("\t// A deterministic short prefix exercises the multi-record append/rebuild",
 "\t// 确定性的短前缀同时覆盖多记录追加/重建路径"),
("\t// path as well as the single fuzzed payload.", "\t// 和单个 fuzz 载荷。"),
("\t// Feed arbitrary bytes through AppendRecord. Invalid JSON may be rejected",
 "\t// 把任意字节送入 AppendRecord。无效 JSON 可能被拒绝"),
("\t// (and must be rolled back cleanly); the only hard requirement is no panic.",
 "\t// （且必须干净回滚）；唯一的硬性要求是不 panic。"),
("\t// This record is our witness that lenient repair never discards valid",
 "\t// 这条记录是我们的见证：宽松修复在处理损坏尾部时"),
("\t// history while coping with a damaged tail.", "\t// 绝不会丢弃有效历史。"),
("\t// Validation must classify every line without panicking. Only hard",
 "\t// 校验必须对每一行分类而不 panic。只有严重"),
("\t// corruption is an error; a torn final line is reported, not rejected.",
 "\t// 损坏才算错误；残缺的最后一行只报告而不拒绝。"),
("\treturn // hard corruption is an accepted, non-panicking outcome",
 "\treturn // 严重损坏是可接受的、不 panic 的结果"),
("\t// After repair every surviving physical line is a well-formed record.",
 "\t// 修复后每一条幸存的物理行都是格式良好的记录。"),
("\t\t// A record may legitimately omit \"data\" entirely (decodeLine only",
 "\t\t// 记录合法地可以完全省略 \"data\"（decodeLine 只"),
("\t\t// requires a non-empty kind); when present it must be valid JSON.",
 "\t\t// 要求 kind 非空）；存在时必须是有效 JSON。"),
("// TestFuzzSessionRecordRoundtripSeeds runs the fuzz seed corpus as an ordinary",
 "// TestFuzzSessionRecordRoundtripSeeds 把 fuzz 种子语料作为普通的"),
("// deterministic test so the regression cases are always covered by go test.",
 "// 确定性测试运行，使回归用例始终被 go test 覆盖。"),
])

# ===== repair_test.go =====
apply("repair_test.go", [
("\t// KeptLines counts physical transcript lines; the read-time schema header",
 "\t// KeptLines 统计物理转录行数；由 Records() 添加的"),
("\t// added by Records() is not one of them.", "\t// 读取时 schema 头不在其中。"),
("\t// Listing-style validation never writes, even for a missing newline.",
 "\t// 列表式校验从不写入，即使缺少换行符也是如此。"),
("// TestValidateAndRepairIsIdempotentWithoutValidPrefix covers a torn first line",
 "// TestValidateAndRepairIsIdempotentWithoutValidPrefix 覆盖首行残缺"),
("// (or an empty/blank file): repairing must not publish a bare \"\\n\", which",
 "// （或空/空白文件）的情形：修复不得发布一个裸 \"\\n\"，因为"),
("// decodeLine classifies as yet another torn tail. A repaired transcript must be",
 "// decodeLine 会把它归类为又一处残缺尾部。修复后的转录必须"),
("// clean on the next validation, otherwise every resume rewrites the file again.",
 "// 在下次校验时是干净的，否则每次恢复都会再次重写文件。"),
("// TestValidateAndRepairKeepsValidPrefixBeforeTornTail makes sure the fix for a",
 "// TestValidateAndRepairKeepsValidPrefixBeforeTornTail 确保对首行残缺的修复"),
("// torn first line does not strip the terminating newline when a real prefix",
 "// 在确有有效前缀存活时不会剥离结束换行符："),
("// survives: the kept records must stay byte-identical and terminate cleanly.",
 "// 保留的记录必须保持逐字节一致并干净地结束。"),
("\t// Directory Sync returns EINVAL on Windows; it must not fail publication.",
 "\t// 目录 Sync 在 Windows 上返回 EINVAL；它不能让发布失败。"),
])
