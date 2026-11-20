package hooks

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSanitizeTelemetryTagTruncatesAtRuneBoundary 校验标签长度上限按 rune 计，
// 且 ASCII 与多字节字符都不会在截断处产生半个 UTF-8 序列。
func TestSanitizeTelemetryTagTruncatesAtRuneBoundary(t *testing.T) {
	ascii := strings.Repeat("a", telemetryTagMaxRunes+64)
	cjk := strings.Repeat("中", telemetryTagMaxRunes+64)

	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "ascii", input: ascii},
		{name: "cjk", input: cjk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeTelemetryTag(tc.input)
			if n := utf8.RuneCountInString(got); n != telemetryTagMaxRunes {
				t.Fatalf("标签 rune 数 = %d, want %d", n, telemetryTagMaxRunes)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("截断结果不是合法 UTF-8: %q", got)
			}
		})
	}
}

// TestSanitizeTelemetryTagWhitelist 校验白名单外的字符使整个值折叠为 "other"，
// 而由白名单字符组成的分类名与事件名原样保留。
func TestSanitizeTelemetryTagWhitelist(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "event name", input: "PreToolUse", want: "PreToolUse"},
		{name: "snake case", input: "turn_end", want: "turn_end"},
		{name: "separators", input: "my.hook:v2-tag_1", want: "my.hook:v2-tag_1"},
		{name: "empty", input: "", want: telemetryTagOther},
		{name: "space", input: "hello world", want: telemetryTagOther},
		{name: "path separator", input: "/tmp/secret.log", want: telemetryTagOther},
		{name: "at sign", input: "user@example.com", want: telemetryTagOther},
		{name: "quote", input: `say "hi"`, want: telemetryTagOther},
		{name: "control char", input: "line\nbreak", want: telemetryTagOther},
		{name: "equals", input: "key=value", want: telemetryTagOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeTelemetryTag(tc.input); got != tc.want {
				t.Fatalf("sanitizeTelemetryTag(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestSanitizeTelemetryTagCJK 校验中日韩文字被视为合法标签字符而非乱码，
// 混有空白的 CJK 值仍按白名单规则折叠，长 CJK 串安全截断。
func TestSanitizeTelemetryTagCJK(t *testing.T) {
	if got := sanitizeTelemetryTag("中断"); got != "中断" {
		t.Fatalf("纯 CJK 值应原样保留, got %q", got)
	}
	if got := sanitizeTelemetryTag("中文标签"); got != "中文标签" {
		t.Fatalf("CJK 标签应原样保留, got %q", got)
	}
	if got := sanitizeTelemetryTag("中文 空格"); got != telemetryTagOther {
		t.Fatalf("含空白的 CJK 值应折叠为 other, got %q", got)
	}

	long := strings.Repeat(" Hook ", 2) + strings.Repeat("中", telemetryTagMaxRunes+10)
	got := sanitizeTelemetryTag(long)
	if got != telemetryTagOther {
		t.Fatalf("含空白的长 CJK 值应折叠为 other, got %q", got)
	}
}

// TestSanitizeTelemetryTagRedactsCredentials 校验凭据形态的值绝不会原样进入标签。
func TestSanitizeTelemetryTagRedactsCredentials(t *testing.T) {
	secrets := []string{
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"sk-proj-abcdefghijklmnopqrstuvwxyz",
		"Bearer abcdefghijklmnopqrstuvwxyz",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghij",
	}
	for _, secret := range secrets {
		got := sanitizeTelemetryTag(secret)
		if got == secret || strings.Contains(got, secret) {
			t.Fatalf("标签泄漏凭据 %q: got %q", secret, got)
		}
		if got != telemetryTagOther {
			t.Fatalf("凭据标签应折叠为 other, got %q", got)
		}
	}
}

// TestTelemetrySafeFiltersFields 校验字段白名单：只保留分类/计数/状态标量，
// 原始内容键、嵌套结构与未知类型全部丢弃。
func TestTelemetrySafeFiltersFields(t *testing.T) {
	event := map[string]any{
		// 保留字段
		"event":       "PreToolUse",
		"count":       3,
		"duration_ms": int64(12),
		"elapsed_ms":  1.5,
		"error_code":  "timeout",
		"slow":        true,
		"blocked":     false,
		// 必须丢弃的原始内容 / 结构
		"prompt":     "用户原始提示词",
		"output":     "工具原始输出",
		"args":       map[string]any{"k": "v"},
		"error":      "boom",
		"command":    "curl https://example.com",
		"path":       "/home/user/.env",
		"api_key":    "sk-proj-should-be-dropped",
		"session_id": "sess-123",
		"nested":     map[string]any{"count": 9},
		"list":       []any{1, 2, 3},
		"nil":        nil,
	}

	got := telemetrySafe(event)

	want := map[string]any{
		"event":       "PreToolUse",
		"count":       3,
		"duration_ms": int64(12),
		"elapsed_ms":  1.5,
		"error_code":  "timeout",
		"slow":        true,
		"blocked":     false,
	}
	if len(got) != len(want) {
		t.Fatalf("字段数 = %d, want %d: %#v", len(got), len(want), got)
	}
	for key, value := range want {
		actual, ok := got[key]
		if !ok {
			t.Fatalf("缺少安全字段 %q", key)
		}
		if actual != value {
			t.Errorf("字段 %q = %#v, want %#v", key, actual, value)
		}
	}

	// 输入不得被修改。
	if event["prompt"] != "用户原始提示词" {
		t.Fatal("telemetrySafe 修改了输入映射")
	}
}

// TestTelemetrySafeRedactsValues 校验允许字段中的字符串同样经过脱敏与规约。
func TestTelemetrySafeRedactsValues(t *testing.T) {
	event := map[string]any{
		"reason":   "failed with ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"category": "正常分类",
		"name":     "check hook",
	}
	got := telemetrySafe(event)

	if secret, ok := got["reason"].(string); !ok || strings.Contains(secret, "ghp_") {
		t.Fatalf("reason 未脱敏: %#v", got["reason"])
	}
	if got["reason"] != telemetryTagOther {
		t.Errorf("含凭据的 reason 应折叠为 other, got %#v", got["reason"])
	}
	if got["category"] != "正常分类" {
		t.Errorf("CJK 分类应原样保留, got %#v", got["category"])
	}
	if got["name"] != telemetryTagOther {
		t.Errorf("含空白的名称应折叠为 other, got %#v", got["name"])
	}
}

// TestTelemetrySafeEmpty 校验 nil 与空输入都产生非 nil 的空映射且不 panic。
func TestTelemetrySafeEmpty(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			got := telemetrySafe(input)
			if got == nil {
				t.Fatal("telemetrySafe 返回了 nil 映射")
			}
			if len(got) != 0 {
				t.Fatalf("空输入应返回空映射, got %#v", got)
			}
		})
	}
}

