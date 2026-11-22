package agent

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

func TestTrimDataURLAtomicElision(t *testing.T) {
	// 长 base64 data URL 出现在工具结果中间；裁剪后不应残留 base64 片段。
	payload := strings.Repeat("QUJD", 2000) // 8000 base64 chars
	content := "prefix\n![img](data:image/png;base64," + payload + ")\nsuffix\n"
	msg := llm.Message{Role: "tool", ToolCallID: "x", Content: content}

	t.Run("off keeps raw data URL", func(t *testing.T) {
		t.Setenv("ARGENT_ELIDE_DATA_URLS", "")
		res := Trim([]llm.Message{msg}, TrimOptions{MaxTokens: 10, KeepTurns: 0})
		if !strings.Contains(res.Messages[0].Content, "base64,"+payload[:20]) {
			t.Fatalf("off path changed content: %.100s", res.Messages[0].Content)
		}
	})

	t.Run("on elides atomically before cut", func(t *testing.T) {
		t.Setenv("ARGENT_ELIDE_DATA_URLS", "1")
		res := Trim([]llm.Message{msg}, TrimOptions{MaxTokens: 10, KeepTurns: 0})
		out := res.Messages[0].Content
		if strings.Contains(out, payload[:40]) {
			t.Fatal("base64 payload survived elision")
		}
		if !strings.Contains(out, "data URL elided") || !strings.Contains(out, "image/png") || !strings.Contains(out, "8000") {
			t.Fatalf("placeholder missing mime/length: %.200s", out)
		}
	})

	t.Run("no data URL unchanged", func(t *testing.T) {
		t.Setenv("ARGENT_ELIDE_DATA_URLS", "1")
		plain := llm.Message{Role: "tool", ToolCallID: "y", Content: "just plain text"}
		res := Trim([]llm.Message{plain}, TrimOptions{MaxTokens: 1000})
		if res.Messages[0].Content != "just plain text" {
			t.Fatalf("plain content changed: %q", res.Messages[0].Content)
		}
	})
}

func TestElideDataURLsHealsFragment(t *testing.T) {
	in := "old fragment data:image/jpeg;base64,abcDEF end"
	elided := textutil.ElideDataURLs(in)
	if strings.Contains(elided, "base64,abcDEF") {
		t.Fatalf("fragment not healed: %s", elided)
	}
	if !strings.Contains(elided, "data URL elided") || !strings.Contains(elided, "image/jpeg") {
		t.Fatalf("fragment placeholder missing: %s", elided)
	}
}
