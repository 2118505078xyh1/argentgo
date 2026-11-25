package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/server"
)

type fakeStore struct {
	infos   []server.SessionInfo
	events  map[string][]server.SessionEvent
	deleted []string
	err     error
}

func (f *fakeStore) List() ([]server.SessionInfo, error) {
	return f.infos, f.err
}

func (f *fakeStore) Load(id string) ([]server.SessionEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	events, ok := f.events[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return events, nil
}

func (f *fakeStore) Delete(id string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func TestSessionsListSortsByUpdated(t *testing.T) {
	now := time.Now()
	store := &fakeStore{infos: []server.SessionInfo{
		{ID: "old", Title: "older", Updated: now.Add(-time.Hour)},
		{ID: "new", Title: "newer", Updated: now},
	}}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"sessions", "list"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "new\t") || !strings.HasPrefix(lines[1], "old\t") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSessionsShow(t *testing.T) {
	store := &fakeStore{events: map[string][]server.SessionEvent{
		"s1": {{ID: "e1", Kind: "user", Data: json.RawMessage(`{"text":"hi"}`)}},
	}}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"sessions", "show", "s1"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"kind":"user"`) {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSessionsShowMissing(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, Sessions: &fakeStore{}}, []string{"sessions", "show", "nope"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "not found") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestSessionsRm(t *testing.T) {
	store := &fakeStore{}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"sessions", "rm", "s1"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "s1" {
		t.Fatalf("deleted = %v", store.deleted)
	}
	if !strings.Contains(out.String(), "removed s1") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSessionsFind(t *testing.T) {
	store := &fakeStore{infos: []server.SessionInfo{
		{ID: "a", Title: "migration work"},
		{ID: "b", Title: "other"},
	}}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"sessions", "find", "migr"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "migration work") || strings.Contains(out.String(), "other") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSessionsUnknownCommand(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: &fakeStore{}}, []string{"sessions", "nope"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown sessions command") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestDoctorReportsOK(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	dir := t.TempDir()
	ioState, out, _ := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"doctor", "--base-url", provider.URL})
	if code != 0 {
		t.Fatalf("exit = %d, out %q", code, out.String())
	}
	for _, want := range []string{"config", "storage", "provider", "git", "go"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestDoctorInvalidConfigFails(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer provider.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ioState, out, _ := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"doctor", "--base-url", provider.URL})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), statusFail) {
		t.Fatalf("out = %q", out.String())
	}
}

func TestDoctorRejectsUnknownFlag(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"doctor", "--nope"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown flag") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestConfigSetGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ioState, out, errOut := newTestIO("", nil)
	app := App{IO: ioState, StorageDir: dir}
	if code := RunWith(app, []string{"config", "set", "providers.x.apiKey", "sk-1"}); code != 0 {
		t.Fatalf("set exit = %d, err %q", code, errOut.String())
	}
	out.Reset()
	if code := RunWith(app, []string{"config", "get", "providers.x.apiKey"}); code != 0 {
		t.Fatalf("get exit = %d", code)
	}
	if out.String() != "sk-1\n" {
		t.Fatalf("out = %q", out.String())
	}
	if code := RunWith(app, []string{"config", "get", "missing.key"}); code != 1 {
		t.Fatalf("missing key exit = %d, want 1", code)
	}
}

func TestConfigSetParsesJSONValues(t *testing.T) {
	dir := t.TempDir()
	ioState, _, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"config", "set", "maxTurns", "20"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	cfg, err := loadConfigFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg["maxTurns"].(float64); !ok || got != 20 {
		t.Fatalf("maxTurns = %#v", cfg["maxTurns"])
	}
}

func TestLoginStoresKeyWithoutEcho(t *testing.T) {
	dir := t.TempDir()
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"login", "openai", "--api-key", "sk-secret"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if strings.Contains(out.String(), "sk-secret") || strings.Contains(errOut.String(), "sk-secret") {
		t.Fatalf("api key echoed: out %q err %q", out.String(), errOut.String())
	}
	cfg, err := loadConfigFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := cfg["providers"].(map[string]any)
	openai, _ := providers["openai"].(map[string]any)
	if openai["apiKey"] != "sk-secret" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoginReadsKeyFromStdin(t *testing.T) {
	dir := t.TempDir()
	ioState, _, _ := newTestIO("sk-stdin\n", nil)
	code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"login", "anthropic"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	cfg, err := loadConfigFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := cfg["providers"].(map[string]any)
	entry, _ := providers["anthropic"].(map[string]any)
	if entry["apiKey"] != "sk-stdin" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoginRequiresProvider(t *testing.T) {
	ioState, _, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

type fakeEvalRunner struct {
	report EvalReport
	err    error
}

func (f fakeEvalRunner) RunScenario(context.Context, string) (EvalReport, error) {
	return f.report, f.err
}

func writeEvalFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "eval.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvalPasses(t *testing.T) {
	path := writeEvalFile(t, `[{"prompt":"list files","expect_tool":"glob","expect_text":"done"}]`)
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, Eval: fakeEvalRunner{report: EvalReport{Text: "all done", Tools: []string{"glob"}}}}
	if code := RunWith(app, []string{"eval", path}); code != 0 {
		t.Fatalf("exit = %d, out %q", code, out.String())
	}
	if !strings.Contains(out.String(), "1/1 scenarios passed") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestEvalFailureExitsOne(t *testing.T) {
	path := writeEvalFile(t, `[{"prompt":"list files","expect_tool":"glob"},{"prompt":"read","expect_text":"nope"}]`)
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, Eval: fakeEvalRunner{report: EvalReport{Text: "done", Tools: []string{"read"}}}}
	if code := RunWith(app, []string{"eval", path}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "0/2 scenarios passed") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestEvalRequiresRunner(t *testing.T) {
	path := writeEvalFile(t, `[]`)
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"eval", path}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "eval runner not configured") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestFSStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"id":"e1","kind":"user","ts":"2026-01-02T03:04:05Z","data":{"text":"plan the migration"}}` + "\n" +
		`{"id":"e2","parent":"e1","kind":"assistant","ts":"2026-01-02T03:04:06Z","data":{"text":"ok"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "abc.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &fsStore{dir: dir}
	infos, err := store.List()
	if err != nil || len(infos) != 1 {
		t.Fatalf("infos = %v, err %v", infos, err)
	}
	if infos[0].ID != "abc" || infos[0].Title != "plan the migration" {
		t.Fatalf("info = %+v", infos[0])
	}

	events, err := store.Load("abc")
	if err != nil || len(events) != 2 || events[1].Parent != "e1" {
		t.Fatalf("events = %v, err %v", events, err)
	}

	if err := store.Delete("abc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load("abc"); err == nil {
		t.Fatal("session still loadable after delete")
	}
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestFSStoreMissingDirIsEmpty(t *testing.T) {
	store := &fsStore{dir: filepath.Join(t.TempDir(), "absent")}
	infos, err := store.List()
	if err != nil || len(infos) != 0 {
		t.Fatalf("infos = %v, err %v", infos, err)
	}
}
