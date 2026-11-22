package session

import (
	"strconv"
	"time"
)

// KindForkLineage 是普通分叉血缘记录的 kind。它是审计元数据，不会被
// messageEntries 投影为模型可见消息，因此不影响对话语义。
const KindForkLineage = "fork_lineage"

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
// 它是可选 API：当前没有与 Session.Fork 接线。Fork 逐字复制到分叉点的记录，
// 不追加任何记录；若在 Fork 内写入本载荷，会改变分叉会话的默认字节，违反
// "默认字节不变"约定，因此这里只保留纯函数供未来显式接线层使用。
// 检查点重试产生的分叉血缘已经由 RetryFromCheckpoint 通过
// Attempt.SourceSession / SourceRecord 记录（见 checkpoint.go），无需再引入
// 重复的血缘载荷类型。
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

// AppendForkLineage 在分叉出的新会话里追加一条血缘记录，标明它从哪个会话的
// 哪条记录分叉而来。显式调用时才写入，因此普通 Fork 的字节保持不变。
// 记录是审计元数据，不进入模型可见消息；写入失败仅返回错误，不修改入参。
func (s *Session) AppendForkLineage(parentID, atRecord string) error {
	_, err := s.AppendRecord(KindForkLineage, forkLineagePayload(parentID, atRecord, time.Now().UnixMilli()))
	return err
}
