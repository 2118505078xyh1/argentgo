package agent

import (
	"math"
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestScoreContextHealthRepeatedGuidanceLowersScore(t *testing.T) {
	guidance := "guidance: stop retrying and change the tool"
	messages := []string{guidance, guidance, guidance, guidance}

	got := ScoreContextHealth(messages)
	if want := 0.75; !nearHealth(got.GuidanceRepeatRatio, want) {
		t.Fatalf("GuidanceRepeatRatio = %v, want %v", got.GuidanceRepeatRatio, want)
	}
	if got.ErrorStubRatio != 0 {
		t.Fatalf("ErrorStubRatio = %v, want 0", got.ErrorStubRatio)
	}
	// Score = 1 - 0.6*0.75 = 0.55，明显低于满分。
	if want := 0.55; !nearHealth(got.Score, want) {
		t.Fatalf("Score = %v, want %v", got.Score, want)
	}
}

func TestScoreContextHealthSimilarGuidanceCountsAsRepeat(t *testing.T) {
	// 仅在大小写与空白上不同的指引，规范化后应视为同一条（"相似"）。
	messages := []string{
		"Guidance:  Retry   the   tool",
		"guidance: retry the tool",
	}
	got := ScoreContextHealth(messages)
	if want := 0.5; !nearHealth(got.GuidanceRepeatRatio, want) {
		t.Fatalf("GuidanceRepeatRatio = %v, want %v (normalization should collapse whitespace/case)", got.GuidanceRepeatRatio, want)
	}
}

func TestScoreContextHealthErrorStubsLowerScore(t *testing.T) {
	messages := []string{
		"普通消息一",
		"[tool output trimmed: 5000 chars] 后续内容",
		"普通消息二",
		"输出 ...[truncated]... 结束",
	}
	got := ScoreContextHealth(messages)
	if want := 0.5; !nearHealth(got.ErrorStubRatio, want) {
		t.Fatalf("ErrorStubRatio = %v, want %v", got.ErrorStubRatio, want)
	}
	if got.GuidanceRepeatRatio != 0 {
		t.Fatalf("GuidanceRepeatRatio = %v, want 0", got.GuidanceRepeatRatio)
	}
	// Score = 1 - 0.5*0.5 = 0.75。
	if want := 0.75; !nearHealth(got.Score, want) {
		t.Fatalf("Score = %v, want %v", got.Score, want)
	}
}

func TestScoreContextHealthHealthyConversationScoresHigh(t *testing.T) {
	messages := []string{
		"请读取 internal/agent/trim.go 的前 40 行",
		"文件已读取，内容如下：package agent ...",
		"把第 17 行的常量改成新的值",
		"修改完成，go test 已通过",
	}
	got := ScoreContextHealth(messages)
	if got.GuidanceRepeatRatio != 0 || got.ErrorStubRatio != 0 {
		t.Fatalf("健康对话不应有污染信号，got %+v", got)
	}
	if got.Score != 1 {
		t.Fatalf("Score = %v, want 1", got.Score)
	}
}

func TestScoreContextHealthEmptyInput(t *testing.T) {
	cases := []struct {
		name     string
		messages []string
	}{
		{name: "nil", messages: nil},
		{name: "empty", messages: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScoreContextHealth(tc.messages)
			want := ContextHealth{Score: 1}
			if got != want {
				t.Fatalf("ScoreContextHealth(%v) = %+v, want %+v", tc.messages, got, want)
			}
		})
	}
}

func TestScoreContextHealthClampsToRange(t *testing.T) {
	// 大量"既是 stub 又是重复指引"的消息，把加权污染压到 1 以上。
	// ErrorStubRatio = 1，GuidanceRepeatRatio = 19/20 = 0.95，
	// weighted = 0.6*0.95 + 0.5*1 = 1.07 → 原值 -0.07，应钳制为 0。
	polluted := make([]string, 20)
	for i := range polluted {
		polluted[i] = "[truncated] guidance: retry and change the tool"
	}
	low := ScoreContextHealth(polluted)
	if low.Score != 0 {
		t.Fatalf("严重污染 Score = %v, want 0 (clamped)", low.Score)
	}
	if low.Score < 0 || low.Score > 1 {
		t.Fatalf("Score 越界: %v", low.Score)
	}

	healthy := ScoreContextHealth([]string{"一条完全正常的消息"})
	if healthy.Score < 0 || healthy.Score > 1 {
		t.Fatalf("Score 越界: %v", healthy.Score)
	}
	if healthy.Score != 1 {
		t.Fatalf("健康 Score = %v, want 1", healthy.Score)
	}
}

func TestScoreContextHealthCJKAndEmojiSafe(t *testing.T) {
	messages := []string{
		"指引：请先诊断失败原因，再改用其他工具 🛠️",
		"指引：请先诊断失败原因，再改用其他工具 🛠️",
		"中文内容完全不同，不含任何标记",
		"输出被裁剪 [truncated] 后面还有中文",
	}
	got := ScoreContextHealth(messages)
	// 两条相同中文指引 → 1 次重复 / 2 条指引 = 0.5；1 条 stub / 4 = 0.25。
	if want := 0.5; !nearHealth(got.GuidanceRepeatRatio, want) {
		t.Fatalf("GuidanceRepeatRatio = %v, want %v", got.GuidanceRepeatRatio, want)
	}
	if want := 0.25; !nearHealth(got.ErrorStubRatio, want) {
		t.Fatalf("ErrorStubRatio = %v, want %v", got.ErrorStubRatio, want)
	}
	if !utf8.ValidString(messages[0]) || !utf8.ValidString(messages[3]) {
		t.Fatal("入参应仍是合法 UTF-8")
	}
}

func TestScoreContextHealthDoesNotMutateInput(t *testing.T) {
	messages := []string{
		"[truncated] guidance: retry",
		"guidance: retry",
		"普通消息",
	}
	original := append([]string(nil), messages...)

	ScoreContextHealth(messages)

	if !reflect.DeepEqual(messages, original) {
		t.Fatalf("入参被修改: got %q, want %q", messages, original)
	}
}

func TestScoreContextHealthDeterministic(t *testing.T) {
	messages := []string{
		"guidance: change the tool before retrying",
		"guidance: change the tool before retrying",
		"[tool output trimmed] x",
		"无关内容",
	}
	first := ScoreContextHealth(messages)
	for i := 0; i < 20; i++ {
		if got := ScoreContextHealth(messages); got != first {
			t.Fatalf("第 %d 次调用 = %+v, want %+v", i, got, first)
		}
	}
}

// nearHealth 以 1e-9 的容差比较健康度浮点值。
func nearHealth(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
