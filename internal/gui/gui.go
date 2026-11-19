// Package gui serves the SolidJS desktop frontend (ui/) from the argent binary.
//
// The original shell was Tauri 2: the page called invoke("<command>", args) over
// the WebView IPC and listened on one "argent://event" channel. This package
// keeps those command names and payloads byte-compatible, but moves them onto
// plain HTTP:
//
//	GET  /              the built frontend (//go:embed dist)
//	POST /rpc/{command} one JSON argument object in, one JSON result out
//	GET  /events        text/event-stream, one Incoming JSON frame per message
//	GET  /healthz       liveness probe for the launcher
//
// Every request must carry the token: "Authorization: Bearer <token>", or
// ?token= for GETs (EventSource cannot set headers, and the launcher opens the
// page with the token in its URL). Anything else is 401. The served index.html
// gets the token injected as window.__ARGENT_TOKEN__ so the page can pick it up
// without a second round trip.
//
// The package never imports the agent: each tab's worker is an injected Runner,
// so the whole bridge is testable with a fake and the real engine is adapted in
// internal/app.
package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// distFS holds the built frontend. Refresh it with scripts/build-ui.ps1; the
// committed placeholder keeps the embed compiling before the first build.
//
//go:embed all:dist
var distFS embed.FS

// tokenMarker is replaced by a script tag when index.html is served.
const tokenMarker = "<!--argent-token-->"

// maxBody caps JSON request bodies.
const maxBody = 4 << 20

// Options configures the desktop bridge.
type Options struct {
	// Dir is the workspace root: relative paths from the UI resolve here and
	// list_files walks it. Empty means the process working directory.
	Dir string
	// Version is reported by /healthz.
	Version string
	// Model, Provider, Protocol and Locale only feed /healthz; each tab's real
	// values arrive in its ready frame.
	Model    string
	Provider string
	Protocol string
	Locale   string
	// StorageDir holds settings.json (telemetry preferences). Empty means
	// ~/.argent.
	StorageDir string
	// NewRunner starts one agent worker per tab. Required.
	NewRunner func() Runner
	// Sessions backs search_sessions and import_session_text. Optional: those
	// commands answer "not supported in the Go build" when it is nil.
	Sessions SessionStore
	// History backs the undo / retry transcript actions. Optional; see
	// TurnHistory in interfaces.go for the exact hook.
	History TurnHistory
	// Optional capability backends. See interfaces.go for the exact method
	// contracts. Jobs, Automation, Workflows, Documents and PTY are
	// injected-only: a nil value keeps the clear "not supported" error.
	// Memory and Usage fall back to stdlib file stores under StorageDir:
	// NewFileMemory (<storage>/memory/*.md) and NewFileUsage
	// (<storage>/sessions/*.jsonl). Inject to override those defaults.
	Jobs       Jobs
	Automation Automation
	Memory     Memory
	Workflows  Workflows
	Documents  Documents
	Usage      Usage
	PTY        PTY
	// OnReady is called with the page URL once the listener is bound, so the
	// launcher can open a browser.
	OnReady func(url string)
}

