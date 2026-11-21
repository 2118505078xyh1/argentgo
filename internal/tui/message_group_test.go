package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// snapshotItems 深拷贝一维结构，区分 nil 与空的非 nil 切片，避免
// 快照比较因 nil/空差异出现假阳性。
func snapshotItems(items []DisplayItem) []DisplayItem {
	if items == nil {
		return nil
	}
	out := make([]DisplayItem, len(items))
	copy(out, items)
	return out
}

// snapshotGroups 深拷贝二维结构，用作入参快照。与 cloneItems 不同，
// 它区分 nil 与空的非 nil 内层切片，避免快照比较出现假阳性。
func snapshotGroups(groups [][]DisplayItem) [][]DisplayItem {
	if groups == nil {
		return nil
	}
	out := make([][]DisplayItem, len(groups))
	for i, group := range groups {
		out[i] = snapshotItems(group)
	}
	return out
}

// wantSummary 构造折叠后的期望占位项：ID/Seq 来自组内首项，Text 带
// 被折叠项数与原角色。字面量 "summary" 用于锁定对外契约。
func wantSummary(first DisplayItem, count int, role string) DisplayItem {
	return DisplayItem{
		ID:   first.ID,
		Role: "summary",
		Text: fmt.Sprintf("collapsed %d %s messages", count, role),
		Seq:  first.Seq,
	}
}

// overwriteItems 改写结果中的每一个 DisplayItem，用于验证返回值没有
// 与入参共享底层数组。
func overwriteItems(groups [][]DisplayItem) {
	for i := range groups {
		for j := range groups[i] {
			groups[i][j] = DisplayItem{ID: "X", Role: "X", Text: "X", Seq: 999}
		}
	}
}

func assertGroupsEqual(t *testing.T, got, want [][]DisplayItem) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %+v, want %+v", got, want)
	}
}

