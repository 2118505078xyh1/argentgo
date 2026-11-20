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

apply("compact_activity_test.go", [
("// activityToolEntry builds a tool result record in the shape the agent writes:",
 "// activityToolEntry 按 agent 写入的形态构建一条工具结果记录："),
("// top-level name plus arguments serialized as a JSON string.",
 "// 顶层 name 加上序列化为 JSON 字符串的 arguments。"),
("// activityAssistantEntry builds an assistant record carrying tool_calls, the",
 "// activityAssistantEntry 构建携带 tool_calls 的 assistant 记录，这是"),
("// other layout a transcript can contain.", "// 转录中可能出现的另一种布局。"),
("\t\t\"read internal/foo.go:10-14\", // offset/limit",
 "\t\t\"read internal/foo.go:10-14\", // offset/limit"),
("\t\t\"read a.txt:18-23\",           // line/before/after",
 "\t\t\"read a.txt:18-23\",           // line/before/after"),
("\t\t\"read b.go:30-40\",            // inline path selector",
 "\t\t\"read b.go:30-40\",            // 内联路径选择器"),
("\t\t\"edit c.go\",                  // edit records the path only",
 "\t\t\"edit c.go\",                  // 编辑只记录路径"),
("\t// An edit has no diff in the recorded data, so it must not invent a range.",
 "\t// 记录的数据中编辑没有 diff，因此不得凭空捏造行范围。"),
("\t\tactivityToolEntry(\"read\", `{\"path\":\"a.go\",\"offset\":1,\"limit\":3}`), // duplicate",
 "\t\tactivityToolEntry(\"read\", `{\"path\":\"a.go\",\"offset\":1,\"limit\":3}`), // 重复"),
("\t\tactivityToolEntry(\"bash\", `{\"command\":\"go build ./...\"}`), // duplicate",
 "\t\tactivityToolEntry(\"bash\", `{\"command\":\"go build ./...\"}`), // 重复"),
("\t\tactivityToolEntry(\"bash\", `{\"cmd\":\"go build ./...\"}`),     // same command, alias key",
 "\t\tactivityToolEntry(\"bash\", `{\"cmd\":\"go build ./...\"}`),     // 同一命令，别名键"),
("\t// \"bash \" prefix plus a command clipped to activityCommandLimit runes.",
 "\t// \"bash \" 前缀加上裁剪到 activityCommandLimit 个符文的命令。"),
])

apply("compact_archive_test.go", [
("// archiveTestEntries returns a mix of plain CJK text and raw JSON payloads so",
 "// archiveTestEntries 返回纯中文文本与原始 JSON 载荷的混合，使"),
("// the round-trip covers both Entry.Content and the original json.RawMessage.",
 "// 往返能同时覆盖 Entry.Content 和原始的 json.RawMessage。"),
("\t// The store-relative path stored in a ref must resolve through dir too.",
 "\t// 引用中存储的相对于 store 的路径也必须能通过 dir 解析。"),
("\t\t// A CJK payload must survive as UTF-8, never as \\uXXXX escapes.",
 "\t\t// 中文载荷必须以 UTF-8 保留，绝不能变成 \\uXXXX 转义。"),
("\t// No file may appear outside the compactions directory.",
 "\t// compactions 目录之外不得出现任何文件。"),
("\t// A trailing .jsonl suffix is accepted and does not duplicate the extension.",
 "\t// 结尾的 .jsonl 后缀会被接受，且不会重复扩展名。"),
("\t// Rewriting the same id must leave exactly one archive, no temp files.",
 "\t// 重写同一 id 必须只留一个归档，没有临时文件。"),
("\t// An encode failure must not leave a temp file behind. Invalid RawMessage",
 "\t// 编码失败不得留下临时文件。无效的 RawMessage"),
("\t// makes json.Encoder fail on that entry.", "// 会让 json.Encoder 在该条目上失败。"),
("\t// The Archive field is additive: absent when nil, present when set.",
 "\t// Archive 字段是附加的：nil 时不存在，设置时存在。"),
])
