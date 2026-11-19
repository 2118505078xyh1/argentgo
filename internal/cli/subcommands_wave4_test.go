package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/jobs"
	"github.com/2118505078xyh1/argent-go/internal/update"
)

// TestWave4SubcommandsDispatched proves each new name reaches its own handler:
// --help is answered by the command without falling through to runMain.
func TestWave4SubcommandsDispatched(t *testing.T) {
	for _, name := range []string{"workflow", "telemetry", "attach", "logs", "jobs", "follow", "stop", "board", "update"} {
		t.Run(name, func(t *testing.T) {
			if _, ok := subcommands[name]; !ok {
				t.Fatalf("%s is missing from the dispatch map", name)
			}
			ioState, out, errOut := newTestIO("", nil)
			if code := RunWith(App{IO: ioState}, []string{name, "--help"}); code != 0 {
				t.Fatalf("exit = %d, want 0 (err %q)", code, errOut.String())
			}
			if !strings.Contains(strings.ToLower(out.String()), "usage: argent "+name) &&
				!strings.Contains(strings.ToLower(out.String()), "usage: argent attach") {
				t.Fatalf("stdout = %q", out.String())
			}
		})
	}
}

func writeWorkflowDef(t *testing.T, path, name string) {
	t.Helper()
	writeFile(t, path, `{"name":"`+name+`","steps":[{"name":"one","prompt":"say hello"}]}`)
}

func seedJob(t *testing.T, storage, id string, status jobs.Status, log string) string {
	t.Helper()
	dir := filepath.Join(storage, "jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, id+".log")
	writeFile(t, logPath, log)
	index := map[string]any{
		"next": 1,
		"jobs": []map[string]any{{
			"id":        id,
			"command":   "sleep",
			"args":      []string{"5"},
			"startedAt": time.Now().UTC().Format(time.RFC3339),
			"status":    string(status),
			"exitCode":  0,
			"logPath":   logPath,
			"pid":       999999,
		}},
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "jobs.json"), string(data))
	return logPath
}

func TestWorkflowRunHappyPath(t *testing.T) {
	def := filepath.Join(t.TempDir(), "wf.json")
	writeWorkflowDef(t, def, "greet")
	factory := func(Options) (Runner, error) { return &stubRunner{reply: "hello back"}, nil }
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"workflow", "run", def})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	for _, want := range []string{"ok   one", "hello back", "workflow greet: ok"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestWorkflowRunWithoutRunnerFails(t *testing.T) {
	def := filepath.Join(t.TempDir(), "wf.json")
	writeWorkflowDef(t, def, "greet")
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState}, []string{"workflow", "run", def})
	if code != 1 || !strings.Contains(errOut.String(), "no runner configured") {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
}

func TestWorkflowListHappyPath(t *testing.T) {
	storage := t.TempDir()
	writeWorkflowDef(t, filepath.Join(storage, "workflows", "wf.json"), "greet")
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"workflow", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "greet") || !strings.Contains(out.String(), "1 steps") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestTelemetryOffPersistsNoOpPreference(t *testing.T) {
	storage := t.TempDir()
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"telemetry", "off"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "disabled in personal build") {
		t.Fatalf("out = %q", out.String())
	}
	data, err := os.ReadFile(filepath.Join(storage, "telemetry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pref telemetryPreference
	if err := json.Unmarshal(data, &pref); err != nil {
		t.Fatal(err)
	}
	if pref.Enabled || pref.Mode != "off" {
		t.Fatalf("preference = %+v", pref)
	}
}

func TestTelemetryStatusReportsDisabled(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"telemetry", "status"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "disabled in personal build") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestAttachAndLogsHappyPath(t *testing.T) {
	storage := t.TempDir()
	seedJob(t, storage, "job1", jobs.StatusExited, "first\nsecond\nthird\n")
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"attach", "job1", "-n", "2"}); code != 0 {
		t.Fatalf("attach exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "second\nthird") || strings.Contains(out.String(), "first") {
		t.Fatalf("attach out = %q", out.String())
	}
	ioState, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"logs"}); code != 0 {
		t.Fatalf("logs exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "job1") || !strings.Contains(out.String(), "job1.log") {
		t.Fatalf("logs out = %q", out.String())
	}
}

