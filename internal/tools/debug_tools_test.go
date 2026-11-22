package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeDapManager 是 DapSessionManager 的测试替身：只记录调用并返回固定响应，
// 不启动任何子进程，因此测试无需真实 dlv。
type fakeDapManager struct {
	starts []fakeDapStart
	calls  []fakeDapCall
	closed []string
}

type fakeDapStart struct {
	root, command, program string
	args                   []string
}

type fakeDapCall struct {
	session, command string
	arguments        map[string]any
}

func (f *fakeDapManager) Start(_ context.Context, root, command string, args []string, program string) (string, error) {
	f.starts = append(f.starts, fakeDapStart{root: root, command: command, args: args, program: program})
	return "dap-test-1", nil
}

func (f *fakeDapManager) Call(_ context.Context, sessionID, command string, arguments map[string]any) (json.RawMessage, error) {
	f.calls = append(f.calls, fakeDapCall{session: sessionID, command: command, arguments: arguments})
	return json.RawMessage(`{"ok":true}`), nil
}

func (f *fakeDapManager) Close(_ context.Context, sessionID string) error {
	f.closed = append(f.closed, sessionID)
	return nil
}

var debugToolNames = []string{
	debugStartToolName, debugBreakpointToolName, debugContinueToolName, debugStepToolName,
	debugThreadsToolName, debugStackToolName, debugVariablesToolName, debugStopToolName,
}

func TestRegisterDebugNotInDefaultGolden(t *testing.T) {
	r := New(t.TempDir(), true)
	for _, name := range debugToolNames {
		for _, existing := range r.Names() {
			if existing == name {
				t.Fatalf("默认注册表不应包含 %q（golden 必须不变）", name)
			}
		}
	}
}

func TestRegisterDebugAppendsInOrderAndIdempotent(t *testing.T) {
	r := New(t.TempDir(), true)
	before := r.Names()

	RegisterDebug(r)
	once := r.Names()
	if len(once) != len(before)+len(debugToolNames) {
		t.Fatalf("注册后数量 = %d，期望 %d", len(once), len(before)+len(debugToolNames))
	}
	for i := range before {
		if once[i] != before[i] {
			t.Fatalf("既有顺序被改变：第 %d 项 %q != %q", i, once[i], before[i])
		}
	}
	if got := once[len(before):]; !reflect.DeepEqual(got, debugToolNames) {
		t.Fatalf("追加顺序 = %v，期望 %v", got, debugToolNames)
	}

	RegisterDebug(r)
	if got := r.Names(); !reflect.DeepEqual(got, once) {
		t.Fatalf("重复注册不应改变列表：%v", got)
	}
	RegisterDebug(nil) // nil 是空操作

	// Specs 同步包含全部工具，且 schema 带意图字段（Specs 兜底注入）。
	found := map[string]bool{}
	for _, spec := range r.Specs() {
		name := spec.Function.Name
		if _, want := specIndex(debugToolNames, name); want {
			found[name] = true
			if !strings.Contains(string(spec.Function.Parameters), `"i"`) {
				t.Fatalf("%s 的 schema 应带 i 意图字段", name)
			}
		}
	}
	for _, name := range debugToolNames {
		if !found[name] {
			t.Fatalf("Specs 缺少 %q", name)
		}
	}
}

func specIndex(names []string, name string) (int, bool) {
	for i, n := range names {
		if n == name {
			return i, true
		}
	}
	return -1, false
}

