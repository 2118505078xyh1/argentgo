package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// usageRateModel 返回带可推进冻结时钟的模型，使 tok/s 与 CH% 完全确定。
func usageRateModel() (*Model, *time.Time) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	m := deterministicUsageModel("en")
	m.now = func() time.Time { return now }
	return m, &now
}

func TestUsageRateFromConsecutiveEvents(t *testing.T) {
	m, now := usageRateModel()
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100, CachedTokens: 150})
	if m.usageRate != "" {
		t.Fatalf("the first Usage must not invent a rate, got %q", m.usageRate)
	}
	*now = now.Add(2 * time.Second)
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 190, CachedTokens: 150})

	if m.usageRate != "45 tok/s" {
		t.Fatalf("usageRate = %q, want \"45 tok/s\"", m.usageRate)
	}
	status := m.Frame(110, 24).RowText(22)
	for _, want := range []string{"↑200", "↓190", "CH75%", "⚡45 tok/s"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %q is missing %q", status, want)
		}
	}
}

func TestCacheHitRatioCases(t *testing.T) {
	cases := []struct {
		name           string
		prompt, cached int
		want           string
		wantAbsent     bool
	}{
		{"zero cached with prompt", 200, 0, "CH0%", false},
		{"partial", 200, 50, "CH25%", false},
		{"cached above prompt clamps", 100, 250, "CH100%", false},
		{"zero prompt omitted", 0, 50, "CH", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := deterministicUsageModel("en")
			m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: tc.prompt, CompletionTokens: 10, CachedTokens: tc.cached})
			status := m.Frame(110, 24).RowText(22)
			if tc.wantAbsent {
				if strings.Contains(status, tc.want) {
					t.Fatalf("status %q must not contain %q", status, tc.want)
				}
				return
			}
			if !strings.Contains(status, tc.want) {
				t.Fatalf("status %q is missing %q", status, tc.want)
			}
		})
	}
}

func TestUsageRateZeroDeltaHasNoNaNOrInf(t *testing.T) {
	m, now := usageRateModel()
	event := Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100, CachedTokens: 150}
	m.HandleEvent(event) // 建立基线
	*now = now.Add(time.Second)
	m.HandleEvent(event) // Δtokens = 0：保持上一个速率，不产生 NaN
	if !strings.Contains(m.usageRate, "tok/s") && m.usageRate != "" {
		t.Fatalf("unexpected usageRate = %q", m.usageRate)
	}
	text := frameText(m, 110, 24)
	if strings.Contains(text, "NaN") || strings.Contains(text, "Inf") {
		t.Fatalf("non-finite rate leaked into the frame:\n%s", text)
	}
	before := frameBytes(m, 110, 24)
	m.HandleEvent(event) // Δt = 0：同样不改变任何字节
	if got := frameBytes(m, 110, 24); got != before {
		t.Fatal("a zero-delta Usage changed the frame")
	}
}

func TestUsageRateAbsentKeepsDefaultFrameByteExact(t *testing.T) {
	base := frameBytes(deterministicUsageModel("en"), 110, 24)
	m := deterministicUsageModel("en")
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100})
	// 只看无速率时的首条 Usage：CH 与用量是 opt-in 门内的新内容，
	// 而没有 Usage 事件的模型仍必须与历史帧逐字节一致。
	withUsage := frameBytes(m, 110, 24)
	if withUsage == base {
		t.Fatal("the usage gate must actually render counters")
	}
	if strings.Contains(base, "⚡") || strings.Contains(base, "CH ") {
		t.Fatalf("the default frame leaked the new metrics: %q", base)
	}
}

func TestFolderLabelBasenameAndEmptyRoot(t *testing.T) {
	if got := folderLabel(filepath.Join("work", "project"), 80); got != "project" {
		t.Fatalf("folderLabel = %q, want project", got)
	}
	if got := folderLabel("", 80); got != "" {
		t.Fatalf("empty root must not render a folder, got %q", got)
	}
	if got := folderLabel("/", 80); got != "" {
		t.Fatalf("root directory must not render a folder, got %q", got)
	}

	base := frameBytes(deterministicUsageModel("en"), 110, 24)
	empty := New(Config{})
	empty.started, empty.now = usageFrameClock, func() time.Time { return usageFrameClock }
	if got := frameBytes(empty, 110, 24); got != base {
		t.Fatal("an empty Root changed the default frame")
	}
}

func TestFolderLabelCJKClipsValidUTF8(t *testing.T) {
	root := filepath.Join("work", strings.Repeat("项目资料", 20))
	label := folderLabel(root, 24)
	if label == "" || label == strings.Repeat("项目资料", 20) {
		t.Fatalf("CJK basename was not clipped: %q", label)
	}
	if !utf8.ValidString(label) {
		t.Fatalf("clipped label is invalid UTF-8: %q", label)
	}
	if w := TextWidth(label); w > 24 {
		t.Fatalf("clipped label width = %d, want <= 24", w)
	}

	m := deterministicUsageModel("en")
	m.root = root
	buf := m.Frame(40, 24)
	for y := 0; y < buf.Height(); y++ {
		row := buf.RowText(y)
		if !utf8.ValidString(row) {
			t.Fatalf("row %d is invalid UTF-8", y)
		}
		if w := TextWidth(row); w > 40 {
			t.Fatalf("row %d width = %d, want <= 40", y, w)
		}
	}
	if !strings.Contains(frameText(m, 40, 24), folderLabel(root, 40)) {
		t.Fatal("the folder basename is missing from the status line")
	}
}
