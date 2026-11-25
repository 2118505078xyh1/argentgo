package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestInitialImagesNilKeepsFirstUserMessageByteIdentical 锁定默认路径：
// InitialImages 为 nil 时首条用户消息必须是裸 {Role:"user", Content:prompt}，
// ContentParts 为 nil，与接线之前逐字节相同。
func TestInitialImagesNilKeepsFirstUserMessageByteIdentical(t *testing.T) {
	const prompt = "看这张图"
	s := newRunState(Options{}, prompt, nil)
	if len(s.messages) != 2 {
		t.Fatalf("消息数 = %d, want 2", len(s.messages))
	}
	user := s.messages[1]
	if user.Role != "user" || user.Content != prompt {
		t.Fatalf("首条用户消息 = %+v, want Role=user Content=%q", user, prompt)
	}
	if user.ContentParts != nil {
		t.Fatalf("无图时 ContentParts 必须为 nil，got %s", user.ContentParts)
	}
}

// TestInitialImagesBuildsContentParts 验证 InitialImages 非空时首条用户消息
// 经 llm.BuildImageMessage 构造出带图像 part 的 ContentParts。
func TestInitialImagesBuildsContentParts(t *testing.T) {
	images := []llm.ImageInput{
		{MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}},
	}
	s := newRunState(Options{InitialImages: images}, "看这张图", nil)
	user := s.messages[1]
	if user.Role != "user" {
		t.Fatalf("首条消息 Role = %q, want user", user.Role)
	}
	if len(user.ContentParts) == 0 || user.ContentParts[0] != '[' {
		t.Fatalf("带图时 ContentParts 应是以 [ 开头的数组，got %s", user.ContentParts)
	}
	var parts []map[string]any
	if err := json.Unmarshal(user.ContentParts, &parts); err != nil {
		t.Fatalf("ContentParts 不是合法 JSON: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("part 数 = %d, want 2（文本 + 图片）: %s", len(parts), user.ContentParts)
	}
	if !strings.Contains(string(user.ContentParts), "image/png") {
		t.Fatalf("ContentParts 未包含 MIME: %s", user.ContentParts)
	}
}

// TestInitialImagesCJKTextPreserved 验证 CJK/emoji 文本在带图路径下原样保留。
func TestInitialImagesCJKTextPreserved(t *testing.T) {
	const prompt = "看这张截图 🐱 谢谢"
	images := []llm.ImageInput{{MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}}
	user := newRunState(Options{InitialImages: images}, prompt, nil).messages[1]
	if !strings.Contains(string(user.ContentParts), prompt) {
		t.Fatalf("ContentParts 未原样保留 CJK 文本 %q: %s", prompt, user.ContentParts)
	}
}

// TestInitialImagesRecordUsesRawPrompt 验证 s.record("user", prompt) 不受图片影响：
// 无论有无图片，记录器都只收到原始 prompt 字符串。
func TestInitialImagesRecordUsesRawPrompt(t *testing.T) {
	const prompt = "看这张图"
	for _, tc := range []struct {
		name   string
		images []llm.ImageInput
	}{
		{"无图", nil},
		{"有图", []llm.ImageInput{{MimeType: "image/png", Data: []byte{0x01, 0x02}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotKind string
			var gotPayload any
			opts := Options{
				InitialImages: tc.images,
				Record: RecorderFunc(func(kind string, payload any) error {
					gotKind, gotPayload = kind, payload
					return nil
				}),
			}
			newRunState(opts, prompt, nil)
			if gotKind != "user" {
				t.Fatalf("record kind = %q, want user", gotKind)
			}
			if got, ok := gotPayload.(string); !ok || got != prompt {
				t.Fatalf("record payload = %#v, want %q", gotPayload, prompt)
			}
		})
	}
}

// TestInitialImagesCoexistWithLoopHistory 锁定 R38-R44 的历史改动共存：
// SystemNodes 仍作用于系统提示，TurnTiming/RecoveryMetrics/直方图/trace 在
// 各自 env 开启时仍被初始化，且 InitialImages 不干扰它们。
func TestInitialImagesCoexistWithLoopHistory(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")

	const baseSystem = "BASE SYSTEM"
	opts := Options{
		System:        baseSystem,
		SystemNodes:   []string{"NODE-1"},
		InitialImages: []llm.ImageInput{{MimeType: "image/png", Data: []byte{0x01}}},
	}
	s := newRunState(opts, "图", nil)

	if !strings.Contains(string(s.messages[0].Content), baseSystem) ||
		!strings.Contains(string(s.messages[0].Content), "NODE-1") {
		t.Fatalf("SystemNodes 未作用于系统提示: %q", s.messages[0].Content)
	}
	if len(s.messages[1].ContentParts) == 0 {
		t.Fatal("InitialImages 未构造 ContentParts")
	}
	if s.turnTiming == nil || s.recovery == nil ||
		s.toolTimingHistogram == nil || s.toolCallTrace == nil {
		t.Fatalf("历史改动未全部初始化: turnTiming=%v recovery=%v histogram=%v trace=%v",
			s.turnTiming != nil, s.recovery != nil, s.toolTimingHistogram != nil, s.toolCallTrace != nil)
	}
}
