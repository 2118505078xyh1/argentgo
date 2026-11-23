package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDangerIntentLineAboveArgs 固定危险命令的中文意图摘要行：
// 它位于裸参数上方，普通请求不出现该行。
func TestDangerIntentLineAboveArgs(t *testing.T) {
	tests := []struct {
		name       string
		danger     string
		wantIntent string
	}{
		{
			name:       "dangerous command shows Chinese intent above args",
			danger:     "删除文件",
			wantIntent: "危险命令意图：删除文件",
		},
		{
			name: "ordinary request has no intent line",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Config{Locale: "zh"})
			done := make(chan bool, 1)
			go func() {
				ok, err := m.ApproveDanger(context.Background(), "bash", `{"command":"sudo rm -rf /"}`, tc.danger)
				if err != nil {
					t.Errorf("ApproveDanger: %v", err)
				}
				done <- ok
			}()
			waitFor(t, "the intent approval modal", func() bool {
				_, _, ok := m.PendingApproval()
				return ok
			})

			lines := m.approvalLines(60)
			var intentIdx, argsIdx = -1, -1
			for i, line := range lines {
				text := line.Text()
				if strings.Contains(text, "危险命令意图：") {
					intentIdx = i
				}
				if strings.Contains(text, "args:") {
					argsIdx = i
				}
			}
			if tc.wantIntent == "" {
				if intentIdx >= 0 {
					t.Fatalf("ordinary request must not show an intent line:\n%s", modalText(lines))
				}
			} else {
				if intentIdx < 0 || !strings.Contains(lines[intentIdx].Text(), tc.wantIntent) {
					t.Fatalf("intent line %q is missing:\n%s", tc.wantIntent, modalText(lines))
				}
				if argsIdx < 0 || intentIdx >= argsIdx {
					t.Fatalf("intent line must appear above args (intent=%d args=%d):\n%s", intentIdx, argsIdx, modalText(lines))
				}
			}

			m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
			select {
			case ok := <-done:
				if ok {
					t.Fatal("n must deny")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("modal did not resolve")
			}
		})
	}
}
