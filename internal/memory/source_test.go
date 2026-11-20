package memory

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// InstructionSources 对 nil/空/全空白输入返回 nil，并跳过
// 混合输入中的空白来源（与 RelevantInstructions/OrderInstructions 一致）。
func TestInstructionSourcesEmptyAndBlankSources(t *testing.T) {
	if got := InstructionSources(nil); got != nil {
		t.Fatalf("nil docs = %#v, want nil", got)
	}
	if got := InstructionSources([]Instruction{}); got != nil {
		t.Fatalf("empty docs = %#v, want nil", got)
	}
	blank := []Instruction{{Source: ""}, {Source: "   "}, {Source: "\t"}}
	if got := InstructionSources(blank); got != nil {
		t.Fatalf("all blank sources = %#v, want nil", got)
	}

	base := t.TempDir()
	in := []Instruction{
		{Source: "", Text: "skip"},
		{Source: filepath.Join(base, "AGENTS.md"), Text: "keep"},
		{Source: "  ", Text: "skip"},
	}
	got := InstructionSources(in)
	if len(got) != 1 || got[0].Source != filepath.Join(base, "AGENTS.md") {
		t.Fatalf("mixed blank sources = %#v, want one entry", got)
	}
}

// 单条指令在集合中没有祖先，因此从不受边界
// 限制；其所有溯源字段都被正确报告。
func TestInstructionSourcesSingle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "项目", "子目录")
	src := filepath.Join(dir, "CLAUDE.md")
	got := InstructionSources([]Instruction{{Source: src, Text: "x"}})
	want := []SourceInfo{{
		Source:          src,
		Name:            "CLAUDE.md",
		Depth:           pathDepth(src),
		ScopeDir:        dir,
		BoundaryLimited: false,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InstructionSources = %#v, want %#v", got, want)
	}
}

// 核心表格：嵌套决定 BoundaryLimited，深度随嵌套增长，
// 处理 CJK 路径，且同目录文件不互相限制。
func TestInstructionSourcesTable(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "项目") // CJK component
	src := filepath.Join(root, "src")
	parser := filepath.Join(src, "parser")
	lexer := filepath.Join(src, "词法") // CJK nested component

	inst := func(dir, name string) Instruction {
		return Instruction{Source: filepath.Join(dir, name), Text: "x"}
	}
	info := func(dir, name string) SourceInfo {
		p := filepath.Join(dir, name)
		return SourceInfo{
			Source:   p,
			Name:     name,
			Depth:    pathDepth(p),
			ScopeDir: dir,
		}
	}

	tests := []struct {
		name string
		in   []Instruction
		// wantLimited 是每个条目预期的 BoundaryLimited 值，与
		// in 对齐（所有输入都保留来源，因此长度一致）。
		wantLimited []bool
		wantOrder   []string // source paths, to assert order + identity
	}{
		{
			name:        "root and nested CJK child: child limited",
			in:          []Instruction{inst(root, "AGENTS.md"), inst(parser, "AGENTS.md")},
			wantLimited: []bool{false, true},
			wantOrder:   []string{filepath.Join(root, "AGENTS.md"), filepath.Join(parser, "AGENTS.md")},
		},
		{
			name: "three levels: only the root is global",
			in: []Instruction{
				inst(root, "AGENTS.md"),
				inst(src, "AGENTS.md"),
				inst(lexer, "AGENTS.md"),
			},
			wantLimited: []bool{false, true, true},
			wantOrder: []string{
				filepath.Join(root, "AGENTS.md"),
				filepath.Join(src, "AGENTS.md"),
				filepath.Join(lexer, "AGENTS.md"),
			},
		},
		{
			name: "same directory AGENTS and CLAUDE do not limit each other",
			in: []Instruction{
				inst(root, "AGENTS.md"),
				inst(root, "CLAUDE.md"),
			},
			wantLimited: []bool{false, false},
			wantOrder:   []string{filepath.Join(root, "AGENTS.md"), filepath.Join(root, "CLAUDE.md")},
		},
		{
			name: "override and CLAUDE in a nested dir with a root above",
			in: []Instruction{
				inst(root, "AGENTS.md"),
				inst(src, "AGENTS.override.md"),
				inst(src, "CLAUDE.md"),
			},
			wantLimited: []bool{false, true, true},
			wantOrder: []string{
				filepath.Join(root, "AGENTS.md"),
				filepath.Join(src, "AGENTS.override.md"),
				filepath.Join(src, "CLAUDE.md"),
			},
		},
		{
			name:        "sibling directories are mutually unlimited",
			in:          []Instruction{inst(parser, "AGENTS.md"), inst(lexer, "AGENTS.md")},
			wantLimited: []bool{false, false},
			wantOrder:   []string{filepath.Join(parser, "AGENTS.md"), filepath.Join(lexer, "AGENTS.md")},
		},
		{
			name:        "ancestor beats prefix lookalike scope",
			in:          []Instruction{inst(src, "AGENTS.md"), inst(filepath.Join(root, "src-other"), "AGENTS.md")},
			wantLimited: []bool{false, false}, // src-other is a sibling, not below src
			wantOrder: []string{
				filepath.Join(src, "AGENTS.md"),
				filepath.Join(root, "src-other", "AGENTS.md"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InstructionSources(tc.in)
			if len(got) != len(tc.in) {
				t.Fatalf("got %d entries, want %d: %#v", len(got), len(tc.in), got)
			}
			for i, g := range got {
				want := info(filepath.Dir(tc.in[i].Source), filepath.Base(tc.in[i].Source))
				want.BoundaryLimited = tc.wantLimited[i]
				if g != want {
					t.Errorf("entry %d = %#v, want %#v", i, g, want)
				}
				if g.Source != tc.wantOrder[i] {
					t.Errorf("entry %d order: Source = %q, want %q", i, g.Source, tc.wantOrder[i])
				}
			}
		})
	}
}

