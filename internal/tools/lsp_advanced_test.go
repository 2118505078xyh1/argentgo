package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// fakeAdvancedLspIde 是确定性的 advancedLspIde，返回固定 DTO 并记录
// 收到的参数，使三个工具的透传行为可测试。
type fakeAdvancedLspIde struct {
	tokens []LspSemanticToken
	hints  []LspInlayHint
	lenses []LspCodeLens

	tokenPath string
	hintPath  string
	hintLine  int
	hintChar  int
	lensPath  string

	calls int
}

func (f *fakeAdvancedLspIde) SemanticTokens(path string) ([]LspSemanticToken, error) {
	f.tokenPath = path
	f.calls++
	return f.tokens, nil
}

func (f *fakeAdvancedLspIde) InlayHints(path string, line, char int) ([]LspInlayHint, error) {
	f.hintPath, f.hintLine, f.hintChar = path, line, char
	f.calls++
	return f.hints, nil
}

func (f *fakeAdvancedLspIde) CodeLens(path string) ([]LspCodeLens, error) {
	f.lensPath = path
	f.calls++
	return f.lenses, nil
}

func cannedAdvancedFake() *fakeAdvancedLspIde {
	return &fakeAdvancedLspIde{
		tokens: []LspSemanticToken{
			{Line: 0, Char: 0, Length: 5, TokenType: 1, Modifiers: 0},
			{Line: 2, Char: 4, Length: 3, TokenType: 9, Modifiers: 2},
		},
		hints: []LspInlayHint{
			{Line: 3, Char: 12, Label: ": int", Kind: "type"},
		},
		lenses: []LspCodeLens{
			{StartLine: 1, StartChar: 0, EndLine: 1, EndChar: 8, Title: "Run Test", Command: "go.test"},
		},
	}
}

var lspAdvancedToolNames = []string{lspSemanticTokensName, lspInlayHintsName, lspCodeLensName}

// TestLspAdvancedNotInDefaultGolden 验证默认 New() 不含任何高级工具，
// 因此既有 6 个 LSP 工具与整体 golden 清单逐字节不变。
func TestLspAdvancedNotInDefaultGolden(t *testing.T) {
	r := New(t.TempDir(), true)
	for _, name := range lspAdvancedToolNames {
		for _, existing := range r.Names() {
			if existing == name {
				t.Fatalf("默认注册表不应包含 %q（golden 必须不变）", name)
			}
		}
	}
}

// TestRegisterLspAdvancedAppendsInOrderAndIdempotent 验证显式注册
// 幂等地把三个工具按稳定顺序追加到末尾，且不改变既有顺序。
func TestRegisterLspAdvancedAppendsInOrderAndIdempotent(t *testing.T) {
	r := New(t.TempDir(), true)
	before := r.Names()

	RegisterLspAdvanced(r)
	once := r.Names()
	if len(once) != len(before)+len(lspAdvancedToolNames) {
		t.Fatalf("注册后数量 = %d，期望 %d", len(once), len(before)+len(lspAdvancedToolNames))
	}
	for i := range before {
		if once[i] != before[i] {
			t.Fatalf("既有顺序被改变：第 %d 项 %q != %q", i, once[i], before[i])
		}
	}
	if got := once[len(before):]; !reflect.DeepEqual(got, lspAdvancedToolNames) {
		t.Fatalf("追加顺序 = %v，期望 %v", got, lspAdvancedToolNames)
	}

	RegisterLspAdvanced(r)
	if got := r.Names(); !reflect.DeepEqual(got, once) {
		t.Fatalf("重复注册不应改变列表：%v", got)
	}
	RegisterLspAdvanced(nil) // nil 是空操作

	// Specs 与 Names 同步，且 Specs 兜底注入 i 意图字段。
	found := map[string]bool{}
	for _, spec := range r.Specs() {
		name := spec.Function.Name
		if _, want := specIndex(lspAdvancedToolNames, name); want {
			found[name] = true
			if !strings.Contains(string(spec.Function.Parameters), `"i"`) {
				t.Fatalf("%s 的 schema 应带 i 意图字段", name)
			}
		}
	}
	for _, name := range lspAdvancedToolNames {
		if !found[name] {
			t.Fatalf("Specs 缺少 %q", name)
		}
	}
}