// Run serves the desktop UI on 127.0.0.1 until ctx is cancelled.
//
// It binds a random port with a random bearer token, reports the ready URL
// through Options.OnReady and returns nil after a clean shutdown.
func Run(ctx context.Context, opts Options) error {
	b, err := newBridge(opts)
	if err != nil {
		return err
	}
	defer b.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("gui: listen: %w", err)
	}
	srv := &http.Server{
		Handler:           b,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if opts.OnReady != nil {
		opts.OnReady(fmt.Sprintf("http://%s/?token=%s", ln.Addr().String(), b.token))
	}

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
	err = srv.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// bridge is the whole server: routes, tabs and the event fan-out.
type bridge struct {
	opts  Options
	dir   string
	token string
	mux   *http.ServeMux
	br    *broker

	mu      sync.Mutex
	tabs    map[string]*tab
	order   []string // tab ids in creation order, for the successor on close
	active  string
	ready   map[string]map[string]any
	nextTab int

	approvalSeq atomic.Uint64
}

// newBridge builds the server state and opens the first tab ("1").
func newBridge(opts Options) (*bridge, error) {
	if opts.NewRunner == nil {
		return nil, errors.New("gui: Options.NewRunner is required")
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	dir := opts.Dir
	if strings.TrimSpace(dir) == "" {
		dir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("gui: workspace: %w", err)
		}
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("gui: workspace: %w", err)
	}
	b := &bridge{
		opts:    opts,
		dir:     dir,
		token:   token,
		br:      newBroker(),
		tabs:    make(map[string]*tab),
		ready:   make(map[string]map[string]any),
		nextTab: 1,
	}
	b.mux = http.NewServeMux()
	b.mux.HandleFunc("/events", b.events)
	b.mux.HandleFunc("/healthz", b.health)
	b.mux.HandleFunc("/rpc/", b.rpc)
	b.mux.HandleFunc("/", b.static)
	if _, err := b.startTab(); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// ServeHTTP enforces the bearer token before any route runs.
func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="argent"`)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	b.mux.ServeHTTP(w, r)
}

// Close asks every worker to flush its session, like the Tauri window close.
func (b *bridge) Close() {
	b.mu.Lock()
	tabs := make([]*tab, 0, len(b.tabs))
	for _, t := range b.tabs {
		tabs = append(tabs, t)
	}
	b.mu.Unlock()
	for _, t := range tabs {
		t.shutdown()
	}
}

// Token returns the bearer token; tests and launchers need it.
func (b *bridge) Token() string { return b.token }

// URL is the page URL for a bound listener.
func (b *bridge) URL(addr string) string {
	return fmt.Sprintf("http://%s/?token=%s", addr, b.token)
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gui: token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// authorized accepts the header on every method and the query on GET only, so
// state-changing calls never carry the token in a URL.
func (b *bridge) authorized(r *http.Request) bool {
	if b.token == "" {
		return false
	}
	got := ""
	header := r.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		got = strings.TrimSpace(header[7:])
	}
	// The page is opened with ?token= once, then its module/script assets are
	// requested without that query. A GET-only cookie keeps those local asset
	// loads working while state-changing RPC calls still require the header.
	// HEAD is a read-only method too, so it follows the same rule as GET.
	safeMethod := r.Method == http.MethodGet || r.Method == http.MethodHead
	if got == "" && safeMethod {
		got = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if got == "" && safeMethod {
		if cookie, err := r.Cookie("argent_token"); err == nil {
			got = strings.TrimSpace(cookie.Value)
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(b.token)) == 1
}

func (b *bridge) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	version := b.opts.Version
	if version == "" {
		version = "dev"
	}
	model, provider, protocol, locale := b.opts.Model, b.opts.Provider, b.opts.Protocol, b.opts.Locale
	if locale == "" {
		locale = "zh-CN"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"version":  version,
		"model":    model,
		"provider": provider,
		"protocol": protocol,
		"locale":   locale,
		"dir":      b.dir,
	})
}

// static serves the embedded build, injecting the token into index.html.
func (b *bridge) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "no embedded ui"})
		return
	}
	clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	name := strings.TrimPrefix(clean, "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(dist, name)
	if err != nil {
		// Unknown routes fall back to the SPA shell; missing assets stay 404 so a
		// broken build is visible instead of silently returning HTML.
		if strings.Contains(path.Base(name), ".") {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		name = "index.html"
		data, err = fs.ReadFile(dist, name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "ui is not built"})
			return
		}
	}
	if name == "index.html" {
		data = injectToken(data, b.token)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.SetCookie(w, &http.Cookie{
			Name:     "argent_token",
			Value:    b.token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	} else if kind := mime.TypeByExtension(path.Ext(name)); kind != "" {
		w.Header().Set("Content-Type", kind)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// injectToken replaces the marker with a script that publishes the bearer
// token, or prepends it when the built page has no marker.
func injectToken(page []byte, token string) []byte {
	script := `<script>window.__ARGENT_TOKEN__="` + token + `";</script>`
	body := string(page)
	if strings.Contains(body, tokenMarker) {
		return []byte(strings.Replace(body, tokenMarker, script, 1))
	}
	if index := strings.Index(body, "</head>"); index >= 0 {
		return []byte(body[:index] + script + body[index:])
	}
	return []byte(script + body)
}

// events streams one JSON frame per message until the client goes away.
func (b *bridge) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	// Subscribe before the handshake so a frame emitted right after the client
	// sees the headers cannot be missed.
	frames := b.br.subscribe()
	defer b.br.unsubscribe(frames)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame, open := <-frames:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", frame); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// startTab opens a tab with a fresh runner and makes it active.
func (b *bridge) startTab() (string, error) {
	runner := b.opts.NewRunner()
	if runner == nil {
		return "", errors.New("gui: runner factory returned nil")
	}
	events, approvals, commands := runner.Events(), runner.Approvals(), runner.Commands()
	if events == nil || approvals == nil || commands == nil {
		return "", errors.New("gui: runner channels must not be nil")
	}
	b.mu.Lock()
	sid := strconv.Itoa(b.nextTab)
	b.nextTab++
	t := &tab{
		id:        sid,
		events:    events,
		approvals: approvals,
		commands:  commands,
		pending:   make(map[uint64]chan<- bool),
		allowed:   make(map[string]bool),
		discard:   make(chan struct{}),
	}
	b.tabs[sid] = t
	b.order = append(b.order, sid)
	b.active = sid
	b.mu.Unlock()
	go b.pump(t)
	return sid, nil
}

// tab looks a session up; an empty sid means the active tab.
func (b *bridge) tab(sid string) (*tab, error) {
	b.mu.Lock()
	id := sid
	if id == "" {
		id = b.active
	}
	t := b.tabs[id]
	b.mu.Unlock()
	if t == nil {
		return nil, fmt.Errorf("session %s not found", id)
	}
	return t, nil
}

func (b *bridge) activeTab() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// publish tags one event with its tab and fans it out to every client.
func (b *bridge) publish(sid string, ev Event) {
	payload, ok := ev.payload()
	if !ok {
		return
	}
	if ev.Kind == EventReady {
		b.mu.Lock()
		b.ready[sid] = payload
		b.mu.Unlock()
	}
	frame := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		frame[key] = value
	}
	frame["sid"] = sid
	data, err := json.Marshal(frame)
	if err != nil {
		return
	}
	b.br.broadcast(data)
}

// broker fans frames out to connected SSE clients.
//
// A client that cannot keep up loses frames instead of stalling the worker: the
// alternative is blocking an agent turn on a paused browser tab.
type broker struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func newBroker() *broker {
	return &broker{clients: make(map[chan []byte]struct{})}
}

func (b *broker) subscribe() chan []byte {
	ch := make(chan []byte, 512)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broker) unsubscribe(ch chan []byte) {
	b.mu.Lock()
	if _, ok := b.clients[ch]; ok {
		delete(b.clients, ch)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *broker) broadcast(frame []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- frame:
		default:
		}
	}
}
