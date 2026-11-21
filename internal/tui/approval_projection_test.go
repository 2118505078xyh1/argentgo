package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// cloneApprovalViews 复制视图切片并保留 nil/非 nil 的区别，便于纯函数断言。
func cloneApprovalViews(items []ApprovalView) []ApprovalView {
	if items == nil {
		return nil
	}
	out := make([]ApprovalView, len(items))
	copy(out, items)
	return out
}

// assertApprovalLineValid 断言一行文本是合法 UTF-8（没有半个多字节序列）、
// 不含换行控制符，且单独一行可安全渲染。
func assertApprovalLineValid(t *testing.T, line string) {
	t.Helper()
	if !utf8.ValidString(line) {
		t.Fatalf("line %q is not valid UTF-8", line)
	}
	for _, r := range line {
		if r == utf8.RuneError {
			t.Fatalf("line %q contains a broken UTF-8 rune", line)
		}
	}
	if strings.ContainsAny(line, "\r\n") {
		t.Fatalf("line %q is not single-line", line)
	}
}

// TestProjectApprovalsSortsBySeq 锁定排序契约：按 Seq 升序，Seq 相同时
// 由稳定排序保持原先后顺序；空输入返回 nil。
func TestProjectApprovalsSortsBySeq(t *testing.T) {
	tests := []struct {
		name  string
		items []ApprovalView
		want  []ApprovalView
	}{
		{
			name:  "nil input",
			items: nil,
			want:  nil,
		},
		{
			name:  "empty input",
			items: []ApprovalView{},
			want:  nil,
		},
		{
			name:  "single item",
			items: []ApprovalView{{Name: "only", Seq: 7}},
			want:  []ApprovalView{{Name: "only", Seq: 7}},
		},
		{
			name: "unsorted seqs",
			items: []ApprovalView{
				{Name: "c", Seq: 9},
				{Name: "a", Seq: 1},
				{Name: "b", Seq: 5},
			},
			want: []ApprovalView{
				{Name: "a", Seq: 1},
				{Name: "b", Seq: 5},
				{Name: "c", Seq: 9},
			},
		},
		{
			name: "equal seqs keep input order",
			items: []ApprovalView{
				{Name: "first", Args: "1", Seq: 2},
				{Name: "second", Args: "2", Seq: 1},
				{Name: "third", Args: "3", Seq: 2},
				{Name: "fourth", Args: "4", Seq: 1},
			},
			want: []ApprovalView{
				{Name: "second", Args: "2", Seq: 1},
				{Name: "fourth", Args: "4", Seq: 1},
				{Name: "first", Args: "1", Seq: 2},
				{Name: "third", Args: "3", Seq: 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneApprovalViews(tt.items)
			got := ProjectApprovals(tt.items, -1)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ProjectApprovals = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.items, before) {
				t.Fatalf("input mutated: got %+v, before %+v", tt.items, before)
			}
		})
	}
}

// TestProjectApprovalsPromotesSelected 锁定 dock 聚焦契约：selected 指向的
// 项被提升到首位，其余项保持按 Seq 排序后的相对顺序。
func TestProjectApprovalsPromotesSelected(t *testing.T) {
	tests := []struct {
		name     string
		items    []ApprovalView
		selected int
		want     []ApprovalView
	}{
		{
			name: "promote middle item",
			items: []ApprovalView{
				{Name: "c", Seq: 3},
				{Name: "a", Seq: 1},
				{Name: "b", Seq: 2},
			},
			selected: 0, // items[0] 是 "c"
			want: []ApprovalView{
				{Name: "c", Seq: 3},
				{Name: "a", Seq: 1},
				{Name: "b", Seq: 2},
			},
		},
		{
			name: "promote last input item to front",
			items: []ApprovalView{
				{Name: "b", Seq: 2},
				{Name: "c", Seq: 3},
				{Name: "a", Seq: 1},
			},
			selected: 2, // items[2] 是 "a"，排序后本在首位
			want: []ApprovalView{
				{Name: "a", Seq: 1},
				{Name: "b", Seq: 2},
				{Name: "c", Seq: 3},
			},
		},
		{
			name: "promote first input item past sorted head",
			items: []ApprovalView{
				{Name: "z", Seq: 9},
				{Name: "a", Seq: 1},
				{Name: "m", Seq: 5},
			},
			selected: 0, // items[0] 是 "z"，排序后本在末位
			want: []ApprovalView{
				{Name: "z", Seq: 9},
				{Name: "a", Seq: 1},
				{Name: "m", Seq: 5},
			},
		},
		{
			name: "equal seqs promote by index not by value",
			items: []ApprovalView{
				{Name: "dup", Args: "first", Seq: 2},  // items[0]
				{Name: "dup", Args: "second", Seq: 2}, // items[1]
				{Name: "other", Seq: 1},
			},
			selected: 1,
			want: []ApprovalView{
				{Name: "dup", Args: "second", Seq: 2}, // 提升的必须是 items[1]
				{Name: "other", Seq: 1},
				{Name: "dup", Args: "first", Seq: 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneApprovalViews(tt.items)
			got := ProjectApprovals(tt.items, tt.selected)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ProjectApprovals(selected=%d) = %+v, want %+v", tt.selected, got, tt.want)
			}
			if !reflect.DeepEqual(tt.items, before) {
				t.Fatalf("input mutated: got %+v, before %+v", tt.items, before)
			}
		})
	}
}

