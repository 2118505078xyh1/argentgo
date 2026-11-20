package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestPollReloadPicksUpDiskChange 验证轮询原语的顺利路径：
// 干净轮询时无变化，随后检测到磁盘编辑、重载
// 进新快照并报告为已变化。对新快照的后续轮询
// 是干净的（幂等性）。
func TestPollReloadPicksUpDiskChange(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "before"})

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	same, changed, err := PollReload(&cfg)
	if err != nil || changed || same != &cfg {
		t.Fatalf("clean poll = (%p, %v, %v), want (cfg, false, nil)", same, changed, err)
	}

	// 长度不同，大小也随之变化；mtime 可能较粗。
	writeJSONFile(t, path, map[string]any{"model": "a-considerably-longer-after-model"})

	next, changed, err := PollReload(&cfg)
	if err != nil {
		t.Fatalf("PollReload: %v", err)
	}
	if !changed {
		t.Fatal("changed=false after a disk edit")
	}
	if next == &cfg {
		t.Error("PollReload returned the old snapshot pointer on a change")
	}
	if next.Model != "a-considerably-longer-after-model" {
		t.Errorf("reloaded model = %q, want the new value", next.Model)
	}
	if next.Generation() != cfg.Generation()+1 {
		t.Errorf("generation = %d, want %d", next.Generation(), cfg.Generation()+1)
	}
	// 接收者未被修改。
	if cfg.Model != "before" || cfg.Generation() != 1 {
		t.Errorf("receiver damaged: model=%q gen=%d", cfg.Model, cfg.Generation())
	}

	// 幂等性：采纳 next 会使下一次轮询成为空操作。
	again, changed, err := PollReload(next)
	if err != nil || changed || again != next {
		t.Errorf("poll of reloaded snapshot = (%p, %v, %v), want (next, false, nil)", again, changed, err)
	}
}

// TestPollReloadNoChangeKeepsSnapshot 防止轮询噪声：没有磁盘活动时
// 重复调用返回相同快照、changed=false 和 nil
// error，且不递增 generation。
func TestPollReloadNoChangeKeepsSnapshot(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"model": "stable"})

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := 0; i < 3; i++ {
		same, changed, err := PollReload(&cfg)
		if err != nil || changed || same != &cfg {
			t.Fatalf("poll %d = (%p, %v, %v), want (cfg, false, nil)", i, same, changed, err)
		}
	}
	if cfg.Generation() != 1 {
		t.Errorf("generation = %d, want 1 with no reloads", cfg.Generation())
	}
}

// TestPollReloadFailureKeepsLastGoodSnapshot 保持重载失败的非破坏性：
// 磁盘编辑格式错误会产生 error、changed=false，
// 以及原样保留、仍可用的最后已知良好快照。
func TestPollReloadFailureKeepsLastGoodSnapshot(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "valid"})

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	same, changed, err := PollReload(&cfg)
	if err == nil {
		t.Fatal("PollReload accepted a malformed config")
	}
	if changed {
		t.Error("changed=true after a failed reload; nothing was adopted")
	}
	if same != &cfg {
		t.Error("PollReload replaced the snapshot on a failed reload")
	}
	if cfg.Model != "valid" || cfg.Generation() != 1 {
		t.Errorf("receiver damaged by failed reload: model=%q gen=%d", cfg.Model, cfg.Generation())
	}
}

// TestPollReloadGenerationIncrementsAcrossEdits 演练长生命周期用法：
// 采纳返回的快照，然后在另一次编辑后再次轮询。
// generation 必须依次递增 1 -> 2 -> 3，且每次都保留覆盖/env。
func TestPollReloadGenerationIncrementsAcrossEdits(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "one"})

	env := envFunc(map[string]string{"HOME": home, "ARGENT_LOCALE": "zh-CN"})
	cfg, err := LoadWithOverrides(cwd, env, map[string]any{"model": "flag-wins"})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if cfg.Generation() != 1 {
		t.Fatalf("initial generation = %d, want 1", cfg.Generation())
	}

	// adopt 断言轮询找到了已变化的、无错误的重载，并返回要采纳的
	// 快照。PollReload 的多返回值是它的唯一参数。
	adopt := func(next *Config, changed bool, err error) (*Config, error) {
		t.Helper()
		if err != nil {
			return nil, err
		}
		if !changed || next == nil {
			t.Fatalf("expected a change with a new snapshot, got (next=%v, changed=%v)", next, changed)
		}
		return next, nil
	}

	writeJSONFile(t, path, map[string]any{"model": "two-longer"})
	current, err := adopt(PollReload(&cfg))
	if err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if current.Generation() != 2 {
		t.Fatalf("generation after first edit = %d, want 2", current.Generation())
	}

	writeJSONFile(t, path, map[string]any{"model": "three-longest"})
	current, err = adopt(PollReload(current))
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if current.Generation() != 3 {
		t.Fatalf("generation after second edit = %d, want 3", current.Generation())
	}
	// 原有覆盖和 env 仍胜过被编辑的磁盘文件。
	if current.Model != "flag-wins" || current.Locale != "zh-CN" {
		t.Errorf("precedence lost across reloads: model=%q locale=%q", current.Model, current.Locale)
	}
}

// TestPollReloadConcurrent 验证同一个 *Config 可以被多个
// goroutine 并发轮询而无竞态：每个调用方都观察到一致、
// 已变化且 generation 已递增的快照。
func TestPollReloadConcurrent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "before"})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	writeJSONFile(t, path, map[string]any{"model": "a-longer-after-model"})

	const n = 16
	nexts := make([]*Config, n)
	changs := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nexts[i], changs[i], errs[i] = PollReload(&cfg)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("poll %d: %v", i, errs[i])
			continue
		}
		if !changs[i] || nexts[i] == &cfg {
			t.Errorf("poll %d did not adopt a new snapshot: changed=%v ptr=%p", i, changs[i], nexts[i])
			continue
		}
		if nexts[i].Model != "a-longer-after-model" || nexts[i].Generation() != 2 {
			t.Errorf("poll %d inconsistent: model=%q gen=%d", i, nexts[i].Model, nexts[i].Generation())
		}
	}
}

// TestPollReloadNoFingerprint 记录 nil Config 以及
// 在 Load 之外创建的 Config 的安全默认：没有指纹意味着没有
// 变更检测，因此它们被原样返回且 changed=false。
func TestPollReloadNoFingerprint(t *testing.T) {
	same, changed, err := PollReload(nil)
	if err != nil || changed || same != nil {
		t.Fatalf("nil poll = (%p, %v, %v), want (nil, false, nil)", same, changed, err)
	}

	handBuilt := Config{Model: "manual"}
	same, changed, err = PollReload(&handBuilt)
	if err != nil || changed || same != &handBuilt {
		t.Fatalf("hand-built poll = (%p, %v, %v), want (cfg, false, nil)", same, changed, err)
	}
}