func TestSetDapInjectionWiresStartHandshake(t *testing.T) {
	r := New(t.TempDir(), true)
	fake := &fakeDapManager{}
	r.SetDap(fake)

	out, err := r.Run(context.Background(), debugStartToolName, `{"program":"./cmd/argent"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dap-test-1") {
		t.Fatalf("输出应含会话 id：%s", out)
	}
	if len(fake.starts) != 1 || fake.starts[0].program != "./cmd/argent" {
		t.Fatalf("Start 调用 = %+v", fake.starts)
	}
	// 握手三条命令按序发出。
	if len(fake.calls) != 3 {
		t.Fatalf("握手调用数 = %d，期望 3：%+v", len(fake.calls), fake.calls)
	}
	want := []string{"initialize", "launch", "configurationDone"}
	for i, cmd := range want {
		if fake.calls[i].command != cmd {
			t.Fatalf("握手第 %d 步 = %q，期望 %q", i, fake.calls[i].command, cmd)
		}
	}

	// 注入不改变注册顺序与数量。
	names := r.Names()
	if got := names[len(names)-len(debugToolNames):]; !reflect.DeepEqual(got, debugToolNames) {
		t.Fatalf("注入不应改变注册顺序：%v", got)
	}

	// 重复注入幂等。
	r.SetDap(fake)
	if len(r.Names()) != len(names) {
		t.Fatal("重复 SetDap 不应改变工具数量")
	}
}

func TestDebugBreakpointRejectsWorkspaceEscape(t *testing.T) {
	root := t.TempDir()
	r := New(root, true)
	fake := &fakeDapManager{}
	r.SetDap(fake)

	// 绝对路径逃逸：指向根之外。
	outside := filepath.Join(filepath.Dir(root), "escape.go")
	_, err := r.Run(context.Background(), debugBreakpointToolName,
		`{"session":"dap-test-1","path":"`+filepath.ToSlash(outside)+`","lines":[1]}`)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("越界路径应被拒绝，实际 err = %v", err)
	}
	for _, c := range fake.calls {
		if c.command == "setBreakpoints" {
			t.Fatal("越界路径不得发出 setBreakpoints 请求")
		}
	}
}

func TestDebugBreakpointAcceptsWorkspacePath(t *testing.T) {
	root := t.TempDir()
	r := New(root, true)
	fake := &fakeDapManager{}
	r.SetDap(fake)

	if _, err := r.Run(context.Background(), debugBreakpointToolName,
		`{"session":"dap-test-1","path":"main.go","lines":[12,34]}`); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || fake.calls[0].command != "setBreakpoints" {
		t.Fatalf("期望恰好一条 setBreakpoints：%+v", fake.calls)
	}
	if _, ok := fake.calls[0].arguments["breakpoints"]; !ok {
		t.Fatalf("setBreakpoints 参数缺少 breakpoints：%+v", fake.calls[0].arguments)
	}
}

func TestDebugStepValidatesKind(t *testing.T) {
	r := New(t.TempDir(), true)
	fake := &fakeDapManager{}
	r.SetDap(fake)

	_, err := r.Run(context.Background(), debugStepToolName, `{"session":"x","kind":"jump"}`)
	if err == nil || !strings.Contains(err.Error(), "invalid step kind") {
		t.Fatalf("非法 kind 应被拒绝，实际 err = %v", err)
	}

	for kind, want := range map[string]string{"next": "next", "in": "stepIn", "out": "stepOut"} {
		fake.calls = nil
		if _, err := r.Run(context.Background(), debugStepToolName,
			`{"session":"x","kind":"`+kind+`"}`); err != nil {
			t.Fatal(err)
		}
		if len(fake.calls) != 1 || fake.calls[0].command != want {
			t.Fatalf("kind=%q 应发 %q，实际 %+v", kind, want, fake.calls)
		}
	}
}

func TestDebugStartRequiresProgram(t *testing.T) {
	r := New(t.TempDir(), true)
	r.SetDap(&fakeDapManager{})
	_, err := r.Run(context.Background(), debugStartToolName, `{}`)
	if err == nil || !strings.Contains(err.Error(), "program is required") {
		t.Fatalf("缺少 program 应被拒绝，实际 err = %v", err)
	}
}

func TestDebugToolsUnavailableWithoutManager(t *testing.T) {
	r := New(t.TempDir(), true)
	RegisterDebug(r)
	for _, name := range debugToolNames {
		out, err := r.Run(context.Background(), name, `{}`)
		if err != nil {
			t.Fatalf("%s 未注入时不应报错：%v", name, err)
		}
		if out != dapUnavailable {
			t.Fatalf("%s 未注入时应报告不可用，实际 %q", name, out)
		}
	}
}

func TestDebugMutatingFlags(t *testing.T) {
	r := New(t.TempDir(), true)
	RegisterDebug(r)
	wantMutating := map[string]bool{
		debugStartToolName: true, debugBreakpointToolName: true, debugContinueToolName: true,
		debugStepToolName: true, debugStopToolName: true,
		debugThreadsToolName: false, debugStackToolName: false, debugVariablesToolName: false,
	}
	for name, want := range wantMutating {
		tool, ok := r.tools[name]
		if !ok {
			t.Fatalf("缺少工具 %q", name)
		}
		if got := tool.Mutating(); got != want {
			t.Fatalf("%s.Mutating() = %v，期望 %v", name, got, want)
		}
	}
}

func TestDapAdapterCommandResolution(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		env     func(string) string
		wantCmd string
		wantArg []string
	}{
		{"", nil, nil, "dlv", []string{"dap"}},
		{"", nil, func(string) string { return "custom --flag" }, "custom", []string{"--flag"}},
		{"my-adapter --x", nil, nil, "my-adapter", []string{"--x"}},
		{"my-adapter --x", []string{"--y"}, nil, "my-adapter", []string{"--y"}},
	}
	for _, tc := range cases {
		cmd, args := dapAdapterCommand(tc.command, tc.args, tc.env)
		if cmd != tc.wantCmd || !reflect.DeepEqual(args, tc.wantArg) {
			t.Fatalf("dapAdapterCommand(%q,%v) = (%q,%v)，期望 (%q,%v)",
				tc.command, tc.args, cmd, args, tc.wantCmd, tc.wantArg)
		}
	}
}
