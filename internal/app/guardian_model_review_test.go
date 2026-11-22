package app

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// fakeOnceChatter 是具备 ChatOnce 的最小 Chatter（Chat 方法满足 llm.Chatter）。
type fakeOnceChatter struct {
	reply string
	err   error
}

func (f *fakeOnceChatter) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	return llm.Message{}, nil
}
func (f *fakeOnceChatter) ChatOnce(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
	return llm.Message{Role: "assistant", Content: f.reply}, f.err
}

func TestGuardianModelReviewVerdicts(t *testing.T) {
	cases := []struct {
		reply string
		want  security.InspectionAction
	}{
		{`{"action":"allow"}`, security.InspectAllow},
		{`{"action":"deny"}`, security.InspectDeny},
		{`{"action":"require"}`, security.InspectRequireApproval},
		{`{"action":"escalate"}`, security.InspectRequireApproval},
		{"prose with allow keyword", security.InspectRequireApproval},
		{`{"action":"garbage"}`, security.InspectRequireApproval},
		{`{"action":"ALLoW"}`, security.InspectAllow},
	}
	for _, tc := range cases {
		review := guardianModelReview(&fakeOnceChatter{reply: tc.reply}, "smol")
		got, err := review(context.Background(), "bash", "echo hi")
		if err != nil {
			t.Fatalf("reply %q: %v", tc.reply, err)
		}
		if got.Action != tc.want {
			t.Errorf("reply %q: action = %v, want %v", tc.reply, got.Action, tc.want)
		}
	}
}

func TestGuardianModelReviewFailClosed(t *testing.T) {
	// nil chatter / 空模型 / ChatOnce 错误 → RequireApproval。
	for _, setup := range []struct {
		name    string
		chatter llm.Chatter
		model   string
	}{
		{"nil chatter", nil, "smol"},
		{"empty model", &fakeOnceChatter{reply: `{"action":"allow"}`}, ""},
	} {
		t.Run(setup.name, func(t *testing.T) {
			got, _ := guardianModelReview(setup.chatter, setup.model)(context.Background(), "bash", "x")
			if got.Action != security.InspectRequireApproval {
				t.Fatalf("action = %v, want RequireApproval", got.Action)
			}
		})
	}
	// 审阅文本绝不进入 reason。
	got, _ := guardianModelReview(&fakeOnceChatter{reply: `{"action":"allow"}`}, "smol")(context.Background(), "bash", "x")
	if strings.Contains(got.Reason, "{") || strings.Contains(got.Reason, "action") {
		t.Fatalf("reason leaked model text: %q", got.Reason)
	}
}

func TestGuardianReviewPromptBounded(t *testing.T) {
	p := guardianReviewPrompt("bash", "x")
	if len(p) < 50 || !strings.Contains(p, "untrusted") {
		t.Fatalf("prompt missing safety instruction: %q", p)
	}
}