// TestGroupConsecutive 表驱动覆盖连续分组的核心行为：空输入、单项、
// 全同角色、角色切换、多段连续以及 CJK 正文的原样保留。每个子用例
// 同时验证入参未被修改、返回结果与入参不共享底层数组。
func TestGroupConsecutive(t *testing.T) {
	tests := []struct {
		name  string
		items []DisplayItem
		want  [][]DisplayItem
	}{
		{
			name:  "nil input",
			items: nil,
			want:  nil,
		},
		{
			name:  "empty input",
			items: []DisplayItem{},
			want:  nil,
		},
		{
			name:  "single item",
			items: []DisplayItem{{ID: "a", Role: "user", Text: "hello", Seq: 1}},
			want: [][]DisplayItem{
				{{ID: "a", Role: "user", Text: "hello", Seq: 1}},
			},
		},
		{
			name: "all same role forms one group",
			items: []DisplayItem{
				{ID: "a", Role: "user", Text: "one", Seq: 1},
				{ID: "b", Role: "user", Text: "two", Seq: 2},
				{ID: "c", Role: "user", Text: "three", Seq: 3},
			},
			want: [][]DisplayItem{
				{
					{ID: "a", Role: "user", Text: "one", Seq: 1},
					{ID: "b", Role: "user", Text: "two", Seq: 2},
					{ID: "c", Role: "user", Text: "three", Seq: 3},
				},
			},
		},
		{
			name: "role switch splits groups",
			items: []DisplayItem{
				{ID: "a", Role: "user", Text: "q", Seq: 1},
				{ID: "b", Role: "assistant", Text: "a", Seq: 2},
				{ID: "c", Role: "user", Text: "q2", Seq: 3},
			},
			want: [][]DisplayItem{
				{{ID: "a", Role: "user", Text: "q", Seq: 1}},
				{{ID: "b", Role: "assistant", Text: "a", Seq: 2}},
				{{ID: "c", Role: "user", Text: "q2", Seq: 3}},
			},
		},
		{
			name: "consecutive runs keep order and sizes",
			items: []DisplayItem{
				{ID: "a", Role: "user", Text: "1", Seq: 1},
				{ID: "b", Role: "user", Text: "2", Seq: 2},
				{ID: "c", Role: "assistant", Text: "3", Seq: 3},
				{ID: "d", Role: "assistant", Text: "4", Seq: 4},
				{ID: "e", Role: "assistant", Text: "5", Seq: 5},
				{ID: "f", Role: "tool", Text: "6", Seq: 6},
				{ID: "g", Role: "user", Text: "7", Seq: 7},
			},
			want: [][]DisplayItem{
				{
					{ID: "a", Role: "user", Text: "1", Seq: 1},
					{ID: "b", Role: "user", Text: "2", Seq: 2},
				},
				{
					{ID: "c", Role: "assistant", Text: "3", Seq: 3},
					{ID: "d", Role: "assistant", Text: "4", Seq: 4},
					{ID: "e", Role: "assistant", Text: "5", Seq: 5},
				},
				{{ID: "f", Role: "tool", Text: "6", Seq: 6}},
				{{ID: "g", Role: "user", Text: "7", Seq: 7}},
			},
		},
		{
			name: "CJK text preserved verbatim",
			items: []DisplayItem{
				{ID: "中1", Role: "用户", Text: "你好，世界", Seq: 1},
				{ID: "中2", Role: "用户", Text: "第二句中文", Seq: 2},
				{ID: "中3", Role: "助手", Text: "这是答复内容", Seq: 3},
			},
			want: [][]DisplayItem{
				{
					{ID: "中1", Role: "用户", Text: "你好，世界", Seq: 1},
					{ID: "中2", Role: "用户", Text: "第二句中文", Seq: 2},
				},
				{{ID: "中3", Role: "助手", Text: "这是答复内容", Seq: 3}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := snapshotItems(tt.items)
			got := GroupConsecutive(tt.items)
			assertGroupsEqual(t, got, tt.want)

			// 纯函数：入参不得被修改。
			if !reflect.DeepEqual(tt.items, before) {
				t.Fatalf("input mutated: got %+v, before %+v", tt.items, before)
			}
			// 返回值不得与入参共享底层数组。
			overwriteItems(got)
			if !reflect.DeepEqual(tt.items, before) {
				t.Fatalf("result aliases input: items = %+v, before %+v", tt.items, before)
			}
		})
	}
}

// collapseFixture 提供一组角色与大小各异的组，供折叠用例共用。
func collapseFixture() [][]DisplayItem {
	return [][]DisplayItem{
		{
			{ID: "m1", Role: "user", Text: "问题一", Seq: 1},
			{ID: "m2", Role: "user", Text: "问题二", Seq: 2},
		},
		{
			{ID: "m3", Role: "assistant", Text: "回答", Seq: 3},
		},
		{
			{ID: "m4", Role: "tool", Text: "结果一", Seq: 4},
			{ID: "m5", Role: "tool", Text: "结果二", Seq: 5},
			{ID: "m6", Role: "tool", Text: "结果三", Seq: 6},
		},
	}
}

// TestCollapseOldGroups 表驱动覆盖折叠行为：保留末尾若干组全文、前面的
// 组折叠为带数量的占位项，以及 keepLast 的全部边界（负数、零、超出
// 长度、空输入）。每个子用例同时验证入参未被修改且返回值与入参隔离。
func TestCollapseOldGroups(t *testing.T) {
	fixture := collapseFixture()
	userGroup, asstGroup, toolGroup := fixture[0], fixture[1], fixture[2]

	userSummary := []DisplayItem{wantSummary(userGroup[0], 2, "user")}
	asstSummary := []DisplayItem{wantSummary(asstGroup[0], 1, "assistant")}
	toolSummary := []DisplayItem{wantSummary(toolGroup[0], 3, "tool")}

	tests := []struct {
		name     string
		groups   [][]DisplayItem
		keepLast int
		want     [][]DisplayItem
	}{
		{
			name:     "nil groups",
			groups:   nil,
			keepLast: 2,
			want:     nil,
		},
		{
			name:     "empty groups",
			groups:   [][]DisplayItem{},
			keepLast: 1,
			want:     nil,
		},
		{
			name:     "keep last one",
			groups:   fixture,
			keepLast: 1,
			want:     [][]DisplayItem{userSummary, asstSummary, cloneItems(toolGroup)},
		},
		{
			name:     "keep last two",
			groups:   fixture,
			keepLast: 2,
			want:     [][]DisplayItem{userSummary, cloneItems(asstGroup), cloneItems(toolGroup)},
		},
		{
			name:     "keep all when equal to length",
			groups:   fixture,
			keepLast: 3,
			want:     [][]DisplayItem{cloneItems(userGroup), cloneItems(asstGroup), cloneItems(toolGroup)},
		},
		{
			name:     "keep all when greater than length",
			groups:   fixture,
			keepLast: 9,
			want:     [][]DisplayItem{cloneItems(userGroup), cloneItems(asstGroup), cloneItems(toolGroup)},
		},
		{
			name:     "keep none with zero",
			groups:   fixture,
			keepLast: 0,
			want:     [][]DisplayItem{userSummary, asstSummary, toolSummary},
		},
		{
			name:     "negative clamps to zero",
			groups:   fixture,
			keepLast: -3,
			want:     [][]DisplayItem{userSummary, asstSummary, toolSummary},
		},
		{
			name:     "single group kept whole",
			groups:   [][]DisplayItem{userGroup},
			keepLast: 1,
			want:     [][]DisplayItem{cloneItems(userGroup)},
		},
		{
			name:     "single group collapsed",
			groups:   [][]DisplayItem{userGroup},
			keepLast: 0,
			want:     [][]DisplayItem{userSummary},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := snapshotGroups(tt.groups)
			got := CollapseOldGroups(tt.groups, tt.keepLast)
			assertGroupsEqual(t, got, tt.want)

			// 纯函数：入参不得被修改。
			if !reflect.DeepEqual(tt.groups, before) {
				t.Fatalf("input mutated: got %+v, before %+v", tt.groups, before)
			}
			// 返回值不得与入参共享底层数组。
			overwriteItems(got)
			if !reflect.DeepEqual(tt.groups, before) {
				t.Fatalf("result aliases input: groups = %+v, before %+v", tt.groups, before)
			}
		})
	}
}

