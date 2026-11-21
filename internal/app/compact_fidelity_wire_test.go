package app

import (
	"strings"
	"testing"
)

// fidelitySummary 按 Summarize 产出的 markdown 结构拼一份压缩摘要。四个布尔值分别
// 控制目标、改动文件、失败、下一步章节是否出现，便于精确构造要素缺失场景。
func fidelitySummary(goal, files, failures, next bool) string {
	var b strings.Builder
	b.WriteString("## Compaction\n")
	b.WriteString("- dropped 3 message(s), kept the last 2\n")
	if goal {
		b.WriteString("\n## Goal\n- 修复登录鉴权\n")
	}
	if files {
		b.WriteString("\n## Files changed\n- internal/auth.go\n")
	}
	if failures {
		b.WriteString("\n## Failures\n- failed: permission denied\n")
	}
	if next {
		b.WriteString("\n## Next steps\n- 下一步：补单元测试\n")
	}
	return b.String()
}

// TestCompactFidelityNoticeComplete 确认四要素齐全的摘要不产生告警。
func TestCompactFidelityNoticeComplete(t *testing.T) {
	if got := compactFidelityNotice(fidelitySummary(true, true, true, true)); got != "" {
		t.Fatalf("完整摘要告警 = %q, want 空串", got)
	}

	// 中文标题同样应被识别为齐全，不产生告警。
	cjk := "## 目标\n- 修复登录\n\n## 改动文件\n- internal/auth.go\n\n" +
		"## 失败\n- 权限不足\n\n## 下一步\n- 补单元测试\n"
	if got := compactFidelityNotice(cjk); got != "" {
		t.Fatalf("中文完整摘要告警 = %q, want 空串", got)
	}
}

// TestCompactFidelityNoticeMissingElements 覆盖单要素、多要素与全部缺失时的告警文本，
// 并锁定缺失要素的固定顺序与中文文案。阈值取 1，缺任意一项即判有损。
func TestCompactFidelityNoticeMissingElements(t *testing.T) {
	tests := []struct {
		name                        string
		goal, files, failures, next bool
		want                        string
	}{
		{"缺目标", false, true, true, true, compactionFidelityNoticePrefix + "摘要缺少目标"},
		{"缺改动文件", true, false, true, true, compactionFidelityNoticePrefix + "摘要缺少改动文件"},
		{"缺失败", true, true, false, true, compactionFidelityNoticePrefix + "摘要缺少失败与错误"},
		{"缺下一步", true, true, true, false, compactionFidelityNoticePrefix + "摘要缺少下一步"},
		{"缺目标与改动文件", false, false, true, true, compactionFidelityNoticePrefix + "摘要缺少目标、改动文件"},
		{"缺失败与下一步", true, true, false, false, compactionFidelityNoticePrefix + "摘要缺少失败与错误、下一步"},
		{"全部缺失", false, false, false, false, compactionFidelityNoticePrefix + "摘要缺少目标、改动文件、失败与错误、下一步"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := compactFidelityNotice(fidelitySummary(tc.goal, tc.files, tc.failures, tc.next))
			if got != tc.want {
				t.Fatalf("告警 = %q, want %q", got, tc.want)
			}
			if !strings.HasPrefix(got, compactionFidelityNoticePrefix) {
				t.Fatalf("告警缺少固定前缀: %q", got)
			}
		})
	}
}

// TestCompactFidelityNoticeEmptyMarkers 确认只有占位符（无实际内容）的章节仍被视为缺失。
func TestCompactFidelityNoticeEmptyMarkers(t *testing.T) {
	summary := "## Goal\n- (none)\n\n## Files changed\n- 无\n\n" +
		"## Failures\n- (none)\n\n## Next steps\n- （无）\n"
	want := compactionFidelityNoticePrefix + "摘要缺少目标、改动文件、失败与错误、下一步"
	if got := compactFidelityNotice(summary); got != want {
		t.Fatalf("占位符摘要告警 = %q, want %q", got, want)
	}
}

// TestCompactFidelityNoticeEmptyAndNoise 覆盖空串与仅含无关章节的输入：应判为有损，
// 且噪声章节下的项目符号不得被误计为要素。
func TestCompactFidelityNoticeEmptyAndNoise(t *testing.T) {
	want := compactionFidelityNoticePrefix + "摘要缺少目标、改动文件、失败与错误、下一步"
	if got := compactFidelityNotice(""); got != want {
		t.Fatalf("空摘要告警 = %q, want %q", got, want)
	}

	noise := "## Compaction\n- dropped 9 message(s)\n\n## Tool activity\n- read internal/a.go\n"
	if got := compactFidelityNotice(noise); got != want {
		t.Fatalf("噪声摘要告警 = %q, want %q", got, want)
	}
}

