package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// panicAndRecover 在受控的 defer 中触发 fn，返回 recover() 捕获到的值，
// 用来模拟真实 worker 中"panic 已经发生、需要被规范化"的现场。
func panicAndRecover(fn func()) (p any) {
	defer func() { p = recover() }()
	fn()
	return p
}

// TestSafeToolPanicNilReturnsNil 检查没有 panic 时不构造任何错误。
func TestSafeToolPanicNilReturnsNil(t *testing.T) {
	if got := safeToolPanic(nil); got != nil {
		t.Fatalf("safeToolPanic(nil) = %v, want nil", got)
	}
}

// TestSafeToolPanicStringValue 检查字符串 panic 被渲染成带类型信息的错误。
func TestSafeToolPanicStringValue(t *testing.T) {
	err := safeToolPanic(panicAndRecover(func() { panic("boom") }))
	if err == nil {
		t.Fatal("safeToolPanic(string panic) = nil, want error")
	}
	got := err.Error()
	if !strings.HasPrefix(got, "tool panic (string): ") {
		t.Fatalf("error = %q, want string type label and value", got)
	}
	if !strings.Contains(got, "boom") {
		t.Fatalf("error = %q, want the panic text kept", got)
	}
}

// TestSafeToolPanicErrorValue 检查 error 对象 panic 保留其 Error() 文本并给出动态类型。
func TestSafeToolPanicErrorValue(t *testing.T) {
	want := errors.New("database unavailable")
	err := safeToolPanic(panicAndRecover(func() { panic(want) }))
	if err == nil {
		t.Fatal("safeToolPanic(error panic) = nil, want error")
	}
	got := err.Error()
	if !strings.Contains(got, "*errors.errorString") {
		t.Fatalf("error = %q, want the concrete type in the label", got)
	}
	if !strings.Contains(got, want.Error()) {
		t.Fatalf("error = %q, want the original Error() text", got)
	}
}

// TestSafeToolPanicCJKKept 检查中文 panic 值保持完整且合法，控制字符被替换为空格。
func TestSafeToolPanicCJKKept(t *testing.T) {
	const msg = "工具崩溃：解析「配置」失败\n第二行"
	err := safeToolPanic(msg)
	if err == nil {
		t.Fatal("safeToolPanic(CJK) = nil, want error")
	}
	got := err.Error()
	if !utf8.ValidString(got) {
		t.Fatalf("error is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "工具崩溃：解析「配置」失败") {
		t.Fatalf("error = %q, want the CJK text kept", got)
	}
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("error = %q, want control characters replaced with spaces", got)
	}
}

// TestSafeToolPanicLargeObjectBounded 检查超大 panic 值（长 CJK 字符串与深结构体）
// 被裁剪到有界长度、带截断标记，且切点落在 rune 边界（CJK 安全）。
func TestSafeToolPanicLargeObjectBounded(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"超长中文字符串", strings.Repeat("界", toolPanicMaxValueBytes+100)},
		{"大结构体", struct{ Data [4096]byte }{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := safeToolPanic(panicAndRecover(func() { panic(tc.value) }))
			if err == nil {
				t.Fatal("safeToolPanic(large value) = nil, want error")
			}
			got := err.Error()
			if !utf8.ValidString(got) {
				t.Fatalf("error is not valid UTF-8: %q", got)
			}
			if !strings.Contains(got, toolPanicTruncatedMarker) {
				t.Fatalf("error = %q, want truncation marker", got)
			}
			prefix := "tool panic (" + fmt.Sprintf("%T", tc.value) + "): "
			valuePart := strings.TrimPrefix(got, prefix)
			if len(valuePart) > toolPanicMaxValueBytes {
				t.Fatalf("value = %d bytes, want <= %d", len(valuePart), toolPanicMaxValueBytes)
			}
		})
	}
}

// panickyError 的 Error 方法自身会 panic；用于验证辅助函数不会因二次 panic 而崩溃。
type panickyError struct{}

func (panickyError) Error() string { panic("error method exploded") }

// TestSafeToolPanicSurvivesPanickingError 检查即使 panic 值的 Error() 方法再次 panic，
// 辅助函数也不会崩溃，而是返回一条可读错误。
func TestSafeToolPanicSurvivesPanickingError(t *testing.T) {
	err := safeToolPanic(panickyError{})
	if err == nil {
		t.Fatal("safeToolPanic(panickyError) = nil, want error")
	}
	got := err.Error()
	if !utf8.ValidString(got) {
		t.Fatalf("error is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "tool panic (") {
		t.Fatalf("error = %q, want the standard panic label", got)
	}
}

// TestSafeToolPanicDeterministic 检查相同 panic 值重复转换得到逐字节相同的错误。
func TestSafeToolPanicDeterministic(t *testing.T) {
	want := safeToolPanic("稳定")
	if want == nil {
		t.Fatal("safeToolPanic(\"稳定\") = nil, want error")
	}
	for i := 0; i < 3; i++ {
		got := safeToolPanic("稳定")
		if got == nil || got.Error() != want.Error() {
			t.Fatalf("safeToolPanic(call %d) = %v, want %v", i, got, want)
		}
	}
}
