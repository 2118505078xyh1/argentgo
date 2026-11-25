package app

// 编辑后诊断接线的离线测试。所有用例通过替换 fetchDiagnostics / startFn /
// editDiagnosticsForPath 接缝运行，不依赖真实语言服务器或模型。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/lsp"
)

// stubToolRunner 是 hookedTools.Run 的最小内层实现：记录调用并原样返回结果。
type stubToolRunner struct {
	out   string
	err   error
	specs []llm.ToolSpec
	names []string
}

func (s *stubToolRunner) Specs() []llm.ToolSpec { return s.specs }

func (s *stubToolRunner) Run(_ context.Context, name, _ string) (string, error) {
	s.names = append(s.names, name)
	return s.out, s.err
}

// enableEditDiagnostics 在单个用例内打开开关并在结束时恢复，避免污染其它用例。
func enableEditDiagnostics(t *testing.T) {
	t.Helper()
	previous := EnableEditDiagnostics
	EnableEditDiagnostics = true
	t.Cleanup(func() { EnableEditDiagnostics = previous })
}

// editHookedTools 构造一个带临时根目录、写工具路径接缝与桩内层的 hookedTools。
func editHookedTools(root string, inner *stubToolRunner, paths ...string) *hookedTools {
	return &hookedTools{
		inner:  inner,
		bridge: &hookBridge{cwd: root},
		mutationPaths: func(_, _ string) []string {
			return paths
		},
	}
}

func TestEditDiagnosticsDisabledLeavesOutputByteIdentical(t *testing.T) {
	previous := EnableEditDiagnostics
	EnableEditDiagnostics = false
	t.Cleanup(func() { EnableEditDiagnostics = previous })

	// 即使接缝会返回诊断，关闭开关时也绝不能被调用。
	called := false
	previousSeam := editDiagnosticsForPath
	editDiagnosticsForPath = func(context.Context, string, string) string {
		called = true
		return "main.go:1:1: boom"
	}
	t.Cleanup(func() { editDiagnosticsForPath = previousSeam })

	inner := &stubToolRunner{out: "original output"}
	h := editHookedTools(t.TempDir(), inner, "main.go")
	out, err := h.Run(context.Background(), "write", `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "original output" {
		t.Fatalf("开关关闭时输出被改动: %q", out)
	}
	if called {
		t.Fatal("开关关闭时诊断接缝仍被调用")
	}
}

func TestEditDiagnosticsDisabledOnReadToolDoesNotFetch(t *testing.T) {
	enableEditDiagnostics(t)
	called := false
	previousSeam := editDiagnosticsForPath
	editDiagnosticsForPath = func(context.Context, string, string) string {
		called = true
		return "x.go:1:1: boom"
	}
	t.Cleanup(func() { editDiagnosticsForPath = previousSeam })

	inner := &stubToolRunner{out: "file body"}
	h := editHookedTools(t.TempDir(), inner, "x.go")
	out, err := h.Run(context.Background(), "read", `{"path":"x.go"}`)
	if err != nil || out != "file body" {
		t.Fatalf("读工具输出被改动: out=%q err=%v", out, err)
	}
	if called {
		t.Fatal("读工具不应触发诊断拉取")
	}
}

func TestEditDiagnosticsEnabledAppendsFormattedError(t *testing.T) {
	enableEditDiagnostics(t)
	root := t.TempDir()
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		return &lsp.Client{}, nil
	})
	previous := fetchDiagnostics
	fetchDiagnostics = func(context.Context, *lsp.Client, string) ([]lsp.Diagnostic, error) {
		return []lsp.Diagnostic{
			{Range: testRange(11, 4, 11, 9), Severity: "error", Message: "undefined: foo", Source: "gopls"},
		}, nil
	}
	t.Cleanup(func() { fetchDiagnostics = previous })

	inner := &stubToolRunner{out: "done"}
	h := editHookedTools(root, inner, "main.go")
	out, err := h.Run(context.Background(), "edit", `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(out, "done") {
		t.Fatalf("工具原始输出未保留: %q", out)
	}
	if !strings.Contains(out, editDiagnosticsLabel+" main.go:12:5 [gopls]: undefined: foo") {
		t.Fatalf("未附加 1-based 格式化诊断: %q", out)
	}
}

func TestEditDiagnosticsEnabledNoServerAppendsNothing(t *testing.T) {
	enableEditDiagnostics(t)
	root := t.TempDir()
	// 不替换 fetchDiagnostics，走真实退化路径：客户端拉取失败后 Diagnostics
	// 返回空且不报错，接缝因此返回空串，工具输出逐字节不变。
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		return nil, errors.New("executable file not found")
	})

	inner := &stubToolRunner{out: "done"}
	h := editHookedTools(root, inner, "main.go")
	out, err := h.Run(context.Background(), "write", `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("无语言服务器时输出应不变, 得到 %q", out)
	}
}

func TestEditDiagnosticsEnabledFiltersNonErrors(t *testing.T) {
	enableEditDiagnostics(t)
	root := t.TempDir()
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		return &lsp.Client{}, nil
	})
	previous := fetchDiagnostics
	fetchDiagnostics = func(context.Context, *lsp.Client, string) ([]lsp.Diagnostic, error) {
		return []lsp.Diagnostic{
			{Range: testRange(0, 0, 0, 1), Severity: "hint", Message: "仅提示信息"},
			{Range: testRange(1, 0, 1, 1), Severity: "warning", Message: "仅警告信息"},
			{Range: testRange(2, 0, 2, 1), Severity: "information", Message: "仅描述信息"},
			{Range: testRange(3, 2, 3, 4), Severity: "error", Message: "真实错误", Source: "gopls"},
		}, nil
	}
	t.Cleanup(func() { fetchDiagnostics = previous })

	inner := &stubToolRunner{out: "done"}
	h := editHookedTools(root, inner, "main.go")
	out, err := h.Run(context.Background(), "multi_edit", `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "main.go:4:3 [gopls]: 真实错误") {
		t.Fatalf("错误级别诊断缺失: %q", out)
	}
	for _, unwanted := range []string{"仅提示信息", "仅警告信息", "仅描述信息"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("非错误级别诊断泄露 (%s): %q", unwanted, out)
		}
	}
}

