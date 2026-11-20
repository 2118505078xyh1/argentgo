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
// session/cancel, session/set_model, session/set_mode, session/request_permission.
// Streamed assistant text is sent as session/update notifications.
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
	case "session/request_permission":
		s.requestPermission(req)
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

// ---- session/request_permission (ACP protocol skeleton) ----
//
// ACP defines a four-way permission choice for a tool call: allow/reject, each
// once or always. argent-go implements the protocol surface only: it builds the
// canonical option set and maps a client-supplied optionId onto an outcome.
// There is deliberately no interactive wait here — bridging a request to the
// durable security.PendingStore (and waiting for approval.answer) is the upper
// layer's job. Until that wiring exists, every input that is not an exact match
// for one of the four choices resolves to "cancelled", so this method can never
// grant a tool call by accident (fail-closed, least privilege).

// PermissionOptionKind is one of the four ACP permission decision semantics.
type PermissionOptionKind string

const (
	PermissionOptionAllowOnce    PermissionOptionKind = "allow_once"
	PermissionOptionAllowAlways  PermissionOptionKind = "allow_always"
	PermissionOptionRejectOnce   PermissionOptionKind = "reject_once"
	PermissionOptionRejectAlways PermissionOptionKind = "reject_always"
)

// PermissionOption is one selectable choice offered to the ACP client.
type PermissionOption struct {
	OptionID string               `json:"optionId"`
	Kind     PermissionOptionKind `json:"kind"`
	Name     string               `json:"name"`
}

// RequestPermissionParams is the inbound ACP request. SessionID and ToolCallID
// identify the tool call being decided; OptionID is the choice echoed by the
// client and is the only field that can produce a "selected" outcome. Options
// mirrors the ACP request shape but is never trusted: permissionOptions() is the
// single source of truth for what can be selected.
type RequestPermissionParams struct {
	SessionID  string             `json:"sessionId"`
	ToolCallID string             `json:"toolCallId"`
	OptionID   string             `json:"optionId,omitempty"`
	Options    []PermissionOption `json:"options,omitempty"`
}

// RequestPermissionOutcome is the ACP "outcome" object: either "selected" with
// the chosen optionId, or "cancelled" with no optionId.
type RequestPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// RequestPermissionResult is the ACP session/request_permission response.
type RequestPermissionResult struct {
	Outcome RequestPermissionOutcome `json:"outcome"`
}

// permissionOptions returns the four canonical ACP permission choices. The
// optionId doubles as the kind string, so validation needs no generated IDs.
func permissionOptions() []PermissionOption {
	return []PermissionOption{
		{OptionID: string(PermissionOptionAllowOnce), Kind: PermissionOptionAllowOnce, Name: "Allow once"},
		{OptionID: string(PermissionOptionAllowAlways), Kind: PermissionOptionAllowAlways, Name: "Always allow"},
		{OptionID: string(PermissionOptionRejectOnce), Kind: PermissionOptionRejectOnce, Name: "Reject once"},
		{OptionID: string(PermissionOptionRejectAlways), Kind: PermissionOptionRejectAlways, Name: "Always reject"},
	}
}

// permissionOutcome maps a client-supplied optionId onto the canonical set.
// Only an exact match is honoured; an empty, unknown or wrongly-cased choice
// yields "cancelled" (fail-closed, least privilege).
func permissionOutcome(optionID string) RequestPermissionOutcome {
	for _, opt := range permissionOptions() {
		if opt.OptionID == optionID {
			return RequestPermissionOutcome{Outcome: "selected", OptionID: opt.OptionID}
		}
	}
	return RequestPermissionOutcome{Outcome: "cancelled"}
}

// requestPermission is the protocol skeleton for session/request_permission.
// Malformed params are a JSON-RPC error; any params that do not carry a
// recognised option (empty, missing or unknown) return a "cancelled" outcome.
func (s *acpState) requestPermission(req request) {
	var params RequestPermissionParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.peer.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	s.peer.respond(req.ID, RequestPermissionResult{Outcome: permissionOutcome(params.OptionID)})
}
