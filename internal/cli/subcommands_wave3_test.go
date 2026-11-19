package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/hooks"
	"github.com/2118505078xyh1/argent-go/internal/server"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testEnv isolates HOME/USERPROFILE so discovery never reads the real home.
func testEnv(t *testing.T) (map[string]string, string) {
	t.Helper()
	home := t.TempDir()
	return map[string]string{"HOME": home, "USERPROFILE": home}, home
}

func TestModelsListsProviderTemplates(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"models"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"anthropic", "openai", "default model"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestModelsJSONIsParseable(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"models", "--json"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var templates []map[string]any
	if err := json.Unmarshal(out.Bytes(), &templates); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(templates) < 3 {
		t.Fatalf("templates = %d", len(templates))
	}
}

func TestMCPListsConfiguredServersWithoutConnecting(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"), `{
		"mcpServers": {
			"files": {"command": "node", "args": ["server.js"]},
			"remote": {"url": "http://127.0.0.1:1/mcp"}
		}
	}`)
	env, _ := testEnv(t)
	ioState, out, errOut := newTestIO("", env)
	if code := RunWith(App{IO: ioState}, []string{"mcp", "-C", root, "--json"}); code != 0 {
		t.Fatalf("exit = %d err %q", code, errOut.String())
	}
	var servers []mcpServerView
	if err := json.Unmarshal(out.Bytes(), &servers); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %v", servers)
	}
	if servers[0].Name != "files" || servers[0].Transport != "stdio" || len(servers[0].Args) != 1 {
		t.Fatalf("stdio server = %+v", servers[0])
	}
	if servers[1].Name != "remote" || servers[1].Transport != "http" {
		t.Fatalf("http server = %+v", servers[1])
	}
}

func TestMCPReportsEmptyConfig(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	ioState, out, _ := newTestIO("", map[string]string{"HOME": home, "USERPROFILE": home})
	if code := RunWith(App{IO: ioState}, []string{"mcp", "-C", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "no MCP servers configured") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSkillsDiscoversProjectSkill(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".argent", "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: Ship the service\n---\nStep one.\n")
	env, _ := testEnv(t)
	ioState, out, _ := newTestIO("", env)
	if code := RunWith(App{IO: ioState}, []string{"skills", "-C", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "deploy") || !strings.Contains(out.String(), "Ship the service") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestCommandsListsMarkdownCommands(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".argent", "commands", "review.md"),
		"---\ndescription: Review the diff\n---\nReview $ARGUMENTS\n")
	env, _ := testEnv(t)
	ioState, out, _ := newTestIO("", env)
	if code := RunWith(App{IO: ioState}, []string{"commands", "-C", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "/review") || !strings.Contains(out.String(), "Review the diff") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestHooksReportsTrustState(t *testing.T) {
	root := t.TempDir()
	doc := filepath.Join(root, ".argent", "hooks.json")
	writeFile(t, doc, `{"hooks":{"PreToolUse":[{"matcher":"bash","hooks":[{"command":"echo checked"}]}]}}`)
	storage := t.TempDir()
	env, _ := testEnv(t)

	ioState, out, _ := newTestIO("", env)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"hooks", "-C", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "untrusted") || !strings.Contains(out.String(), "events=1") {
		t.Fatalf("out = %q", out.String())
	}

	trust := hooks.NewTrustStore()
	if _, err := trust.TrustFile(doc); err != nil {
		t.Fatal(err)
	}
	if err := trust.SaveFile(filepath.Join(storage, "hooks-trust.json")); err != nil {
		t.Fatal(err)
	}
	ioState, out, _ = newTestIO("", env)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"hooks", "-C", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "trusted") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestAuthShowsStatusAndLogoutClears(t *testing.T) {
	storage := t.TempDir()
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"login", "openai", "--api-key", "sk-secret"}); code != 0 {
		t.Fatalf("login exit = %d", code)
	}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"auth"}); code != 0 {
		t.Fatalf("auth exit = %d err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "openai") || !strings.Contains(out.String(), "configured") {
		t.Fatalf("out = %q", out.String())
	}
	if strings.Contains(out.String(), "sk-secret") {
		t.Fatalf("secret leaked: %q", out.String())
	}

	ioState, out, _ = newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"logout", "openai"}); code != 0 {
		t.Fatalf("logout exit = %d", code)
	}
	if !strings.Contains(out.String(), "logged out openai") {
		t.Fatalf("out = %q", out.String())
	}
	ioState, out, _ = newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"auth"}); code != 0 {
		t.Fatalf("auth exit = %d", code)
	}
	if !strings.Contains(out.String(), "no stored credentials") {
		t.Fatalf("after logout: %q", out.String())
	}
}

func TestPolicyReportsEffectiveModes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"), `{"sandboxMode":"read-only","yolo":true,"model":"m1"}`)
	env, _ := testEnv(t)
	ioState, out, _ := newTestIO("", env)
	if code := RunWith(App{IO: ioState}, []string{"policy", "-C", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"read-only", "auto", "mutating tools disabled", "m1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestAuditTailsTheNewestEntries(t *testing.T) {
	storage := t.TempDir()
	day := time.Now().UTC().Format("2006-01-02")
	writeFile(t, filepath.Join(storage, "audit", day+".jsonl"),
		`{"ts":"2026-01-01T00:00:00Z","tool":"read","decision":"allow"}
{"ts":"2026-01-01T00:00:01Z","tool":"write","decision":"ask","reason":"operator said yes"}
`)
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"audit", "--tail", "1"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "write") || strings.Contains(out.String(), "read") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestUsageSumsTurnRecords(t *testing.T) {
	store := &fakeStore{infos: []server.SessionInfo{{ID: "s1", Title: "one"}}, events: map[string][]server.SessionEvent{
		"s1": {
			{Kind: "turn", Data: json.RawMessage(`{"turns":2,"usage":{"prompt_tokens":10,"completion_tokens":4}}`)},
			{Kind: "turn", Data: json.RawMessage(`{"turns":1,"usage":{"prompt_tokens":5,"completion_tokens":2}}`)},
			{Kind: "tool", Data: json.RawMessage(`{"name":"read"}`)},
		},
	}}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"usage"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"3", "15", "6", "1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestUsageJSONTotals(t *testing.T) {
	store := &fakeStore{infos: []server.SessionInfo{{ID: "s1"}}, events: map[string][]server.SessionEvent{
		"s1": {{Kind: "turn", Data: json.RawMessage(`{"turns":1,"usage":{"prompt_tokens":7,"completion_tokens":3}}`)}},
	}}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"usage", "--json"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var report struct {
		Total struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"total"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("json: %v", err)
	}
	if report.Total.PromptTokens != 7 || report.Total.CompletionTokens != 3 {
		t.Fatalf("total = %+v", report.Total)
	}
}

func TestReplayPrintsTranscript(t *testing.T) {
	store := &fakeStore{events: map[string][]server.SessionEvent{
		"s1": {
			{ID: "e1", Kind: "user", Data: json.RawMessage(`{"text":"plan the migration"}`)},
			{ID: "e2", Kind: "assistant", Data: json.RawMessage(`{"text":"on it"}`)},
		},
	}}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"replay", "s1"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "user> plan the migration") || !strings.Contains(out.String(), "assistant> on it") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestCompletionScriptsMentionSurface(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			ioState, out, _ := newTestIO("", nil)
			if code := RunWith(App{IO: ioState}, []string{"completion", shell}); code != 0 {
				t.Fatalf("exit = %d", code)
			}
			for _, want := range []string{"--model", "sessions", "tui"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("%s missing %q", shell, want)
				}
			}
		})
	}
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"completion", "fish"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown shell") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestBugWritesDiagnosticsDump(t *testing.T) {
	storage := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "dump.txt")
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage, Version: "9.9.9"}, []string{"bug", "--out", outPath}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), outPath) {
		t.Fatalf("out = %q", out.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"argent-go 9.9.9", "go: go", "storage:", "tools:"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("dump missing %q:\n%s", want, data)
		}
	}
}

func TestListingFlagsRouteBeforeRunning(t *testing.T) {
	store := &fakeStore{infos: []server.SessionInfo{{ID: "s1", Title: "first"}}}
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"--list-sessions"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "first") {
		t.Fatalf("out = %q", out.String())
	}

	ioState, out, _ = newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"--list-models"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "anthropic") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestListProfilesUsesInjectedLister(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, Profiles: func(Options) ([]string, string, error) {
		return []string{"work", "home"}, "home", nil
	}}
	if code := RunWith(app, []string{"--list-profiles"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "* home") || !strings.HasPrefix(lines[1], "  work") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestTokensFlagUsesReporter(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, TokenReport: func(Options) (string, error) {
		return "payload breakdown\n", nil
	}}
	if code := RunWith(app, []string{"--tokens", "hello"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out.String() != "payload breakdown\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestTUISubcommandAndFlagReachInjectedFrontend(t *testing.T) {
	var got []Options
	frontend := func(_ context.Context, opts Options) error {
		got = append(got, opts)
		return nil
	}
	ioState, _, errOut := newTestIO("", nil)
	app := App{IO: ioState, TUI: frontend}
	if code := RunWith(app, []string{"tui", "--model", "m1"}); code != 0 {
		t.Fatalf("exit = %d err %q", code, errOut.String())
	}
	if code := RunWith(app, []string{"--tui", "--model", "m2", "ignored"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(got) != 2 || got[0].Model != "m1" || got[1].Model != "m2" || !got[1].TUI {
		t.Fatalf("frontend got %+v", got)
	}
}

func TestTUIMissingFrontendFails(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"tui"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "TUI not configured") {
		t.Fatalf("err = %q", errOut.String())
	}
}
