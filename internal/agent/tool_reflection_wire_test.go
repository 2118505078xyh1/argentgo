package agent

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// 本文件针对接线原语 ReflectionForDispatch 的端到端行为：
// 门控、可修复性判定、脱敏、有界、CJK 安全、入参不变与 setter 并发安全。
// 这些测试不调用 SetToolErrorReflection(true) 之外的全局状态变更，
// 且都在结束时 defer 恢复为关闭，避免污染同包其它测试。

// TestReflectionForDispatchGateOffPassthrough 检查门控关闭时包装函数逐字节原样返回，
// 连凭据形态的文本也不改动——既有行为在 opt-in 之前完全不变。
func TestReflectionForDispatchGateOffPassthrough(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(false)

	cases := []string{
		"error: bad arguments: cannot parse JSON",
		"error: 编辑失败：锚点不存在",
		"api_key=sk_live_abcdefghijklmnop failed",
		"",
		"   ",
		"error: line1\nline2\r\n\tend",
	}
	for _, in := range cases {
		if got := ReflectionForDispatch("edit", in); got != in {
			t.Fatalf("门控关闭：ReflectionForDispatch(%q) = %q，期望逐字节原样 %q", in, got, in)
		}
	}
}

// TestReflectionForDispatchGateOnWraps 检查门控开启且错误可修复时，
// 反馈含修复指令行与 "[tool: X] ERROR" 头，并保留错误正文。
func TestReflectionForDispatchGateOnWraps(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	const errText = "error: bad arguments: cannot parse JSON"
	got := ReflectionForDispatch("edit", errText)

	if !strings.HasPrefix(got, toolErrorFixInstruction+"\n") {
		t.Fatalf("反馈 = %q，期望以修复指令行开头", got)
	}
	if !strings.Contains(got, "[tool: EDIT] ERROR") {
		t.Fatalf("反馈 = %q，期望含大写标签的错误头", got)
	}
	if !strings.Contains(got, "cannot parse JSON") {
		t.Fatalf("反馈 = %q，期望保留错误正文", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("反馈不是合法 UTF-8：%q", got)
	}
}

// TestReflectionForDispatchSkipsUnfixable 检查门控开启但错误不可修复（或无可修复证据）时
// 原样透传，不占用反思预算。
func TestReflectionForDispatchSkipsUnfixable(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	cases := []string{
		"error: unauthorized: invalid api key",
		"error: network unreachable: connection refused",
		"error: configuration error: missing config",
		"error: something totally unknown happened",
		"   ",
	}
	for _, in := range cases {
		if got := ReflectionForDispatch("edit", in); got != in {
			t.Fatalf("不可修复错误：ReflectionForDispatch(%q) = %q，期望原样 %q", in, got, in)
		}
	}
}

// TestReflectionForDispatchRedactsSecrets 检查门控开启的反馈在包装前先脱敏，
// 凭据不会随反射反馈泄漏到下一轮模型上下文。
func TestReflectionForDispatchRedactsSecrets(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	got := ReflectionForDispatch("edit", "error: bad arguments: api_key=sk_live_abcdefghijklmnop failed")
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("反馈泄漏了凭据：%q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("反馈 = %q，期望含 [redacted] 标记", got)
	}
}

// TestReflectionForDispatchBounded 检查超长错误正文被裁剪到有界长度并带截断标记，
// 整条反馈（指令行 + 错误头 + 正文）因此保持有界。
func TestReflectionForDispatchBounded(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	out := "error: bad arguments: " + strings.Repeat("Z", toolErrorReflectionMaxBytes+500)
	got := ReflectionForDispatch("edit", out)

	prefix := toolErrorFixInstruction + "\n[tool: EDIT] ERROR\n"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("反馈 = %q，期望前缀 %q", got, prefix)
	}
	body := strings.TrimPrefix(got, prefix)
	if len(body) > toolErrorReflectionMaxBytes {
		t.Fatalf("正文 = %d 字节，期望 <= %d", len(body), toolErrorReflectionMaxBytes)
	}
	if !strings.HasPrefix(body, verifyTruncatedMarker) {
		t.Fatalf("正文 = %q，期望截断标记", body)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("反馈不是合法 UTF-8：%q", got)
	}
}

// TestReflectionForDispatchCJKNotBroken 检查中文标签与中文正文在超长裁剪下
// 仍保持合法 UTF-8，切点不落在多字节 rune 中间。
func TestReflectionForDispatchCJKNotBroken(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	// "编辑失败" 是可修复标记，确保触发反射；正文超长以覆盖裁剪切点。
	out := "error: 编辑失败：" + strings.Repeat("锚点界", toolErrorReflectionMaxBytes) + "END_TAIL"
	got := ReflectionForDispatch("编辑", out)

	if !utf8.ValidString(got) {
		t.Fatalf("反馈不是合法 UTF-8：%q", got)
	}
	if !strings.Contains(got, "[tool: 编辑] ERROR") {
		t.Fatalf("反馈 = %q，期望保留中文标签", got)
	}
}

// TestReflectionForDispatchDoesNotMutateInput 检查包装调用不改动调用方持有的入参。
// Go 字符串不可变，这里以字节切片为锚点断言其内容在调用前后一致。
func TestReflectionForDispatchDoesNotMutateInput(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	raw := []byte("error: bad arguments: cannot parse JSON")
	in := string(raw)
	_ = ReflectionForDispatch("edit", in)
	if string(raw) != in {
		t.Fatalf("入参被改动：%q", raw)
	}
}

// TestSetToolErrorReflectionConcurrentSafe 检查 setter 与读取可并发调用而不 panic/数据竞争，
// 并在结束时恢复为默认关闭（defer 恢复）。
func TestSetToolErrorReflectionConcurrentSafe(t *testing.T) {
	defer SetToolErrorReflection(false)

	const (
		workers    = 32
		iterations = 200
	)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				SetToolErrorReflection((i+j)%2 == 0)
				_ = toolErrorReflectionEnabled()
				_ = ReflectionForDispatch("edit", "error: bad arguments: x")
			}
		}(i)
	}
	wg.Wait()
}