// TestHookTelemetrySafeMap 校验观察记录转映射时命令被规约，安全标量保留。
func TestHookTelemetrySafeMap(t *testing.T) {
	record := HookTelemetry{
		Name:     "/usr/local/bin/run-hook --token=ghp_abcdefghijklmnopqrstuvwxyz",
		Event:    PreToolUse,
		Elapsed:  150 * time.Millisecond,
		Err:      nil,
		ExitCode: 1,
		TimedOut: false,
		Blocked:  true,
		Slow:     true,
	}
	got := record.SafeMap()

	if got["name"] != telemetryTagOther {
		t.Errorf("含路径/凭据的命令名应折叠为 other, got %#v", got["name"])
	}
	if got["event"] != string(PreToolUse) {
		t.Errorf("event = %#v, want %q", got["event"], PreToolUse)
	}
	if got["elapsed_ms"] != int64(150) {
		t.Errorf("elapsed_ms = %#v, want 150", got["elapsed_ms"])
	}
	if got["exit_code"] != 1 || got["blocked"] != true || got["slow"] != true {
		t.Errorf("状态标量丢失: %#v", got)
	}
	if got["failed"] != true {
		t.Errorf("failed 应由 Failed() 推导为 true: %#v", got)
	}
	// 原始命令绝不能进入输出。
	for _, value := range got {
		if s, ok := value.(string); ok && strings.Contains(s, "ghp_") {
			t.Fatalf("SafeMap 泄漏原始命令: %#v", got)
		}
	}
}

// TestSafeTelemetryAdapter 校验适配器是默认关闭的可选接线点，并隔离 sink panic。
func TestSafeTelemetryAdapter(t *testing.T) {
	if adapter := SafeTelemetryAdapter(nil); adapter != nil {
		t.Fatal("nil sink 应返回 nil 适配器")
	}

	var captured map[string]any
	adapter := SafeTelemetryAdapter(func(m map[string]any) { captured = m })
	adapter(HookTelemetry{Name: "run-hook", Event: PostToolUse, Elapsed: time.Millisecond})
	if captured == nil {
		t.Fatal("适配器未调用 sink")
	}
	if captured["name"] != "run-hook" || captured["event"] != string(PostToolUse) {
		t.Fatalf("适配器传递了错误的映射: %#v", captured)
	}

	// sink panic 被隔离，不得向外传播。
	panicking := SafeTelemetryAdapter(func(map[string]any) { panic("bad sink") })
	panicking(HookTelemetry{Name: "run-hook"})
}
