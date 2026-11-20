package update

import (
	"strings"
	"testing"
)

func TestFormatUpdateNotice(t *testing.T) {
	longNotes := strings.Repeat("很长", 100) // 200 CJK runes, single prose line
	tests := []struct {
		name    string
		current string
		latest  string
		notes   string
		want    string
	}{
		{
			name:    "normal upgrade",
			current: "1.0.0",
			latest:  "1.2.0",
			notes:   "",
			want:    "有新版本 1.0.0→1.2.0，运行 argent update 升级",
		},
		{
			name:    "v prefix and prerelease are preserved",
			current: "v1.2.0",
			latest:  "v2.0.0-beta.1",
			notes:   "",
			want:    "有新版本 v1.2.0→v2.0.0-beta.1，运行 argent update 升级",
		},
		{
			name:    "already latest",
			current: "2.0.0",
			latest:  "1.2.0",
			notes:   "一些发布说明。",
			want:    "已是最新版本",
		},
		{
			name:    "equal versions",
			current: "1.2.0",
			latest:  "1.2.0",
			notes:   "",
			want:    "已是最新版本",
		},
		{
			name:    "CJK notes summary",
			current: "1.0.0",
			latest:  "1.1.0",
			notes:   "这是一个新版本。\n\n## 更新日志\n- 修复了中文路径的问题\n- 提升了启动速度\n",
			want:    "有新版本 1.0.0→1.1.0，运行 argent update 升级：这是一个新版本。",
		},
		{
			name:    "notes without summary fall back to first change",
			current: "1.0.0",
			latest:  "1.1.0",
			notes:   "## 更新日志\n- 修复了一个错误\n- 提升了启动速度\n",
			want:    "有新版本 1.0.0→1.1.0，运行 argent update 升级：修复了一个错误",
		},
		{
			name:    "overlong notes are truncated by rune",
			current: "1.0.0",
			latest:  "2.0.0",
			notes:   longNotes,
			want: "有新版本 1.0.0→2.0.0，运行 argent update 升级：" +
				strings.Repeat("很长", maxNoticeSummaryRunes/2) + "…",
		},
		{
			name:    "injection in versions and notes is sanitized",
			current: "1.0.0\n\x1b[31mrm -rf /\x1b[0m",
			latest:  "2.0.0",
			notes:   "修复 [问题](http://evil.example)\x1b[31m\n第二行",
			want:    "有新版本 ?→2.0.0，运行 argent update 升级：修复 问题 [31m",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := FormatUpdateNotice(test.current, test.latest, test.notes)
			if got != test.want {
				t.Errorf("FormatUpdateNotice = %q, want %q", got, test.want)
			}
			if strings.ContainsAny(got, "\r\n\x1b") {
				t.Errorf("notice contains control characters: %q", got)
			}
		})
	}
}

func TestFormatUpdateNoticeIsPureAndDeterministic(t *testing.T) {
	first := FormatUpdateNotice("1.0.0", "1.1.0", "修复问题。")
	for index := 0; index < 3; index++ {
		if got := FormatUpdateNotice("1.0.0", "1.1.0", "修复问题。"); got != first {
			t.Fatalf("call %d = %q, want %q", index, got, first)
		}
	}
}
