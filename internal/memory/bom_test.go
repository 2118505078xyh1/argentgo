package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkBOMDoc 针对内容预算断言某个加载器的结果。前导
// BOM 是元数据：上限适用于移除它之后的内容字节，
// 而非包含它的磁盘字节。
func checkBOMDoc(t *testing.T, text string, truncated, wantTruncated bool, wantText string) {
	t.Helper()
	body := text
	if truncated {
		body = strings.TrimSuffix(text, TruncationMarker)
		if body == text {
			t.Errorf("truncated doc is missing its marker: %q", text)
		}
	} else if strings.Contains(text, TruncationMarker) {
		t.Errorf("untruncated doc contains a truncation marker: %q", text)
	}
	if body != wantText {
		t.Errorf("body = %q, want %q", body, wantText)
	}
	if truncated != wantTruncated {
		t.Errorf("Truncated = %v, want %v", truncated, wantTruncated)
	}
}

// TestBOMDoesNotConsumePerFileByteBudget 覆盖 LoadIn 和 LoadWithBoundary，
// 这两个加载器具有可配置的单文件上限。修复前，上限是按
// 原始字节（含 BOM）计算的，因此内容刚好符合限制的文件
// 会丢失最后三个字节并被标记为已截断。
func TestBOMDoesNotConsumePerFileByteBudget(t *testing.T) {
	const bom = "\ufeff"
	cases := []struct {
		name      string
		body      string
		limit     int
		wantText  string
		wantTrunc bool
	}{
		{"content exactly at limit", strings.Repeat("a", 10), 10, strings.Repeat("a", 10), false},
		{"one content byte over limit", strings.Repeat("b", 11), 10, strings.Repeat("b", 10), true},
		{"multibyte clipped on rune boundary", "你好世界", 7, "你好", true},
		{"short content well under limit", "hello", 8, "hello", false},
		{"no BOM control", strings.Repeat("c", 10), 10, strings.Repeat("c", 10), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := bom
			if tc.name == "no BOM control" {
				prefix = ""
			}
			content := prefix + tc.body

			root := t.TempDir()
			writeFile(t, root, "AGENTS.md", content)
			docs, err := LoadIn(root, root, tc.limit)
			if err != nil {
				t.Fatalf("LoadIn: %v", err)
			}
			if len(docs) != 1 {
				t.Fatalf("LoadIn docs = %d, want 1: %#v", len(docs), docs)
			}
			checkBOMDoc(t, docs[0].Text, docs[0].Truncated, tc.wantTrunc, tc.wantText)

			root2 := t.TempDir()
			writeFile(t, root2, "AGENTS.md", content)
			if err := os.MkdirAll(filepath.Join(root2, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			insts, err := LoadWithBoundary(root2, true, LoadOptions{Home: t.TempDir(), Limit: tc.limit})
			if err != nil {
				t.Fatalf("LoadWithBoundary: %v", err)
			}
			if len(insts) != 1 {
				t.Fatalf("LoadWithBoundary insts = %d, want 1: %#v", len(insts), insts)
			}
			checkBOMDoc(t, insts[0].Text, insts[0].Truncated, tc.wantTrunc, tc.wantText)
		})
	}
}

// TestBOMDoesNotConsumeBudgetInLoadForDirs 覆盖嵌套加载器，它
// 总是按 DefaultLimit 裁剪。同样的"BOM 在限幅之前"契约必须成立。
func TestBOMDoesNotConsumeBudgetInLoadForDirs(t *testing.T) {
	const bom = "\ufeff"
	cases := []struct {
		name      string
		body      string
		wantText  string
		wantTrunc bool
	}{
		{"exactly at DefaultLimit", strings.Repeat("a", DefaultLimit), strings.Repeat("a", DefaultLimit), false},
		{"three bytes over DefaultLimit", strings.Repeat("b", DefaultLimit+3), strings.Repeat("b", DefaultLimit), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "AGENTS.md", bom+tc.body)
			docs := LoadForDirs(root, []string{"."})
			if len(docs) != 1 {
				t.Fatalf("docs = %d, want 1: %#v", len(docs), docs)
			}
			checkBOMDoc(t, docs[0].Text, docs[0].Truncated, tc.wantTrunc, tc.wantText)
		})
	}
}
