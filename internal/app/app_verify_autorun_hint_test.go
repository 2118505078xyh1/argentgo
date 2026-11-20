package app

import (
	"strings"
	"testing"
)

// TestVerifyAutoRunHintConfigured 保证配置了验证命令时会返回审计要求的中文声明，
// 且其内容精确匹配约定的措辞。
func TestVerifyAutoRunHintConfigured(t *testing.T) {
	const want = "编辑后会自动运行验证命令，不要在回复中建议用户重复运行"
	if got := verifyAutoRunHint(true); got != want {
		t.Fatalf("verifyAutoRunHint(true) = %q, want %q", got, want)
	}
}

// TestVerifyAutoRunHintNotConfigured 保证未配置验证命令时返回空串，
// 使默认提示的字节保持不变。
func TestVerifyAutoRunHintNotConfigured(t *testing.T) {
	if got := verifyAutoRunHint(false); got != "" {
		t.Fatalf("verifyAutoRunHint(false) = %q, want 空串", got)
	}
}

// TestVerifyAutoRunHintCoversKeySemantics 保证声明包含 aider 安全审计要求的关键语义：
// 编辑后自动运行、验证命令、不要建议用户重复运行。
func TestVerifyAutoRunHintCoversKeySemantics(t *testing.T) {
	hint := verifyAutoRunHint(true)
	for _, want := range []string{
		"编辑后",
		"自动",
		"验证命令",
		"不要",
		"建议用户",
		"重复运行",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("verifyAutoRunHint(true) 缺少关键语义 %q：%q", want, hint)
		}
	}
}

// TestVerifyAutoRunHintStable 保证多次调用逐字节一致（纯函数，可用于缓存稳定的提示前缀）。
func TestVerifyAutoRunHintStable(t *testing.T) {
	first := verifyAutoRunHint(true)
	for i := 0; i < 3; i++ {
		if got := verifyAutoRunHint(true); got != first {
			t.Fatalf("verifyAutoRunHint(true) 不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
		}
	}
}

// TestVerifyAutoRunHintEnabledOptIn 保证接线开关默认关闭，
// 只有显式设置 ARGENT_VERIFY_AUTORUN_HINT=1 才开启。
func TestVerifyAutoRunHintEnabledOptIn(t *testing.T) {
	if verifyAutoRunHintEnabled(nil) {
		t.Fatal("env 为 nil 时必须默认关闭")
	}
	if verifyAutoRunHintEnabled(envFrom(nil)) {
		t.Fatal("未设置 ARGENT_VERIFY_AUTORUN_HINT 时必须默认关闭")
	}
	for _, off := range []string{"", "0", "yes", "true", " 0 "} {
		if verifyAutoRunHintEnabled(envFrom(map[string]string{"ARGENT_VERIFY_AUTORUN_HINT": off})) {
			t.Errorf("ARGENT_VERIFY_AUTORUN_HINT=%q 不应开启", off)
		}
	}
	for _, on := range []string{"1", " 1 "} {
		if !verifyAutoRunHintEnabled(envFrom(map[string]string{"ARGENT_VERIFY_AUTORUN_HINT": on})) {
			t.Errorf("ARGENT_VERIFY_AUTORUN_HINT=%q 应开启", on)
		}
	}
}
