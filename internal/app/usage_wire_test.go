package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/tools"
)

func TestUsageSourceReportsRealSnapshotJSON(t *testing.T) {
	src := newUsageSource()
	usage := llm.Usage{PromptTokens: 120, CompletionTokens: 30, CachedTokens: 20}
	total := llm.Usage{PromptTokens: 900, CompletionTokens: 110, CachedTokens: 100}
	src.Update(3, usage, total)

	raw := src.UsageJSON()
	var got usageSnapshot
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("UsageJSON is not valid JSON: %q: %v", raw, err)
	}
	if got.Turn != 3 || got.Usage != usage || got.TotalUsage != total {
		t.Fatalf("snapshot = %+v, want turn=3 usage=%+v total=%+v", got, usage, total)
	}
	// JSON 必须使用文档化的线上键，而非 Go 字段名。
	for _, key := range []string{`"turn"`, `"usage"`, `"total_usage"`, `"prompt_tokens"`} {
		if !strings.Contains(raw, key) {
			t.Errorf("UsageJSON %q missing key %s", raw, key)
		}
	}
}

func TestUsageSourceBeforeFirstUpdateIsValidZeroJSON(t *testing.T) {
	raw := newUsageSource().UsageJSON()
	var got usageSnapshot
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("zero UsageJSON is not valid JSON: %q: %v", raw, err)
	}
	if got != (usageSnapshot{}) {
		t.Fatalf("zero snapshot = %+v, want all zeros", got)
	}
}

func TestWireUsageInstallsSourceOnRegistry(t *testing.T) {
	src := newUsageSource()
	src.Update(1, llm.Usage{PromptTokens: 7}, llm.Usage{PromptTokens: 42, CompletionTokens: 8})

	registry := tools.New(t.TempDir(), true)
	wireUsage(registry, src)

	out, err := registry.Run(context.Background(), "usage", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != src.UsageJSON() {
		t.Fatalf("usage tool = %q, want source JSON %q", out, src.UsageJSON())
	}
}

func TestUsageUnavailableWithoutWiredSource(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	out, err := registry.Run(context.Background(), "usage", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "usage data unavailable" {
		t.Fatalf("unwired usage tool = %q, want %q", out, "usage data unavailable")
	}
	// nil 的源必须是空操作，而不是 panic。
	wireUsage(registry, nil)
	wireUsage(nil, newUsageSource())
}

func TestToolsetWiresUsageIntoEveryRegistryAndUpdateUsage(t *testing.T) {
	root := t.TempDir()
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, AddDirs: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, err := store.NewSession("usage-wire")
	if err != nil {
		t.Fatal(err)
	}
	set := newToolset(context.Background(), cli.IO{Env: env}, res, sess, nil, nil)
	defer set.Close()

	if set.usage == nil {
		t.Fatal("newToolset did not create a usage source")
	}
	if len(set.base.regs) == 0 {
		t.Fatal("toolset has no registries")
	}
	// 第一次调用报告真实的（零）快照，而非在所有注册表上都显示 "unavailable"，
	// 证明 SetUsageSource 在接线时生效。
	const unavailable = "usage data unavailable"
	for i, reg := range set.base.regs {
		out, err := reg.Run(context.Background(), "usage", `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if out == unavailable {
			t.Fatalf("registry %d: usage still reports %q", i, unavailable)
		}
	}

	total := llm.Usage{PromptTokens: 321, CompletionTokens: 45, CachedTokens: 12}
	set.UpdateUsage(2, llm.Usage{PromptTokens: 99, CompletionTokens: 11}, total)
	for i, reg := range set.base.regs {
		out, err := reg.Run(context.Background(), "usage", `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if out != set.usage.UsageJSON() || !strings.Contains(out, `"total_usage"`) ||
			!strings.Contains(out, `"prompt_tokens":321`) {
			t.Fatalf("registry %d after UpdateUsage: out = %q", i, out)
		}
	}

	// 对 nil 工具集的 UpdateUsage 必须保持安全的空操作。
	var nilSet *toolset
	nilSet.UpdateUsage(1, llm.Usage{}, llm.Usage{})
}

func TestUsageSourceConcurrentUpdateAndRead(t *testing.T) {
	src := newUsageSource()
	const workers = 8
	const iterations = 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				u := llm.Usage{PromptTokens: w*iterations + i, CompletionTokens: i}
				src.Update(i, u, u)
				_ = src.UsageJSON()
				_ = src.Snapshot()
			}
		}(w)
	}
	wg.Wait()

	var got usageSnapshot
	if err := json.Unmarshal([]byte(src.UsageJSON()), &got); err != nil {
		t.Fatalf("UsageJSON after concurrent access is invalid: %v", err)
	}
}
