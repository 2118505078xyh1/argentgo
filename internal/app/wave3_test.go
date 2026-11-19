package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/session"
	"github.com/2118505078xyh1/argent-go/internal/tui"
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

func testEnv(t *testing.T) (func(string) string, string) {
	t.Helper()
	home := t.TempDir()
	pairs := map[string]string{"HOME": home, "USERPROFILE": home}
	return envFrom(pairs), home
}

func TestResolveHonoursProviderProfileAndBudget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"),
		`{"profiles":{"work":{"model":"prof-model","maxTurns":7}}}`)
	env, home := testEnv(t)

	res, err := Resolve(cli.IO{Env: env}, cli.Options{
		Cwd:         root,
		Profile:     "work",
		Provider:    "anthropic",
		MaxTokens:   100,
		MaxCost:     0.25,
		MaxSeconds:  5,
		PriceInput:  1,
		PriceOutput: 2,
		AddDirs:     []string{"extra"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Model != "prof-model" || res.MaxTurns != 7 {
		t.Fatalf("profile not applied: model=%q turns=%d", res.Model, res.MaxTurns)
	}
	if res.Profile != "work" {
		t.Errorf("profile = %q", res.Profile)
	}
	if res.Provider != "anthropic" || res.Protocol != "anthropic-messages" {
		t.Fatalf("provider = %q protocol = %q", res.Provider, res.Protocol)
	}
	if !strings.Contains(res.BaseURL, "anthropic.com") {
		t.Errorf("base URL = %q", res.BaseURL)
	}
	if res.MaxTokens != 100 || res.MaxCostUSD != 0.25 || res.MaxSeconds != 5 {
		t.Errorf("budget = %d/%v/%d", res.MaxTokens, res.MaxCostUSD, res.MaxSeconds)
	}
	if price := res.Prices[res.Model]; price.Input != 1 || price.Output != 2 {
		t.Errorf("prices = %v", res.Prices)
	}
	if len(res.AddDirs) != 1 || res.AddDirs[0] != filepath.Join(root, "extra") {
		t.Errorf("add dirs = %v", res.AddDirs)
	}
	if res.Storage != filepath.Join(home, ".argent-go") {
		t.Errorf("storage = %q", res.Storage)
	}
}

func TestResolveUnknownProviderFails(t *testing.T) {
	env, _ := testEnv(t)
	_, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir(), Provider: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveUnknownProfileFails(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"), `{"profiles":{"work":{}}}`)
	env, _ := testEnv(t)
	_, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, Profile: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveOutputSchemaAndThinking(t *testing.T) {
	root := t.TempDir()
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{
		Cwd:          root,
		Thinking:     "high",
		OutputSchema: `{"type":"object","properties":{"ok":{"type":"boolean"}}}`,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(res.System, "JSON Schema") || !strings.Contains(res.System, `"ok"`) {
		t.Fatalf("schema missing from system prompt: %q", res.System)
	}
	if !strings.Contains(res.System, "Reasoning effort: high") {
		t.Fatalf("thinking missing from system prompt: %q", res.System)
	}

	bad := filepath.Join(root, "schema.json")
	writeFile(t, bad, "{not json")
	if _, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, OutputSchema: bad}); err == nil {
		t.Fatal("invalid schema file accepted")
	}
	if _, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, OutputSchema: "missing.json"}); err == nil {
		t.Fatal("missing schema file accepted")
	}
}

func TestTokenReportIncludesOverheadAndLimits(t *testing.T) {
	env, _ := testEnv(t)
	report, err := TokenReport(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir(), MaxTurns: 3, MaxTokens: 500})
	if err != nil {
		t.Fatalf("TokenReport: %v", err)
	}
	for _, want := range []string{"system prompt", "tools:", "fixed request overhead", "turns=3", "tokens=500"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %q in\n%s", want, report)
		}
	}
}

func TestListProfilesReportsActiveProfile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"),
		`{"profiles":{"work":{},"home":{}},"profile":"work"}`)
	env, _ := testEnv(t)
	names, active, err := ListProfiles(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if strings.Join(names, ",") != "home,work" || active != "work" {
		t.Fatalf("names = %v active = %q", names, active)
	}
}

func TestToolsetAdvertisesSkillsAndRoutesUseSkill(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".argent", "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: Ship the service\n---\nStep one.\n")
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	store, err := session.Open(filepath.Join(res.Storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("test")
	if err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	set := newToolset(context.Background(), cli.IO{Env: env, Out: stdout}, res, sess, nil)
	defer set.Close()

	names := specNames(set.Specs())
	if !contains(names, "read") || !contains(names, "use_skill") {
		t.Fatalf("specs = %v", names)
	}
	out, err := set.Run(context.Background(), "use_skill", `{"name":"deploy"}`)
	if err != nil {
		t.Fatalf("use_skill: %v", err)
	}
	if !strings.Contains(out, "Step one.") || !strings.Contains(out, "source:") {
		t.Fatalf("use_skill output = %q", out)
	}
}

func TestToolsetSkipsBrokenMCPServerOnce(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"),
		`{"mcpServers":{"broken":{"command":"argent-go-no-such-binary-xyz"}}}`)
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	store, err := session.Open(filepath.Join(res.Storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("test")
	if err != nil {
		t.Fatal(err)
	}
	errOut := &bytes.Buffer{}
	set := newToolset(context.Background(), cli.IO{Env: env, Err: errOut}, res, sess, nil)
	defer set.Close()

	if !strings.Contains(errOut.String(), "skipped") {
		t.Fatalf("no fail-soft note: %q", errOut.String())
	}
	if strings.Contains(strings.Join(specNames(set.Specs()), ","), "broken__") {
		t.Fatalf("broken server contributed tools: %v", specNames(set.Specs()))
	}
}

func TestTUIApproverModalAllowsAndDenies(t *testing.T) {
	for _, answer := range []struct {
		rune rune
		want bool
	}{{'y', true}, {'n', false}} {
		model := tui.New(tui.Config{})
		approver := NewApprover()
		approver.attach(model)

		type result struct {
			ok  bool
			err error
		}
		done := make(chan result, 1)
		go func() {
			ok, err := approver.Ask(context.Background(), "write", `{"path":"x"}`)
			done <- result{ok: ok, err: err}
		}()
		waitForApproval(t, model)
		model.HandleKey(tui.Key{Kind: tui.KindRune, Rune: answer.rune})
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("Ask: %v", got.err)
			}
			if got.ok != answer.want {
				t.Fatalf("rune %q answered %v, want %v", answer.rune, got.ok, answer.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("approver did not return")
		}
	}
}

func TestTUIApproverHonoursCancellation(t *testing.T) {
	approver := NewApprover()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := approver.Ask(ctx, "write", "{}"); err == nil {
		t.Fatal("cancelled approval returned no error")
	}
}

func TestTUIFrontendRejectsBadOptionsBeforeRunning(t *testing.T) {
	env, _ := testEnv(t)
	err := TUI(cli.IO{Env: env})(context.Background(), cli.Options{Cwd: t.TempDir(), Provider: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("err = %v", err)
	}
}

func waitForApproval(t *testing.T, model *tui.Model) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := model.PendingApproval(); ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("approval modal never appeared")
}

func specNames(specs []llm.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Function.Name)
	}
	return names
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
