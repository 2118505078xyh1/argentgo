package tui

import (
	"testing"
	"unicode"
	"unicode/utf8"
)

// assertSignal 比较投影结果的三个字段，便于表驱动用例给出可读的失败信息。
func assertSignal(t *testing.T, got, want RenderSignal) {
	t.Helper()
	if got != want {
		t.Fatalf("signal = %+v, want %+v", got, want)
	}
}

// TestProjectRenderPhases 表驱动覆盖每个 TurnPhase 的映射：空闲（有/无
// 历史消息）、流式输出、等待工具、作答以及未知阶段的保守回退。每个用例
// 同时确认结果是确定性的（重复调用一致）且入参状态未被修改。
func TestProjectRenderPhases(t *testing.T) {
	tests := []struct {
		name      string
		state     TurnState
		itemCount int
		want      RenderSignal
	}{
		{
			name:      "idle with history accepts input, no spinner",
			state:     TurnState{Phase: TurnIdle},
			itemCount: 5,
			want:      RenderSignal{AcceptInput: true, StatusText: statusReady},
		},
		{
			name:      "idle without history shows welcome hint",
			state:     TurnState{},
			itemCount: 0,
			want:      RenderSignal{AcceptInput: true, StatusText: statusWelcome},
		},
		{
			name:      "negative item count treated as empty",
			state:     TurnState{Phase: TurnIdle},
			itemCount: -3,
			want:      RenderSignal{AcceptInput: true, StatusText: statusWelcome},
		},
		{
			name:      "thinking spins and blocks input",
			state:     TurnState{Phase: TurnThinking},
			itemCount: 2,
			want:      RenderSignal{ShowSpinner: true, StatusText: statusThinking},
		},
		{
			name:      "tool running spins and blocks input",
			state:     TurnState{Phase: TurnToolRunning},
			itemCount: 2,
			want:      RenderSignal{ShowSpinner: true, StatusText: statusToolRunning},
		},
		{
			name:      "answering spins and blocks input",
			state:     TurnState{Phase: TurnAnswering},
			itemCount: 2,
			want:      RenderSignal{ShowSpinner: true, StatusText: statusAnswering},
		},
		{
			name:      "unknown phase falls back to active",
			state:     TurnState{Phase: TurnPhase(99)},
			itemCount: 2,
			want:      RenderSignal{ShowSpinner: true, StatusText: statusThinking},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.state
			got := ProjectRender(tt.state, false, tt.itemCount)
			assertSignal(t, got, tt.want)
			// 纯函数：重复调用结果一致，且不修改入参状态。
			assertSignal(t, ProjectRender(tt.state, false, tt.itemCount), got)
			if tt.state != before {
				t.Fatalf("state mutated: got %+v, before %+v", tt.state, before)
			}
		})
	}
}

// TestProjectRenderApprovalOverridesPhase 确认 hasPendingApproval 优先于
// 阶段与消息数：任何阶段、任何 itemCount 下都只显示审批提示，不转
// spinner、不接受文本输入。
func TestProjectRenderApprovalOverridesPhase(t *testing.T) {
	phases := []TurnPhase{TurnIdle, TurnThinking, TurnToolRunning, TurnAnswering, TurnPhase(99)}
	counts := []int{0, 7}
	want := RenderSignal{StatusText: statusApproval}
	for _, phase := range phases {
		for _, count := range counts {
			got := ProjectRender(TurnState{Phase: phase}, true, count)
			if got != want {
				t.Errorf("phase %s, count %d: signal = %+v, want %+v", phase, count, got, want)
			}
		}
	}
}

