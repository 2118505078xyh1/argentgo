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

// writeReloadConfig 写入一份配置，其模型字符串在编辑之间长度会变化，
// 因此即使在 mtime 分辨率较粗的文件系统上，config.Changed 也能通过文件大小检测到重写。
// （接上行）
func writeReloadConfig(t *testing.T, path, model string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"model":"`+model+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reloadCfg 从隔离的临时工作区加载配置，并返回其快照以及配置文件路径，
// 使测试能模拟磁盘上的编辑。
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
	// 默认日志改为键路径差异摘要（ARGENT_CONFIG_RELOAD_DIFF 默认开，=0 才回退旧格式），
	// 只列变化的键名与脱敏占位，不再输出 generation 或配置值。
	if !strings.Contains(logs.String(), "config reloaded: model") {
		t.Errorf("change was not logged as a key-path diff: %q", logs.String())
	}
	// 采用返回的快照会使下一次轮询成为空操作。
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

	// 一次性运行：单个提示永远不会到达轮次边界，因此不会轮询。
	if _, err := r.Prompt(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("one-shot prompt polled config: %q", logs.String())
	}
	if r.reload.snapshot() != before {
		t.Fatal("snapshot changed after the first prompt")
	}

	// 操作人员在轮次之间编辑配置。
	writeReloadConfig(t, path, "a-considerably-longer-after-model")

	// 第二个提示跨越轮次边界并采用新快照。
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