func TestFollowPrintsHistoryAndStopsOnCancel(t *testing.T) {
	storage := t.TempDir()
	seedJob(t, storage, "job1", jobs.StatusExited, "line one\nline two\n")
	ioState, out, errOut := newTestIO("", nil)
	app := App{IO: ioState, StorageDir: storage}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := runFollow(app, ctx, []string{"job1", "-n", "2"}); code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "line two") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestStopTerminalJobReportsState(t *testing.T) {
	storage := t.TempDir()
	seedJob(t, storage, "job1", jobs.StatusExited, "done\n")
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"stop", "job1"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "already exited") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestBoardHappyPath(t *testing.T) {
	storage := t.TempDir()
	seedJob(t, storage, "job1", jobs.StatusExited, "x\n")
	writeFile(t, filepath.Join(storage, "automations", "automations.json"),
		`{"version":1,"specs":[{"id":"nightly","name":"nightly review","schedule":{"kind":"interval","expr":"30m"},"prompt":"review","enabled":true}]}`)
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"board"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	for _, want := range []string{"jobs:", "automations:", "job1", "nightly"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestJobsCommandsFailWithoutJobsDir(t *testing.T) {
	for _, args := range [][]string{
		{"attach", "job1"},
		{"logs"},
		{"jobs"},
		{"follow", "job1"},
		{"stop", "job1"},
	} {
		t.Run(args[0], func(t *testing.T) {
			ioState, _, errOut := newTestIO("", nil)
			code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, args)
			if code != 1 || !strings.Contains(errOut.String(), "no jobs directory") {
				t.Fatalf("exit = %d, err %q", code, errOut.String())
			}
			if strings.Count(errOut.String(), "\n") != 1 {
				t.Fatalf("expected one-line error, got %q", errOut.String())
			}
		})
	}
}

func TestUpdateCheckHappyPath(t *testing.T) {
	ops := &UpdateOps{Check: func(_ context.Context, repo, current string, _ *http.Client) (update.Release, bool, error) {
		if repo != "owner/repo" || current != "0.0.1" {
			t.Fatalf("check got %q %q", repo, current)
		}
		return update.Release{Tag: "v1.2.3", URL: "https://example.invalid/r", Assets: []update.Asset{{Name: "argent.zip", URL: "u"}}}, true, nil
	}}
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, Version: "0.0.1", UpdateOps: ops}, []string{"update", "--repo", "owner/repo"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "v1.2.3") || !strings.Contains(out.String(), "argent.zip") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestUpdateApplyHappyPathVerifiesAndWritesNotes(t *testing.T) {
	storage := t.TempDir()
	target := filepath.Join(storage, "installed.exe")
	binary := []byte("new binary")
	sum := sha256.Sum256(binary)
	checksum := hex.EncodeToString(sum[:])
	downloaded, applied := false, false
	ops := &UpdateOps{
		Check: func(context.Context, string, string, *http.Client) (update.Release, bool, error) {
			return update.Release{Tag: "v2.0.0", Body: "release body", Assets: []update.Asset{{Name: "argent-windows-amd64.zip", URL: "u"}}}, true, nil
		},
		Checksum: func(context.Context, update.Release, update.Asset, *http.Client) (string, error) {
			return checksum, nil
		},
		Download: func(_ context.Context, _ update.Asset, dest, want string) error {
			downloaded = true
			if want != checksum {
				t.Fatalf("download checksum = %q", want)
			}
			return os.WriteFile(dest, binary, 0o644)
		},
		Apply: func(dest, to string) error {
			applied = true
			if to != target || !strings.Contains(dest, "argent-windows-amd64.zip") {
				t.Fatalf("apply %q -> %q", dest, to)
			}
			return nil
		},
	}
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: storage, UpdateOps: ops}, []string{"update", "--apply", "--asset", "argent-windows-amd64.zip", "--target", target})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !downloaded || !applied {
		t.Fatalf("downloaded=%v applied=%v", downloaded, applied)
	}
	notes := filepath.Join(storage, "updates", "2.0.0-notes.md")
	if !strings.Contains(out.String(), notes) {
		t.Fatalf("out = %q", out.String())
	}
	data, err := os.ReadFile(notes)
	if err != nil || string(data) != "release body" {
		t.Fatalf("notes = %q, err %v", data, err)
	}
}

func TestUpdateApplyWithoutChecksumNeverOverwrites(t *testing.T) {
	applied := false
	ops := &UpdateOps{
		Check: func(context.Context, string, string, *http.Client) (update.Release, bool, error) {
			return update.Release{Tag: "v2.0.0", Assets: []update.Asset{{Name: "argent-windows-amd64.zip", URL: "u"}}}, true, nil
		},
		Checksum: func(context.Context, update.Release, update.Asset, *http.Client) (string, error) {
			return "", nil
		},
		Download: func(context.Context, update.Asset, string, string) error {
			t.Fatal("download called without a checksum")
			return nil
		},
		Apply: func(string, string) error {
			applied = true
			return nil
		},
	}
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: t.TempDir(), UpdateOps: ops}, []string{"update", "--apply", "--asset", "argent-windows-amd64.zip"})
	if code != 1 || applied {
		t.Fatalf("exit = %d, applied = %v, err %q", code, applied, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no checksum") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestUpdateMissingReleaseFails(t *testing.T) {
	ops := &UpdateOps{Check: func(context.Context, string, string, *http.Client) (update.Release, bool, error) {
		return update.Release{}, false, nil
	}}
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, UpdateOps: ops}, []string{"update"})
	if code != 1 || !strings.Contains(errOut.String(), "no release found") {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
}