// TestLspAdvancedToolsMetadata 验证三个工具的 schema 合法、全部只读，
// 且描述长度受控（可在只读沙箱中安全公布）。
func TestLspAdvancedToolsMetadata(t *testing.T) {
	tools := []Tool{
		newLspSemanticTokensTool(nil), newLspInlayHintsTool(nil), newLspCodeLensTool(nil),
	}
	for i, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			if tool.Name() != lspAdvancedToolNames[i] {
				t.Fatalf("名字 = %q，期望 %q", tool.Name(), lspAdvancedToolNames[i])
			}
			if !json.Valid([]byte(tool.Schema())) {
				t.Fatalf("schema 不是合法 JSON: %s", tool.Schema())
			}
			if tool.Mutating() {
				t.Fatalf("%s 必须只读", tool.Name())
			}
			if len(tool.Description()) >= maxToolDescLen {
				t.Fatalf("描述过长: %d 字节", len(tool.Description()))
			}
		})
	}
}

// TestLspAdvancedToolsReturnExpectedJSON 验证每个工具把适配器结果
// 序列化为中立 DTO 的紧凑 JSON。
func TestLspAdvancedToolsReturnExpectedJSON(t *testing.T) {
	ctx := context.Background()
	fake := cannedAdvancedFake()

	cases := []struct {
		name     string
		tool     Tool
		args     string
		expected any
	}{
		{lspSemanticTokensName, newLspSemanticTokensTool(fake), `{"path":"a.go"}`, fake.tokens},
		{lspInlayHintsName, newLspInlayHintsTool(fake), `{"path":"a.go","line":3,"char":12}`, fake.hints},
		{lspCodeLensName, newLspCodeLensTool(fake), `{"path":"a.go"}`, fake.lenses},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.tool.Run(ctx, t.TempDir(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("Run 返回错误: %v", err)
			}
			want := mustJSON(t, tc.expected)
			if got != want {
				t.Fatalf("输出不匹配\n got: %s\nwant: %s", got, want)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("输出不是合法 JSON: %s", got)
			}
		})
	}
}

// TestLspAdvancedToolsUnavailableWithoutAdapter 检查未注入适配器时
// 三个工具都给出优雅消息、无错误且不 panic。
func TestLspAdvancedToolsUnavailableWithoutAdapter(t *testing.T) {
	ctx := context.Background()
	for _, tool := range []Tool{
		newLspSemanticTokensTool(nil), newLspInlayHintsTool(nil), newLspCodeLensTool(nil),
	} {
		t.Run(tool.Name(), func(t *testing.T) {
			got, err := tool.Run(ctx, t.TempDir(), json.RawMessage(`{"path":"a.go"}`))
			if err != nil {
				t.Fatalf("Run 返回错误: %v", err)
			}
			if got != lspIdeUnavailable {
				t.Fatalf("got %q, want %q", got, lspIdeUnavailable)
			}
		})
	}
}

// TestLspAdvancedToolsPassZeroBasedPositions 验证坐标原样到达适配器，
// 且省略 line/char 时默认为 0。
func TestLspAdvancedToolsPassZeroBasedPositions(t *testing.T) {
	ctx := context.Background()
	fake := cannedAdvancedFake()

	if _, err := newLspInlayHintsTool(fake).Run(ctx, t.TempDir(),
		json.RawMessage(`{"path":"a.go","line":0,"char":7}`)); err != nil {
		t.Fatalf("inlay hints: %v", err)
	}
	if fake.hintPath != "a.go" || fake.hintLine != 0 || fake.hintChar != 7 {
		t.Fatalf("inlay hints got path=%q line=%d char=%d", fake.hintPath, fake.hintLine, fake.hintChar)
	}

	fake = cannedAdvancedFake()
	if _, err := newLspInlayHintsTool(fake).Run(ctx, t.TempDir(),
		json.RawMessage(`{"path":"a.go"}`)); err != nil {
		t.Fatalf("inlay hints defaults: %v", err)
	}
	if fake.hintLine != 0 || fake.hintChar != 0 {
		t.Fatalf("inlay hints 默认值 got line=%d char=%d", fake.hintLine, fake.hintChar)
	}
}

