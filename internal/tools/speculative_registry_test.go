package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// spyGuard 记录 Check 的调用次数与最近一次工具名，用于断言守卫是否被触发。
type spyGuard struct {
	mu     sync.Mutex
	calls  int
	names  []string
	reject error
}

func (s *spyGuard) Check(ctx context.Context, name string, args json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.names = append(s.names, name)
	return s.reject
}

func (s *spyGuard) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// countingTool 记录 Run 次数，用于断言 lookup 命中时真正的工具执行被跳过。
// 它声明为非变更，且输出为固定 body，便于区分"来自工具"与"来自 lookup"。
type countingTool struct {
	mu   sync.Mutex
	runs int
	body string
}

func (c *countingTool) Name() string        { return "read" }
func (c *countingTool) Description() string { return "counting test tool" }
func (c *countingTool) Schema() string      { return `{}` }
func (c *countingTool) Mutating() bool      { return false }
func (c *countingTool) Run(ctx context.Context, root string, args json.RawMessage) (string, error) {
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	return c.body, nil
}

func (c *countingTool) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs
}

// TestSpecLookupContext 检查 context 注入与读取：默认 ctx 为 nil；
// 注入后可读回同一函数；nil 注入是空操作；后注入者覆盖先注入者。
func TestSpecLookupContext(t *testing.T) {
	if got := SpecLookupFrom(context.Background()); got != nil {
		t.Fatalf("默认 ctx 的 SpecLookupFrom = %v, want nil", got)
	}

	want := "命中-中文"
	fn := SpecLookup(func(name, args string) (string, bool) { return want, true })
	ctx := WithSpecLookup(context.Background(), fn)
	got := SpecLookupFrom(ctx)
	if got == nil {
		t.Fatal("注入后 SpecLookupFrom 为 nil")
	}
	if out, ok := got("read", `{}`); !ok || out != want {
		t.Fatalf("SpecLookupFrom()() = (%q, %t), want (%q, true)", out, ok, want)
	}

	// nil 注入等价于未设置，原 ctx 不变。
	nilCtx := WithSpecLookup(context.Background(), nil)
	if got := SpecLookupFrom(nilCtx); got != nil {
		t.Fatalf("WithSpecLookup(nil) 后 SpecLookupFrom = %v, want nil", got)
	}

	// 后注入覆盖先注入（context 的标准语义）。
	override := SpecLookup(func(name, args string) (string, bool) { return "覆盖", true })
	if out, ok := SpecLookupFrom(WithSpecLookup(ctx, override))("read", `{}`); !ok || out != "覆盖" {
		t.Fatalf("嵌套注入未生效: (%q, %t)", out, ok)
	}
}

// TestSpeculativeRegistryHasNoGuard 检查推测注册表刻意不安装 guard：
// guard 字段为 nil，checkGuard 是空操作，外部准备的 spy Guard 计数保持 0，
// 因此预跑零审计、零审批。
func TestSpeculativeRegistryHasNoGuard(t *testing.T) {
	reg := NewSpeculativeRegistry(t.TempDir(), 100)
	if reg.guard != nil {
		t.Fatalf("推测注册表 guard = %v, want nil", reg.guard)
	}

	spy := &spyGuard{}
	// 对照：真实注册表安装 spy 后确实会计数，证明 spy 本身有效。
	main := NewWithLimit(t.TempDir(), false, 100)
	main.SetGuard(spy)
	// read 空路径会读目录失败；这里只关心守卫已被调用。
	_, _ = main.Run(context.Background(), "read", `{}`)
	if got := spy.count(); got != 1 {
		t.Fatalf("对照注册表 spy 计数 = %d, want 1", got)
	}

	// 推测注册表路径：guard 为 nil，spy 不会被触发。
	if err := reg.checkGuard(context.Background(), "read", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("checkGuard on speculative registry = %v, want nil", err)
	}
	if got := spy.count(); got != 1 {
		t.Fatalf("推测注册表触发了 guard，spy 计数 = %d, want 保持 1", got)
	}
}

