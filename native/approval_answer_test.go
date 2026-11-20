package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// approvalRPCFixture attaches a fake engine bridge to the app, seeds a real
// approval (one carrying an engine id) and records every approval.answer RPC.
// The recorder returns ok unless a test overrides it to simulate a failure.
func approvalRPCFixture(t *testing.T) (*chat, *[]string) {
	t.Helper()
	app := newTestChat()
	app.engine = &bridge{} // non-nil simulates a real engine bridge
	// The chat rail fetches session.list on its first frame; make that fail so
	// this fixture keeps the demo rail and the zero-value bridge is never
	// called (its pending map is nil).
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return nil, errors.New("session.list unavailable")
	}
	app.approval = &approval{id: "call_42", name: "bash", args: "rm -rf /tmp/x"}
	calls := new([]string)
	app.approvalAnswerRPC = func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		*calls = append(*calls, method+" "+string(raw))
		return json.RawMessage(`{"ok":true}`), nil
	}
	return app, calls
}

// TestApprovalAllowButtonSendsAnswerRPC checks that clicking 允许 sends
// approval.answer with the approval id and granted=true, then clears the card.
func TestApprovalAllowButtonSendsAnswerRPC(t *testing.T) {
	app, calls := approvalRPCFixture(t)

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("允许"); err != nil {
		t.Fatalf("click 允许: %v (texts %q)", err, tt.Texts())
	}
	if len(*calls) != 1 {
		t.Fatalf("RPC calls = %v, want exactly one approval.answer", *calls)
	}
	if want := `approval.answer {"granted":true,"id":"call_42"}`; (*calls)[0] != want {
		t.Errorf("RPC call = %q, want %q", (*calls)[0], want)
	}
	if app.approval != nil {
		t.Errorf("card should be cleared after a successful answer: %+v", app.approval)
	}
	if app.status != "已响应，等待引擎确认" {
		t.Errorf("status = %q", app.status)
	}
}

// TestApprovalDenyButtonSendsAnswerRPC checks that clicking 拒绝 sends
// approval.answer with granted=false.
func TestApprovalDenyButtonSendsAnswerRPC(t *testing.T) {
	app, calls := approvalRPCFixture(t)

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("拒绝"); err != nil {
		t.Fatalf("click 拒绝: %v (texts %q)", err, tt.Texts())
	}
	if len(*calls) != 1 {
		t.Fatalf("RPC calls = %v, want exactly one approval.answer", *calls)
	}
	if want := `approval.answer {"granted":false,"id":"call_42"}`; (*calls)[0] != want {
		t.Errorf("RPC call = %q, want %q", (*calls)[0], want)
	}
	if app.approval != nil {
		t.Errorf("card should be cleared after a successful answer: %+v", app.approval)
	}
}

// TestApprovalKeysSendAnswerRPC checks that the Y/N shortcuts use the same
// RPC path as the buttons, once focus leaves the auto-focused composer.
func TestApprovalKeysSendAnswerRPC(t *testing.T) {
	t.Run("Y allows", func(t *testing.T) {
		app, calls := approvalRPCFixture(t)
		tt := ui.NewTester(app.view, 1180, 720)
		if err := tt.Click("文档"); err != nil { // move focus out of the composer
			t.Fatal(err)
		}
		tt.Key(0, ui.KeyY)
		if len(*calls) != 1 || (*calls)[0] != `approval.answer {"granted":true,"id":"call_42"}` {
			t.Fatalf("Y RPC calls = %v", *calls)
		}
		if app.approval != nil {
			t.Errorf("Y did not clear the card: %+v", app.approval)
		}
	})

	t.Run("N denies", func(t *testing.T) {
		app, calls := approvalRPCFixture(t)
		tt := ui.NewTester(app.view, 1180, 720)
		if err := tt.Click("文档"); err != nil {
			t.Fatal(err)
		}
		tt.Key(0, ui.KeyN)
		if len(*calls) != 1 || (*calls)[0] != `approval.answer {"granted":false,"id":"call_42"}` {
			t.Fatalf("N RPC calls = %v", *calls)
		}
		if app.approval != nil {
			t.Errorf("N did not clear the card: %+v", app.approval)
		}
	})
}

// TestApprovalDemoModeSkipsRPC checks that the canned demo approval is still
// resolved locally when no engine bridge is attached.
func TestApprovalDemoModeSkipsRPC(t *testing.T) {
	app := newTestChat() // engine == nil
	app.approval = &approval{name: "write_file", args: "{}"}
	called := false
	app.approvalAnswerRPC = func(string, any) (json.RawMessage, error) {
		called = true
		return nil, nil
	}

	app.answerApproval(true)

	if called {
		t.Error("demo mode must not call approval.answer")
	}
	if app.approval != nil {
		t.Errorf("demo card should be cleared: %+v", app.approval)
	}
	if len(app.messages) != 1 || app.messages[0].status != "done" ||
		app.messages[0].text != "✓ 已允许 write_file" {
		t.Fatalf("demo result row = %+v", app.messages)
	}
}

// TestApprovalAnswerFailureKeepsCardForRetry checks that a failed RPC leaves
// the card in place and shows a redacted error, so the user can retry the very
// same approval (the second attempt succeeds and clears the card).
func TestApprovalAnswerFailureKeepsCardForRetry(t *testing.T) {
	app, calls := approvalRPCFixture(t)
	app.approvalAnswerRPC = func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		*calls = append(*calls, method+" "+string(raw))
		return nil, errors.New("api_key: sk-abcdefghijklmnopqrstuvwxyz\nsecond line")
	}

	app.answerApproval(false)

	if app.approval == nil || app.approval.id != "call_42" {
		t.Fatalf("failed answer dropped the card: %+v", app.approval)
	}
	want := "审批回应失败：" + engineErrorNotice(errors.New("api_key: sk-abcdefghijklmnopqrstuvwxyz\nsecond line"))
	if app.status != want {
		t.Errorf("status = %q, want %q", app.status, want)
	}
	if strings.Contains(app.status, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("status leaked the credential: %q", app.status)
	}
	if !strings.Contains(app.status, "[redacted]") {
		t.Errorf("status has no redaction marker: %q", app.status)
	}
	// The card must still be rendered so the user can click again.
	tt := ui.NewTester(app.view, 1180, 720)
	if !tt.HasText("需要批准") {
		t.Errorf("approval card disappeared after failure: %q", tt.Texts())
	}

	// Retry: swap in a working recorder and answer through the UI again.
	app.approvalAnswerRPC = func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		*calls = append(*calls, method+" "+string(raw))
		return json.RawMessage(`{"ok":true}`), nil
	}
	if err := tt.Click("允许"); err != nil {
		t.Fatalf("retry click 允许: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("RPC calls = %v, want the failure and the retry", *calls)
	}
	if (*calls)[1] != `approval.answer {"granted":true,"id":"call_42"}` {
		t.Errorf("retry RPC = %q", (*calls)[1])
	}
	if app.approval != nil {
		t.Errorf("retry should clear the card: %+v", app.approval)
	}
}
