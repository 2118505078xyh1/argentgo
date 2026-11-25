package session

import "fmt"

// ErrUnknownKind 表示转录中出现一个既不在已知白名单、也未标记 ignorable 的 kind。
// 它让未知事件无法被静默丢弃（清单 #186：反模式"未知事件静默丢弃"）。
var ErrUnknownKind = fmt.Errorf("session: unknown record kind")

// knownKinds 是本版本能投影或有意保留为元数据的全部 kind。
// 新增 kind 时必须同时在此登记，否则旧版本读取会 fail-closed 报错——
// 这是有意的：新事件必须显式声明它是否可被旧客户端安全忽略。
var knownKinds = map[string]bool{
	KindHeader: true, KindMessage: true, KindUser: true, KindAssistant: true,
	KindTool: true, KindSummary: true, KindCompaction: true, KindCheckpoint: true,
	KindAttempt: true, KindTitle: true, KindCondensation: true, KindContextEdit: true,
	KindImageOffload: true, KindRequestSnapshot: true,
	KindToolClaim: true, KindSchema: true, KindForkLineage: true,
	KindModelRoute: true,
	"archive":      true,
	// 统计与进度类记录（生产与测试均写入）。
	"stats": true, "usage": true, "progress": true, "token": true,
	"cost": true, "budget": true, "entry": true, "notice": true, "error": true,
	"event": true, "trace": true, "snapshot": true,
	"system": true, "tool_progress": true, "turn": true,
	// 兼容别名：messageEntry 接受的旧 kind。
	"prompt": true, "model": true, "reply": true,
	"tool_result": true, "tool-result": true, "toolresult": true,
	// 兼容 session_message.go 中的历史拼写（KindSchema 的值即 "schema"）。
	"session": true,
}

// CheckUnknownKinds 校验一批记录：未知 kind 必须带 ignorable=true 才允许跳过。
// 返回第一个违规记录的错误；全部合法时返回 nil。纯函数，不修改输入。
func CheckUnknownKinds(records []Record) error {
	for _, rec := range records {
		if knownKinds[rec.Kind] || rec.Ignorable {
			continue
		}
		return fmt.Errorf("%w: %q (id=%s); add an ignorable=true marker or register the kind",
			ErrUnknownKind, rec.Kind, rec.ID)
	}
	return nil
}