// TestCollapseOldGroupsPlaceholderFields 单独锁定折叠占位项的字段契约：
// Role 固定为 summary，ID/Seq 取自组内首项，Text 同时含项数与原角色。
func TestCollapseOldGroupsPlaceholderFields(t *testing.T) {
	groups := [][]DisplayItem{
		{
			{ID: "first", Role: "assistant", Text: "早期内容", Seq: 7},
			{ID: "second", Role: "assistant", Text: "更早内容", Seq: 8},
		},
		{{ID: "last", Role: "user", Text: "保留", Seq: 9}},
	}
	got := CollapseOldGroups(groups, 1)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	summary := got[0]
	if len(summary) != 1 {
		t.Fatalf("collapsed group len = %d, want 1", len(summary))
	}
	item := summary[0]
	if item.Role != "summary" {
		t.Errorf("role = %q, want %q", item.Role, "summary")
	}
	if item.ID != "first" || item.Seq != 7 {
		t.Errorf("anchor = (%q, %d), want (%q, 7)", item.ID, item.Seq, "first")
	}
	if !strings.Contains(item.Text, "2") || !strings.Contains(item.Text, "assistant") {
		t.Errorf("summary text %q must carry count and original role", item.Text)
	}
	if got[1][0].Text != "保留" {
		t.Errorf("kept group text = %q, want %q", got[1][0].Text, "保留")
	}
}

// TestCollapseOldGroupsEmptyInnerGroup 确认空组被折叠成计数为 0 的占位
// 项，且不会产生 nil 元素或崩溃。
func TestCollapseOldGroupsEmptyInnerGroup(t *testing.T) {
	groups := [][]DisplayItem{
		{},
		{{ID: "k", Role: "user", Text: "保留", Seq: 1}},
	}
	got := CollapseOldGroups(groups, 1)
	if len(got) != 2 || len(got[0]) != 1 {
		t.Fatalf("shape = %+v, want two groups with a single placeholder first", got)
	}
	want := DisplayItem{Role: "summary", Text: "collapsed 0  messages"}
	if !reflect.DeepEqual(got[0][0], want) {
		t.Fatalf("placeholder = %+v, want %+v", got[0][0], want)
	}
}
