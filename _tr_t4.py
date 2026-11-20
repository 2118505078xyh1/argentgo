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

# ===== ordinal_test.go =====
apply("ordinal_test.go", [
("// stamped builds a message record with an explicit ordinal in its Data envelope.",
 "// stamped 构建一条在 Data 信封中带有显式序号的消息记录。"),
("// unstamped builds a legacy message record with no ordinal metadata at all.",
 "// unstamped 构建一条完全没有序号元数据的旧消息记录。"),
("// normalizedSeqObject decodes a JSON object for semantic comparison, ignoring",
 "// normalizedSeqObject 解码 JSON 对象以进行语义比较，忽略"),
("// the reserved ordinal key and any insignificant formatting differences.",
 "// 保留的序号键以及任何无关紧要的格式差异。"),
("\t// A reserved field from another feature must survive untouched.",
 "\t// 来自其他特性的保留字段必须原样保留。"),
("\t\t[]byte(`{\"_seq\":9223372036854775808}`), // overflows int64",
 "\t\t[]byte(`{\"_seq\":9223372036854775808}`), // 超出 int64 范围"),
("\t// Decode as RawMessage: the reserved _seq is a number, not a string.",
 "\t// 按 RawMessage 解码：保留字段 _seq 是数字，而不是字符串。"),
("\t// All three records were written in the same clock tick: TS cannot order",
 "\t// 三条记录都写入于同一时钟周期：TS 无法排序，"),
("\t// them, so only the ordinal can. Deliberately list the winner in the middle.",
 "\t// 只有序号可以。故意把胜者放在中间。"),
("\trecords[1].Data = data // \"c\" has the highest seq (3)",
 "\trecords[1].Data = data // \"c\" 的 seq 最高（3）"),
("\t// A legacy record with the same ts and a lexically greater id must not win.",
 "\t// ts 相同但字典序 id 更大的旧记录不能胜出。"),
("\t// Equal timestamps fall back to the greater id, matching Store.List.",
 "\t// 时间戳相等时回退到较大的 id，与 Store.List 一致。"),
("\t// Legacy records have no _seq; their array index is the effective ordinal.",
 "\t// 旧记录没有 _seq；它们的数组索引就是有效序号。"),
])

# ===== page_test.go =====
apply("page_test.go", [
("// pageTestRecords builds n records with uniform line lengths so a byte cap",
 "// pageTestRecords 构建 n 条行长度统一的记录，使字节上限"),
("// maps cleanly to a fixed number of records per page. Parent is left empty on",
 "// 能干净地映射为每页固定的记录数。Parent 故意留空："),
("// purpose: pagination is file-order based, and equal-length lines keep the",
 "// 分页基于文件顺序，而等长行能保持"),
("// page-size arithmetic exact.", "// 页大小计算的精确性。"),
("// (a) Walking forward with Next must visit every record exactly once, in",
 "// (a) 用 Next 向前遍历必须按顺序恰好访问每条记录一次，"),
("// order, with no page over the byte cap.", "// 且没有任何页超过字节上限。"),
("\tmaxBytes := 3*lineLen + 1 // exactly three uniform records fit",
 "\tmaxBytes := 3*lineLen + 1 // 恰好容纳三条等长记录"),
("// (b) fromEnd=true returns the last page; feeding Next back walks backwards",
 "// (b) fromEnd=true 返回最后一页；把 Next 传回可向前（往回）遍历，"),
("// until every record has been seen exactly once.", "// 直到每条记录都被恰好看到一次。"),
("// (c) Malformed and blank lines are skipped leniently, in both directions.",
 "// (c) 双向遍历时都宽松地跳过格式错误和空白行。"),
("// (d) A record larger than the whole cap is returned whole and alone; when",
 "// (d) 大于整个上限的记录会整条单独返回；当后面"),
("// more records follow, HasMore lets the caller continue past it.",
 "// 还有更多记录时，HasMore 让调用方可以继续。"),
("// (e) A non-empty afterID resumes strictly after that record for forward",
 "// (e) 非空 afterID 对正向读取严格从该记录之后恢复。"),
("// reads. A cursor past the end yields an empty page.",
 "// 越过末尾的游标返回空页。"),
("\tmaxBytes := 2*lineLen + 1 // two records per page",
 "\tmaxBytes := 2*lineLen + 1 // 每页两条记录"),
("\t// A cursor on the final record has nothing after it.",
 "\t// 位于最后一条记录上的游标之后没有任何内容。"),
("\t// An unknown cursor is lenient: forward reads start from the beginning.",
 "\t// 未知游标是宽松的：正向读取从开头开始。"),
("// (f) An empty (or absent) file yields an empty page without an error.",
 "// (f) 空文件（或不存在的文件）返回空页且不报错。"),
("// Generated IDs must agree with readRecords so a cursor minted from one read",
 "// 生成的 id 必须与 readRecords 一致，使一个读取 API 生成的游标"),
("// API resolves in the other.", "// 能在另一个中解析。"),
("\t// readRecords injects a read-time schema header that ReadPage (a lenient",
 "\t// readRecords 会注入读取时的 schema 头，而 ReadPage（宽松的"),
("\t// raw-file reader) does not; compare the transcript's own records only.",
 "\t// 原始文件读取器）不会；因此只比较转录自身的记录。"),
("// The Session method must delegate to the same file the handle writes to.",
 "// Session 方法必须委托给句柄写入的同一个文件。"),
])
