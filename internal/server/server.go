// Package server hosts the headless transports of the agent: the HTTP/SSE
// service behind --serve, newline-delimited JSON-RPC behind --rpc, and the ACP
// editor protocol behind acp.
//
// Every transport drives an injected Runner so the package stays independent of
// the agent loop and can be tested with a fake.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultAddr is the loopback address used when Config.Addr is empty.
const DefaultAddr = "127.0.0.1:7777"

// maxBody caps request bodies for JSON endpoints.
const maxBody = 1 << 20

// Runner is the slice of the agent loop the transports drive.
type Runner interface {
	// Prompt runs one turn, forwarding streamed text to onText, and returns the
	// final answer.
	Prompt(ctx context.Context, text string, onText func(string)) (string, error)
	// Abort cancels the turn currently running, if any.
	Abort()
}

// Config configures every transport in this package.
type Config struct {
	// Addr is the listen address for --serve; empty means DefaultAddr.
	Addr string
	// Token is the bearer token required by --serve. It is mandatory: Listen
	// refuses to start without one.
	Token string
	// Version is reported by /v1/health and ACP initialize.
	Version string
	// Runner executes prompts. Required.
	Runner Runner
	// Sessions backs the session.list / session.load RPC methods. Optional.
	Sessions SessionStore
}

// Listen binds the HTTP transport and returns the server plus its listener.
// It fails when no bearer token or no runner is configured.
func Listen(cfg Config) (*http.Server, net.Listener, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, nil, errors.New("server: a bearer token is required (--serve-token or ARGENT_SERVE_TOKEN)")
	}
	if cfg.Runner == nil {
		return nil, nil, errors.New("server: no runner configured")
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = DefaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("server: listen %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           NewHandler(cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv, ln, nil
}

// ServeListener serves until ctx is cancelled or the server fails.
func ServeListener(ctx context.Context, srv *http.Server, ln net.Listener) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		case <-done:
		}
	}()
	err := srv.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Serve listens and serves in one call.
func Serve(ctx context.Context, cfg Config) error {
	srv, ln, err := Listen(cfg)
	if err != nil {
		return err
	}
	return ServeListener(ctx, srv, ln)
}

// NewHandler builds the HTTP/SSE handler without binding a socket.
func NewHandler(cfg Config) http.Handler {
	h := &handler{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", h.health)
	mux.HandleFunc("/v1/prompt", h.auth(h.prompt))
	mux.HandleFunc("/v1/abort", h.auth(h.abort))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return mux
}

type handler struct {
	cfg Config
}

func (h *handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="argent"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (h *handler) authorized(r *http.Request) bool {
	want := strings.TrimSpace(h.cfg.Token)
	if want == "" {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" {
		got = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	version := h.cfg.Version
	if version == "" {
		version = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version})
}

func (h *handler) abort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if h.cfg.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	h.cfg.Runner.Abort()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// prompt handles POST /v1/prompt, streaming Server-Sent Events:
//
//	data: {"type":"text","text":"..."}
//	data: {"type":"done"}
func (h *handler) prompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if h.cfg.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	text := requestText(body)
	if strings.TrimSpace(text) == "" {
		writeError(w, http.StatusBadRequest, "prompt has no text")
		return
	}

	flusher, _ := w.(http.Flusher)
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(payload any) bool {
		line, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	failed := false
	_, runErr := h.cfg.Runner.Prompt(r.Context(), text, func(chunk string) {
		if failed || chunk == "" {
			return
		}
		if !emit(map[string]any{"type": "text", "text": chunk}) {
			failed = true
		}
	})
	if failed {
		return
	}
	if runErr != nil {
		emit(map[string]any{"type": "error", "error": runErr.Error()})
		return
	}
	emit(map[string]any{"type": "done"})
}

// requestText is the HTTP side of prompt extraction. The parsing and trim
// rules live solely in promptText so HTTP, --rpc and acp cannot drift apart.
func requestText(body []byte) string {
	return promptText(body)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}
