package session

import "strconv"

// 分叉血缘载荷使用的稳定键。键名固定为英文下划线形式，保证跨版本、
// 跨语言的转录读取方都能用同一组字段识别这是一条"从哪个会话的哪条记录分叉"的
// 血缘记录：parent_id 表示来源会话的 id，at_record 表示分叉点所在的记录 id，
// ts 表示分叉发生的时刻。值统一为普通 Go 字符串，便于直接写进 JSONL 的一行记录。
const (
	forkLineageParentKey   = "parent_id"
	forkLineageAtRecordKey = "at_record"
	forkLineageTsKey       = "ts"
)

// forkLineagePayload 构建可写入 JSONL 的"分叉血缘"载荷，用于在分叉出的
// 新会话里留下来源标记（从哪个会话、哪条记录分叉而来），方便之后追溯
// 会话树中每个分支的来龙去脉。
//
// 它是纯函数：不读取全局状态、不修改入参，每次调用都返回全新的 map。
// 只包含非空字段：parentID/atRecord 为空、ts 为 0 时都不写入，因此三个值
// 都为空时得到非 nil 的空 map（编码为 `{}` 而不是 `null`），让调用方可以
// 无条件地把它交给 Append。空值判定只针对精确的空字符串，含空白的值按普通
// 值保留；ts 用十进制字符串保存，负数（1970 年以前）原样保留。
//
// 键名固定且由 encoding/json 按字典序输出，所以相同输入总会得到相同的字节；
// parentID/atRecord 等值是普通 Go 字符串，CJK 文本会原样写成 UTF-8，
// 不会被转义成 \uXXXX 序列。
func forkLineagePayload(parentID, atRecord string, ts int64) map[string]string {
	payload := make(map[string]string, 3)
	if parentID != "" {
		payload[forkLineageParentKey] = parentID
	}
	if atRecord != "" {
		payload[forkLineageAtRecordKey] = atRecord
	}
	if ts != 0 {
		payload[forkLineageTsKey] = strconv.FormatInt(ts, 10)
	}
	return payload
}
