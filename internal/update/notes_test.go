package update

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

var (
	sumA = strings.Repeat("a", 64)
	sumB = strings.Repeat("B", 64) // uppercase: must be lowercased by the parser
)

func TestParseReleaseNotes(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantSummary string
		wantChanges []string
		wantSums    []NoteChecksum
	}{
		{
			name: "english changelog and fenced checksum",
			body: " Argent 1.2.0 is **here**.\n\n" +
				"## What's Changed\n" +
				"- Add `dark mode` support\n" +
				"- Fix [crash](https://example.com) on startup\n\n" +
				"## Checksums\n" +
				"```\n" + sumA + "  argent_1.2.0_windows_amd64.zip\n```\n",
			wantSummary: "Argent 1.2.0 is here.",
			wantChanges: []string{"Add dark mode support", "Fix crash on startup"},
			wantSums:    []NoteChecksum{{Hash: sumA, Name: "argent_1.2.0_windows_amd64.zip"}},
		},
		{
			name: "chinese headings keep nested bullets and end on same level",
			body: "这是一个新的版本。\n\n" +
				"## 更新日志\n" +
				"- 修复了中文路径的问题\n" +
				"- 提升了启动速度\n\n" +
				"### 内部改动\n" +
				"- 重构了下载模块\n\n" +
				"## 校验和\n" + sumB + " *argent.exe\n" +
				"- 这一行不属于更新日志\n",
			wantSummary: "这是一个新的版本。",
			wantChanges: []string{"修复了中文路径的问题", "提升了启动速度", "重构了下载模块"},
			wantSums:    []NoteChecksum{{Hash: strings.ToLower(sumB), Name: "argent.exe"}},
		},
		{
			name:        "no changelog heading collects every bullet",
			body:        "Notes without a heading.\n- one\n* two\n+ three\n1. four\n",
			wantSummary: "Notes without a heading.",
			wantChanges: []string{"one", "two", "three", "four"},
		},
		{
			name:        "bare and list prefixed digests",
			body:        "Release.\n- " + sumA + "\n" + strings.ToLower(sumB) + "\n",
			wantSummary: "Release.",
			wantSums: []NoteChecksum{
				{Hash: sumA, Name: ""},
				{Hash: strings.ToLower(sumB), Name: ""},
			},
		},
		{
			name:        "mixed label and CRLF line endings",
			body:        "Version 2.0\r\n\r\n## Changelog 更新日志\r\n- 修复了一个错误\r\n- fixed a bug\r\n",
			wantSummary: "Version 2.0",
			wantChanges: []string{"修复了一个错误", "fixed a bug"},
		},
		{
			name:        "empty body",
			body:        "",
			wantSummary: "",
			wantChanges: nil,
			wantSums:    nil,
		},
		{
			name:        "malformed digests are not checksums",
			body:        "Notes.\n" + strings.Repeat("c", 63) + "\n" + strings.Repeat("d", 65) + "\n",
			wantSummary: "Notes.",
			wantChanges: nil,
			wantSums:    nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ParseReleaseNotes(test.body)
			if got.Summary != test.wantSummary {
				t.Errorf("Summary = %q, want %q", got.Summary, test.wantSummary)
			}
			if !reflect.DeepEqual(got.Changes, test.wantChanges) {
				t.Errorf("Changes = %#v, want %#v", got.Changes, test.wantChanges)
			}
			if !reflect.DeepEqual(got.Checksums, test.wantSums) {
				t.Errorf("Checksums = %#v, want %#v", got.Checksums, test.wantSums)
			}
		})
	}
}

func TestReleaseNotesMethodUsesTheBody(t *testing.T) {
	release := Release{Body: "Intro.\n\n## 更新日志\n- 修复问题\n"}
	notes := release.Notes()
	if notes.Summary != "Intro." {
		t.Fatalf("Summary = %q", notes.Summary)
	}
	if want := []string{"修复问题"}; !reflect.DeepEqual(notes.Changes, want) {
		t.Fatalf("Changes = %#v, want %#v", notes.Changes, want)
	}
}

func TestSummarizeReleaseNotes(t *testing.T) {
	changes := make([]string, 12)
	for index := range changes {
		changes[index] = fmt.Sprintf("变更 %d", index)
	}
	tests := []struct {
		name  string
		notes ReleaseNotes
		want  string
	}{
		{
			name: "summary changes and checksum count",
			notes: ReleaseNotes{
				Summary:   "新版本发布。",
				Changes:   []string{"修复 A", "新增 B"},
				Checksums: []NoteChecksum{{Hash: sumA, Name: "x"}},
			},
			want: "新版本发布。\n- 修复 A\n- 新增 B\n1 SHA-256 checksum(s)",
		},
		{
			name:  "empty notes",
			notes: ReleaseNotes{},
			want:  "",
		},
		{
			name:  "changes are capped",
			notes: ReleaseNotes{Changes: changes},
			want: "- 变更 0\n- 变更 1\n- 变更 2\n- 变更 3\n- 变更 4\n" +
				"- 变更 5\n- 变更 6\n- 变更 7\n- 变更 8\n- 变更 9\n" +
				"... and 2 more change(s)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SummarizeReleaseNotes(test.notes); got != test.want {
				t.Errorf("SummarizeReleaseNotes = %q, want %q", got, test.want)
			}
		})
	}
}
