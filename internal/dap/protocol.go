package dap

import "encoding/json"

// 本文件定义 DAP（Debug Adapter Protocol）的线上消息形态。
// DAP 与 LSP 共用 Content-Length 帧，但消息体带 seq/type 三态：
// request（命令）、response（对 request_seq 的回应）、event（adapter 主动通知）。
// 字段名遵循 DAP 规范的 camelCase（requestSeq/threadId 等）。

const (
	TypeRequest  = "request"
	TypeResponse = "response"
	TypeEvent    = "event"
)

// Message 是所有 DAP 消息的通用形态。用 json.RawMessage 保留 body/arguments，
// 让高层 API 按命令解析，避免在这里定义几十种 body 类型。
type Message struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	Command    string          `json:"command,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	RequestSeq int             `json:"requestSeq,omitempty"`
	Success    *bool           `json:"success,omitempty"`
	Message    string          `json:"message,omitempty"` // 失败原因（response）
	Body       json.RawMessage `json:"body,omitempty"`    // 成功结果或事件载荷
	Event      string          `json:"event,omitempty"`
}

// Event 是 adapter 主动推送的事件（stopped/terminated/breakpoint/output…）。
type Event struct {
	Seq   int
	Event string
	Body  json.RawMessage
}

// stopped/terminated 等常用事件载荷的轻量投影。字段用指针以区分"未设置"
// 与"零值"，例如 threadId==0 在某些 adapter 里是合法的。
type StoppedBody struct {
	Reason   string `json:"reason"`
	ThreadID int    `json:"threadId"`
	Text     string `json:"text,omitempty"`
}

// response 是内部用于请求匹配的简化结果。
type response struct {
	RequestSeq int
	Success    bool
	Message    string
	Body       json.RawMessage
}
