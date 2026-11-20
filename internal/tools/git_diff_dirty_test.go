package tools

import (
	"reflect"
	"testing"
)

// TestParseDirtyFiles 覆盖脏文件清单解析：新增/修改/删除、重命名、
// 引号路径、CJK 八进制转义以及去重排序。
func TestParseDirtyFiles(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "空输入",
			input: "",
			want:  nil,
		},
		{
			name:  "新增修改与删除",
			input: "A  new.txt\n M a.txt\n D gone.txt\n",
			want:  []string{"a.txt", "gone.txt", "new.txt"},
		},
		{
			name:  "重命名取新路径",
			input: "R  old.txt -> new.txt\n",
			want:  []string{"new.txt"},
		},
		{
			name:  "带空格的引号路径",
			input: "?? \"my file.txt\"\n",
			want:  []string{"my file.txt"},
		},
		{
			name:  "重命名两侧均为引号路径",
			input: "R  \"old name.txt\" -> \"new name.txt\"\n",
			want:  []string{"new name.txt"},
		},
		{
			name:  "引号内字面箭头不误判为分隔符",
			input: "?? \"a -> b.txt\"\n",
			want:  []string{"a -> b.txt"},
		},
		{
			name:  "CJK 八进制转义还原",
			input: "?? \"\\344\\270\\255\\346\\226\\207.txt\"\n",
			want:  []string{"中文.txt"},
		},
		{
			name:  "引号与反斜杠转义还原",
			input: "?? \"a\\\"b\\\\c.txt\"\n",
			want:  []string{"a\"b\\c.txt"},
		},
		{
			name:  "跳过分支头并去重排序",
			input: "## main\n M z.txt\n M a.txt\nA  a.txt\n?? b.txt\n",
			want:  []string{"a.txt", "b.txt", "z.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDirtyFiles(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDirtyFiles(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

// TestGitDiffCommand 覆盖有/无提交以及文件参数两种维度的参数构造。
func TestGitDiffCommand(t *testing.T) {
	tests := []struct {
		name       string
		hasCommits bool
		files      []string
		want       []string
	}{
		{
			name:       "有提交带文件",
			hasCommits: true,
			files:      []string{"a.txt", "b.txt"},
			want:       []string{"diff", "HEAD", "--", "a.txt", "b.txt"},
		},
		{
			name:       "有提交无文件",
			hasCommits: true,
			files:      nil,
			want:       []string{"diff", "HEAD", "--"},
		},
		{
			name:       "无提交带文件",
			hasCommits: false,
			files:      []string{"a.txt"},
			want:       []string{"diff", "--cached", "--", "a.txt"},
		},
		{
			name:       "无提交无文件",
			hasCommits: false,
			files:      nil,
			want:       []string{"diff", "--cached", "--"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitDiffCommand(tt.hasCommits, tt.files)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("gitDiffCommand(%v, %#v) = %#v, want %#v", tt.hasCommits, tt.files, got, tt.want)
			}
		})
	}
}

// TestGitWorktreeDiffCommand 验证空仓库下的工作区对比参数。
func TestGitWorktreeDiffCommand(t *testing.T) {
	got := gitWorktreeDiffCommand([]string{"a.txt", "b.txt"})
	want := []string{"diff", "--", "a.txt", "b.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gitWorktreeDiffCommand = %#v, want %#v", got, want)
	}
}
