package session

import (
	"reflect"
	"strings"
	"testing"
)

// rec 用 id/parent 构造一条最小记录，便于树导出的表驱动测试。
func rec(id, parent string) Record {
	return Record{ID: id, Parent: parent}
}

// TestExportTreeSingleChain 验证单链的节点顺序、children、depth 与 active 标记。
func TestExportTreeSingleChain(t *testing.T) {
	records := []Record{rec("a", ""), rec("b", "a"), rec("c", "b")}

	got := ExportTree(records, "c")
	want := []TreeNode{
		{ID: "a", Parent: "", Children: []string{"b"}, Depth: 0},
		{ID: "b", Parent: "a", Children: []string{"c"}, Depth: 1},
		{ID: "c", Parent: "b", Children: []string{}, IsActive: true, Depth: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExportTree 单链:\n got %#v\nwant %#v", got, want)
	}
}

// TestExportTreeFork 验证分叉树按深度优先前序排列，且各分支深度与 children 正确。
func TestExportTreeFork(t *testing.T) {
	records := []Record{
		rec("a", ""),
		rec("b", "a"),
		rec("c", "a"),
		rec("d", "b"),
		rec("e", "c"),
	}

	got := ExportTree(records, "d")
	want := []TreeNode{
		{ID: "a", Parent: "", Children: []string{"b", "c"}, Depth: 0},
		{ID: "b", Parent: "a", Children: []string{"d"}, Depth: 1},
		{ID: "d", Parent: "b", Children: []string{}, IsActive: true, Depth: 2},
		{ID: "c", Parent: "a", Children: []string{"e"}, Depth: 1},
		{ID: "e", Parent: "c", Children: []string{}, Depth: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExportTree 分叉:\n got %#v\nwant %#v", got, want)
	}
}

// TestExportTreeRootsAndUnknownParent 验证多根与悬空 parent 都按根处理，且顺序稳定。
func TestExportTreeRootsAndUnknownParent(t *testing.T) {
	records := []Record{
		rec("x", ""),
		rec("a", ""),
		rec("orphan", "missing"),
		rec("b", "a"),
	}
	got := ExportTree(records, "")
	want := []TreeNode{
		{ID: "x", Parent: "", Children: []string{}, Depth: 0},
		{ID: "a", Parent: "", Children: []string{"b"}, Depth: 0},
		{ID: "b", Parent: "a", Children: []string{}, Depth: 1},
		{ID: "orphan", Parent: "missing", Children: []string{}, Depth: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExportTree 多根/悬空 parent:\n got %#v\nwant %#v", got, want)
	}
}

// TestExportTreeDuplicateIDs 验证重复 id 只取首次出现，子节点不重复。
func TestExportTreeDuplicateIDs(t *testing.T) {
	records := []Record{
		rec("a", ""),
		rec("b", "a"),
		rec("b", "a"), // 重复 id，应被忽略
		rec("c", "b"),
	}
	got := ExportTree(records, "")
	want := []TreeNode{
		{ID: "a", Parent: "", Children: []string{"b"}, Depth: 0},
		{ID: "b", Parent: "a", Children: []string{"c"}, Depth: 1},
		{ID: "c", Parent: "b", Children: []string{}, Depth: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExportTree 重复 id:\n got %#v\nwant %#v", got, want)
	}
}

// TestExportTreeCycleSafe 验证互环与自环都只产生有限个节点，且不会无限递归。
func TestExportTreeCycleSafe(t *testing.T) {
	t.Run("mutual", func(t *testing.T) {
		got := ExportTree([]Record{rec("a", "b"), rec("b", "a")}, "")
		if len(got) != 2 {
			t.Fatalf("互环应输出 2 个节点，得到 %d: %#v", len(got), got)
		}
		if got[0].ID != "a" || got[1].ID != "b" {
			t.Fatalf("互环节点顺序错误: %#v", got)
		}
	})

	t.Run("self", func(t *testing.T) {
		got := ExportTree([]Record{rec("a", "a")}, "")
		if len(got) != 1 || got[0].ID != "a" {
			t.Fatalf("自环应输出单个 a 节点，得到 %#v", got)
		}
	})

	t.Run("back edge", func(t *testing.T) {
		// a 为根，b 的 parent 指向后代 c 形成回边；仍须终止且不丢节点。
		got := ExportTree([]Record{rec("a", ""), rec("b", "c"), rec("c", "a")}, "")
		if len(got) != 3 {
			t.Fatalf("回边应输出 3 个节点，得到 %d: %#v", len(got), got)
		}
		seen := map[string]bool{}
		for _, node := range got {
			if seen[node.ID] {
				t.Fatalf("节点 %q 重复输出: %#v", node.ID, got)
			}
			seen[node.ID] = true
		}
	})
}

// TestExportTreeDoesNotMutateInput 验证导出过程不修改入参（含 Data 切片）。
func TestExportTreeDoesNotMutateInput(t *testing.T) {
	records := []Record{
		{ID: "a", Parent: "", Kind: KindUser, Data: []byte(`{"content":"hi"}`)},
		{ID: "b", Parent: "a", Kind: KindAssistant, Data: []byte(`{"content":"yo"}`)},
	}
	// 深拷贝用于比较：逐条复制 Record 并克隆 Data 底层字节。
	snapshot := make([]Record, len(records))
	for i, record := range records {
		snapshot[i] = record
		snapshot[i].Data = append([]byte(nil), record.Data...)
	}

	got := ExportTree(records, "b")
	if len(got) == 0 {
		t.Fatal("ExportTree 不应返回空结果")
	}
	if !reflect.DeepEqual(records, snapshot) {
		t.Fatalf("ExportTree 修改了入参:\n got %#v\nwant %#v", records, snapshot)
	}
}

// TestExportTreeDeterministic 验证相同输入多次调用得到完全一致的结果。
func TestExportTreeDeterministic(t *testing.T) {
	records := []Record{
		rec("root", ""),
		rec("b", "root"),
		rec("c", "root"),
		rec("d", "b"),
		rec("e", "c"),
		rec("orphan", "missing"),
	}
	first := ExportTree(records, "d")
	for i := 0; i < 30; i++ {
		if got := ExportTree(records, "d"); !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次调用结果不一致:\n got %#v\nwant %#v", i, got, first)
		}
	}
}

// TestRenderTreeASCIIShape 验证树形渲染的连接线与缩进形状。
func TestRenderTreeASCIIShape(t *testing.T) {
	records := []Record{
		rec("a", ""),
		rec("b", "a"),
		rec("c", "a"),
		rec("d", "b"),
		rec("e", "c"),
	}
	nodes := ExportTree(records, "")
	want := strings.Join([]string{
		"a",
		"├── b",
		"│   └── d",
		"└── c",
		"    └── e",
	}, "\n")
	if got := RenderTreeASCII(nodes); got != want {
		t.Fatalf("RenderTreeASCII 形状:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderTreeASCIIActiveMarker 验证 active 节点行尾带有标记，其余节点不带。
func TestRenderTreeASCIIActiveMarker(t *testing.T) {
	records := []Record{rec("a", ""), rec("b", "a"), rec("c", "a")}
	nodes := ExportTree(records, "b")
	rendered := RenderTreeASCII(nodes)

	if !strings.Contains(rendered, "├── b"+asciiActive) {
		t.Fatalf("active 节点缺少标记:\n%s", rendered)
	}
	if strings.Contains(rendered, "└── c"+asciiActive) {
		t.Fatalf("非 active 节点不应带标记:\n%s", rendered)
	}
}

// TestRenderTreeASCIICJK 验证 CJK 标题被逐字保留、不按字节截断，且形状对齐正确。
func TestRenderTreeASCIICJK(t *testing.T) {
	records := []Record{
		rec("根", ""),
		rec("子节点甲", "根"),
		rec("子节点乙", "根"),
	}
	nodes := ExportTree(records, "子节点甲")
	want := strings.Join([]string{
		"根",
		"├── 子节点甲" + asciiActive,
		"└── 子节点乙",
	}, "\n")
	if got := RenderTreeASCII(nodes); got != want {
		t.Fatalf("RenderTreeASCII CJK:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderTreeASCIIEmpty 验证空输入返回空串。
func TestRenderTreeASCIIEmpty(t *testing.T) {
	if got := RenderTreeASCII(nil); got != "" {
		t.Fatalf("空输入应返回空串，得到 %q", got)
	}
	if got := RenderTreeASCII([]TreeNode{}); got != "" {
		t.Fatalf("空切片应返回空串，得到 %q", got)
	}
}

// TestRenderTreeASCIIUnknownAndCycle 验证未知子节点与环不会导致挂起或多余行。
func TestRenderTreeASCIIUnknownAndCycle(t *testing.T) {
	t.Run("unknown child", func(t *testing.T) {
		nodes := []TreeNode{
			{ID: "a", Parent: "", Children: []string{"ghost", "b"}},
			{ID: "b", Parent: "a", Children: []string{}},
		}
		want := strings.Join([]string{"a", "└── b"}, "\n")
		if got := RenderTreeASCII(nodes); got != want {
			t.Fatalf("未知子节点应被忽略:\n got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		nodes := []TreeNode{
			{ID: "a", Parent: "b", Children: []string{"b"}},
			{ID: "b", Parent: "a", Children: []string{"a"}},
		}
		got := RenderTreeASCII(nodes)
		lines := strings.Split(got, "\n")
		if len(lines) != 2 {
			t.Fatalf("环应渲染 2 行，得到 %d 行:\n%s", len(lines), got)
		}
	})
}
