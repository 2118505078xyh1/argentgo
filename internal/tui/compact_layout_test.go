package tui

import "testing"

// TestShouldUseCompactLayoutThresholds 覆盖宽高两个维度的阈值判定：
// 低于阈值进入紧凑，达到或超过阈值保持常规布局。
func TestShouldUseCompactLayoutThresholds(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		want   bool
	}{
		{"width just below threshold", 119, 40, true},
		{"width at threshold", 120, 40, false},
		{"width above threshold", 121, 40, false},
		{"height just below threshold", 160, 29, true},
		{"height at threshold", 160, 30, false},
		{"height above threshold", 160, 31, false},
		{"both far above threshold", 200, 50, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseCompactLayout(tt.width, tt.height); got != tt.want {
				t.Errorf("shouldUseCompactLayout(%d, %d) = %v, want %v", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

// TestShouldUseCompactLayoutBoundaries 精确锁定 119/120 与 29/30 两组边界，
// 确保比较使用严格小于而非小于等于。
func TestShouldUseCompactLayoutBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		want   bool
	}{
		{"119x30 width boundary is compact", 119, 30, true},
		{"120x30 both boundaries are non compact", 120, 30, false},
		{"120x29 height boundary is compact", 120, 29, true},
		{"119x29 both boundaries are compact", 119, 29, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseCompactLayout(tt.width, tt.height); got != tt.want {
				t.Errorf("shouldUseCompactLayout(%d, %d) = %v, want %v", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

// TestShouldUseCompactLayoutCombinations 验证“宽或高任一过小即紧凑”的
// 或语义：仅宽小、仅高小都为 true，仅两者都达标才为 false。
func TestShouldUseCompactLayoutCombinations(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		want   bool
	}{
		{"small width large height", 80, 60, true},
		{"large width small height", 200, 20, true},
		{"small width small height", 80, 20, true},
		{"large width large height", 200, 60, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseCompactLayout(tt.width, tt.height); got != tt.want {
				t.Errorf("shouldUseCompactLayout(%d, %d) = %v, want %v", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

// TestShouldUseCompactLayoutNonPositive 覆盖零值与负值：
// 它们自然落入“小终端”一侧，返回 true，无需调用方归一化。
func TestShouldUseCompactLayoutNonPositive(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
	}{
		{"zero width", 0, 40},
		{"zero height", 160, 0},
		{"both zero", 0, 0},
		{"negative width", -1, 40},
		{"negative height", 160, -1},
		{"both negative", -10, -10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !shouldUseCompactLayout(tt.width, tt.height) {
				t.Errorf("shouldUseCompactLayout(%d, %d) = false, want true for non-positive size", tt.width, tt.height)
			}
		})
	}
}

// TestCompactLayoutOptInDefaultsOff 锁定 opt-in 开关默认关闭：
// 未显式打开时，即使尺寸很小也不隐藏侧栏，从而保持现有布局不变。
func TestCompactLayoutOptInDefaultsOff(t *testing.T) {
	if CompactLayout {
		t.Fatal("CompactLayout should default to false")
	}
	if panelsHiddenInCompactLayout(40, 20) {
		t.Fatal("panels should stay visible while opt-in switch is off")
	}
}

// TestPanelsHiddenInCompactLayout 验证接线辅助函数：开关关闭时恒为 false；
// 开关打开后仅在紧凑尺寸隐藏侧栏，常规尺寸仍保持可见。
func TestPanelsHiddenInCompactLayout(t *testing.T) {
	original := CompactLayout
	defer func() { CompactLayout = original }()

	CompactLayout = true
	tests := []struct {
		name   string
		width  int
		height int
		want   bool
	}{
		{"compact size hides panels", 100, 40, true},
		{"short size hides panels", 160, 24, true},
		{"non compact size keeps panels", 160, 40, false},
		{"zero size hides panels", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panelsHiddenInCompactLayout(tt.width, tt.height); got != tt.want {
				t.Errorf("panelsHiddenInCompactLayout(%d, %d) = %v, want %v", tt.width, tt.height, got, tt.want)
			}
		})
	}
}
