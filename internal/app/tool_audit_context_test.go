package app

// 工具审计上下文的离线测试。所有用例走注入时钟的纯函数，不依赖真实模型、
// 真实工具或 sysdate，断言覆盖字段完整、脱敏、CJK、空参数与大结果截断。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/hooks"
)

func testAuditTime() time.Time {
	return time.Date(2026, 11, 20, 3, 4, 5, 123456789, time.UTC)
}

func TestToolAuditContextFieldsComplete(t *testing.T) {
	at := testAuditTime()
	ctx := toolAuditContextAt("bash", `{"command":"ls"}`, toolAuditResult{
		output:  "file.txt",
		elapsed: 1500 * time.Millisecond,
	}, at)

	want := map[string]string{
		"tool":    "bash",
		"args":    `{"command":"ls"}`,
		"result":  "file.txt",
		"status":  "ok",
		"elapsed": "1.5s",
		"ts":      at.Format(time.RFC3339Nano),
	}
	if len(ctx) != len(want) {
		t.Fatalf("field count = %d, want %d: %v", len(ctx), len(want), ctx)
	}
	for key, value := range want {
		if ctx[key] != value {
			t.Errorf("field %q = %q, want %q", key, ctx[key], value)
		}
	}
}

func TestToolAuditContextRedactsCredentials(t *testing.T) {
	secrets := []string{"hunter2", "sk-proj-abcdefghijkl", "ghp_abcdefghijklmno"}
	ctx := toolAuditContextAt("bash", `{"api_key":"hunter2","note":"use sk-proj-abcdefghijkl"}`,
		toolAuditResult{output: "token ghp_abcdefghijklmno appended"}, testAuditTime())

	for _, secret := range secrets {
		for key, value := range ctx {
			if strings.Contains(value, secret) {
				t.Errorf("field %q leaked %q: %q", key, secret, value)
			}
		}
	}
	if !strings.Contains(ctx["args"], "[redacted:api key]") {
		t.Errorf("credential-named key was not redacted: %q", ctx["args"])
	}
	if !strings.Contains(ctx["result"], "[redacted:token]") {
		t.Errorf("credential-shaped value was not redacted: %q", ctx["result"])
	}
}

func TestToolAuditContextCJKPreserved(t *testing.T) {
	args := `{"command":"你好，世界"}`
	output := "工具执行成功：中文内容完整保留 ✅"
	ctx := toolAuditContextAt("write", args, toolAuditResult{output: output}, testAuditTime())

	if ctx["args"] != args {
		t.Errorf("CJK args changed: got %q, want %q", ctx["args"], args)
	}
	if ctx["result"] != output {
		t.Errorf("CJK result changed: got %q, want %q", ctx["result"], output)
	}
	for key, value := range ctx {
		if !utf8.ValidString(value) {
			t.Errorf("field %q is not valid UTF-8: %q", key, value)
		}
		if strings.ContainsRune(value, utf8.RuneError) {
			t.Errorf("field %q contains a replacement rune: %q", key, value)
		}
	}
}

func TestToolAuditContextEmptyArgs(t *testing.T) {
	for _, args := range []string{"", "   \n\t"} {
		ctx := toolAuditContextAt("read", args, toolAuditResult{output: "ok"}, testAuditTime())
		if ctx["args"] != "" {
			t.Errorf("args %q produced summary %q, want empty", args, ctx["args"])
		}
		if ctx["tool"] != "read" || ctx["status"] != "ok" {
			t.Errorf("empty args broke the rest of the context: %v", ctx)
		}
	}
}

func TestToolAuditContextLargeResultTruncated(t *testing.T) {
	large := strings.Repeat("中文A", 20000)
	ctx := toolAuditContextAt("write", "", toolAuditResult{output: large}, testAuditTime())

	got := ctx["result"]
	if !strings.HasSuffix(got, toolAuditTruncatedMark) {
		t.Fatalf("large result not marked truncated: %q", got[len(got)-32:])
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated result is not valid UTF-8")
	}
	wantRunes := toolAuditMaxSummaryRunes + utf8.RuneCountInString(toolAuditTruncatedMark)
	if n := utf8.RuneCountInString(got); n != wantRunes {
		t.Fatalf("truncated result runes = %d, want %d", n, wantRunes)
	}
}

func TestToolAuditContextErrorStatusAndNegativeElapsed(t *testing.T) {
	ctx := toolAuditContextAt("bash", "{}", toolAuditResult{
		err:     errors.New("boom sk-ant-secret123"),
		elapsed: -time.Second,
	}, testAuditTime())

	if ctx["status"] != "error" {
		t.Errorf("status = %q, want error", ctx["status"])
	}
	if strings.Contains(ctx["result"], "sk-ant-secret123") {
		t.Errorf("error text leaked: %q", ctx["result"])
	}
	if ctx["elapsed"] != "0s" {
		t.Errorf("negative elapsed = %q, want 0s", ctx["elapsed"])
	}
}

func TestHookedToolsAuditSinkIsOptIn(t *testing.T) {
	bridge := &hookBridge{dispatcher: &recordingDispatcher{}, config: &hooks.Config{}}
	inner := &stubToolRunner{out: "tool output"}
	var captured map[string]string
	h := &hookedTools{
		inner:  inner,
		bridge: bridge,
		auditSink: func(ctx map[string]string) {
			captured = ctx
		},
	}

	out, err := h.Run(context.Background(), "bash", `{"command":"echo hi"}`)
	if err != nil || out != "tool output" {
		t.Fatalf("Run = %q, %v; want the untouched tool output", out, err)
	}
	if captured == nil {
		t.Fatal("audit sink was not called")
	}
	if captured["tool"] != "bash" || captured["status"] != "ok" || captured["result"] != "tool output" {
		t.Fatalf("audit context = %v, want the completed call", captured)
	}

	// 未接线时不得投递，且工具输出与接线时逐字节一致。
	captured = nil
	h.auditSink = nil
	unwired, err := h.Run(context.Background(), "bash", `{"command":"echo hi"}`)
	if err != nil || unwired != out {
		t.Fatalf("unwired Run = %q, %v; want %q", unwired, err, out)
	}
	if captured != nil {
		t.Fatalf("unwired Run still emitted %v", captured)
	}
}
