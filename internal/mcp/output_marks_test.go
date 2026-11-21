package mcp

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// TestRenderMarks 表驱动覆盖渲染原语：全零返回空串、各标记单独渲染、
// 全部设置时按固定顺序排列，以及 CJK 路径保持完整。
func TestRenderMarks(t *testing.T) {
	tests := []struct {
		name string
		m    OutputMarks
		want string
	}{
		{"全零返回空串", OutputMarks{}, ""},
		{"退出码为零不渲染", OutputMarks{ExitCode: 0}, ""},
		{"仅非零退出码", OutputMarks{ExitCode: 7}, "[exit code: 7]"},
		{"负退出码", OutputMarks{ExitCode: -1}, "[exit code: -1]"},
		{"仅超时", OutputMarks{TimedOut: true}, "[timed out]"},
		{"仅信号", OutputMarks{Signal: "SIGKILL"}, "[killed by signal: SIGKILL]"},
		{"仅截断", OutputMarks{Truncated: true}, "[truncated]"},
		{"仅落盘路径", OutputMarks{SpillPath: "/tmp/out.txt"}, "[full output: /tmp/out.txt]"},
		{
			name: "全部设置按稳定顺序",
			m: OutputMarks{
				ExitCode: 7, TimedOut: true, Signal: "SIGTERM",
				Truncated: true, SpillPath: "/tmp/o",
			},
			want: "[exit code: 7]\n[timed out]\n[killed by signal: SIGTERM]\n[truncated]\n[full output: /tmp/o]",
		},
		{
			name: "CJK 落盘路径保持完整",
			m:    OutputMarks{SpillPath: "产物/完整输出.txt"},
			want: "[full output: 产物/完整输出.txt]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderMarks(tt.m)
			if got != tt.want {
				t.Errorf("RenderMarks(%+v) = %q, want %q", tt.m, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("RenderMarks(%+v) 产出非法 UTF-8: %q", tt.m, got)
			}
		})
	}
}

// TestRenderMarksStableOrder 验证字段赋值顺序不影响渲染结果，标记始终
// 按退出码 → 超时 → 信号 → 截断 → 落盘路径排列。
func TestRenderMarksStableOrder(t *testing.T) {
	a := OutputMarks{ExitCode: 7, TimedOut: true, Signal: "SIGTERM", Truncated: true, SpillPath: "/tmp/o"}
	b := OutputMarks{SpillPath: "/tmp/o", Truncated: true, Signal: "SIGTERM", TimedOut: true, ExitCode: 7}
	if got, want := RenderMarks(a), RenderMarks(b); got != want {
		t.Fatalf("字段赋值顺序影响渲染: %q != %q", got, want)
	}

	want := "[exit code: 7]\n[timed out]\n[killed by signal: SIGTERM]\n[truncated]\n[full output: /tmp/o]"
	for i := 0; i < 50; i++ {
		if got := RenderMarks(a); got != want {
			t.Fatalf("第 %d 次渲染不稳定: got %q, want %q", i, got, want)
		}
	}
}

// TestParseMarks 表驱动覆盖解析原语：各种标记、乱序/混杂文本、
// 带时长的超时写法、畸形输入容错，以及纯 CJK 文本不产生误判。
func TestParseMarks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want OutputMarks
	}{
		{"空串", "", OutputMarks{}},
		{"纯 CJK 文本无误判", "这是一段普通的中文输出，没有任何标记。", OutputMarks{}},
		{"仅退出码", "[exit code: 7]", OutputMarks{ExitCode: 7}},
		{"负退出码", "[exit code: -3]", OutputMarks{ExitCode: -3}},
		{"仅超时", "[timed out]", OutputMarks{TimedOut: true}},
		{"带时长的超时", "[timed out after 5s]", OutputMarks{TimedOut: true}},
		{"仅信号", "[killed by signal: SIGKILL]", OutputMarks{Signal: "SIGKILL"}},
		{"仅截断", "[truncated]", OutputMarks{Truncated: true}},
		{"仅落盘路径", "[full output: /tmp/out.txt]", OutputMarks{SpillPath: "/tmp/out.txt"}},
		{
			name: "全部标记乱序混杂文本",
			in:   "前置输出\n[truncated]\n[full output: /tmp/o]\n[exit code: 7]\n[killed by signal: SIGTERM]\n[timed out]\n后置",
			want: OutputMarks{
				ExitCode: 7, TimedOut: true, Signal: "SIGTERM",
				Truncated: true, SpillPath: "/tmp/o",
			},
		},
		{"CJK 落盘路径完整", "[full output: 产物/完整输出.txt]", OutputMarks{SpillPath: "产物/完整输出.txt"}},
		{"畸形退出码忽略", "[exit code: abc]", OutputMarks{}},
		{"未闭合标记忽略", "[exit code: 7", OutputMarks{}},
		{"空退出码忽略", "[exit code: ]", OutputMarks{}},
		{"取第一个合法退出码", "[exit code: 1] ... [exit code: 2]", OutputMarks{ExitCode: 1}},
		{"CJK 文本围绕标记", "中文[truncated]中文[exit code: 4]结尾", OutputMarks{ExitCode: 4, Truncated: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMarks(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseMarks(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestMarksRoundTrip 验证 RenderMarks 与 ParseMarks 互逆：
// 任意标记组合渲染后再解析应完全还原，含 CJK 路径与负退出码。
func TestMarksRoundTrip(t *testing.T) {
	tests := []OutputMarks{
		{},
		{ExitCode: 7},
		{ExitCode: -9},
		{TimedOut: true},
		{Signal: "SIGKILL"},
		{Truncated: true},
		{SpillPath: "/tmp/out.txt"},
		{SpillPath: "产物/目录/完整输出.txt"},
		{ExitCode: 42, TimedOut: true, Signal: "SIGTERM", Truncated: true, SpillPath: "产物/o.bin"},
	}

	for _, want := range tests {
		t.Run(RenderMarks(want), func(t *testing.T) {
			got := ParseMarks(RenderMarks(want))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("round-trip: %+v -> %q -> %+v", want, RenderMarks(want), got)
			}
		})
	}
}

// TestMarksDoNotMutateInputs 验证两个原语均为纯函数：渲染不修改传入的
// OutputMarks，解析不修改传入的字符串。
func TestMarksDoNotMutateInputs(t *testing.T) {
	m := OutputMarks{ExitCode: 7, TimedOut: true, Signal: "SIGTERM", Truncated: true, SpillPath: "产物/o"}
	before := m
	_ = RenderMarks(m)
	if m != before {
		t.Errorf("RenderMarks 修改了入参: got %+v, want %+v", m, before)
	}

	s := "中文[exit code: 7][truncated]尾"
	beforeS := s
	_ = ParseMarks(s)
	if s != beforeS {
		t.Errorf("ParseMarks 修改了入参: got %q, want %q", s, beforeS)
	}
}