// TestProjectRenderCJKStatusText 锁定文案契约：每个阶段的 StatusText 都是
// 非空、合法的 UTF-8，且含汉字（后续迁入 locale 表时应保持中文）。
func TestProjectRenderCJKStatusText(t *testing.T) {
	cases := []struct {
		name string
		got  RenderSignal
	}{
		{"ready", ProjectRender(TurnState{Phase: TurnIdle}, false, 1)},
		{"welcome", ProjectRender(TurnState{}, false, 0)},
		{"thinking", ProjectRender(TurnState{Phase: TurnThinking}, false, 0)},
		{"tool running", ProjectRender(TurnState{Phase: TurnToolRunning}, false, 0)},
		{"answering", ProjectRender(TurnState{Phase: TurnAnswering}, false, 0)},
		{"approval", ProjectRender(TurnState{}, true, 0)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.got.StatusText
			if s == "" {
				t.Fatal("StatusText must not be empty")
			}
			if !utf8.ValidString(s) {
				t.Fatalf("StatusText %q is not valid UTF-8", s)
			}
			hasHan := false
			for _, r := range s {
				if unicode.Is(unicode.Han, r) {
					hasHan = true
					break
				}
			}
			if !hasHan {
				t.Fatalf("StatusText %q must contain a Han character", s)
			}
		})
	}
}

// TestMergeSignals 表驱动覆盖"primary 非空字段优先、缺失字段回退"的合
// 并语义，包括零值退化、逐字段回退，以及 bool false 被视为缺失而回退的
// 刻意约定。
func TestMergeSignals(t *testing.T) {
	fallback := RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "备"}
	primary := RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "主"}

	tests := []struct {
		name     string
		primary  RenderSignal
		fallback RenderSignal
		want     RenderSignal
	}{
		{
			name:     "zero primary yields fallback",
			primary:  RenderSignal{},
			fallback: fallback,
			want:     fallback,
		},
		{
			name:     "full primary yields primary",
			primary:  primary,
			fallback: fallback,
			want:     primary,
		},
		{
			name:     "zero fallback yields primary",
			primary:  primary,
			fallback: RenderSignal{},
			want:     primary,
		},
		{
			name:     "both zero yields zero",
			primary:  RenderSignal{},
			fallback: RenderSignal{},
			want:     RenderSignal{},
		},
		{
			name:     "missing status falls back, bools kept",
			primary:  RenderSignal{ShowSpinner: true, AcceptInput: true},
			fallback: fallback,
			want:     RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "备"},
		},
		{
			name:     "primary status wins, bools fall back",
			primary:  RenderSignal{StatusText: "主"},
			fallback: fallback,
			want:     RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "主"},
		},
		{
			name:     "spinner true kept, other fields fall back",
			primary:  RenderSignal{ShowSpinner: true},
			fallback: RenderSignal{ShowSpinner: false, AcceptInput: true, StatusText: "备"},
			want:     RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "备"},
		},
		{
			name:     "accept input true kept, other fields fall back",
			primary:  RenderSignal{AcceptInput: true},
			fallback: RenderSignal{ShowSpinner: true, AcceptInput: false, StatusText: "备"},
			want:     RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "备"},
		},
		{
			name:     "bool false is missing and falls back",
			primary:  RenderSignal{StatusText: "主"},
			fallback: RenderSignal{ShowSpinner: true, AcceptInput: false},
			want:     RenderSignal{ShowSpinner: true, AcceptInput: false, StatusText: "主"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pBefore, fBefore := tt.primary, tt.fallback
			assertSignal(t, MergeSignals(tt.primary, tt.fallback), tt.want)
			// 纯函数：两个入参都不得被修改。
			if tt.primary != pBefore || tt.fallback != fBefore {
				t.Fatalf("input mutated: primary=%+v fallback=%+v", tt.primary, tt.fallback)
			}
		})
	}
}

// TestProjectRenderMergeWithLegacyWorking 还原预期用法：新状态缺失（零值
// primary）时回退到由旧 working 布尔构造的备用信号；新状态到位后则由
// 投影结果提供文案与输入许可。
func TestProjectRenderMergeWithLegacyWorking(t *testing.T) {
	legacyWorking := RenderSignal{ShowSpinner: true, AcceptInput: false, StatusText: statusThinking}

	// 新状态尚未到达：完全回退旧 working 信号。
	assertSignal(t, MergeSignals(RenderSignal{}, legacyWorking), legacyWorking)

	// 新状态已到达且为空闲：文案与输入许可取新值，spinner 因 primary
	// 的 false 被视为缺失而回退——这正是逐字段合并的既有约定。
	got := MergeSignals(ProjectRender(TurnState{Phase: TurnIdle}, false, 3), legacyWorking)
	assertSignal(t, got, RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: statusReady})
}
