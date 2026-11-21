package security

import "testing"

// TestClassifySandboxEnforcement 覆盖两个信号的所有组合：沙箱未参与时返回零值，
// 完整执行返回 full，任一维度被降级返回 partial。
func TestClassifySandboxEnforcement(t *testing.T) {
	tests := []struct {
		name     string
		active   bool
		degraded bool
		want     SandboxEnforcement
	}{
		{"沙箱未参与", false, false, ""},
		{"未参与但标记降级仍为零值", false, true, ""},
		{"完整执行", true, false, SandboxEnforcementFull},
		{"降级执行", true, true, SandboxEnforcementPartial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifySandboxEnforcement(tt.active, tt.degraded)
			if got != tt.want {
				t.Errorf("ClassifySandboxEnforcement(%v, %v) = %q, want %q", tt.active, tt.degraded, got, tt.want)
			}
		})
	}
}

// TestSandboxEnforcementIsFull 验证只有确证的 full 才被视为完整，
// 零值与未知值一律保守地视为非完整。
func TestSandboxEnforcementIsFull(t *testing.T) {
	tests := []struct {
		name string
		in   SandboxEnforcement
		want bool
	}{
		{"full", SandboxEnforcementFull, true},
		{"partial", SandboxEnforcementPartial, false},
		{"零值", "", false},
		{"未知值", "bogus", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.IsFull(); got != tt.want {
				t.Errorf("%q.IsFull() = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestClassifySandboxDenial 覆盖空值、三类已知拒绝、unknown 字面值与任意未知词：
// 未知词必须归为 unknown，绝不能静默变成"无拒绝"。
func TestClassifySandboxDenial(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want SandboxDenial
	}{
		{"空值为无拒绝", "", SandboxDenialNone},
		{"文件系统", sandboxViolationFileSystem, SandboxDenialFileSystem},
		{"网络", sandboxViolationNetwork, SandboxDenialNetwork},
		{"权限", sandboxViolationPermission, SandboxDenialPermission},
		{"unknown 字面值", "unknown", SandboxDenialUnknown},
		{"未知词归为 unknown", "quota-exceeded", SandboxDenialUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifySandboxDenial(tt.kind)
			if got != tt.want {
				t.Errorf("ClassifySandboxDenial(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// TestSandboxDenialDeniedAndString 验证拒绝判定与上报字符串：
// 零值不表示拒绝且上报为 "none"，其余所有取值都表示拒绝。
func TestSandboxDenialDeniedAndString(t *testing.T) {
	tests := []struct {
		name       string
		in         SandboxDenial
		wantDenied bool
		wantString string
	}{
		{"零值", SandboxDenialNone, false, "none"},
		{"文件系统", SandboxDenialFileSystem, true, "file-system"},
		{"网络", SandboxDenialNetwork, true, "network"},
		{"权限", SandboxDenialPermission, true, "permission"},
		{"unknown", SandboxDenialUnknown, true, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Denied(); got != tt.wantDenied {
				t.Errorf("%q.Denied() = %v, want %v", tt.in, got, tt.wantDenied)
			}
			if got := tt.in.String(); got != tt.wantString {
				t.Errorf("%q.String() = %q, want %q", tt.in, got, tt.wantString)
			}
		})
	}
}

// TestClassifySandboxFailClosed 覆盖失败即拒绝判定的完整真值表：
// 有未决信号时只看是否发生拒绝；无未决信号时只看执行强度是否为 full。
func TestClassifySandboxFailClosed(t *testing.T) {
	tests := []struct {
		name        string
		enforcement SandboxEnforcement
		denial      SandboxDenial
		unresolved  bool
		want        bool
	}{
		{"完整执行无未决信号", SandboxEnforcementFull, SandboxDenialNone, false, true},
		{"完整执行且确证拒绝", SandboxEnforcementFull, SandboxDenialFileSystem, false, true},
		{"部分执行无未决信号", SandboxEnforcementPartial, SandboxDenialNone, false, false},
		{"零值无未决信号", "", SandboxDenialNone, false, false},
		{"完整执行有未决但无拒绝=失败即通过", SandboxEnforcementFull, SandboxDenialNone, true, false},
		{"部分执行有未决且拒绝", SandboxEnforcementPartial, SandboxDenialNetwork, true, true},
		{"部分执行有未决但无拒绝", SandboxEnforcementPartial, SandboxDenialNone, true, false},
		{"未知拒绝在有未决时也算失败即拒绝", SandboxEnforcementPartial, SandboxDenialUnknown, true, true},
		{"零值有未决且拒绝", "", SandboxDenialPermission, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifySandboxFailClosed(tt.enforcement, tt.denial, tt.unresolved)
			if got != tt.want {
				t.Errorf("ClassifySandboxFailClosed(%q, %q, %v) = %v, want %v",
					tt.enforcement, tt.denial, tt.unresolved, got, tt.want)
			}
		})
	}
}

// TestDescribeSandboxExecution 用端到端信号组合验证归约结果：
// 沙箱未参与时保持零值；各维度独立生效；未知违规类别被提升为 unknown。
func TestDescribeSandboxExecution(t *testing.T) {
	tests := []struct {
		name       string
		active     bool
		degraded   bool
		unresolved bool
		kind       string
		want       SandboxExecution
	}{
		{
			name: "沙箱未参与保持零值",
			want: SandboxExecution{},
		},
		{
			name:   "完整执行无拒绝",
			active: true,
			want:   SandboxExecution{Enforcement: SandboxEnforcementFull, FailClosed: true},
		},
		{
			name:       "降级执行且网络拒绝有未决",
			active:     true,
			degraded:   true,
			unresolved: true,
			kind:       sandboxViolationNetwork,
			want: SandboxExecution{
				Enforcement: SandboxEnforcementPartial,
				Denial:      SandboxDenialNetwork,
				FailClosed:  true,
			},
		},
		{
			name:   "完整执行遇到未知类别",
			active: true,
			kind:   "something-new",
			want: SandboxExecution{
				Enforcement: SandboxEnforcementFull,
				Denial:      SandboxDenialUnknown,
				FailClosed:  true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DescribeSandboxExecution(tt.active, tt.degraded, tt.unresolved, tt.kind)
			if got != tt.want {
				t.Errorf("DescribeSandboxExecution() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestSandboxExecutionReport 验证上报结构 faithfully 映射三个维度，
// 且零值描述上报为稳定的 "none"/空串，不产生副作用。
func TestSandboxExecutionReport(t *testing.T) {
	tests := []struct {
		name string
		in   SandboxExecution
		want SandboxExecutionReport
	}{
		{
			name: "零值",
			in:   SandboxExecution{},
			want: SandboxExecutionReport{Enforcement: "", Denial: "none", FailClosed: false},
		},
		{
			name: "部分执行权限拒绝失败即拒绝",
			in: SandboxExecution{
				Enforcement: SandboxEnforcementPartial,
				Denial:      SandboxDenialPermission,
				FailClosed:  true,
			},
			want: SandboxExecutionReport{Enforcement: "partial", Denial: "permission", FailClosed: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Report(); got != tt.want {
				t.Errorf("Report() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
