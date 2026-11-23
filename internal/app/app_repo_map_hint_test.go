package app

import (
	"strings"
	"testing"
)

// TestRepoMapReadOnlyHintNonEmpty 保证注入声明不是空串，避免误接线时悄悄失效。
func TestRepoMapReadOnlyHintNonEmpty(t *testing.T) {
	if strings.TrimSpace(repoMapReadOnlyHint()) == "" {
		t.Fatal("repoMapReadOnlyHint() 返回空串")
	}
}

// TestRepoMapReadOnlyHintCoversKeySemantics 保证声明包含 aider 安全审计要求的关键语义：
// 只读参考、文件出现不等于编辑授权、编辑前必须先读取并加入上下文。
func TestRepoMapReadOnlyHintCoversKeySemantics(t *testing.T) {
	hint := repoMapReadOnlyHint()
	for _, want := range []string{
		"repo map",
		"仅供参考",
		"不构成",
		"编辑授权",
		"读取",
		"上下文",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("repoMapReadOnlyHint() 缺少关键语义 %q：%q", want, hint)
		}
	}
}

// TestRepoMapReadOnlyHintStable 保证多次调用逐字节一致（纯函数，可用于缓存稳定的提示前缀）。
func TestRepoMapReadOnlyHintStable(t *testing.T) {
	first := repoMapReadOnlyHint()
	for i := 0; i < 3; i++ {
		if got := repoMapReadOnlyHint(); got != first {
			t.Fatalf("repoMapReadOnlyHint() 不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
		}
	}
}

// TestRepoMapHintEnabledDefaultOn 保证接线开关默认开启，只有 ARGENT_REPO_MAP_HINT=0 才关闭。
func TestRepoMapHintEnabledDefaultOn(t *testing.T) {
	if !repoMapHintEnabled(nil) {
		t.Fatal("env 为 nil 时必须默认开启")
	}
	if !repoMapHintEnabled(envFrom(nil)) {
		t.Fatal("未设置 ARGENT_REPO_MAP_HINT 时必须默认开启")
	}
	for _, off := range []string{"0", " 0 "} {
		if repoMapHintEnabled(envFrom(map[string]string{"ARGENT_REPO_MAP_HINT": off})) {
			t.Errorf("ARGENT_REPO_MAP_HINT=%q 应关闭", off)
		}
	}
	for _, on := range []string{"", "1", " 1 ", "true", "yes", " true "} {
		if !repoMapHintEnabled(envFrom(map[string]string{"ARGENT_REPO_MAP_HINT": on})) {
			t.Errorf("ARGENT_REPO_MAP_HINT=%q 应开启", on)
		}
	}
}
