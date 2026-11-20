package agent

import (
	"strconv"
	"sync/atomic"
)

// partialRecordKind 是部分文本落盘时使用的记录类型。session 的记录类型是开放的：
// 未知类型会被逐字节保留、但不会进入模型可见的会话视图，因此把残文写进去既能在 resume
// 时被读到，又不会被当成一条消息注入后续请求。
const partialRecordKind = "partial"

// 落盘 payload 的键名。保持固定，让读取方无需依赖 agent 的结构体即可取字段。
const (
	partialFieldText       = "text"
	partialFieldStopReason = "stop_reason"
	partialFieldModel      = "model"
	partialFieldTurn       = "turn"
	partialFieldTruncated  = "truncated"
)

// partialRecordPayload 把失败/取消边界的部分文本快照转换成可落盘的键值表。
// 所有值都是字符串：turn 与 truncated 分别用十进制与布尔文本表达；文本原样保留，
// 由 encoding/json 负责 CJK/emoji 的编码，这里不做任何按字节的切分。
func partialRecordPayload(snap partialSnapshot) map[string]string {
	return map[string]string{
		partialFieldText:       snap.Text,
		partialFieldStopReason: snap.StopReason,
		partialFieldModel:      snap.Model,
		partialFieldTurn:       strconv.Itoa(snap.Turn),
		partialFieldTruncated:  strconv.FormatBool(snap.Truncated),
	}
}

// recordPartialEnabled 控制 emitPartial 是否把部分文本一并写入 Recorder。用 atomic.Bool
// 而不是普通 bool，保证并发运行时切换开关与 emitPartial 之间没有数据竞争。
var recordPartialEnabled atomic.Bool

// SetPartialRecordPersistence 打开或关闭“部分文本落盘”能力，默认关闭。关闭时失败/取消
// 仍只发 EventPartial，不写任何额外记录，既有行为逐字节不变。这是进程级开关：它在 Runner
// 构造之外生效，适合由 app 层在启动时统一开启。
func SetPartialRecordPersistence(enabled bool) {
	recordPartialEnabled.Store(enabled)
}

// partialRecordPersistenceEnabled 报告当前是否启用了部分文本落盘。
func partialRecordPersistenceEnabled() bool {
	return recordPartialEnabled.Load()
}
