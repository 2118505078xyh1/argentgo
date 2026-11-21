package eval

import (
	"reflect"
	"strings"
	"testing"
)

// TestCheckWorkspaceFileEquals 覆盖 FileEquals 的精确字节判据：
// 精确相等通过；子串但长度不同失败；缺文件失败；多文件（全过/其一失败）；
// CJK 精确相等与近失；路径逃逸被拒。既有 FileContains/Exists 语义不变。
func TestCheckWorkspaceFileEquals(t *testing.T) {
	dir := t.TempDir()
	if err := seed(dir, map[string]string{
		"exact.txt":   "hello world\n",
		"cjk.txt":     "你好，世界\n",
		"multi/a.txt": "a\n",
		"multi/b.txt": "b\n",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, tc := range []struct {
		name  string
		check Check
		want  string // "" 表示必须通过
	}{
		{
			name:  "exact bytes pass",
			check: Check{FileEquals: map[string]string{"exact.txt": "hello world\n"}},
		},
		{
			// 期望值是实际内容的前缀子串：长度不同，必须失败。
			name:  "substring shorter than actual fails",
			check: Check{FileEquals: map[string]string{"exact.txt": "hello world"}},
			want:  `file "exact.txt" does not equal the expected bytes`,
		},
		{
			// 实际内容是期望值的子串：长度不同，同样失败。
			name:  "actual is substring of expected fails",
			check: Check{FileEquals: map[string]string{"exact.txt": "hello world\n!"}},
			want:  `file "exact.txt" does not equal the expected bytes`,
		},
		{
			name:  "missing file fails",
			check: Check{FileEquals: map[string]string{"missing.txt": "x"}},
			want:  `cannot read "missing.txt"`,
		},
		{
			name: "multiple files all pass",
			check: Check{FileEquals: map[string]string{
				"multi/a.txt": "a\n",
				"multi/b.txt": "b\n",
			}},
		},
		{
			// 排序后 a.txt 通过、b.txt 失败，稳定报告 b.txt。
			name: "multiple files one fails",
			check: Check{FileEquals: map[string]string{
				"multi/a.txt": "a\n",
				"multi/b.txt": "wrong",
			}},
			want: `file "multi/b.txt" does not equal the expected bytes`,
		},
		{
			name:  "CJK exact bytes pass",
			check: Check{FileEquals: map[string]string{"cjk.txt": "你好，世界\n"}},
		},
		{
			name:  "CJK near miss fails",
			check: Check{FileEquals: map[string]string{"cjk.txt": "你好，世界"}},
			want:  `file "cjk.txt" does not equal the expected bytes`,
		},
		{
			name:  "path escape rejected",
			check: Check{FileEquals: map[string]string{"../outside.txt": "x"}},
			want:  "escapes the workspace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkWorkspace(tc.check, dir)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("checkWorkspace = %q, want a pass", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("checkWorkspace = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestCheckWorkspaceFileEqualsDoesNotMutateInput 锁定纯读语义：
// 调用前后 Check.FileEquals 的内容完全一致。
func TestCheckWorkspaceFileEqualsDoesNotMutateInput(t *testing.T) {
	dir := t.TempDir()
	if err := seed(dir, map[string]string{"x.txt": "body\n"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	check := Check{
		FileEquals:   map[string]string{"x.txt": "body\n"},
		FileContains: map[string]string{"x.txt": "body"},
		FilesExist:   []string{"x.txt"},
	}
	equalsBefore := make(map[string]string, len(check.FileEquals))
	for path, value := range check.FileEquals {
		equalsBefore[path] = value
	}
	containsBefore := make(map[string]string, len(check.FileContains))
	for path, value := range check.FileContains {
		containsBefore[path] = value
	}
	existBefore := append([]string(nil), check.FilesExist...)

	if got := checkWorkspace(check, dir); got != "" {
		t.Fatalf("checkWorkspace = %q, want a pass", got)
	}
	if !reflect.DeepEqual(check.FileEquals, equalsBefore) {
		t.Fatalf("FileEquals mutated: %+v", check.FileEquals)
	}
	if !reflect.DeepEqual(check.FileContains, containsBefore) {
		t.Fatalf("FileContains mutated: %+v", check.FileContains)
	}
	if !reflect.DeepEqual(check.FilesExist, existBefore) {
		t.Fatalf("FilesExist mutated: %+v", check.FilesExist)
	}
}
