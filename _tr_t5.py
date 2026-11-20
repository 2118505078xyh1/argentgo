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

# ===== reasoning_test.go =====
apply("reasoning_test.go", [
("// TestAssistantReasoningRoundTrip verifies that an assistant record written",
 "// TestAssistantReasoningRoundTrip 验证带推理内容写入的 assistant 记录在"),
("// with reasoning survives a close/reopen and is projected onto Entry, including",
 "// 关闭/重新打开后能存活并投影到 Entry，包括其中"),
("// multi-byte CJK text that must not be cut anywhere along the way.",
 "// 绝不能被切断的多字节中文文本。"),
("// TestReasoningOfAcceptsWireAliases checks both the neutral reasoning key and",
 "// TestReasoningOfAcceptsWireAliases 检查中性的 reasoning 键和"),
("// the OpenAI-compatible reasoning_content key, and that a missing key leaves",
 "// OpenAI 兼容的 reasoning_content 键，以及缺失键会让"),
("// the entry empty so legacy transcripts are unchanged.",
 "// 条目保持为空，使旧转录不变。"),
("// TestLegacyAssistantEntryHasNoReasoning guards the compatibility contract: an",
 "// TestLegacyAssistantEntryHasNoReasoning 守护兼容性契约："),
("// old assistant record without any reasoning key still yields a valid entry",
 "// 没有任何推理键的旧 assistant 记录仍能产生有效条目，"),
("// whose Reasoning is empty.", "// 其 Reasoning 为空。"),
("// TestAssistantRecordOmitsEmptyReasoning ensures a write with no reasoning does",
 "// TestAssistantRecordOmitsEmptyReasoning 确保不带推理的写入"),
("// not introduce a new field into the persisted payload, keeping old bytes.",
 "// 不会在持久化载荷中引入新字段，从而保持旧字节。"),
("// TestEntryJSONReasoningOmittedWhenEmpty checks the Entry archive encoding:",
 "// TestEntryJSONReasoningOmittedWhenEmpty 检查 Entry 归档编码："),
("// empty reasoning is omitted entirely so archives from older builds stay",
 "// 空推理被完全省略，使较早版本生成的归档保持"),
("// byte-identical, and a non-empty value round-trips.",
 "// 逐字节一致，而非空值能正确往返。"),
])

# ===== reconcile_test.go =====
apply("reconcile_test.go", [
("// reconcilerCall builds an assistant Entry whose Data carries the full",
 "// reconcilerCall 构建 assistant Entry，其 Data 携带 agent 循环持久化的"),
("// llm.Message \"tool_calls\" payload shape persisted by the agent loop.",
 "// 完整 llm.Message \"tool_calls\" 载荷形态。"),
("// reconcilerResult builds a tool Entry whose result id is recorded under the",
 "// reconcilerResult 构建工具 Entry，其结果 id 记录在"),
("// key messageEntry maps to ToolCallID (\"id\").",
 "// messageEntry 映射为 ToolCallID 的键（\"id\"）下。"),
("// abortedEntry is the expected synthetic result for a dangling call id.",
 "// abortedEntry 是悬空调用 id 所期望的合成结果。"),
("\t// Input must be untouched.", "\t// 输入必须不被修改。"),
("\t// A tool entry with no call metadata must pass through verbatim.",
 "\t// 没有调用元数据的工具条目必须原样通过。"),
("\t// Nil and empty inputs are unchanged.", "\t// nil 和空输入保持不变。"),
])