// 深度必须每嵌套一层恰好增加一，并与
// OrderInstructions 的 pathDepth 度量一致；CJK 目录名算作普通
// 路径组成部分（各计一个分隔符）。
func TestInstructionSourcesDepthMatchesPathDepth(t *testing.T) {
	base := t.TempDir()
	d0 := filepath.Join(base, "AGENTS.md")
	d1 := filepath.Join(base, "甲", "AGENTS.md")
	d2 := filepath.Join(base, "甲", "乙", "丙", "AGENTS.md")
	got := InstructionSources([]Instruction{{Source: d0}, {Source: d1}, {Source: d2}})
	if len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	for i, src := range []string{d0, d1, d2} {
		if got[i].Depth != pathDepth(src) {
			t.Errorf("entry %d Depth = %d, want pathDepth = %d", i, got[i].Depth, pathDepth(src))
		}
	}
	if got[1].Depth != got[0].Depth+1 || got[2].Depth != got[1].Depth+2 {
		t.Errorf("Depth should grow with nesting (one separator per CJK level): %d, %d, %d",
			got[0].Depth, got[1].Depth, got[2].Depth)
	}
}

// InstructionSources 是纯函数：输入切片不变，结果是一个
// 新切片，对其的修改不会泄漏回调用方的 docs。
func TestInstructionSourcesIsPure(t *testing.T) {
	base := t.TempDir()
	in := []Instruction{
		{Source: filepath.Join(base, "AGENTS.md"), Text: "root"},
		{Source: filepath.Join(base, "src", "AGENTS.md"), Text: "nested"},
	}
	snapshot := append([]Instruction(nil), in...)

	got := InstructionSources(in)
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("input mutated: got %#v, want %#v", in, snapshot)
	}
	got[0].Source = "CHANGED"
	got[0].BoundaryLimited = !got[0].BoundaryLimited
	if in[0].Source != snapshot[0].Source {
		t.Fatalf("output aliases input: %#v", in)
	}
}

// TestInstructionSourcesCaseInsensitiveAncestorOnWindows 守护文档中记录的
// Windows 上不区分大小写的包含关系，用于检测祖先作用域；
// 在其他平台上为空操作。
func TestInstructionSourcesCaseInsensitiveAncestorOnWindows(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("case-insensitive path containment applies on Windows only")
	}
	base := t.TempDir()
	lowerRoot := filepath.Join(base, "src", "AGENTS.md")
	upperAncestorScope := filepath.Join(strings.ToUpper(base), "SRC", "deep", "AGENTS.md")
	got := InstructionSources([]Instruction{{Source: lowerRoot}, {Source: upperAncestorScope}})
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	if !got[1].BoundaryLimited {
		t.Errorf("differently-cased ancestor scope should mark the nested doc limited: %#v", got)
	}
}