func TestEditDiagnosticsEnabledKeepsCJKTextIntact(t *testing.T) {
	enableEditDiagnostics(t)
	root := t.TempDir()
	path := "源码/主程序.go"
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		return &lsp.Client{}, nil
	})
	previous := fetchDiagnostics
	fetchDiagnostics = func(context.Context, *lsp.Client, string) ([]lsp.Diagnostic, error) {
		return []lsp.Diagnostic{
			{Range: testRange(6, 1, 6, 2), Severity: "error", Message: "变量未定义：示例变量", Source: "gopls"},
		}, nil
	}
	t.Cleanup(func() { fetchDiagnostics = previous })

	inner := &stubToolRunner{out: "完成"}
	h := editHookedTools(root, inner, path)
	out, err := h.Run(context.Background(), "write", `{"path":"`+path+`"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := editDiagnosticsLabel + " " + path + ":7:2 [gopls]: 变量未定义：示例变量"
	if !strings.Contains(out, want) {
		t.Fatalf("中文路径/消息未完整保留: %q, want contains %q", out, want)
	}
}

// TestEditDiagnosticsAutoInjectionOverridesGlobalOff 验证 R25 可注入开关：
// 全局 EnableEditDiagnostics 关闭时，仅把 hookedTools.autoDiagnostics 置 true
// 就应让诊断路径生效；零值仍退化为旧的全局开关行为。
func TestEditDiagnosticsAutoInjectionOverridesGlobalOff(t *testing.T) {
	previous := EnableEditDiagnostics
	EnableEditDiagnostics = false
	t.Cleanup(func() { EnableEditDiagnostics = previous })

	called := false
	previousSeam := editDiagnosticsForPath
	editDiagnosticsForPath = func(context.Context, string, string) string {
		called = true
		return "main.go:1:1: boom"
	}
	t.Cleanup(func() { editDiagnosticsForPath = previousSeam })

	// 零值：全局关且未注入时接缝绝不被调用，输出逐字节不变。
	inner := &stubToolRunner{out: "original output"}
	h := editHookedTools(t.TempDir(), inner, "main.go")
	out, err := h.Run(context.Background(), "write", `{"path":"main.go"}`)
	if err != nil || out != "original output" || called {
		t.Fatalf("零值注入时诊断不应生效: out=%q err=%v called=%v", out, err, called)
	}

	// 注入 true：全局仍关闭，但诊断路径生效并附加格式化诊断。
	h.autoDiagnostics = true
	out, err = h.Run(context.Background(), "write", `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Fatal("注入 autoDiagnostics=true 后诊断接缝应被调用")
	}
	if !strings.Contains(out, editDiagnosticsLabel+" main.go:1:1: boom") {
		t.Fatalf("未附加诊断: %q", out)
	}
	if !strings.HasPrefix(out, "original output") {
		t.Fatalf("工具原始输出未保留: %q", out)
	}
}

func TestAppendDiagnosticNoteNoopOnEmptyMessage(t *testing.T) {
	if got := appendDiagnosticNote("done", "  "); got != "done" {
		t.Fatalf("空诊断消息应保持原输出, 得到 %q", got)
	}
	if got := appendDiagnosticNote("", ""); got != "" {
		t.Fatalf("空输出+空消息应为空, 得到 %q", got)
	}
}
