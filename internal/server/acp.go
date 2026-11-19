package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// RunACP serves the Agent Client Protocol (JSON-RPC 2.0 over stdio, one message
// per line) until in reaches EOF or ctx is cancelled.
//
// Supported methods: initialize, authenticate, session/new, session/prompt,
// session/cancel, session/set_model, session/set_mode. Streamed assistant text
// is sent as session/update notifications.
func RunACP(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	ctx, cancelAll := context.WithCancel(ctx)

	state := &acpState{
		cfg:      cfg,
		peer:     newPeer(out),
		sessions: make(map[string]bool),
	}

	var wg sync.WaitGroup
	defer func() {
		cancelAll()
		wg.Wait()
	}()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			state.peer.fail(json.RawMessage("null"), codeParseError, "parse error: "+err.Error())
			continue
		}
		if req.Method == "" {
			state.peer.fail(req.ID, codeInvalidRequest, "missing method")
			continue
		}
		state.handle(ctx, req, &wg)
	}
	return scanner.Err()
}

type acpState struct {
	cfg  Config
	peer *peer

	mu           sync.Mutex
	sessionID    string
	sessions     map[string]bool
	cancelPrompt context.CancelFunc
	busy         atomic.Bool
}

func (s *acpState) handle(ctx context.Context, req request, wg *sync.WaitGroup) {
	switch req.Method {
	case "initialize":
		version := s.cfg.Version
		if version == "" {
			version = "dev"
		}
		s.peer.respond(req.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"promptCapabilities": map[string]any{
					"image":           true,
					"audio":           false,
					"embeddedContext": true,
				},
				"mcpCapabilities": map[string]any{"http": false, "sse": false},
				"loadSession":     false,
			},
			"authMethods": []any{},
			"agentInfo":   map[string]any{"name": "argentgo", "version": version},
		})
	case "authenticate":
		s.peer.respond(req.ID, map[string]any{})
	case "session/new":
		id := newSessionID()
		s.mu.Lock()
		s.sessions[id] = true
		s.sessionID = id
		s.mu.Unlock()
		s.peer.respond(req.ID, map[string]any{"sessionId": id})
	case "session/prompt":
		s.prompt(ctx, req, wg)
	case "session/cancel":
		s.cancel()
		s.peer.respond(req.ID, map[string]any{})
	case "session/set_model", "session/set_mode":
		s.peer.respond(req.ID, map[string]any{})
	case "session/load":
		s.peer.fail(req.ID, codeMethodNotFound, "session/load is not supported; start a new session")
	default:
		s.peer.fail(req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}

func (s *acpState) prompt(parent context.Context, req request, wg *sync.WaitGroup) {
	text := promptText(req.Params)
	if text == "" {
		s.peer.fail(req.ID, codeInvalidParams, "prompt has no text")
		return
	}
	if s.cfg.Runner == nil {
		s.peer.fail(req.ID, codeServerError, "no runner configured")
		return
	}

	s.mu.Lock()
	requested := sessionIDFromParams(req.Params)
	current := s.sessionID
	s.mu.Unlock()
	if requested != "" && requested != current {
		s.peer.fail(req.ID, codeInvalidParams, "unknown session")
		return
	}
	if !s.busy.CompareAndSwap(false, true) {
		s.peer.fail(req.ID, codeInvalidRequest, "a prompt is already running")
		return
	}

	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancelPrompt = cancel
	s.mu.Unlock()

	id := req.ID
	session := current
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer s.busy.Store(false)
		defer cancel()
		defer func() {
			s.mu.Lock()
			s.cancelPrompt = nil
			s.mu.Unlock()
		}()

		result, err := s.cfg.Runner.Prompt(ctx, text, func(chunk string) {
			if chunk == "" {
				return
			}
			s.peer.notify("session/update", map[string]any{
				"sessionId": session,
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": chunk},
				},
			})
		})
		_ = result
		if err != nil {
			if ctx.Err() != nil {
				s.peer.respond(id, map[string]any{"stopReason": "cancelled"})
				return
			}
			s.peer.fail(id, codeInternalError, err.Error())
			return
		}
		s.peer.respond(id, map[string]any{"stopReason": "end_turn"})
	}()
}

func (s *acpState) cancel() {
	s.mu.Lock()
	cancel := s.cancelPrompt
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if s.cfg.Runner != nil {
		s.cfg.Runner.Abort()
	}
}

func sessionIDFromParams(raw json.RawMessage) string {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if len(raw) == 0 {
		return ""
	}
	_ = json.Unmarshal(raw, &params)
	return params.SessionID
}

func newSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return "sess_" + hex.EncodeToString(buf[:])
}
