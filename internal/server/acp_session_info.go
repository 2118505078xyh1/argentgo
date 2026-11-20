package server

import "time"

// 本文件提供 ACP session_info_update 通知载荷的纯构造函数。
//
// 背景：goose 会话学习发现，当多个客户端挂载到同一会话时，标题（以及
// 其他会话元数据）的变化应当定向推送给各客户端，而不是让客户端轮询。
// ACP 用 session/update 通知里的 session_info_update 变体承载这类变化。
//
// 这里刻意只交付纯函数，不改 acp.go 的通知流：任何对现有通知序列的
// 插入都会改变线协议并破坏既有的 ACP 测试基线。接线点见本文件末尾
// 注释。待上游具备标题变化事件源时，再由 opt-in 入口把本函数的产物
// 交给 peer.notify 发出。

const (
	// acpSessionInfoUpdateKind 是 ACP session/update 通知中
	// session_info_update 变体的判别值。
	acpSessionInfoUpdateKind = "session_info_update"
	// acpSessionUpdateMethod 是承载所有会话更新的 JSON-RPC 通知方法名。
	acpSessionUpdateMethod = "session/update"
	// acpSessionInfoMetaKey 是元信息字段名。ACP 保留 _meta 承载扩展数据，
	// 这里按约定固定使用 meta 键，且始终存在以保证字段稳定。
	acpSessionInfoMetaKey = "meta"
)

// sessionInfoUpdate 构造一条 ACP session_info_update 通知的 params 载荷。
//
// 返回的 map 可作为 peer.notify(acpSessionUpdateMethod, payload) 的参数直接使用：
//   - "sessionId" 为目标会话 id，使多客户端场景能把更新定向到具体会话；
//   - "update" 内的 "sessionUpdate" 固定为 "session_info_update"；
//   - "title" 原样携带传入的标题，含 CJK 字符时按字节拼接，不拆分 UTF-8
//     序列，也不做裁剪或转义，因此 CJK 标题安全；
//   - "updatedAt" 把 int64 的 Unix 秒时间戳转换为 ACP 要求的 ISO 8601
//     （RFC3339 UTC）字符串；
//   - "meta" 是一个稳定的空 map，始终存在（不因输入省略），供上层填充
//     自定义元数据，ACP 客户端不得对其取值做假设。
//
// 该函数是纯函数：不读取全局状态、不发 IO，对任意输入都返回键集合固定
// 的新 map（字段稳定）。标题为空时也保留 "title" 键（值为空字符串），
// 因此调用方无需处理键缺失的分支。
func sessionInfoUpdate(title, sessionID string, updatedAt int64) map[string]any {
	return map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate":       acpSessionInfoUpdateKind,
			"title":               title,
			"updatedAt":           time.Unix(updatedAt, 0).UTC().Format(time.RFC3339),
			acpSessionInfoMetaKey: map[string]any{},
		},
	}
}

// 接线点（未实施，默认关闭）：
//
// acp.go 的 (*acpState).prompt 通过 s.peer.notify 发出 session/update；
// 会话标题的变化目前没有事件源流入本包。要在 opt-in 前提下接线，建议：
//
//  1. 在 Config 增加 SessionInfoUpdates bool（默认 false，保持默认线协议
//     与现有测试不变）；
//  2. 在 acpState 增加 emitSessionInfo(title, sessionID string, ts int64)
//     方法，仅在开关打开时调用
//     s.peer.notify(acpSessionUpdateMethod, sessionInfoUpdate(title, sessionID, ts))；
//  3. 由 internal/app 在会话标题变化时通过该入口调用——这是唯一需要感知
//     "标题变化"事件的地方，传输层本身保持协议无关。
//
// 在标题变化事件源接入之前不实施上述步骤，避免为一种尚无生产者的事件
// 改动已冻结的通知序列。
