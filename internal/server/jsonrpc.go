package server

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

// JSON-RPC 2.0 framing shared by --rpc and acp.

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// standard JSON-RPC error codes.
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

// peer writes newline-delimited JSON-RPC messages to one writer.
type peer struct {
	mu  sync.Mutex
	buf *bufio.Writer
}

func newPeer(w io.Writer) *peer {
	return &peer{buf: bufio.NewWriter(w)}
}

func (p *peer) write(payload any) error {
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.buf.Write(line); err != nil {
		return err
	}
	if err := p.buf.WriteByte('\n'); err != nil {
		return err
	}
	return p.buf.Flush()
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

func (p *peer) notify(method string, params any) {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	_ = p.write(message)
}

// promptText extracts prompt text from {"text":...}, {"prompt":"..."} or
// {"prompt":[{"type":"text","text":...}]}.
func promptText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		Text   string          `json:"text"`
		Prompt json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
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