// TestProjectApprovalsOutOfRangeSelected 锁定越界契约：selected 越界时只做
// 排序、不提升，且结果与传入负数/超大正数无关。
func TestProjectApprovalsOutOfRangeSelected(t *testing.T) {
	items := []ApprovalView{
		{Name: "c", Seq: 3},
		{Name: "a", Seq: 1},
		{Name: "b", Seq: 2},
	}
	want := []ApprovalView{
		{Name: "a", Seq: 1},
		{Name: "b", Seq: 2},
		{Name: "c", Seq: 3},
	}
	for _, selected := range []int{-1, -100, len(items), len(items) + 5, 1 << 30} {
		t.Run("selected="+approvalItoa(selected), func(t *testing.T) {
			before := cloneApprovalViews(items)
			got := ProjectApprovals(items, selected)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ProjectApprovals(selected=%d) = %+v, want %+v", selected, got, want)
			}
			if !reflect.DeepEqual(items, before) {
				t.Fatalf("input mutated at selected=%d", selected)
			}
		})
	}
}

// approvalItoa 是测试里把小整数转成子测试名的辅助函数，避免为本包引入 fmt 依赖。
func approvalItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestProjectApprovalsNoAliasing 锁定返回值与入参不共享底层数组：改动结果
// 不得回写入参（即使入参之后被排序过）。
func TestProjectApprovalsNoAliasing(t *testing.T) {
	items := []ApprovalView{
		{Name: "b", Seq: 2},
		{Name: "a", Seq: 1},
	}
	before := cloneApprovalViews(items)
	got := ProjectApprovals(items, 0)
	got[0].Name = "MUTATED"
	got[1].Seq = 999
	if !reflect.DeepEqual(items, before) {
		t.Fatalf("mutating result changed input: got %+v, before %+v", items, before)
	}
}

// TestApprovalLineFormat 表驱动锁定单行格式：光标、危险标记、名称、参数、
// 原因各段的拼接方式与空白折叠。
func TestApprovalLineFormat(t *testing.T) {
	tests := []struct {
		name     string
		view     ApprovalView
		selected bool
		want     string
	}{
		{
			name: "zero value keeps cursor width",
			view: ApprovalView{},
			want: approvalCursorIdle,
		},
		{
			name:     "selected zero value",
			view:     ApprovalView{},
			selected: true,
			want:     approvalCursorSelected,
		},
		{
			name: "name only",
			view: ApprovalView{Name: "bash"},
			want: "  bash",
		},
		{
			name:     "selected name and args",
			view:     ApprovalView{Name: "bash", Args: "ls -la"},
			selected: true,
			want:     "> bash ls -la",
		},
		{
			name: "danger mark only when dangerous",
			view: ApprovalView{Name: "bash", Args: "sudo rm -rf /", Danger: true},
			want: "  ⚠ bash sudo rm -rf /",
		},
		{
			name:     "selected danger with reason",
			view:     ApprovalView{Name: "bash", Danger: true, Reason: "会删除根目录"},
			selected: true,
			want:     "> ⚠ bash — 会删除根目录",
		},
		{
			name: "reason without args",
			view: ApprovalView{Name: "edit", Reason: "mutating file"},
			want: "  edit — mutating file",
		},
		{
			name: "whitespace in every field collapses to one line",
			view: ApprovalView{
				Name:   "  read\n file ",
				Args:   "{\"path\":\n\"a.txt\"}",
				Reason: "拒绝\t理由\n二",
				Danger: true,
			},
			want: "  ⚠ read file {\"path\": \"a.txt\"} — 拒绝 理由 二",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.view
			got := ApprovalLine(tt.view, tt.selected)
			if got != tt.want {
				t.Fatalf("ApprovalLine = %q, want %q", got, tt.want)
			}
			assertApprovalLineValid(t, got)
			if tt.view != before {
				t.Fatalf("input view mutated: got %+v, before %+v", tt.view, before)
			}
		})
	}
}

