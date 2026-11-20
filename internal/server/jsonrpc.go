package server

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
)

// --rpc 和 acp 共享的 JSON-RPC 2.0 帧格式。

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// 标准 JSON-RPC 错误码。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeServerError    = -32000
)

func isNotification(id json.RawMessage) bool {
	return len(strings.TrimSpace(string(id))) == 0
}

// peer 把换行分隔的 JSON-RPC 消息写入一个 writer。
//
// 每条消息都在持有 mu 时作为单个帧直接写入底层 writer。
// 过去两者之间有一个 bufio.Writer，但 write() 在每条消息后
// 都会刷新，所以它只增加了一次拷贝而没有批处理
// 任何写入。一次调用写入整个帧，既保持顺序和投递语义
// 不变，又去掉了那次拷贝。
type peer struct {
	mu sync.Mutex
	w  io.Writer
}

func newPeer(w io.Writer) *peer {
	return &peer{w: w}
}

func (p *peer) write(payload any) error {
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := append(line, '\n')
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(frame) > 0 {
		n, err := p.w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func (p *peer) respond(id json.RawMessage, result any) {
	if isNotification(id) {
		return
	}
	_ = p.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (p *peer) fail(id json.RawMessage, code int, message string) {
	if isNotification(id) {
		return
	}
	_ = p.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

// failWithData 是 fail 加上可选的附加 error.data 载荷。
// 它是独立的方法，使所有现有的 fail() 调用方保持原来的两字段
// 错误信封不变；只有选择加入的调用方才会发出 data 成员。
func (p *peer) failWithData(id json.RawMessage, code int, message string, data any) {
	if isNotification(id) {
		return
	}
	errObj := map[string]any{"code": code, "message": message}
	if data != nil {
		errObj["data"] = data
	}
	_ = p.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   errObj,
	})
}

func (p *peer) notify(method string, params any) {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	_ = p.write(message)
}

// promptText 从各传输使用的每一种提示载体中提取提示文本：
// 原始文本体、{"text":...}、{"prompt":"..."} 或
// {"prompt":[{"type":"text","text":...}]}。它是 JSON-RPC/ACP 参数和
// HTTP /v1/prompt 体的唯一事实来源。
func promptText(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "{") {
		// 原始文本体（HTTP）——保持历史上的裁剪行为。
		return trimmed
	}
	var payload struct {
		Text   string          `json:"text"`
		Prompt json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return ""
	}
	if text := strings.TrimSpace(payload.Text); text != "" {
		return payload.Text
	}
	if len(payload.Prompt) == 0 || string(payload.Prompt) == "null" {
		return ""
	}
	var single string
	if err := json.Unmarshal(payload.Prompt, &single); err == nil {
		return single
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload.Prompt, &blocks); err == nil {
		var texts []string
		for _, block := range blocks {
			if block.Type == "" || block.Type == "text" {
				if block.Text != "" {
					texts = append(texts, block.Text)
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}
