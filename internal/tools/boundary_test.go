package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestMutationPathsOnlyReportsInWorkspaceMutatingPaths(t *testing.T) {
	root := t.TempDir()
	registry := tools.New(root, true)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := registry.MutationPaths("write", `{"path":"src/a.txt"}`)
	if len(got) != 1 || got[0] != "src/a.txt" {
		t.Fatalf("mutation paths = %v", got)
	}
	if paths := registry.MutationPaths("read", `{"path":"src/a.txt"}`); len(paths) != 0 {
		t.Fatalf("read-only tool reported paths: %v", paths)
	}
	outside, err := filepath.Abs(filepath.Join(root, "..", "outside.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"path":"../outside.txt"}`, `{"path":"` + filepath.ToSlash(outside) + `"}`, `{`, `{}`} {
		if paths := registry.MutationPaths("write", args); len(paths) != 0 {
			t.Fatalf("write %s reported paths %v", args, paths)
		}
	}
	paths := registry.MutationPaths("edit", `{"paths":["src/b.txt","../escape","src/a.txt"]}`)
	if len(paths) != 2 || paths[0] != "src/a.txt" || paths[1] != "src/b.txt" {
		t.Fatalf("paths array = %v", paths)
	}

	snapshots, err := tools.SnapshotMutations(root, []string{"src/a.txt", "src/missing.txt"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshots) != 2 || !snapshots[0].Exists || snapshots[0].SHA256 == "" {
		t.Fatalf("existing snapshot = %+v", snapshots)
	}
	if snapshots[1].Exists || snapshots[1].SHA256 != "" {
		t.Fatalf("missing snapshot = %+v", snapshots[1])
	}
	if _, err := tools.SnapshotMutations(root, []string{"../escape.txt"}); err == nil {
		t.Fatal("snapshot accepted a path escape")
	}
}

func TestMultiEditAcceptsOldTextNewTextAliases(t *testing.T) {
	root := t.TempDir()
	registry := tools.New(root, true)
	path := filepath.Join(root, "aliases.txt")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, registry, "multi_edit", `{"path":"aliases.txt","edits":[{"old_text":"alpha","new_text":"one"},{"old":"beta","new":"two"}]}`)
	if !strings.Contains(out, "2 edits") {
		t.Fatalf("multi_edit = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "one two" {
		t.Fatalf("file = %q err=%v", data, err)
	}
	for _, spec := range registry.Specs() {
		if spec.Function.Name == "multi_edit" && !strings.Contains(string(spec.Function.Parameters), "old_text") {
			t.Fatal("multi_edit schema does not advertise old_text")
		}
	}
}

type recordingGuard struct{ err error }

func (g recordingGuard) Check(context.Context, string, json.RawMessage) error { return g.err }

func TestBackgroundAfterMSReportsWiringStatus(t *testing.T) {
	root := t.TempDir()
	registry := tools.New(root, true)
	var reports []tools.BackgroundReport
	registry.SetBackgroundReporter(func(r tools.BackgroundReport) { reports = append(reports, r) })

	_, _ = registry.Run(context.Background(), "bash", `{"command":"echo quick"}`)
	if len(reports) != 0 {
		t.Fatalf("reported a call without background_after_ms: %+v", reports)
	}
	_, _ = registry.Run(context.Background(), "bash", `{"command":"echo quick","background_after_ms":25}`)
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	if reports[0].RequestedMS != 25 || reports[0].JobsAvailable || reports[0].Status != "unavailable" {
		t.Fatalf("report = %+v", reports[0])
	}

	jobRegistry, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry.SetJobs(jobRegistry)
	_, _ = registry.Run(context.Background(), "bash", `{"command":"echo quick","background_after_ms":25}`)
	if len(reports) != 2 || !reports[1].JobsAvailable || reports[1].Status != "enabled" {
		t.Fatalf("enabled report = %+v", reports)
	}
}

func TestToolSearchRanksNameMatchesBeforeDescriptionMatches(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	lines := strings.Split(mustRun(t, registry, "tool_search", `{"query":"web","limit":3}`), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "web_") {
		t.Fatalf("ranking = %v", lines)
	}
	lines = strings.Split(mustRun(t, registry, "tool_search", `{"query":"search web","limit":3}`), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "web_search:") {
		t.Fatalf("multi-word ranking = %v", lines)
	}
	byDescription := mustRun(t, registry, "tool_search", `{"query":"IANA"}`)
	if !strings.Contains(byDescription, "current_time:") {
		t.Fatalf("description match lost: %q", byDescription)
	}
	if out := mustRun(t, registry, "tool_search", `{"query":"web iana"}`); !strings.Contains(out, "no tools match") {
		t.Fatalf("unrelated tokens must not both match: %q", out)
	}
}

func TestSubmitPlanValidatesBeforeWritingState(t *testing.T) {
	stateDir := t.TempDir()
	registry := tools.New(t.TempDir(), true)
	registry.SetSessionDir(stateDir)
	planPath := filepath.Join(stateDir, "plan.json")

	tooMany, err := json.Marshal(map[string]any{"steps": make([]string, 101)})
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		string(tooMany),
		`{"steps":["` + strings.Repeat("x", 501) + `"]}`,
		`{"steps":["first\nsecond"]}`,
		`{"steps":["repeat","REPEAT"]}`,
		`{"steps":["ok"],"status":"bad\nstatus"}`,
	}
	for _, args := range cases {
		if _, err := registry.Run(context.Background(), "submit_plan", args); err == nil {
			t.Fatalf("submit_plan accepted invalid input: %.80s", args)
		}
	}
	if _, err := os.Stat(planPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid plan wrote state: stat err=%v", err)
	}
	if out := mustRun(t, registry, "submit_plan", `{}`); out != "plan is empty" {
		t.Fatalf("plan after rejected writes = %q", out)
	}
}

func TestWebSearchReportsUnconfiguredEndpointWithoutNetwork(t *testing.T) {
	t.Setenv("ARGENT_WEB_SEARCH_URL", "")
	for _, args := range []string{`{"query":"go"}`, `{"query":"go","limit":5}`} {
		_, err := tools.New(t.TempDir(), true).Run(context.Background(), "web_search", args)
		if err == nil || !strings.Contains(err.Error(), "ARGENT_WEB_SEARCH_URL") || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("unconfigured web_search err = %v", err)
		}
	}
	t.Setenv("ARGENT_WEB_SEARCH_URL", "   ")
	if _, err := tools.New(t.TempDir(), true).Run(context.Background(), "web_search", `{"query":"go"}`); err == nil {
		t.Fatal("whitespace-only endpoint must count as unconfigured")
	}
}

