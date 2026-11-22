package cli

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/server"
)

// danglingCallEvents 构造一个以未应答工具调用结束的会话：
// user → assistant（声明 call-1）→ 没有对应 tool 结果，即悬空调用。
func danglingCallEvents() []server.SessionEvent {
	return []server.SessionEvent{
		{ID: "e0", Kind: "header", Data: json.RawMessage(`{"title":"修复会话"}`)},
		{ID: "e1", Parent: "e0", Kind: "user", Data: json.RawMessage(`{"text":"请修复这个 bug"}`)},
		{
			ID: "e2", Parent: "e1", Kind: "assistant",
			Data: json.RawMessage(`{"text":"我来调用工具","tool_calls":[{"id":"call-1","function":{"name":"edit","arguments":{}}}]}`),
		},
	}
}

// verifyLine 在输出里找以 prefix 开头的整行。
func verifyLine(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func TestReplayVerifyDanglingToolCall(t *testing.T) {
	store := &fakeStore{events: map[string][]server.SessionEvent{"s1": danglingCallEvents()}}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"replay", "s1", "--verify"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	got := out.String()

	if line := verifyLine(got, "Category: "); line != "Category: interrupted_turn" {
		t.Fatalf("Category 行 = %q", line)
	}
	if line := verifyLine(got, "Unpaired: "); line != "Unpaired: 1" {
		t.Fatalf("未配对数行 = %q", line)
	}
	if line := verifyLine(got, "Broken links: "); line != "Broken links: 0" {
		t.Fatalf("断链行 = %q", line)
	}
	if line := verifyLine(got, "Active leaf: "); line != "Active leaf: e2" {
		t.Fatalf("活跃叶行 = %q", line)
	}
	if !strings.Contains(got, "未配对工具调用 1 个，会话不完整") {
		t.Fatalf("缺少 ReplaySummary 中文摘要: %q", got)
	}
	if !strings.Contains(got, "有 1 个未完成的工具调用") {
		t.Fatalf("缺少 HintFromReplay 公告: %q", got)
	}
	fp := strings.TrimPrefix(verifyLine(got, "Fingerprint: "), "Fingerprint: ")
	if len(fp) != 64 {
		t.Fatalf("fingerprint 长度 = %d, want 64: %q", len(fp), fp)
	}
	if _, err := hex.DecodeString(fp); err != nil {
		t.Fatalf("fingerprint 非十六进制: %q: %v", fp, err)
	}
	if line := verifyLine(got, "Verify: "); line != "Verify: ok" {
		t.Fatalf("Verify 行 = %q", line)
	}
	// 诊断模式不应回退到逐行转录渲染。
	if strings.Contains(got, "user> ") || strings.Contains(got, "assistant> ") {
		t.Fatalf("--verify 混入了转录行: %q", got)
	}
}

func TestReplayDefaultBaselineUnchanged(t *testing.T) {
	store := &fakeStore{events: map[string][]server.SessionEvent{
		"s1": {
			{ID: "e1", Kind: "user", Data: json.RawMessage(`{"text":"plan"}`)},
			{ID: "e2", Kind: "assistant", Data: json.RawMessage(`{"text":"on it"}`)},
			{ID: "e3", Kind: "tool", Data: json.RawMessage(`{"name":"edit","output":"done"}`)},
		},
	}}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"replay", "s1"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	want := "== s1  ==\nuser> plan\nassistant> on it\ntool edit> done\n"
	if got := out.String(); got != want {
		t.Fatalf("默认渲染变化:\n got = %q\nwant = %q", got, want)
	}
}

func TestReplayVerifyCJK(t *testing.T) {
	now := time.Now()
	store := &fakeStore{
		infos: []server.SessionInfo{{ID: "s1", Title: "中文会话标题", Updated: now}},
		events: map[string][]server.SessionEvent{
			"s1": {
				{ID: "e1", Kind: "user", Data: json.RawMessage(`{"text":"检查中文输出"}`)},
				{ID: "e2", Kind: "assistant", Data: json.RawMessage(`{"text":"好的","tool_calls":[{"id":"调用-甲"}]}`)},
			},
		},
	}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"replay", "--verify"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "中文会话标题") {
		t.Fatalf("CJK 标题丢失: %q", got)
	}
	if !strings.Contains(got, "未配对工具调用 1 个，会话不完整") ||
		!strings.Contains(got, "有 1 个未完成的工具调用") {
		t.Fatalf("CJK 中文诊断异常: %q", got)
	}
	if line := verifyLine(got, "Unpaired: "); line != "Unpaired: 1" {
		t.Fatalf("未配对数行 = %q", line)
	}
}

func TestReplayVerifyInvalidID(t *testing.T) {
	store := &fakeStore{events: map[string][]server.SessionEvent{"s1": danglingCallEvents()}}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"replay", "--verify", "nope"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "error") {
		t.Fatalf("缺少错误提示: %q", errOut.String())
	}
	if out.String() != "" {
		t.Fatalf("非法 id 不应输出诊断: %q", out.String())
	}
}

func TestReplayHelpDocumentsVerify(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	if code := runReplay(App{IO: ioState}, nil, []string{"--help"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "--verify") {
		t.Fatalf("help 未列出 --verify: %q", out.String())
	}
}
