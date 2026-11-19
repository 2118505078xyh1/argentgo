package server

import (
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
//
// Each message is written directly to the underlying writer as a single frame
// while holding mu. A bufio.Writer used to sit between the two, but write()
// flushed after every message, so it only added a copy without ever batching
// writes. Writing the frame in one call keeps ordering and delivery semantics
// intact while dropping that copy.
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

func (p *peer) notify(method string, params any) {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	_ = p.write(message)
}

// promptText extracts prompt text from every prompt carrier used by the
// transports: a raw text body, {"text":...}, {"prompt":"..."} or
// {"prompt":[{"type":"text","text":...}]}. It is the single source of truth
// for both JSON-RPC/ACP params and the HTTP /v1/prompt body.
func promptText(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "{") {
		// Raw text body (HTTP) — keep the historical trim behaviour.
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
