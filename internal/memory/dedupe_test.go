package memory

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestDedupeContainedDocs(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	child := filepath.Join(root, "src", "parser")
	deeper := filepath.Join(child, "nested")

	parent := "rule one\nrule two\n\nrule three"
	// 子文件把父文件的三段原样抄一遍，并在中间夹了一段自己的规则：
	// 父文件的段落序列仍是子文件的连续子序列，应被去重。
	childText := "local header\n\n" + parent + "\n\nlocal footer"
	// 改写版：把其中一段改了字，父文件不再是连续子序列，两者都保留。
	rewritten := "rule one\nrule TWO CHANGED\n\nrule three"

	tests := []struct {
		name string
		docs []Doc
		want []string
	}{
		{
			name: "parent fully contained by deeper child is dropped",
			docs: []Doc{
				{Path: filepath.Join(child, "AGENTS.md"), Text: childText},
				{Path: filepath.Join(root, "AGENTS.md"), Text: parent},
			},
			want: []string{childText},
		},
		{
			name: "rewritten paragraph keeps both",
			docs: []Doc{
				{Path: filepath.Join(child, "AGENTS.md"), Text: rewritten},
				{Path: filepath.Join(root, "AGENTS.md"), Text: parent},
			},
			want: []string{rewritten, parent},
		},
		{
			name: "interleaved paragraphs keep both",
			docs: []Doc{
				{Path: filepath.Join(child, "AGENTS.md"), Text: "rule one\n\ninserted\n\nrule two\n\nrule three"},
				{Path: filepath.Join(root, "AGENTS.md"), Text: parent},
			},
			want: []string{"rule one\n\ninserted\n\nrule two\n\nrule three", parent},
		},
		{
			name: "siblings at same depth are never deduped",
			docs: []Doc{
				{Path: filepath.Join(child, "AGENTS.md"), Text: parent},
				{Path: filepath.Join(child, "CLAUDE.md"), Text: parent},
			},
			want: []string{parent, parent},
		},
		{
			name: "truncated parent is never dropped",
			docs: []Doc{
				{Path: filepath.Join(child, "AGENTS.md"), Text: childText},
				{Path: filepath.Join(root, "AGENTS.md"), Text: parent, Truncated: true},
			},
			want: []string{childText, parent},
		},
		{
			name: "three levels collapse to the most specific container",
			docs: []Doc{
				{Path: filepath.Join(deeper, "AGENTS.md"), Text: "deep\n\n" + childText},
				{Path: filepath.Join(child, "AGENTS.md"), Text: childText},
				{Path: filepath.Join(root, "AGENTS.md"), Text: parent},
			},
			want: []string{"deep\n\n" + childText},
		},
		{
			name: "whitespace and CRLF differences still match",
			docs: []Doc{
				{Path: filepath.Join(child, "AGENTS.md"), Text: "  local  \r\n\r\n  rule one  \r\n  rule two  \r\n\r\n  rule three  "},
				{Path: filepath.Join(root, "AGENTS.md"), Text: parent},
			},
			want: []string{"  local  \r\n\r\n  rule one  \r\n  rule two  \r\n\r\n  rule three  "},
		},
		{
			name: "single doc unchanged",
			docs: []Doc{{Path: filepath.Join(root, "AGENTS.md"), Text: parent}},
			want: []string{parent},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DedupeContainedDocs(tc.docs)
			texts := make([]string, len(got))
			for i, doc := range got {
				texts[i] = doc.Text
			}
			if !reflect.DeepEqual(texts, tc.want) {
				t.Fatalf("texts = %#v, want %#v", texts, tc.want)
			}
		})
	}
}

func TestNormalizeParagraphs(t *testing.T) {
	got := normalizeParagraphs(" a \n b \r\n\r\n  c\r\n\r\n")
	want := []string{"a\nb", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeParagraphs = %#v, want %#v", got, want)
	}
	if got := normalizeParagraphs(" \n \t\n"); len(got) != 0 {
		t.Fatalf("blank text should yield no paragraphs, got %#v", got)
	}
}
