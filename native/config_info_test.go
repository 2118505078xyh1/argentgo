package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// realConfigInfo 是 server config.info RPC 在线上的形态：
// {"model":...,"provider":...,"workspace":...}。native 只按 JSON 解析，
// 不编译期依赖 server 的实现。
const realConfigInfo = `{"model":"claude-3-7-sonnet","provider":"anthropic","workspace":"~/real-project"}`

// configInfoFixture 构建一个带 fake engine 的 app 并注入 config.info 记录器。
// session.list 被故意设为失败，使零值 bridge 在渲染首帧时不被调用
// （其 pending map 为 nil），与仓库内其它测试夹具保持一致。
func configInfoFixture(t *testing.T, call func(string, any) (json.RawMessage, error)) *chat {
	t.Helper()
	app := newTestChat()
	app.engine = &bridge{}
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return nil, errors.New("session.list unavailable")
	}
	app.configInfoRPC = call
	return app
}

func TestConfigInfoRealValuesReplaceStatusFallback(t *testing.T) {
	calls := 0
	app := configInfoFixture(t, func(method string, params any) (json.RawMessage, error) {
		if method != "config.info" {
			t.Errorf("config.info RPC method = %q, want config.info", method)
		}
		if params == nil {
			t.Error("config.info params must be an empty object, not nil")
		}
		calls++
		return json.RawMessage(realConfigInfo), nil
	})
	app.ensureConfigInfo()

	tt := ui.NewTester(app.view, 1180, 720)

	// 状态栏文字本身包含真实 model/workspace，且不再含 demo 占位 model。
	label := app.configInfoLabel()
	if !strings.Contains(label, "claude-3-7-sonnet") || !strings.Contains(label, "~/real-project") {
		t.Errorf("status label = %q, want real model and workspace", label)
	}
	if strings.Contains(label, "gpt-4o-mini") {
		t.Errorf("status label still contains the demo model: %q", label)
	}
	if label != "claude-3-7-sonnet · ~/real-project" {
		t.Errorf("status label = %q, want exact model · workspace", label)
	}
	// 真实值确实渲染在状态栏上；旧的逐字节占位串不再出现。
	if !tt.HasText("claude-3-7-sonnet") || !tt.HasText("~/real-project") {
		t.Errorf("real config missing from view: %q", tt.Texts())
	}
	if tt.HasText(demoStatusInfo) {
		t.Errorf("demo status fallback still rendered: %q", tt.Texts())
	}
	// 与 session.list 一样，config.info 每次 chat 只获取一次。
	if calls != 1 {
		t.Fatalf("config.info calls = %d, want 1 (fetched once)", calls)
	}
	app.ensureConfigInfo()
	if calls != 1 {
		t.Errorf("config.info calls after second ensure = %d, want 1", calls)
	}
}

func TestConfigInfoFailureKeepsFallbackByteForByte(t *testing.T) {
	app := configInfoFixture(t, func(string, any) (json.RawMessage, error) {
		return nil, errors.New("config.info unavailable")
	})
	app.ensureConfigInfo()

	tt := ui.NewTester(app.view, 1180, 720)

	if got := app.configInfoLabel(); got != demoStatusInfo {
		t.Errorf("status label = %q, want byte-for-byte fallback %q", got, demoStatusInfo)
	}
	if !tt.HasText(demoStatusInfo) {
		t.Errorf("fallback status line missing from view: %q", tt.Texts())
	}
	if app.configInfo != (configInfoSnapshot{}) {
		t.Errorf("configInfo = %+v, want zero value after failure", app.configInfo)
	}
}

func TestConfigInfoMalformedPayloadKeepsFallback(t *testing.T) {
	app := configInfoFixture(t, func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"model":`), nil
	})
	app.ensureConfigInfo()

	_ = ui.NewTester(app.view, 1180, 720)

	if got := app.configInfoLabel(); got != demoStatusInfo {
		t.Errorf("status label = %q, want fallback after malformed payload", got)
	}
}

func TestDemoModeDoesNotCallConfigInfo(t *testing.T) {
	app := newTestChat() // engine == nil
	called := false
	app.configInfoRPC = func(string, any) (json.RawMessage, error) {
		called = true
		return json.RawMessage(realConfigInfo), nil
	}
	app.ensureConfigInfo()

	_ = ui.NewTester(app.view, 1180, 720)

	if called {
		t.Error("demo mode must not call config.info")
	}
	if app.configInfoLoaded {
		t.Error("demo mode must not mark config info as loaded")
	}
	if got := app.configInfoLabel(); got != demoStatusInfo {
		t.Errorf("status label = %q, want fallback in demo mode", got)
	}
}

func TestConfigInfoRedactedWorkspaceShownAsIs(t *testing.T) {
	redacted := "/home/[redacted]/proj"
	app := configInfoFixture(t, func(string, any) (json.RawMessage, error) {
		payload := map[string]string{
			"model":     "claude-3-7-sonnet",
			"provider":  "anthropic",
			"workspace": redacted,
		}
		raw, _ := json.Marshal(payload)
		return raw, nil
	})
	app.ensureConfigInfo()

	tt := ui.NewTester(app.view, 1180, 720)

	label := app.configInfoLabel()
	if !strings.Contains(label, redacted) {
		t.Errorf("status label = %q, want the redacted workspace verbatim", label)
	}
	if !tt.HasText(redacted) {
		t.Errorf("redacted workspace missing from view: %q", tt.Texts())
	}
}
