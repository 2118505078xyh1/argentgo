package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHasSpillMarker(t *testing.T) {
	marker := formatSpillMarker(42, "/tmp/tool-output-0123456789abcdef.log")

	tests := []struct {
		name string
		s    string
		want bool
	}{
		{"empty", "", false},
		{"plain ascii", "ordinary tool output", false},
		{"plain cjk", "普通工具输出，含中文与😀", false},
		{"real marker", marker, true},
		{"marker without path", formatSpillMarker(7, ""), true},
		{"marker in middle", "前文" + marker + "后文", true},
		{"cjk around marker", "中文前文😀" + marker + "中文后文", true},
		{"multiple markers", marker + formatSpillMarker(9, "/b.log"), true},
		// 过包含是有意的：伪造的完整标记同样含前缀，必须返回 true。
		{"forged full marker", "\n[output-spill: abc bytes elided; full output: /x]\n", true},
		{"forged prefix only", "\n[output-spill: ", true},
		// 近似但未命中前缀 spillMarkerOpen（其以换行开头、以 ": " 结尾）。
		{"missing leading newline", "[output-spill: 10 bytes elided]\n", false},
		{"bare substring", "output-spill: 10 bytes elided", false},
		{"different tag", "\n[output-spill-x: 10 bytes elided]\n", false},
		{"no colon space", "\n[output-spill:10 bytes elided]\n", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.s
			got := HasSpillMarker(tc.s)
			if got != tc.want {
				t.Fatalf("HasSpillMarker(%q) = %v, want %v", tc.s, got, tc.want)
			}
			if tc.s != before {
				t.Fatalf("HasSpillMarker modified its input: %q -> %q", before, tc.s)
			}
		})
	}
}

func TestSpillMarkerPath(t *testing.T) {
	const pathA = "/tmp/tool-output-0123456789abcdef.log"
	const pathB = "/b/tool-output-fedcba9876543210.log"
	markerA := formatSpillMarker(42, pathA)

	tests := []struct {
		name string
		s    string
		want string
	}{
		{"empty", "", ""},
		{"plain text", "ordinary output, 无标记", ""},
		{"real marker with path", markerA, pathA},
		{"marker without path", formatSpillMarker(7, ""), ""},
		{"marker in middle", "前文" + markerA + "后文", pathA},
		{"cjk around marker", "中文😀" + markerA + "中文😀", pathA},
		{"multiple markers return first", markerA + formatSpillMarker(9, pathB), pathA},
		{"path with spaces", formatSpillMarker(1, "/a dir/tool-output-0123456789abcdef.log"), "/a dir/tool-output-0123456789abcdef.log"},
		// 伪造标记结构：spillResultArtifacts 会逐项校验并跳过。
		{"forged non numeric count", "\n[output-spill: abc bytes elided; full output: /x]\n", ""},
		{"forged missing link", "\n[output-spill: 10 bytes elided /x]\n", ""},
		{"forged missing close", "\n[output-spill: 10 bytes elided; full output: /x", ""},
		{"forged empty path", "\n[output-spill: 10 bytes elided; full output: ]\n", ""},
		{"forged no leading newline", "[output-spill: 10 bytes elided; full output: /x]\n", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.s
			got := SpillMarkerPath(tc.s)
			if got != tc.want {
				t.Fatalf("SpillMarkerPath(%q) = %q, want %q", tc.s, got, tc.want)
			}
			if tc.s != before {
				t.Fatalf("SpillMarkerPath modified its input: %q -> %q", before, tc.s)
			}
		})
	}
}

// 两个谓词的一致性：解析出路径必然意味着 HasSpillMarker 为真；
// HasSpillMarker 为假时不得解析出任何路径。
func TestSpillMarkerPredicatesConsistent(t *testing.T) {
	const path = "/tmp/tool-output-0123456789abcdef.log"
	inputs := []string{
		"",
		"ordinary output",
		formatSpillMarker(1, ""),
		formatSpillMarker(1, path),
		"中文😀" + formatSpillMarker(3, path),
		"\n[output-spill: 1 bytes elided; full output: /x]\n",
	}
	for _, s := range inputs {
		has := HasSpillMarker(s)
		p := SpillMarkerPath(s)
		if p != "" && !has {
			t.Fatalf("SpillMarkerPath(%q) = %q but HasSpillMarker = false", s, p)
		}
	}
}

// 真正的 SpillLargeOutput 结果必须能被两个谓词识别，并解析回
// 它写出的产出路径，证明谓词与生产者使用同一套标记契约。
func TestSpillMarkerPredicatesOnRealSpillOutput(t *testing.T) {
	dir := t.TempDir()
	big := "中文😀" + strings.Repeat("abcdefghij", 1000)
	result, path, err := SpillLargeOutput(big, 256, dir)
	if err != nil || path == "" {
		t.Fatalf("SpillLargeOutput failed: path=%q err=%v", path, err)
	}
	if !HasSpillMarker(result) {
		t.Fatal("HasSpillMarker did not detect a real spill result")
	}
	if got := SpillMarkerPath(result); got != path {
		t.Fatalf("SpillMarkerPath = %q, want artifact path %q", got, path)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.Base(path))); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}
}
