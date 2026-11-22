package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

func v2CompactionEntries(summary string) []session.Entry {
	comp := session.Compaction{Summary: summary, Dropped: 3, Keep: 2}
	data, _ := json.Marshal(comp)
	return []session.Entry{
		{Role: "system", Data: data},
	}
}

func TestBuildContinuationBriefV2(t *testing.T) {
	tests := []struct {
		name      string
		entries   []session.Entry
		wantHas   []string
		wantEmpty bool
	}{
		{
			name:      "nil entries",
			entries:   nil,
			wantEmpty: true,
		},
		{
			name:      "extractive summary has no V2 extension fields",
			entries:   v2CompactionEntries("## Compaction\n- dropped 3\n\n## Goal\n- build lexer\n\n## Next steps\n- add tests\n"),
			wantEmpty: true, // 旧的 Goal/Next steps 不属于 V2 扩展字段
		},
		{
			name:    "V2 extension fields rendered",
			entries: v2CompactionEntries("## Objective\n- ship v2\n\n## Requirements\n- stdlib only\n\n## Decisions\n- use JSONL\n\n## Work State\n- wiring done\n\n## Relevant Files\n- a.go\n\n## Important Context\n- keep zero deps\n"),
			wantHas: []string{"会话续接·扩展字段", "目标", "ship v2", "需求", "stdlib only", "决策", "use JSONL", "工作状态", "wiring done", "相关文件", "a.go", "重要上下文", "keep zero deps"},
		},
		{
			name:    "Chinese headings",
			entries: v2CompactionEntries("## 目标\n- 发布\n\n## 决策\n- 单仓\n"),
			wantHas: []string{"目标", "发布", "决策", "单仓"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildContinuationBriefV2(tt.entries, 2000)
			if tt.wantEmpty {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
				return
			}
			for _, want := range tt.wantHas {
				if !strings.Contains(got, want) {
					t.Fatalf("brief %q missing %q", got, want)
				}
			}
		})
	}
}

func TestBuildContinuationBriefV2Bounded(t *testing.T) {
	entries := v2CompactionEntries("## Objective\n- " + strings.Repeat("目标", 500) + "\n")
	got := BuildContinuationBriefV2(entries, 100)
	runes := []rune(got)
	if len(runes) > 100 {
		t.Fatalf("brief rune count = %d, want <= 100", len(runes))
	}
}

func TestSummaryFieldsV2Enabled(t *testing.T) {
	tests := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil", nil, false},
		{"empty", func(string) string { return "" }, false},
		{"one", func(k string) string {
			if k == summaryFieldsV2Env {
				return "1"
			}
			return ""
		}, true},
		{"spaces", func(k string) string {
			if k == summaryFieldsV2Env {
				return " 1 "
			}
			return ""
		}, true},
		{"other", func(k string) string {
			if k == summaryFieldsV2Env {
				return "true"
			}
			return ""
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryFieldsV2Enabled(tt.env); got != tt.want {
				t.Fatalf("enabled = %v, want %v", got, tt.want)
			}
		})
	}
}