// TestSpecLookupHitShortCircuitsTool 检查 lookup 命中时：
// 输出来自 lookup，真正的工具执行被跳过；推测注册表无 guard，守卫不被调用。
// 同时验证 args 归一化（空白参数变为 "{}"）发生在查询之前。
func TestSpecLookupHitShortCircuitsTool(t *testing.T) {
	reg := NewSpeculativeRegistry(t.TempDir(), 100)
	tool := &countingTool{body: "真实工具输出"}
	reg.tools["read"] = tool

	var seenName, seenArgs string
	ctx := WithSpecLookup(context.Background(), func(name, args string) (string, bool) {
		seenName, seenArgs = name, args
		return "推测命中：中文内容", true
	})

	out, err := reg.Run(ctx, "read", "   ")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out != "推测命中：中文内容" {
		t.Fatalf("Run() = %q, want lookup 输出", out)
	}
	if tool.count() != 0 {
		t.Fatalf("lookup 命中却执行了工具 %d 次", tool.count())
	}
	if seenName != "read" || seenArgs != "{}" {
		t.Fatalf("lookup 收到 (%q, %q), want (\"read\", \"{}\")", seenName, seenArgs)
	}
	if reg.guard != nil {
		t.Fatal("推测注册表不应有 guard")
	}
}

// TestSpecLookupMissRunsTool 检查 lookup 未命中时不改变行为：
// 工具照常执行，输出来自工具本身。
func TestSpecLookupMissRunsTool(t *testing.T) {
	reg := NewSpeculativeRegistry(t.TempDir(), 100)
	tool := &countingTool{body: "真实工具输出"}
	reg.tools["read"] = tool

	ctx := WithSpecLookup(context.Background(), func(name, args string) (string, bool) {
		return "", false
	})
	out, err := reg.Run(ctx, "read", `{"path":"x"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out != "真实工具输出" {
		t.Fatalf("Run() = %q, want 工具输出", out)
	}
	if tool.count() != 1 {
		t.Fatalf("工具执行次数 = %d, want 1", tool.count())
	}
}

// TestSpecLookupHitStillRunsGuard 验证旁路插入点在 checkGuard 之后：
// 在带 spy 守卫的主注册表上，lookup 命中仍会先经过守卫（审计/审批不被绕过），
// 随后才短路。若把旁路误放在守卫之前，本测试会失败。
func TestSpecLookupHitStillRunsGuard(t *testing.T) {
	reg := NewWithLimit(t.TempDir(), false, 100)
	spy := &spyGuard{}
	reg.SetGuard(spy)
	tool := &countingTool{body: "真实工具输出"}
	reg.tools["read"] = tool

	ctx := WithSpecLookup(context.Background(), func(name, args string) (string, bool) {
		return "缓存输出", true
	})
	out, err := reg.Run(ctx, "read", `{}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out != "缓存输出" {
		t.Fatalf("Run() = %q, want 缓存输出", out)
	}
	if got := spy.count(); got != 1 {
		t.Fatalf("lookup 命中时守卫调用次数 = %d, want 1（旁路必须在 checkGuard 之后）", got)
	}
	if got := len(spy.names); got != 1 || spy.names[0] != "read" {
		t.Fatalf("守卫收到的工具名 = %v, want [read]", spy.names)
	}
	if tool.count() != 0 {
		t.Fatalf("lookup 命中却执行了工具 %d 次", tool.count())
	}
}

// TestRunWithoutLookupUnchanged 默认 ctx（无 lookup）时 Run 行为逐字节不变：
// 读取含中文的文件，输出必须与文件内容完全一致（无分页尾注、无截断）。
func TestRunWithoutLookupUnchanged(t *testing.T) {
	root := t.TempDir()
	const content = "你好，世界 — 推测旁路未启用"
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewWithLimit(root, false, 100)
	out, err := reg.Run(context.Background(), "read", `{"path":"notes.txt"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out != content {
		t.Fatalf("Run() = %q, want 逐字节等于 %q", out, content)
	}
}

// TestSpeculativeConcurrent 检查并发安全：每个 goroutine 注入各自的 lookup，
// Run 必须路由到本 goroutine 的函数，输出互不串扰，且工具不被执行。
func TestSpeculativeConcurrent(t *testing.T) {
	const n = 64
	reg := NewSpeculativeRegistry(t.TempDir(), 100)
	tool := &countingTool{body: "真实工具输出"}
	reg.tools["read"] = tool

	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("命中-%d-中文", i)
			ctx := WithSpecLookup(context.Background(), func(name, args string) (string, bool) {
				return want, true
			})
			out, err := reg.Run(ctx, "read", `{"path":"x"}`)
			if err != nil {
				errs <- fmt.Sprintf("goroutine %d: %v", i, err)
				return
			}
			if out != want {
				errs <- fmt.Sprintf("goroutine %d: got %q, want %q", i, out, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if tool.count() != 0 {
		t.Fatalf("并发 lookup 命中却执行了工具 %d 次", tool.count())
	}
}