// TestApprovalLineDangerMark 确认危险标记的存在完全由 Danger 决定，
// 与 selected、Reason 等字段无关。
func TestApprovalLineDangerMark(t *testing.T) {
	for _, selected := range []bool{false, true} {
		for _, reason := range []string{"", "理由"} {
			v := ApprovalView{Name: "bash", Reason: reason, Danger: true}
			if line := ApprovalLine(v, selected); !strings.Contains(line, approvalDangerMark) {
				t.Fatalf("danger line %q missing marker", line)
			}
			v.Danger = false
			if line := ApprovalLine(v, selected); strings.Contains(line, approvalDangerMark) {
				t.Fatalf("ordinary line %q unexpectedly has danger marker", line)
			}
		}
	}
}

// TestApprovalLineCJKEmojiSafe 用 CJK、emoji 与 ZWJ 组合序列验证所有字段
// 都不在截断或拼接时被切坏：结果是合法 UTF-8 且不含替换字符。
func TestApprovalLineCJKEmojiSafe(t *testing.T) {
	tests := []struct {
		name     string
		view     ApprovalView
		selected bool
		want     string
	}{
		{
			name:     "CJK name args reason",
			view:     ApprovalView{Name: "你好", Args: "参数😀", Reason: "原因🚀"},
			selected: true,
			want:     "> 你好 参数😀 — 原因🚀",
		},
		{
			name: "ZWJ emoji stays intact",
			view: ApprovalView{
				Name:   "工具",
				Args:   "👨‍👩‍👧",
				Reason: "拒绝👨‍👩‍👧组合",
			},
			want: "  工具 👨‍👩‍👧 — 拒绝👨‍👩‍👧组合",
		},
		{
			name: "long CJK reason truncated at grapheme boundary",
			view: ApprovalView{Name: "删除", Reason: strings.Repeat("字", 30)},
			want: "  删除 — " + strings.Repeat("字", (approvalMaxReasonCells-1)/2) + "…",
		},
		{
			name: "long emoji reason truncated at grapheme boundary",
			view: ApprovalView{Name: "删除", Reason: strings.Repeat("😀", 25)},
			want: "  删除 — " + strings.Repeat("😀", (approvalMaxReasonCells-1)/2) + "…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApprovalLine(tt.view, tt.selected)
			if got != tt.want {
				t.Fatalf("ApprovalLine = %q, want %q", got, tt.want)
			}
			assertApprovalLineValid(t, got)
		})
	}
}

// TestApprovalLineReasonTruncation 锁定 reason 的有界截断：ASCII 长文本按
// 单元格上限截断并带省略号；恰好等于上限时不截断；未超限时逐字节保留。
func TestApprovalLineReasonTruncation(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{
			name:   "short reason unchanged",
			reason: "short reason",
			want:   "  bash — short reason",
		},
		{
			name:   "exactly at limit unchanged",
			reason: strings.Repeat("a", approvalMaxReasonCells),
			want:   "  bash — " + strings.Repeat("a", approvalMaxReasonCells),
		},
		{
			name:   "long reason truncated",
			reason: strings.Repeat("a", approvalMaxReasonCells+20),
			want:   "  bash — " + strings.Repeat("a", approvalMaxReasonCells-1) + "…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApprovalLine(ApprovalView{Name: "bash", Reason: tt.reason}, false)
			if got != tt.want {
				t.Fatalf("ApprovalLine = %q, want %q", got, tt.want)
			}
			assertApprovalLineValid(t, got)
		})
	}
}

// TestApprovalLineSelectedOnlyChangesCursor 确认 selected 只改光标前缀，
// 其余文案逐字节一致。
func TestApprovalLineSelectedOnlyChangesCursor(t *testing.T) {
	v := ApprovalView{Name: "bash", Args: "ls", Reason: "理由", Danger: true, Seq: 3}
	idle := ApprovalLine(v, false)
	selected := ApprovalLine(v, true)
	if !strings.HasPrefix(idle, approvalCursorIdle) || !strings.HasPrefix(selected, approvalCursorSelected) {
		t.Fatalf("wrong cursors: idle=%q selected=%q", idle, selected)
	}
	if idle[len(approvalCursorIdle):] != selected[len(approvalCursorSelected):] {
		t.Fatalf("selected changed content beyond cursor: idle=%q selected=%q", idle, selected)
	}
}

// TestApprovalViewZeroValue 锁定零值契约：零值视图排序与渲染都稳定且惰性。
func TestApprovalViewZeroValue(t *testing.T) {
	var v ApprovalView
	if v.Name != "" || v.Args != "" || v.Reason != "" || v.Danger || v.Seq != 0 {
		t.Fatalf("zero ApprovalView is not zero: %+v", v)
	}
	if got := ProjectApprovals(nil, 0); got != nil {
		t.Fatalf("ProjectApprovals(nil) = %+v, want nil", got)
	}
	if got := ApprovalLine(v, false); got != approvalCursorIdle {
		t.Fatalf("zero ApprovalLine = %q, want %q", got, approvalCursorIdle)
	}
}
