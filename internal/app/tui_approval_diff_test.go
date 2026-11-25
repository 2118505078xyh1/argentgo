package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/tui"
)

// TestApproverAskRendersRealDiff 验证 Approver.Ask 把写类工具的 before/after
// 交给 ApproveDiff：弹窗默认出现中文 diff 摘要，且与危险意图摘要可共存。
func TestApproverAskRendersRealDiff(t *testing.T) {
	tui.ConfigureApprovalDiff(nil) // 默认开启
	tests := []struct {
		name    string
		tool    string
		args    string
		wantIn  string
		wantOut string
	}{
		{
			name:   "edit shows real diff summary",
			tool:   "edit",
			args:   `{"path":"a.go","old":"one\n","new":"two\n"}`,
			wantIn: "diff：1 个文件，+1/-1 行",
		},
		{
			name:    "read tool stays without diff",
			tool:    "read",
			args:    `{"path":"a.go"}`,
			wantOut: "diff：",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approver := NewApprover()
			model := tui.New(tui.Config{})
			approver.attach(model)
			done := make(chan bool, 1)
			go func() {
				ok, err := approver.Ask(context.Background(), tc.tool, tc.args)
				if err != nil {
					t.Errorf("Ask: %v", err)
				}
				done <- ok
			}()

			deadline := time.Now().Add(2 * time.Second)
			for {
				if _, _, ok := model.PendingApproval(); ok {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("approval modal did not open")
				}
				time.Sleep(time.Millisecond)
			}
			buf := model.Frame(90, 24)
			var body strings.Builder
			for y := 0; y < buf.Height(); y++ {
				body.WriteString(buf.RowText(y))
			}
			if tc.wantIn != "" && !strings.Contains(body.String(), tc.wantIn) {
				t.Fatalf("modal is missing %q:\n%s", tc.wantIn, body.String())
			}
			if tc.wantOut != "" && strings.Contains(body.String(), tc.wantOut) {
				t.Fatalf("modal unexpectedly contains %q:\n%s", tc.wantOut, body.String())
			}
			model.HandleKey(tui.Key{Kind: tui.KindRune, Rune: 'y'})
			select {
			case ok := <-done:
				if !ok {
					t.Fatal("y must approve")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("approval did not resolve")
			}
		})
	}
}
