package server

import (
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

	results := scanLines(ctx, in)
	for {
		// Prioritise cancellation so a queued line cannot delay the exit.
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case res, ok := <-results:
			if !ok { // EOF
				return nil
			}
			if res.err != nil {
				if reportScanError(state.peer, res.err) {
					return res.err
				}
				continue
			}
			var req request
			if err := json.Unmarshal(res.line, &req); err != nil {
				state.peer.fail(json.RawMessage("null"), codeParseError, "parse error: "+err.Error())
				continue
			}
			if req.Method == "" {
				state.peer.fail(req.ID, codeInvalidRequest, "missing method")
				continue
			}
			state.handle(ctx, req, &wg)
		}
	}
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
	if requested == "" {
		// ACP requires sessionId, but stay permissive for clients that rely on
		// the legacy "most recently created session" behaviour.
		requested = s.sessionID
	}
	// Validate against every session created on this connection, not just the
	// latest one. A client may open several sessions and return to an earlier
	// one; sessions is the registry session/new maintains for exactly this.
	_, known := s.sessions[requested]
	s.mu.Unlock()
	if !known {
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
	session := requested
	wg.Add(1)
	go func() {
		defer wg.Done()

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

		// Capture the outcome before calling cancel() below: once cancel()
		// runs, ctx.Err() is context.Canceled for every error and a genuine
		// failure would be misreported as a normal stop.
		cancelled := err != nil && isCancelled(ctx, err)

		// Release the single-prompt slot *before* publishing the terminal
		// result. The result write is observable by the client: a client that
		// starts the next prompt as soon as it sees this result must not be
		// rejected by a stale busy flag that deferred cleanup had not reached
		// yet. Clear cancelPrompt while busy is still held so a new prompt can
		// never observe a stale cancel handle, then drop busy.
		s.mu.Lock()
		s.cancelPrompt = nil
		s.mu.Unlock()
		cancel()
		s.busy.Store(false)

		if err != nil {
			// Same cancellation rule as --rpc and --serve; the wire result
			// stays {"stopReason":"cancelled"} as ACP clients expect.
			if cancelled {
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
