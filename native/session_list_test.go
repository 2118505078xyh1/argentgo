package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// realSessionList is the shape server.SessionInfo arrives as on the wire:
// {"sessions":[{"id":...,"title":...,"updated":<RFC3339>}, ...]}.
const realSessionList = `{"sessions":[
	{"id":"sess-abc","title":"真实会话A","updated":"2026-11-20T10:00:00Z"},
	{"id":"sess-old","title":"真实会话B","updated":"2020-01-02T03:04:05Z"}
]}`

func TestRealEngineLoadsSessionListAndRendersRealRows(t *testing.T) {
	app := newTestChat()
	app.engine = &bridge{}
	listCalls := 0
	app.sessionListRPC = func(method string, params any) (json.RawMessage, error) {
		if method != "session.list" {
			t.Errorf("list RPC method = %q, want session.list", method)
		}
		listCalls++
		return json.RawMessage(realSessionList), nil
	}
	app.sessionLoadRPC = func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	for _, want := range []string{"真实会话A", "真实会话B"} {
		if !tt.HasText(want) {
			t.Errorf("real session %q missing from rail: %q", want, tt.Texts())
		}
	}
	if tt.HasText("重构聊天界面") {
		t.Error("fake sessions still shown after a successful session.list")
	}
	if listCalls != 1 {
		t.Fatalf("session.list calls = %d, want 1 (fetched once)", listCalls)
	}
	if len(app.sessions) != 2 {
		t.Fatalf("parsed sessions = %d, want 2", len(app.sessions))
	}
	if got := app.sessions[0]; got.id != "sess-abc" || got.title != "真实会话A" {
		t.Errorf("first session = %+v, want id sess-abc / title 真实会话A", got)
	}
	// The old row's timestamp maps deterministically to the 更早 / 01-02 labels.
	if got := app.sessions[1]; got.group != "更早" || got.meta != "01-02" {
		t.Errorf("old session labels = %q / %q, want 更早 / 01-02", got.group, got.meta)
	}

	// Rebuilding the view (as a Click would) must not re-fetch the list.
	_ = tt.Click("真实会话A")
	if listCalls != 1 {
		t.Errorf("session.list calls after re-render = %d, want 1", listCalls)
	}
}

func TestSessionRowClickLoadsRealSessionID(t *testing.T) {
	app := newTestChat()
	app.engine = &bridge{}
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"sessions":[{"id":"sess-abc","title":"真实会话A","updated":"2026-11-20T10:00:00Z"}]}`), nil
	}
	var calls []string
	app.sessionLoadRPC = func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		calls = append(calls, method+" "+string(raw))
		return json.RawMessage(`{}`), nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("真实会话A"); err != nil {
		t.Fatalf("click real session row: %v (texts %q)", err, tt.Texts())
	}
	if len(calls) != 1 || calls[0] != `session.load {"id":"sess-abc"}` {
		t.Fatalf("session.load calls = %v, want the real id sess-abc", calls)
	}
	if app.selectedSession != "sess-abc" {
		t.Errorf("selectedSession = %q, want real id sess-abc", app.selectedSession)
	}
	if app.status != "已打开会话：真实会话A" {
		t.Errorf("status = %q, want title in the human-facing status", app.status)
	}
}

func TestDemoModeDoesNotCallSessionList(t *testing.T) {
	app := newTestChat() // engine == nil
	called := false
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		called = true
		return nil, nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if called {
		t.Error("demo mode must not call session.list")
	}
	if !tt.HasText("重构聊天界面") {
		t.Fatalf("demo rail missing fake sessions: %q", tt.Texts())
	}
	if app.sessionListLoaded {
		t.Error("demo mode must not mark the session list as loaded")
	}
}

func TestSessionListFailureKeepsRailAndShowsRedactedNotice(t *testing.T) {
	app := newTestChat()
	app.engine = &bridge{}
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return nil, errors.New("api_key: sk-abcdefghijklmnopqrstuvwxyz")
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if !tt.HasText("重构聊天界面") {
		t.Errorf("failed session.list must keep the existing rail: %q", tt.Texts())
	}
	if app.sessions != nil {
		t.Errorf("sessions = %+v, want nil so the demo rail stays", app.sessions)
	}
	if !strings.Contains(app.status, "会话列表加载失败") || !strings.Contains(app.status, "[redacted]") {
		t.Errorf("status = %q, want a redacted session list notice", app.status)
	}
	if strings.Contains(app.status, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("status leaked the credential: %q", app.status)
	}
}

func TestSessionListMalformedPayloadKeepsRail(t *testing.T) {
	app := newTestChat()
	app.engine = &bridge{}
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"sessions":[`), nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if !tt.HasText("重构聊天界面") {
		t.Errorf("malformed session.list must keep the existing rail: %q", tt.Texts())
	}
	if app.sessions != nil {
		t.Errorf("sessions = %+v, want nil after a malformed payload", app.sessions)
	}
	if !strings.Contains(app.status, "会话列表加载失败") {
		t.Errorf("status = %q, want a session list failure notice", app.status)
	}
}

func TestEmptyRealSessionListHidesFakeRows(t *testing.T) {
	app := newTestChat()
	app.engine = &bridge{}
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"sessions":[]}`), nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if tt.HasText("重构聊天界面") {
		t.Errorf("an empty real session list must not fall back to fake rows: %q", tt.Texts())
	}
	if app.sessions == nil || len(app.sessions) != 0 {
		t.Errorf("sessions = %+v, want a non-nil empty slice", app.sessions)
	}
}
