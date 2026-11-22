package agent

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 本文件守护 R54#2 接线：callResult.refusal 此前只被内部分类、模型看不到拒绝决策。
// 现在复用 refusal_reason opt-in，把 refusal.ModelHint() 追加到回灌给模型的错误文本。
// 核心承诺：关闭（默认）或未命中已知类别时逐字节不变；开启时提示可见且不重复堆叠。

// TestAppendRefusalModelHintGate 表驱动验证接线包装的门控与追加格式。
func TestAppendRefusalModelHintGate(t *testing.T) {
	defer SetRefusalReason(false)

	matched := security.RefusalReason{
		Kind: security.RefusalKindPolicyDeny, Tool: "bash", Detail: "deny list",
	}
	hint := matched.ModelHint()
	if hint == "" || !strings.Contains(hint, "策略") {
		t.Fatalf("ModelHint 应为非空中文提示，got %q", hint)
	}

	cases := []struct {
		name    string
		enabled bool
		errText string
		refusal security.RefusalReason
		want    string
	}{
		{
			name:    "opt-in off keeps bytes",
			enabled: false,
			errText: "error: denied\n",
			refusal: matched,
			want:    "error: denied\n",
		},
		{
			name:    "opt-in on but zero refusal keeps bytes",
			enabled: true,
			errText: "error: ordinary failure",
			refusal: security.RefusalReason{},
			want:    "error: ordinary failure",
		},
		{
			name:    "opt-in on appends hint with newline",
			enabled: true,
			errText: "error: denied",
			refusal: matched,
			want:    "error: denied" + "\n" + hint,
		},
		{
			name:    "opt-in on trims trailing whitespace before hint",
			enabled: true,
			errText: "error: denied  \r\n",
			refusal: matched,
			want:    "error: denied" + "\n" + hint,
		},
		{
			name:    "empty errText yields hint only",
			enabled: true,
			errText: "",
			refusal: matched,
			want:    hint,
		},
		{
			name:    "hint already present is not appended twice",
			enabled: true,
			errText: "error: denied\n" + hint,
			refusal: matched,
			want:    "error: denied\n" + hint,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetRefusalReason(tc.enabled)
			got := AppendRefusalModelHint(tc.errText, tc.refusal)
			if got != tc.want {
				t.Fatalf("AppendRefusalModelHint(%q, %+v) = %q, want %q", tc.errText, tc.refusal, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("输出非法 UTF-8: %q", got)
			}
		})
	}
}

// TestAppendRefusalModelHintIdempotent 验证多次拒绝不会重复追加同一段提示：
// 对同一 errText 连续调用保持幂等，且把已追加的提示再喂入也不会二次堆叠。
func TestAppendRefusalModelHintIdempotent(t *testing.T) {
	defer SetRefusalReason(false)
	SetRefusalReason(true)

	matched := security.RefusalReason{
		Kind: security.RefusalKindToolError, Detail: "feedback", Recoverable: true,
	}
	hint := matched.ModelHint()
	base := "error: feedback"

	first := AppendRefusalModelHint(base, matched)
	if first != base+"\n"+hint {
		t.Fatalf("首次追加 = %q", first)
	}
	second := AppendRefusalModelHint(first, matched)
	if second != first {
		t.Fatalf("二次追加不幂等: %q -> %q", first, second)
	}
	if strings.Count(second, hint) != 1 {
		t.Fatalf("提示重复堆叠: %q", second)
	}
}

// TestAppendRefusalModelHintCJK 验证 CJK 错误正文与 CJK 拒绝细节都不会被切坏，
// 且提示本身（中文）完整出现在结果中。
func TestAppendRefusalModelHintCJK(t *testing.T) {
	defer SetRefusalReason(false)
	SetRefusalReason(true)

	errText := "error: 写入文件被拒绝：只读文件系统"
	refusal := security.RefusalReason{
		Kind: security.RefusalKindSandboxDenied, Tool: "写入文件", Detail: "只读文件系统",
	}
	hint := refusal.ModelHint()
	if !strings.Contains(hint, "沙箱") {
		t.Fatalf("沙箱拒绝提示应为中文，got %q", hint)
	}

	got := AppendRefusalModelHint(errText, refusal)
	if !strings.Contains(got, hint) {
		t.Fatalf("CJK 场景缺少提示: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("CJK 输出非法 UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "\n"+hint) {
		t.Fatalf("CJK 提示未按换行追加: %q", got)
	}
}

// runDispatchWithRefusalError 驱动一次完整派发：脚本化工具返回 security 拒绝错误，
// 第二轮以文本收尾。返回会话结果与 fakeChatter（可读模型实际看到的请求）。
func runDispatchWithRefusalError(t *testing.T, rawErr error) (Result, *fakeChatter) {
	t.Helper()
	const toolName = "edit"
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec(toolName)}}
	tools.run = func(_ context.Context, _ string, _ string) (string, error) {
		return "", rawErr
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", toolName, `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		MaxTurns: 4,
	}}
	res, err := runner.Run(context.Background(), "investigate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, chat
}

// TestRefusalModelHintVisibleInToolMessage 是端到端守卫：
//   - 关闭时，会话与模型看到的 tool 消息都与基线逐字节一致；
//   - 开启时，模型看到的错误文本含 refusal.ModelHint()（中文），且与会话记录一致。
func TestRefusalModelHintVisibleInToolMessage(t *testing.T) {
	defer SetRefusalReason(false)
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(false)

	refusalErr := &security.StopError{Tool: "bash", Reason: "tool is on the deny list"}
	baseline := "error: " + refusalErr.Error()
	reason, ok := security.ReasonFromError(refusalErr)
	if !ok {
		t.Fatalf("测试错误未被分类: %v", refusalErr)
	}
	hint := reason.ModelHint()

	SetRefusalReason(false)
	resOff, chatOff := runDispatchWithRefusalError(t, refusalErr)
	if got := lastToolContent(resOff.Messages); got != baseline {
		t.Fatalf("关闭时会话 tool 消息 = %q, want %q", got, baseline)
	}
	seenOff := chatOff.requestsSeen()
	if len(seenOff) < 2 {
		t.Fatalf("关闭时请求轮次 = %d, want >= 2", len(seenOff))
	}
	if got := lastToolContent(seenOff[1]); got != baseline {
		t.Fatalf("关闭时模型看到的 tool 消息 = %q, want %q", got, baseline)
	}

	SetRefusalReason(true)
	resOn, chatOn := runDispatchWithRefusalError(t, refusalErr)
	wantOn := baseline + "\n" + hint
	stored := lastToolContent(resOn.Messages)
	if stored != wantOn {
		t.Fatalf("开启时会话 tool 消息 = %q, want %q", stored, wantOn)
	}
	seenOn := chatOn.requestsSeen()
	if len(seenOn) < 2 {
		t.Fatalf("开启时请求轮次 = %d, want >= 2", len(seenOn))
	}
	modelSaw := lastToolContent(seenOn[1])
	if modelSaw != wantOn {
		t.Fatalf("开启时模型看到的 tool 消息 = %q, want %q", modelSaw, wantOn)
	}
	if modelSaw != stored {
		t.Fatalf("模型看到的消息与会话记录不一致: seen=%q stored=%q", modelSaw, stored)
	}
	if !strings.Contains(modelSaw, hint) {
		t.Fatalf("模型看到的错误文本缺少 ModelHint: %q", modelSaw)
	}
	if !utf8.ValidString(modelSaw) {
		t.Fatalf("模型看到的消息非法 UTF-8: %q", modelSaw)
	}
}
