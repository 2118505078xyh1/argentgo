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
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
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
	// Version is reported by /v1/health, /v1/version and ACP initialize.
	Version string
	// Runner executes prompts. Required.
	Runner Runner
	// Sessions backs the session.list / session.load RPC methods. Optional.
	Sessions SessionStore
	// Events is the shared event hub for /v1/event and event/subscribe. When
	// nil, a runner exposing EventHub() *EventHub (as app.Factory's runner does)
	// is used, so all headless transports share one hub without extra wiring.
	// Only when neither is present does each transport create a private hub.
	Events *EventHub
}

// isCancelled reports whether a Runner.Prompt error means the user (or the
// transport) stopped the turn, as opposed to a genuine failure. The agent loop
// returns context.Canceled — possibly wrapped — on abort, and a disconnected
// client leaves ctx.Err() == context.Canceled. A deadline is a real failure and
// keeps its original error path in all three transports.
func isCancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled
}

// selectHub resolves the hub a transport publishes to. An explicit Config.Events
// wins; otherwise a runner built by internal/app exposes its shared hub through
// a small interface that keeps this package independent of the wirer. The
// interface assertion means fakes and standalone callers keep the original
// "private hub" behaviour.
func selectHub(cfg Config) *EventHub {
	if cfg.Events != nil {
		return cfg.Events
	}
	if typed, ok := cfg.Runner.(interface{ EventHub() *EventHub }); ok {
		if hub := typed.EventHub(); hub != nil {
			return hub
		}
	}
	return NewEventHub()
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
	h := newHandler(cfg)
	srv := newServer(h)
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
	return newHandler(cfg).routes()
}

// newHandler creates the stateful route receiver shared by every transport
// endpoint. Listen keeps the returned pointer so it can register the shutdown
// hook; standalone callers of NewHandler never flip the flag.
func newHandler(cfg Config) *handler {
	return &handler{cfg: cfg}
}

// newServer builds the HTTP server for a handler and wires the closing flag to
// the standard Shutdown/Close hook. Keeping this separate from Listen lets the
// wiring be tested without binding a socket.
func newServer(h *handler) *http.Server {
	srv := &http.Server{
		Handler:           h.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Shutdown (and Close) call this hook, so a health probe that still reaches
	// an in-flight keep-alive connection during a graceful shutdown observes
	// "closing" instead of a misleading 200.
	srv.RegisterOnShutdown(h.beginShutdown)
	return srv
}

func (h *handler) routes() http.Handler {
	hub := selectHub(h.cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", h.health)
	mux.HandleFunc("/v1/version", h.version)
	mux.HandleFunc("/v1/prompt", h.auth(h.prompt))
	mux.HandleFunc("/v1/abort", h.auth(h.abort))
	mux.HandleFunc("/v1/event", h.auth(NewEventHandler(hub).ServeHTTP))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return mux
}

type handler struct {
	cfg     Config
	closing atomic.Bool
}

// beginShutdown is wired to http.Server.RegisterOnShutdown in newServer. It is
// also callable directly by tests that drive the handler without a socket.
func (h *handler) beginShutdown() {
	h.closing.Store(true)
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
	// RFC 7235 defines auth-scheme as case-insensitive, so "bearer", "BEARER"
	// and "BeArEr" are all the same scheme as "Bearer". Compare the prefix
	// without case folding the token itself: tokens remain byte-exact.
	const scheme = "Bearer "
	header := r.Header.Get("Authorization")
	var got string
	if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
		got = strings.TrimSpace(header[len(scheme):])
	} else {
		// A present header with a different scheme is not a bearer credential.
		// Keep the whole value as the candidate (as TrimPrefix did before) so
		// a bogus scheme cannot fall through to the query-string token.
		got = strings.TrimSpace(header)
	}
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
	if h.closing.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "closing",
			"error":  "server is shutting down",
		})
		return
	}
	version := h.cfg.Version
	if version == "" {
		version = defaultVersion
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version})
}

// version serves the structured build report. Like health, it is a diagnostic
// endpoint and needs no bearer token; it exposes only build metadata, never
// credentials or configuration.
func (h *handler) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, newVersionInfo(h.cfg.Version))
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
//
// The HTTP 200 header is withheld until the first text event (or a terminal
// cancelled event). If the runner fails before producing any text, the client
// gets a non-2xx JSON error instead of a 200 carrying an error event, so a
// status-code-only client cannot mistake a total failure for success. Once the
// stream has started the status is committed, so a later failure is reported
// with a terminal SSE error event. A user abort remains a normal 200 stop with
// {"type":"cancelled"}.
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

	// started tracks whether the SSE response header is already on the wire.
	// Holding it back until the first event lets a pre-stream failure still
	// choose an honest non-2xx status and a JSON body.
	started := false
	ensureStarted := func() {
		if started {
			return
		}
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		started = true
	}

	emit := func(payload any) bool {
		line, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		ensureStarted()
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
		if isCancelled(r.Context(), runErr) {
			// Abort is not a failure; emitting the terminal cancelled event
			// also commits the 200 header when no text preceded the abort.
			emit(map[string]any{"type": "cancelled"})
			return
		}
		if started {
			// The stream is already committed to 200, so the status can no
			// longer be changed; terminate with a terminal SSE error event.
			emit(map[string]any{"type": "error", "error": wireErrorText(runErr)})
			return
		}
		writeError(w, promptFailureStatus(runErr), wireErrorText(runErr))
		return
	}
	emit(map[string]any{"type": "done"})
}

// wireErrorText redacts an error before it crosses a transport boundary.
//
// Runner failures come straight from the provider layer, whose error bodies may
// echo the server's Authorization header, API key, password or account e-mail.
// promptFailureStatus already classifies through llm.ClassifyAPIError (which
// redacts internally), but the wire body used to carry err.Error() verbatim —
// so a client holding only the bearer token could read the server's own
// credentials out of a failed prompt. Redact with the same llm policy the
// classifier uses so the status and the body agree on what is safe to reveal.
func wireErrorText(err error) string {
	return llm.RedactError(err)
}

// promptFailureStatus maps a genuine pre-stream runner failure to an HTTP
// status. Auth (401) and rate-limit (429) failures are actionable client errors;
// everything else is a 500 so clients cannot read an early failure as success.
// The family classifier keeps this aligned with the CLI's error presentation.
func promptFailureStatus(err error) int {
	actionable := llm.ClassifyAPIError(err)
	if actionable == nil {
		return http.StatusInternalServerError
	}
	switch actionable.Family {
	case llm.FamilyAuth:
		return http.StatusUnauthorized
	case llm.FamilyRateLimit:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
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