// TestCompactFidelityNoticeCJK 确认 CJK 内容不影响缺失判定，且中文缺失要素文案正确。
func TestCompactFidelityNoticeCJK(t *testing.T) {
	summary := "## 目标\n- 实现登录功能\n\n## 改动文件\n- internal/auth.go\n\n## 失败\n- 编译失败：未定义符号\n"
	want := compactionFidelityNoticePrefix + "摘要缺少下一步"
	if got := compactFidelityNotice(summary); got != want {
		t.Fatalf("CJK 摘要告警 = %q, want %q", got, want)
	}
}

// TestCompactFidelityNoticeStableAndPure 锁定纯函数契约：相同输入总得到相同文本，
// 反复调用不修改入参字符串。
func TestCompactFidelityNoticeStableAndPure(t *testing.T) {
	summary := fidelitySummary(true, true, false, false)
	before := summary

	first := compactFidelityNotice(summary)
	second := compactFidelityNotice(summary)
	if first != second {
		t.Fatalf("相同输入告警不稳定: %q != %q", first, second)
	}
	if first == "" {
		t.Fatal("有损摘要的告警不应为空")
	}
	if summary != before {
		t.Fatal("compactFidelityNotice 修改了入参")
	}
}

// TestNewCompactionFidelityObserverStates 覆盖观察者开关的各种状态：nil、零值、其他键
// 开启、显式开启、显式关闭与 Reset，确认只有显式开启才返回非 nil 观察者。
func TestNewCompactionFidelityObserverStates(t *testing.T) {
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInContextPressure, true)
	turnedOff := new(OptInRegistry)
	turnedOff.Set(OptInCompactionFidelity, true)
	turnedOff.Set(OptInCompactionFidelity, false)
	reset := new(OptInRegistry)
	reset.Set(OptInCompactionFidelity, true)
	reset.Reset()

	tests := []struct {
		name string
		reg  *OptInRegistry
		want bool
	}{
		{"nil 注册表", nil, false},
		{"零值注册表", new(OptInRegistry), false},
		{"仅其他键开启", otherOnly, false},
		{"显式开启", func() *OptInRegistry {
			r := new(OptInRegistry)
			r.Set(OptInCompactionFidelity, true)
			return r
		}(), true},
		{"显式关闭", turnedOff, false},
		{"Reset 后", reset, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := newCompactionFidelityObserver(tc.reg)
			if (got != nil) != tc.want {
				t.Fatalf("观察者非 nil = %v, want %v", got != nil, tc.want)
			}
		})
	}
}

// TestCompactionFidelityObserverBehavior 确认开启后的观察者与纯函数行为一致：
// 有损摘要返回告警，完整摘要返回空串。
func TestCompactionFidelityObserverBehavior(t *testing.T) {
	reg := new(OptInRegistry)
	reg.Set(OptInCompactionFidelity, true)
	observer := newCompactionFidelityObserver(reg)
	if observer == nil {
		t.Fatal("开启状态下观察者不应为 nil")
	}

	lossy := fidelitySummary(true, true, true, false)
	if got, want := observer(lossy), compactFidelityNotice(lossy); got != want {
		t.Fatalf("有损摘要观察者 = %q, want %q", got, want)
	}
	if got := observer(fidelitySummary(true, true, true, true)); got != "" {
		t.Fatalf("完整摘要观察者 = %q, want 空串", got)
	}
}

// TestCompactionFidelityKeyIsRegistered 证明开关键已进入统一注册表：既出现在全量清单里，
// 也能从 ARGENT_OPT_INS 的解析结果中被识别，未知键与缺失环境变量保持默认关闭。
func TestCompactionFidelityKeyIsRegistered(t *testing.T) {
	defaults := DefaultOptIns()
	if enabled, ok := defaults[OptInCompactionFidelity]; !ok || enabled {
		t.Fatalf("DefaultOptIns()[OptInCompactionFidelity] = (%v, %v), want (false, true)", enabled, ok)
	}
	if !ParseOptInEnv(func(string) string { return "compaction_fidelity" })[OptInCompactionFidelity] {
		t.Fatal("ParseOptInEnv(compaction_fidelity) 未开启该键")
	}
	if ParseOptInEnv(nil)[OptInCompactionFidelity] {
		t.Fatal("env 为 nil 时该键应保持关闭")
	}
	if ParseOptInEnv(func(string) string { return "unknown_key" })[OptInCompactionFidelity] {
		t.Fatal("未知键不应开启该键")
	}
}
