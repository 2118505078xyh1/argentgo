package app

import (
	"strings"
	"testing"
)

// TestApplyPrivacyPostureAnyIsNoOp 锁定默认姿态："any"（以及未设置/空模式）保留 Web 工具，并接受远程 base URL。
// （接上行）
func TestApplyPrivacyPostureAnyIsNoOp(t *testing.T) {
	webTools := []string{"web_search", "web_fetch"}
	for _, mode := range []string{"any", "", "  "} {
		blocked, err := ApplyPrivacyPosture(mode, "https://api.openai.com/v1", webTools)
		if err != nil {
			t.Fatalf("mode %q: unexpected error: %v", mode, err)
		}
		if len(blocked) != 0 {
			t.Fatalf("mode %q: blocked = %v, want none", mode, blocked)
		}
	}
}

// TestApplyPrivacyPostureLocalOnlyBlocksWebTools 验证 local-only 保留所有 Web 工具，同时仍允许回环聊天端点。
// （接上行）
func TestApplyPrivacyPostureLocalOnlyBlocksWebTools(t *testing.T) {
	for _, baseURL := range []string{
		"http://localhost:11434",
		"http://127.0.0.1:8080/v1",
		"http://[::1]:9000",
	} {
		blocked, err := ApplyPrivacyPosture("local-only", baseURL, []string{"web_search", "web_fetch"})
		if err != nil {
			t.Fatalf("baseURL %q: unexpected error: %v", baseURL, err)
		}
		if strings.Join(blocked, ",") != "web_search,web_fetch" {
			t.Fatalf("baseURL %q: blocked = %v", baseURL, blocked)
		}
	}
}

// TestApplyPrivacyPostureLocalOnlyRejectsRemoteBaseURL 验证安全失败的端点检查：远程 base URL 是错误，而非静默降级。
// （接上行）
func TestApplyPrivacyPostureLocalOnlyRejectsRemoteBaseURL(t *testing.T) {
	blocked, err := ApplyPrivacyPosture("local-only", "https://api.openai.com/v1", []string{"web_search"})
	if err == nil {
		t.Fatal("expected an error for a remote base URL in local-only mode")
	}
	if len(blocked) != 0 {
		t.Fatalf("blocked = %v, want none when the endpoint is rejected", blocked)
	}
	if !strings.Contains(err.Error(), "remote endpoint blocked") {
		t.Fatalf("error does not carry the audit reason: %v", err)
	}
}

// TestApplyPrivacyPostureUnknownModeRejected 锁定无法识别模式下的安全失败行为，与 security.AllowRemote 一致。
// （接上行）
func TestApplyPrivacyPostureUnknownModeRejected(t *testing.T) {
	if _, err := ApplyPrivacyPosture("bogus", "http://localhost:8080", nil); err == nil {
		t.Fatal("expected an error for an unknown privacy mode")
	}
}