func TestRepoMapCapsLargeReposAndMarksTruncation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 120; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, tools.New(root, true), "repo_map", `{}`)
	if !strings.Contains(out, "files (80):") || !strings.Contains(out, "... [capped at 80 files]") {
		t.Fatalf("repo_map did not report truncation:\n%s", out)
	}
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  f") {
			count++
		}
	}
	if count != 80 {
		t.Fatalf("mapped files = %d, want 80", count)
	}
	if !strings.Contains(out, "  f000.go:1") || strings.Contains(out, "  f119.go") {
		t.Fatalf("wrong file window:\n%s", out)
	}
	if strings.Contains(out, ".git") {
		t.Fatalf("ignored directory appeared in map:\n%s", out)
	}
}

func TestGuardBlocksToolBeforeItTouchesTheWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "protected.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.New(root, true)
	registry.SetGuard(recordingGuard{err: errors.New("blocked by a PreToolUse hook")})

	_, err := registry.Run(context.Background(), "write", `{"path":"protected.txt","content":"changed"}`)
	if err == nil || !strings.Contains(err.Error(), "blocked by a PreToolUse hook") {
		t.Fatalf("guard error was not returned to the caller: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "original" {
		t.Fatalf("blocked tool changed the file: %q err=%v", data, readErr)
	}
}
