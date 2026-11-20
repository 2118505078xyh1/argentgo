package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSessionRowClickCallsSessionLoadWhenEngineAttached(t *testing.T) {
	var calls []string
	app := newTestChat()
	app.engine = &bridge{} // non-nil simulates a real engine bridge
	// session.list 失败降级到 demo 会话栏，保留本测试点击的
	// 假行。零值 bridge 在真实调用时会 panic。
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return nil, errors.New("session.list unavailable")
	}
	app.sessionLoadRPC = func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		calls = append(calls, method+" "+string(raw))
		return json.RawMessage(`{}`), nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("重构聊天界面"); err != nil {
		t.Fatalf("click session row: %v (texts %q)", err, tt.Texts())
	}
	if len(calls) != 1 {
		t.Fatalf("RPC calls = %v, want exactly one session.load", calls)
	}
	if want := `session.load {"id":"重构聊天界面"}`; calls[0] != want {
		t.Errorf("RPC call = %q, want %q", calls[0], want)
	}
	if app.selectedSession != "重构聊天界面" {
		t.Errorf("selectedSession = %q", app.selectedSession)
	}
	if app.status != "已打开会话：重构聊天界面" {
		t.Errorf("status = %q", app.status)
	}
}

func TestSessionRowClickInDemoModeSkipsRPC(t *testing.T) {
	app := newTestChat() // engine == nil
	called := false
	app.sessionLoadRPC = func(string, any) (json.RawMessage, error) {
		called = true
		return nil, nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("修复工具调用状态"); err != nil {
		t.Fatalf("click session row: %v (texts %q)", err, tt.Texts())
	}
	if called {
		t.Error("demo mode must not call session.load")
	}
	if app.selectedSession != "修复工具调用状态" || app.status != "已打开会话：修复工具调用状态" {
		t.Errorf("demo switch failed: selected=%q status=%q", app.selectedSession, app.status)
	}
}

func TestOpenSessionReportsEngineFailure(t *testing.T) {
	app := newTestChat()
	app.engine = &bridge{}
	app.sessionLoadRPC = func(string, any) (json.RawMessage, error) {
		return nil, errors.New("api_key: sk-abcdefghijklmnopqrstuvwxyz")
	}

	app.openSession(session{title: "整理记忆索引"})

	const want = "打开会话失败：⚠ 引擎错误: api_key: [redacted]"
	if app.status != want {
		t.Errorf("status = %q, want %q", app.status, want)
	}
	if app.selectedSession != "整理记忆索引" {
		t.Errorf("selectedSession = %q", app.selectedSession)
	}
}
