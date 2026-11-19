package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner is one tab's worker: it records commands and lets the test emit
// events and approvals by writing to its channels.
type fakeRunner struct {
	events    chan Event
	approvals chan Approval
	commands  chan Command
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		events:    make(chan Event, 64),
		approvals: make(chan Approval, 8),
		commands:  make(chan Command, 64),
	}
}

func (f *fakeRunner) Events() <-chan Event       { return f.events }
func (f *fakeRunner) Approvals() <-chan Approval { return f.approvals }
func (f *fakeRunner) Commands() chan<- Command   { return f.commands }

// runnerFactory keeps every tab's fake so tests can inspect each one.
type runnerFactory struct {
	mu      sync.Mutex
	runners []*fakeRunner
}

func (f *runnerFactory) newRunner() Runner {
	runner := newFakeRunner()
	f.mu.Lock()
	f.runners = append(f.runners, runner)
	f.mu.Unlock()
	return runner
}

func (f *runnerFactory) at(index int) *fakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.runners) {
		return nil
	}
	return f.runners[index]
}

func (f *runnerFactory) last() *fakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runners) == 0 {
		return nil
	}
	return f.runners[len(f.runners)-1]
}

type testEnv struct {
	t       *testing.T
	bridge  *bridge
	srv     *httptest.Server
	factory *runnerFactory
}

func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()
	factory := &runnerFactory{}
	if opts.NewRunner == nil {
		opts.NewRunner = factory.newRunner
	}
	instance, err := newBridge(opts)
	if err != nil {
		t.Fatalf("newBridge: %v", err)
	}
	srv := httptest.NewServer(instance)
	t.Cleanup(func() {
		srv.Close()
		instance.Close()
	})
	return &testEnv{t: t, bridge: instance, srv: srv, factory: factory}
}

func (e *testEnv) rpc(command, body string) (int, []byte) {
	e.t.Helper()
	request, err := http.NewRequest(http.MethodPost, e.srv.URL+"/rpc/"+command, strings.NewReader(body))
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+e.bridge.token)
	request.Header.Set("Content-Type", "application/json")
	return do(e.t, request)
}

func (e *testEnv) object(command, body string) map[string]any {
	e.t.Helper()
	status, data := e.rpc(command, body)
	if status != http.StatusOK {
		e.t.Fatalf("%s: status %d: %s", command, status, data)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		e.t.Fatalf("%s: decode %q: %v", command, data, err)
	}
	return result
}

func (e *testEnv) text(command, body string) string {
	e.t.Helper()
	status, data := e.rpc(command, body)
	if status != http.StatusOK {
		e.t.Fatalf("%s: status %d: %s", command, status, data)
	}
	var result string
	if err := json.Unmarshal(data, &result); err != nil {
		e.t.Fatalf("%s: decode %q: %v", command, data, err)
	}
	return result
}

func (e *testEnv) commandError(command, body string) string {
	e.t.Helper()
	message, _ := e.object(command, body)["error"].(string)
	return message
}

func (e *testEnv) get(target string, authorized bool) (int, string) {
	e.t.Helper()
	request, err := http.NewRequest(http.MethodGet, e.srv.URL+target, nil)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	if authorized {
		request.Header.Set("Authorization", "Bearer "+e.bridge.token)
	}
	status, data := do(e.t, request)
	return status, string(data)
}

func do(t *testing.T, request *http.Request) (int, []byte) {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do %s: %v", request.URL, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, data
}

func waitFor(t *testing.T, label string, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

func TestAuthRejectsEverythingWithoutTheToken(t *testing.T) {
	env := newTestEnv(t, Options{})

	if status, _ := env.get("/", false); status != http.StatusUnauthorized {
		t.Fatalf("GET / without token = %d, want 401", status)
	}
	bad := "/?token=" + strings.Repeat("0", 64)
	if status, _ := env.get(bad, false); status != http.StatusUnauthorized {
		t.Fatalf("GET / with wrong token = %d, want 401", status)
	}
	status, body := env.get("/", true)
	if status != http.StatusOK {
		t.Fatalf("GET / with header = %d: %s", status, body)
	}
	if !strings.Contains(body, "window.__ARGENT_TOKEN__=\""+env.bridge.token+"\"") {
		t.Fatalf("index.html did not receive the token: %s", body)
	}
	// The query form is for GETs only; rpc must use the header.
	request, _ := http.NewRequest(http.MethodPost,
		env.srv.URL+"/rpc/get_state?token="+env.bridge.token, strings.NewReader("{}"))
	if status, _ := do(t, request); status != http.StatusUnauthorized {
		t.Fatalf("POST /rpc with query token = %d, want 401", status)
	}
	if status, _ := env.get("/missing.js", true); status != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", status)
	}
	if status, _ := env.get("/deep/link", true); status != http.StatusOK {
		t.Fatalf("SPA fallback = %d, want 200", status)
	}
}

