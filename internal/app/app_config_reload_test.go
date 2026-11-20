package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// writeReloadConfig writes a config whose model string changes length between
// edits, so config.Changed detects the rewrite via file size even on filesystems
// with coarse mtime resolution.
func writeReloadConfig(t *testing.T, path, model string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"model":"`+model+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reloadCfg loads a config from an isolated temp workspace and returns its
// snapshot plus the config file path so a test can simulate an on-disk edit.
func reloadCfg(t *testing.T, model string) (*config.Config, string) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeReloadConfig(t, path, model)
	cfg, err := config.Load(cwd, envFrom(map[string]string{"HOME": home, "USERPROFILE": home}))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return &cfg, path
}

func TestConfigReloaderAdoptsDiskChangeAndLogs(t *testing.T) {
	cfg, path := reloadCfg(t, "before-model")
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	if r.poll() {
		t.Fatal("clean poll reported an adoption")
	}
	if got := r.snapshot(); got != cfg || got.Generation() != 1 {
		t.Fatalf("clean poll changed the snapshot: gen=%d", got.Generation())
	}

	writeReloadConfig(t, path, "a-considerably-longer-after-model")
	if !r.poll() {
		t.Fatal("poll after a disk edit did not adopt")
	}
	snap := r.snapshot()
	if snap == cfg {
		t.Error("snapshot pointer was not atomically replaced")
	}
	if snap.Model != "a-considerably-longer-after-model" || snap.Generation() != 2 {
		t.Errorf("adopted snapshot = %q gen=%d", snap.Model, snap.Generation())
	}
	if cfg.Model != "before-model" || cfg.Generation() != 1 {
		t.Errorf("old snapshot was mutated: %q gen=%d", cfg.Model, cfg.Generation())
	}
	if !strings.Contains(logs.String(), "reloaded:") || !strings.Contains(logs.String(), "generation 2") {
		t.Errorf("change was not logged: %q", logs.String())
	}
	// Adopting the returned snapshot makes the next poll a no-op.
	if r.poll() {
		t.Fatal("second poll adopted again (not idempotent)")
	}
}

func TestConfigReloaderFailureKeepsLastGood(t *testing.T) {
	cfg, path := reloadCfg(t, "valid-model")
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r.poll() {
		t.Fatal("failed reload was reported as adopted")
	}
	if got := r.snapshot(); got != cfg || got.Model != "valid-model" || got.Generation() != 1 {
		t.Errorf("last-good snapshot was replaced: %+v", got)
	}
	if !strings.Contains(logs.String(), "reload failed") {
		t.Errorf("failure was not logged: %q", logs.String())
	}
}

func TestConfigReloaderNilSafe(t *testing.T) {
	var nilR *configReloader
	if nilR.poll() || nilR.snapshot() != nil {
		t.Fatal("nil reloader was not a no-op")
	}
	r := newConfigReloader(nil, nil)
	if r.poll() || r.snapshot() != nil {
		t.Fatal("reloader with a nil snapshot was not a no-op")
	}
}

func TestRunnerPollsConfigAtTurnBoundaryOnly(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeReloadConfig(t, path, "before-model")
	env := envFrom(map[string]string{"HOME": home, "USERPROFILE": home})
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var logs bytes.Buffer
	r := &runner{
		stdio: cli.IO{Env: env, Err: &logs},
		res:   res,
		store: testStore(t, res),
		execute: func(context.Context, cli.IO, Resolved, *session.Session, string, func(string)) (agent.Result, error) {
			return agent.Result{Text: "ok"}, nil
		},
		reload: newConfigReloader(&res.Cfg, func(msg string) { logs.WriteString(msg + "\n") }),
	}
	before := r.reload.snapshot()

	// One-shot: a single prompt never reaches a turn boundary, so no poll.
	if _, err := r.Prompt(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("one-shot prompt polled config: %q", logs.String())
	}
	if r.reload.snapshot() != before {
		t.Fatal("snapshot changed after the first prompt")
	}

	// An operator edits the config between turns.
	writeReloadConfig(t, path, "a-considerably-longer-after-model")

	// The second prompt crosses a turn boundary and adopts the new snapshot.
	if _, err := r.Prompt(context.Background(), "second", nil); err != nil {
		t.Fatal(err)
	}
	if got := r.reload.snapshot(); got == before || got.Model != "a-considerably-longer-after-model" {
		t.Fatalf("turn boundary did not adopt config: %+v", got)
	}
	if !strings.Contains(logs.String(), "reloaded:") {
		t.Errorf("reload was not logged: %q", logs.String())
	}
}
