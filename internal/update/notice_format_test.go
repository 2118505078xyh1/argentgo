package update

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNoticeDisplayFormat(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		notes   string
		want    string
	}{
		{
			name:    "无说明只返回首行",
			current: "1.0.0",
			latest:  "1.2.0",
			notes:   "",
			want:    "新版本 1.2.0 可用（当前 1.0.0）",
		},
		{
			name:    "有说明含摘要与变更项",
			current: "1.0.0",
			latest:  "1.1.0",
			notes:   "修复了中文路径的问题\n\n## 更新日志\n- 提升了启动速度\n",
			want:    "新版本 1.1.0 可用（当前 1.0.0）\n修复了中文路径的问题\n- 提升了启动速度",
		},
		{
			name:    "版本前缀与预发布号保留",
			current: "v1.2.0",
			latest:  "v2.0.0-beta.1",
			notes:   "",
			want:    "新版本 v2.0.0-beta.1 可用（当前 v1.2.0）",
		},
		{
			name:    "已是最新版本",
			current: "2.0.0",
			latest:  "1.2.0",
			notes:   "一些说明",
			want:    "已是最新版本",
		},
		{
			name:    "相同版本",
			current: "1.2.0",
			latest:  "1.2.0",
			notes:   "",
			want:    "已是最新版本",
		},
		{
			name:    "恶意版本字符串被净化",
			current: "1.0.0\n\x1b[31m",
			latest:  "2.0.0",
			notes:   "",
			want:    "新版本 2.0.0 可用（当前 ?）",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := formatUpdateNotice(test.current, test.latest, test.notes)
			if got != test.want {
				t.Errorf("formatUpdateNotice = %q, want %q", got, test.want)
			}
			if strings.ContainsAny(got, "\r\x1b") {
				t.Errorf("notice contains control characters: %q", got)
			}
		})
	}
}

func TestNoticeDisplayTruncatesCJKByRune(t *testing.T) {
	notes := strings.Repeat("很", maxNoticeNoteLineRunes+10)
	got := formatUpdateNotice("1.0.0", "2.0.0", notes)
	if !utf8.ValidString(got) {
		t.Fatalf("notice is not valid UTF-8: %q", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), got)
	}
	wantLine := strings.Repeat("很", maxNoticeNoteLineRunes) + "…"
	if lines[1] != wantLine {
		t.Errorf("note line = %q, want %q", lines[1], wantLine)
	}
}

func TestNoticeDisplayLimitsNoteLines(t *testing.T) {
	var builder strings.Builder
	for index := 0; index < maxNoticeNoteLines+3; index++ {
		builder.WriteString("- 变更项 甲甲甲\n")
	}
	got := formatUpdateNotice("1.0.0", "2.0.0", builder.String())
	lines := strings.Split(got, "\n")
	if len(lines) != maxNoticeNoteLines+1 { // 首行 + N 行说明
		t.Fatalf("got %d lines, want %d: %q", len(lines), maxNoticeNoteLines+1, got)
	}
	if lines[len(lines)-1] != noticeMoreMarker {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], noticeMoreMarker)
	}
}

func TestNoticeDisplayUsesChangelogSection(t *testing.T) {
	changelog := "# 更新日志\n\n## v2.0.0 — 2026-02-01\n\n- 全新架构\n- 支持插件\n\n" +
		"## v1.0.0 — 2025-12-01\n\n- 首个版本\n"
	got := formatUpdateNotice("1.0.0", "2.0.0", changelog)
	want := "新版本 2.0.0 可用（当前 1.0.0）\n- 全新架构\n- 支持插件"
	if got != want {
		t.Errorf("formatUpdateNotice = %q, want %q", got, want)
	}
}

func TestNoticeDisplayEmptyVersions(t *testing.T) {
	if got := formatUpdateNotice("", "", ""); got != "已是最新版本" {
		t.Errorf("空版本 = %q, want 已是最新版本", got)
	}
	if got := formatUpdateNotice("", "1.0.0", ""); got != "新版本 1.0.0 可用（当前 ?）" {
		t.Errorf("空当前版本 = %q", got)
	}
}

func TestNoticeDisplayIsDeterministic(t *testing.T) {
	first := formatUpdateNotice("1.0.0", "1.1.0", "修复问题。")
	for index := 0; index < 3; index++ {
		if got := formatUpdateNotice("1.0.0", "1.1.0", "修复问题。"); got != first {
			t.Fatalf("call %d = %q, want %q", index, got, first)
		}
	}
}
