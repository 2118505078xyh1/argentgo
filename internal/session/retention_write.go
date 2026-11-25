package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/security"
)

// retentionDataLimit 在 maxLineLen 之下为记录信封和 SetRetention 添加的小 _retention 字段
// 留出余量。超过限制的载荷会被拒绝，而不是生成一条会被读取器视为过大而跳过的 JSONL 行。
const retentionDataLimit = maxLineLen - 8192

// RetentionFromKind 将记录 kind 映射到其默认保留提示。该策略刻意简单且自动，
// 这样调用方不必记得为每条重要记录打标记：
//
//   - checkpoint、summary、compaction：持久的上下文锚点。它们携带恢复或解释一次运行所需的状态，
//     因此在压缩后保留，并植入子会话（RetainAlways）。
//   - title：为子代理提供廉价的定位信息，但不值得固定在父线程中；只传播到子会话（RetainInChildren）。
//   - 其他所有类型（消息、工具、尝试、进度、统计、用量、header、未知 kind）：RetainDefault。
//     读写行为与这些提示出现之前完全一致。
//
// 匹配不区分大小写并去除首尾空白，因此在本包之外定义的 kind 也能得到合理的默认值。
func RetentionFromKind(kind string) Retention {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindCheckpoint, KindSummary, KindCompaction:
		return RetainAlways
	case KindTitle:
		return RetainInChildren
	default:
		return RetainDefault
	}
}

// StampIfImportant 将 RetentionFromKind 应用于已编码的载荷，并返回要写入的字节。
// 默认 kind 的字节原样返回，因此普通消息/工具写入不受影响。如果重要 kind 的载荷不是 JSON 对象，
// SetRetention 会拒绝标记它，并返回原始字节，而不是为了附加提示而牺牲载荷本身。
// 已经带有显式 _retention 提示的载荷原样返回：kind 默认值只是给未做选择的调用方兜底。
func StampIfImportant(kind string, data []byte) []byte {
	r := RetentionFromKind(kind)
	if r == RetainDefault {
		return data
	}
	// 显式提示优先于 kind 的自动默认值。AppendWithRetention 在调用 AppendRecord 前
	// 已打上自己的值，压缩也会重新发出已带提示的记录，因此在这里覆盖它会
	// 悄然改变这两条路径上的保留语义。
	if hasRetentionHint(data) {
		return data
	}
	stamped, err := SetRetention(data, r)
	if err != nil {
		return data
	}
	return stamped
}

// AppendWithRetention 写入一条带有显式保留提示的预序列化记录。载荷会先被限制大小
// 并通过共享的 JSON 脱敏器，然后打上标记，最后经 AppendRecord 写入，
// 因此 id、分支和持久性与任何其他追加完全一致。
//
// data 必须是 JSON 对象信封（或为空/null）：SetRetention 会拒绝其他形状，
// 而不是重写一个它无法安全保留的载荷。
func (s *Session) AppendWithRetention(kind string, data []byte, r Retention) (Record, error) {
	if len(data) > retentionDataLimit {
		return Record{}, fmt.Errorf("session: retention payload too large: %d bytes (limit %d)", len(data), retentionDataLimit)
	}
	cleaned := data
	if len(bytes.TrimSpace(data)) > 0 {
		cleaned = []byte(security.RedactJSON(data))
	}
	stamped, err := SetRetention(cleaned, r)
	if err != nil {
		return Record{}, err
	}
	if len(stamped) > maxLineLen {
		return Record{}, fmt.Errorf("session: retention payload too large after stamping: %d bytes", len(stamped))
	}
	return s.AppendRecord(kind, json.RawMessage(stamped))
}
