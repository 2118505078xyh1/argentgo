package hooks

import (
	"path/filepath"
	"testing"
)

func TestNeedsTrustGate(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	global := t.TempDir()

	tests := []struct {
		name string
		doc  string
		root string
		want bool
	}{
		{"文档直接位于项目根", filepath.Join(root, "hooks.json"), root, true},
		{"文档位于项目子目录", filepath.Join(root, ".claude", "hooks.json"), root, true},
		{"文档位于深层子目录", filepath.Join(root, "a", "b", "c", "hooks.json"), root, true},
		{"文档等于项目根", root, root, true},
		{"通过点路径穿越仍在根内", filepath.Join(root, "sub", "..", "hooks.json"), root, true},
		{"通过点路径逃出根外", filepath.Join(root, "..", "..", "outside", "hooks.json"), root, false},
		{"同级前缀更长的目录不算在内", filepath.Join(base, "project", "hooks.json"), root, false},
		{"数字结尾的同级目录不算在内", filepath.Join(base, "proj2", "hooks.json"), root, false},
		{"父目录不算在内", base, root, false},
		{"用户全局路径不门控", filepath.Join(global, "hooks.json"), root, false},
		{"用户全局路径下的同名目录也不门控", filepath.Join(global, "proj", "hooks.json"), root, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsTrustGate(tt.doc, tt.root); got != tt.want {
				t.Fatalf("needsTrustGate(%q, %q) = %v, want %v", tt.doc, tt.root, got, tt.want)
			}
		})
	}
}

func TestNeedsTrustGateNormalizesPaths(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")

	// "./"、重复的 ".." 与结尾分隔符都规范化为同一条路径。
	messyRoot := filepath.Join(base, "tmp", "..", "proj") + string(filepath.Separator)
	messyDoc := filepath.Join(root, "sub", "..", "sub", ".", "hooks.json")
	if !needsTrustGate(messyDoc, messyRoot) {
		t.Fatalf("规范化路径应识别为在根内: doc=%q root=%q", messyDoc, messyRoot)
	}

	// 混合 "/" 与平台原生分隔符必须等价。
	mixedRoot := filepath.FromSlash(root + "/")
	mixedDoc := filepath.FromSlash(root + "/sub/hooks.json")
	if !needsTrustGate(mixedDoc, mixedRoot) {
		t.Fatalf("混合分隔符应识别为在根内: doc=%q root=%q", mixedDoc, mixedRoot)
	}

	// 相对路径相对当前目录解析后再比较。
	if !needsTrustGate(filepath.Join("sub", "hooks.json"), ".") {
		t.Fatal("当前目录下的相对文档应在根内")
	}
	if needsTrustGate(filepath.Join("..", "outside", "hooks.json"), ".") {
		t.Fatal("逃出当前目录的相对文档不应在根内")
	}
}

func TestNeedsTrustGateCJKPaths(t *testing.T) {
	base := t.TempDir()
	global := t.TempDir()
	root := filepath.Join(base, "项目", "工作区")

	if !needsTrustGate(filepath.Join(root, "子目录", "钩子.json"), root) {
		t.Fatal("CJK 子目录下的文档应需要门控")
	}
	if needsTrustGate(filepath.Join(global, "用户", "配置", "钩子.json"), root) {
		t.Fatal("CJK 用户全局路径不应需要门控")
	}
	// "工作区二" 与 "工作区" 仅多一个汉字，却应是完全不同的目录，
	// 证明比较基于完整路径分量与分隔符边界，而非裸字符串前缀。
	if needsTrustGate(filepath.Join(base, "项目", "工作区二", "钩子.json"), root) {
		t.Fatal("CJK 同级前缀目录不应被误判为在根内")
	}
	if needsTrustGate(filepath.Join(base, "项目", "钩子.json"), root) {
		t.Fatal("CJK 父目录不应被误判为在根内")
	}
}

func TestNeedsTrustGateCaseFoldingFollowsFilesystem(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Project")
	doc := filepath.Join(base, "project", "hooks.json")

	// Windows 文件系统不区分大小写，其他平台区分。
	want := filepath.Separator == '\\'
	if got := needsTrustGate(doc, root); got != want {
		t.Fatalf("needsTrustGate(%q, %q) = %v, want %v", doc, root, got, want)
	}
}

func TestNeedsTrustGateEmptyInputs(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	doc := filepath.Join(root, "hooks.json")

	tests := []struct {
		name string
		doc  string
		root string
	}{
		{"空文档路径", "", root},
		{"仅空白的文档路径", "  \t ", root},
		{"空项目根", doc, ""},
		{"仅空白的项目根", doc, "  "},
		{"两者都为空", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if needsTrustGate(tt.doc, tt.root) {
				t.Fatalf("空输入不应需要门控: doc=%q root=%q", tt.doc, tt.root)
			}
		})
	}
}
