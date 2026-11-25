package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/server"
)

// TestStatusProviderReturnsSnapshotBytes 验证默认适配器逐字节返回
// StatusSnapshot.JSON()：三段字段顺序、脱敏、CJK 原样与无尾换行全部继承，
// 不额外注入 "capabilities" 字段。
func TestStatusProviderReturnsSnapshotBytes(t *testing.T) {
	turn := map[string]string{"阶段": "流式输出 <>& 中文"}
	readiness := map[string]string{"ready": "true"}
	integrity := map[string]string{"model": "gpt-4o"}
	provider := newStatusProvider(turn, readiness, integrity)

	got := provider.StatusJSON()
	want := NewStatusSnapshot(turn, readiness, integrity).JSON()
	if !bytes.Equal(got, want) {
		t.Fatalf("StatusJSON() = %s, want %s (verbatim)", got, want)
	}
	if !bytes.Contains(got, []byte("流式输出 <>& 中文")) {
		t.Fatalf("CJK/HTML 字符被转义: %s", got)
	}
	if bytes.HasSuffix(got, []byte("\n")) {
		t.Fatalf("StatusJSON() 不应带尾换行: %q", got)
	}
}

// TestStatusProviderZeroSections 验证三段全空时输出 {}，与 StatusSnapshot 的
// 零值语义一致，便于“未就绪”时仍返回合法 JSON。
func TestStatusProviderZeroSections(t *testing.T) {
	if got := string(newStatusProvider(nil, nil, nil).StatusJSON()); got != "{}" {
		t.Fatalf("StatusJSON() = %s, want {}", got)
	}
}

// TestStatusProviderWithCapabilities 是 capability_status 的接入断言：
// CapabilityBoard 的能力目录被汇聚进 /v1/status 输出的顶层 "capabilities"
// 字段，逐字节保留 board.JSON() 的内容（含 CJK 分类），且既有三段不受影响。
func TestStatusProviderWithCapabilities(t *testing.T) {
	var board server.CapabilityBoard
	board.Register("apply_patch", "工具", true)
	board.Register("grep_ex", "search", false)

	turn := map[string]string{"阶段": "流式输出"}
	provider := newStatusProviderWithCapabilities(turn, nil, nil, &board)
	got := provider.StatusJSON()

	// 顶层字段按字典序：capabilities 在 turn 之前。
	turnJSON := NewStatusSnapshot(turn, nil, nil).JSON()
	want := `{"capabilities":` + string(board.JSON()) + `,` +
		string(turnJSON[1:len(turnJSON)-1]) + `}`
	if string(got) != want {
		t.Fatalf("StatusJSON() = %s, want %s", got, want)
	}
	// board.JSON() 本身已保证能力名与 CJK 分类按 UTF-8 原样输出。
	if !bytes.Contains(got, []byte(`"工具"`)) {
		t.Fatalf("能力目录的 CJK 内容缺失: %s", got)
	}
	if !bytes.Equal(provider.StatusJSON(), got) {
		t.Fatal("StatusJSON() 不稳定")
	}
}

// TestConfigInfoProviderImplementsStatusProvider 锁定默认接线路径：
// app.NewConfigInfoProvider 返回的实例必须同时实现 server.StatusProvider，
// cli 侧才能通过接口断言把它注入 --serve / --rpc，无需改动 cmd/argent。
func TestConfigInfoProviderImplementsStatusProvider(t *testing.T) {
	resolve := func(cli.IO, cli.Options) (Resolved, error) {
		return Resolved{Model: "gpt-4o", Provider: "openai", Root: `D:\work\demo`}, nil
	}
	provider := newConfigInfoProvider(cli.IO{Env: func(string) string { return "" }}, resolve)
	sp, ok := any(provider).(server.StatusProvider)
	if !ok {
		t.Fatal("ConfigInfoProvider 未实现 server.StatusProvider")
	}
	body := sp.StatusJSON()
	var parsed struct {
		Integrity struct {
			Values struct {
				Model    string `json:"model"`
				Provider string `json:"provider"`
			} `json:"values"`
		} `json:"integrity"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("StatusJSON 不是合法 JSON: %s (%v)", body, err)
	}
	if parsed.Integrity.Values.Model != "gpt-4o" || parsed.Integrity.Values.Provider != "openai" {
		t.Fatalf("状态分段未包含已解析配置: %s", body)
	}
	if bytes.Contains(body, []byte(`D:\work\demo`)) {
		t.Fatalf("状态输出泄露 workspace 路径: %s", body)
	}
}

// TestResolveErrorStatusProviderStillValid 验证配置解析失败时嵌入的
// statusProvider 为零值，StatusJSON 返回 {} 而非 panic 或非法 JSON。
func TestResolveErrorStatusProviderStillValid(t *testing.T) {
	provider := newConfigInfoProvider(cli.IO{}, func(cli.IO, cli.Options) (Resolved, error) {
		return Resolved{}, errResolveForTest
	})
	sp, ok := any(provider).(server.StatusProvider)
	if !ok {
		t.Fatal("零值适配器未实现 server.StatusProvider")
	}
	if got := string(sp.StatusJSON()); got != "{}" {
		t.Fatalf("StatusJSON() = %s, want {}", got)
	}
}

// errResolveForTest 是仅供本文件使用的稳定错误哨兵。
var errResolveForTest = &testResolveError{}

type testResolveError struct{}

func (*testResolveError) Error() string { return "load config: boom" }