func TestIndexCookieAuthorizesStaticAssetsButNotRPC(t *testing.T) {
	env := newTestEnv(t, Options{})

	request, _ := http.NewRequest(http.MethodGet,
		env.srv.URL+"/?token="+env.bridge.token, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET / with query token = %d: %s", response.StatusCode, body)
	}
	var tokenCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == "argent_token" {
			tokenCookie = cookie
		}
	}
	if tokenCookie == nil || tokenCookie.Value != env.bridge.token {
		t.Fatalf("index response did not set the asset cookie: %v", response.Cookies())
	}
	start := strings.Index(string(body), `src="/assets/`)
	if start < 0 {
		t.Fatalf("index has no module asset: %s", body)
	}
	asset := string(body)[start+len(`src="`):]
	end := strings.Index(asset, `"`)
	if end < 0 {
		t.Fatalf("bad module asset reference: %q", asset)
	}
	assetRequest, _ := http.NewRequest(http.MethodGet, env.srv.URL+asset[:end], nil)
	assetRequest.AddCookie(tokenCookie)
	if status, data := do(t, assetRequest); status != http.StatusOK {
		t.Fatalf("asset with cookie = %d: %s", status, data)
	}

	rpcRequest, _ := http.NewRequest(http.MethodPost,
		env.srv.URL+"/rpc/get_state", strings.NewReader("{}"))
	rpcRequest.AddCookie(tokenCookie)
	if status, _ := do(t, rpcRequest); status != http.StatusUnauthorized {
		t.Fatalf("POST /rpc with cookie only = %d, want 401", status)
	}
}

func TestGetStateCarriesActiveTabReadyAndChannel(t *testing.T) {
	env := newTestEnv(t, Options{Version: "v1"})
	state := env.object("get_state", "")
	if state["sid"] != "1" {
		t.Fatalf("sid = %v, want 1", state["sid"])
	}
	if state["ready"] != nil {
		t.Fatalf("ready before the worker's frame = %v, want null", state["ready"])
	}
	if state["channel"] != "stable" {
		t.Fatalf("channel = %v, want stable", state["channel"])
	}

	env.factory.last().events <- Event{
		Kind: EventReady,
		Ready: &Ready{
			Model:      "m",
			Level:      "low",
			ContextMax: 128_000,
			Models:     []ModelInfo{{ID: "m", ContextLength: 960_000}},
			Thinking:   []string{"low", "high"},
			Cwd:        "/work",
			Provider:   "local",
			Protocol:   "openai-chat-completions",
		},
	}
	waitFor(t, "ready frame", 2*time.Second, func() bool {
		ready, ok := env.object("get_state", "")["ready"].(map[string]any)
		return ok && ready["model"] == "m"
	})
	ready := env.object("get_state", "")["ready"].(map[string]any)
	if ready["kind"] != "ready" || ready["contextMax"] != float64(128_000) {
		t.Fatalf("ready shape = %v", ready)
	}
	models := ready["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["contextLength"] != float64(960_000) {
		t.Fatalf("ready models = %v", models)
	}
	if status, body := env.get("/healthz", true); status != http.StatusOK ||
		!strings.Contains(string(body), "v1") {
		t.Fatalf("healthz = %d %s", status, body)
	}
}

func TestUnsupportedCommandsAnswerImmediately(t *testing.T) {
	env := newTestEnv(t, Options{})
	if message := env.commandError("pty_list", ""); !strings.Contains(message, "not supported in the Go build") {
		t.Fatalf("pty_list error = %q", message)
	}
	if message := env.commandError("undo", ""); !strings.Contains(message, "not supported") {
		t.Fatalf("undo error = %q", message)
	}
	if message := env.commandError("totally_unknown", ""); !strings.Contains(message, "unknown command") {
		t.Fatalf("unknown command error = %q", message)
	}
	if message := env.commandError("steer", `{"sid":"1","text":"   "}`); message != "steering text is empty" {
		t.Fatalf("empty steer error = %q", message)
	}
}

func TestTelemetryPreferencesRoundTripInStorageDir(t *testing.T) {
	dir := t.TempDir()
	env := newTestEnv(t, Options{StorageDir: dir})
	prefs := env.object("get_telemetry_preferences", "")
	if prefs["enabled"] != false || prefs["consent"] != false {
		t.Fatalf("default preferences = %v", prefs)
	}
	status, body := env.rpc("set_telemetry_preferences", `{"enabled":true,"consent":true}`)
	if status != http.StatusOK || strings.Contains(string(body), "error") {
		t.Fatalf("set preferences = %d %s", status, body)
	}
	prefs = env.object("get_telemetry_preferences", "")
	if prefs["enabled"] != true || prefs["consent"] != true {
		t.Fatalf("saved preferences = %v", prefs)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatalf("settings.json missing: %v", err)
	}
}

func TestListFilesOnlyReturnsWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "sub/b.md", "node_modules/c.txt", ".hidden/d.txt"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all := listFiles(root, "")
	if strings.Contains(strings.Join(all, ","), "node_modules") ||
		strings.Contains(strings.Join(all, ","), ".hidden") || len(all) != 2 {
		t.Fatalf("listFiles = %v", all)
	}
	filtered := listFiles(root, "B.MD")
	if len(filtered) != 1 || filtered[0] != "sub/b.md" {
		t.Fatalf("filtered listFiles = %v", filtered)
	}
}

func TestResolveRejectsPathsOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"../escape.txt", "/etc/passwd", "sub/../../escape.txt", "  "} {
		if _, err := resolve(root, relative, false); err == nil {
			t.Fatalf("resolve(%q) unexpectedly succeeded", relative)
		}
	}
	target, err := resolve(root, "sub/file.txt", false)
	if err != nil || !strings.HasPrefix(target, root) {
		t.Fatalf("resolve valid path = %q, %v", target, err)
	}
}