// TestLspAdvancedToolsRejectBadArguments 验证畸形参数与空 path 被拒绝，
// 且不会调用适配器。
func TestLspAdvancedToolsRejectBadArguments(t *testing.T) {
	ctx := context.Background()
	fake := cannedAdvancedFake()
	for _, tool := range []Tool{
		newLspSemanticTokensTool(fake), newLspInlayHintsTool(fake), newLspCodeLensTool(fake),
	} {
		for _, args := range []string{`{`, `{"line":1}`} {
			if _, err := tool.Run(ctx, t.TempDir(), json.RawMessage(args)); err == nil {
				t.Fatalf("%s 应拒绝参数 %s", tool.Name(), args)
			}
		}
	}
	if fake.calls != 0 {
		t.Fatalf("坏参数不应调用适配器，实际调用 %d 次", fake.calls)
	}
}

// TestSetLspAdvancedSwapsAdapter 验证 app 接线：注入前注册的工具报告
// 不可用，注入后原地指向新适配器，顺序与数量不变且重复注入幂等。
func TestSetLspAdvancedSwapsAdapter(t *testing.T) {
	ctx := context.Background()
	r := New(t.TempDir(), true)
	RegisterLspAdvanced(r)

	before, err := r.Run(ctx, lspSemanticTokensName, `{"path":"a.go"}`)
	if err != nil {
		t.Fatalf("注入前 Run: %v", err)
	}
	if before != lspIdeUnavailable {
		t.Fatalf("注入前 got %q, want %q", before, lspIdeUnavailable)
	}

	fake := cannedAdvancedFake()
	names := r.Names()
	r.SetLspAdvanced(fake)

	after, err := r.Run(ctx, lspSemanticTokensName, `{"path":"a.go"}`)
	if err != nil {
		t.Fatalf("注入后 Run: %v", err)
	}
	if want := mustJSON(t, fake.tokens); after != want {
		t.Fatalf("注入后 got %s, want %s", after, want)
	}
	if fake.tokenPath != "a.go" {
		t.Fatalf("适配器收到 path %q，期望 %q", fake.tokenPath, "a.go")
	}

	// 三个工具都指向新适配器。
	for _, name := range lspAdvancedToolNames {
		out, err := r.Run(ctx, name, `{"path":"a.go"}`)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out == lspIdeUnavailable {
			t.Fatalf("%s 注入后仍报告不可用", name)
		}
	}
	// 前面单独跑了一次 semantic tokens，循环又各跑一次，共 4 次。
	if fake.calls != 4 {
		t.Fatalf("适配器调用次数 = %d，期望 4", fake.calls)
	}
	if got := r.Names(); !reflect.DeepEqual(got, names) {
		t.Fatalf("注入不应改变注册顺序：%v", got)
	}

	// 重复注入与 nil/错误类型都不改变清单；nil 让工具回到不可用状态。
	r.SetLspAdvanced(fake)
	r.SetLspAdvanced("not an adapter")
	if got := r.Names(); !reflect.DeepEqual(got, names) {
		t.Fatalf("重复注入不应改变注册顺序：%v", got)
	}
	r.SetLspAdvanced(nil)
	back, err := r.Run(ctx, lspCodeLensName, `{"path":"a.go"}`)
	if err != nil {
		t.Fatalf("nil 注入后 Run: %v", err)
	}
	if back != lspIdeUnavailable {
		t.Fatalf("nil 注入后 got %q, want %q", back, lspIdeUnavailable)
	}
}

// TestSetLspAdvancedWithoutRegister 验证仅注入也能补建工具（与
// SetLspWillRename 一致），且重复调用幂等。
func TestSetLspAdvancedWithoutRegister(t *testing.T) {
	r := New(t.TempDir(), true)
	before := r.Names()
	fake := cannedAdvancedFake()

	r.SetLspAdvanced(fake)
	once := r.Names()
	if len(once) != len(before)+len(lspAdvancedToolNames) {
		t.Fatalf("注入后数量 = %d，期望 %d", len(once), len(before)+len(lspAdvancedToolNames))
	}
	if got := once[len(before):]; !reflect.DeepEqual(got, lspAdvancedToolNames) {
		t.Fatalf("注入追加顺序 = %v，期望 %v", got, lspAdvancedToolNames)
	}

	// 先注入后注册不会重复追加。
	RegisterLspAdvanced(r)
	if got := r.Names(); !reflect.DeepEqual(got, once) {
		t.Fatalf("注册不应重复追加：%v", got)
	}
}
