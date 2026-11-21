package session

import "strconv"

// 修订注记载荷使用的稳定键。键名固定为英文，保证跨版本、跨语言的转录
// 读取方都能用同一组字段识别这是一条"谁在何时改了什么"的审计记录：
// kind 表示修订类型，actor 表示执行者，ts 表示发生时刻。
// 值统一为普通 Go 字符串，便于直接写进 JSONL 的一行记录。
const (
	revisionKindKey  = "kind"
	revisionActorKey = "actor"
	revisionTsKey    = "ts"
)

// revisionNote 构建可写入 JSONL 的"修订注记"载荷，用于在会话记录里留下
// 编辑/压缩/撤销的审计轨迹，方便之后按 kind 与 ts 追溯每次改动。
//
// 它是纯函数：不读取全局状态、不修改入参，每次调用都返回全新的 map。
// 只包含非空字段：kind/actor 为空、ts 为 0 时都不写入，因此三个值都为空时
// 得到非 nil 的空 map（编码为 `{}` 而不是 `null`），让调用方可以无条件地
// 把它交给 Append。ts 用十进制字符串保存，负数（1970 年以前）原样保留。
//
// 键名固定且由 encoding/json 按字典序输出，所以相同输入总会得到相同的字节；
// actor 等值是普通 Go 字符串，CJK 文本会原样写成 UTF-8，不会被转义成 \uXXXX 序列。
func revisionNote(kind, actor string, ts int64) map[string]string {
	payload := make(map[string]string, 3)
	if kind != "" {
		payload[revisionKindKey] = kind
	}
	if actor != "" {
		payload[revisionActorKey] = actor
	}
	if ts != 0 {
		payload[revisionTsKey] = strconv.FormatInt(ts, 10)
	}
	return payload
}
