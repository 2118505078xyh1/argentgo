package app

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// preserveReg 构造一个已显式开启摘要章节保留开关的注册表。
func preserveReg() *OptInRegistry {
	reg := new(OptInRegistry)
	reg.Set(OptInPreserveSummarySections, true)
	return reg
}

// TestPreserveSectionsEnabledStates 覆盖开关的各种状态：nil、零值、其他键开启、
// 显式开启、显式关闭与 Reset，确认只有显式开启才返回 true。
func TestPreserveSectionsEnabledStates(t *testing.T) {
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInContextPressure, true)
	turnedOff := new(OptInRegistry)
	turnedOff.Set(OptInPreserveSummarySections, true)
	turnedOff.Set(OptInPreserveSummarySections, false)
	reset := new(OptInRegistry)
	reset.Set(OptInPreserveSummarySections, true)
	reset.Reset()

	tests := []struct {
		name string
		reg  *OptInRegistry
		want bool
	}{
		{"nil 注册表", nil, false},
		{"零值注册表", new(OptInRegistry), false},
		{"仅其他键开启", otherOnly, false},
		{"显式开启", preserveReg(), true},
		{"显式关闭", turnedOff, false},
		{"Reset 后", reset, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := preserveSectionsEnabled(tc.reg); got != tc.want {
				t.Fatalf("preserveSectionsEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPreserveSummarySectionsDisabledIsByteIdentical 锁定默认路径契约：关闭时原样返回
// rebuilt，逐字节不变。覆盖空串、尾部空白、CJK 与非法 UTF-8 字节序列，并用多种"未开启"
// 注册表状态保证 fail-closed。
func TestPreserveSummarySectionsDisabledIsByteIdentical(t *testing.T) {
	turnedOff := new(OptInRegistry)
	turnedOff.Set(OptInPreserveSummarySections, true)
	turnedOff.Set(OptInPreserveSummarySections, false)
	reset := new(OptInRegistry)
	reset.Set(OptInPreserveSummarySections, true)
	reset.Reset()
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInCompactionFidelity, true)

	inputs := []string{
		"",
		"## Compaction\n- dropped 3 message(s), kept the last 2\n",
		"带尾部空白  \t\n\n",
		"## 目标\n- 修复登录鉴权\n",
		"\xff\xfe\x00binary\tbytes\n",
	}
	regs := []struct {
		name string
		reg  *OptInRegistry
	}{
		{"nil", nil},
		{"零值", new(OptInRegistry)},
		{"显式关闭", turnedOff},
		{"Reset", reset},
		{"其他键", otherOnly},
	}

	for _, input := range inputs {
		for _, rc := range regs {
			t.Run(rc.name, func(t *testing.T) {
				orig := "## Goal\n- 修复登录鉴权\n\n## Tool activity\n- read internal/auth.go\n"
				got := preserveSummarySections(input, orig, rc.reg)
				if got != input {
					t.Fatalf("关闭时未逐字节返回 rebuilt:\n got = %q\nwant = %q", got, input)
				}
			})
		}
	}
}

// TestPreserveSummarySectionsEnabledPreservesGoalAndActivity 确认开启后原摘要中的目标与
// 工具活动章节被保留：重建摘要缺失这些章节时，结果中出现原文内容。
func TestPreserveSummarySectionsEnabledPreservesGoalAndActivity(t *testing.T) {
	rebuilt := "## Compaction\n- dropped 3 message(s), kept the last 2\n\n## Files changed\n- (none)\n"
	orig := "## Compaction\n- dropped 3 message(s), kept the last 2\n" +
		"\n## Goal\n- 修复登录鉴权\n" +
		"\n## Tool activity\n- read internal/auth.go\n"

	got := preserveSummarySections(rebuilt, orig, preserveReg())
	if !strings.Contains(got, "修复登录鉴权") {
		t.Fatalf("开启后未保留 Goal 章节: %q", got)
	}
	if !strings.Contains(got, "read internal/auth.go") {
		t.Fatalf("开启后未保留 Tool activity 章节: %q", got)
	}
}

// TestPreserveSummarySectionsCJK 确认 CJK 内容与标题不影响章节保留，中文目标原文出现在
// 结果中。
func TestPreserveSummarySectionsCJK(t *testing.T) {
	rebuilt := "## Compaction\n- dropped 1 message(s), kept the last 2\n"
	orig := "## Compaction\n- dropped 1 message(s), kept the last 2\n" +
		"\n## 目标\n- 修复中文编码问题\n" +
		"\n## 工具活动\n- 读取 internal/编码.go\n"

	got := preserveSummarySections(rebuilt, orig, preserveReg())
	if !strings.Contains(got, "修复中文编码问题") {
		t.Fatalf("CJK 目标章节未保留: %q", got)
	}
	if !strings.Contains(got, "读取 internal/编码.go") {
		t.Fatalf("CJK 工具活动章节未保留: %q", got)
	}
}

// TestPreserveSummarySectionsDelegatesAndPure 锁定委托与纯函数契约：开启时结果与直接调用
// session.PreserveSections 一致，重复调用稳定，且两个入参字符串均未被修改。
func TestPreserveSummarySectionsDelegatesAndPure(t *testing.T) {
	rebuilt := "## Compaction\n- dropped 2 message(s), kept the last 2\n"
	orig := rebuilt + "\n## Goal\n- 完成接线\n\n## Tool activity\n- bash go test ./...\n"
	rebuiltBefore, origBefore := rebuilt, orig
	reg := preserveReg()

	first := preserveSummarySections(rebuilt, orig, reg)
	second := preserveSummarySections(rebuilt, orig, reg)
	want := session.PreserveSections(rebuilt, orig)
	if first != want {
		t.Fatalf("开启时未委托给 session.PreserveSections:\n got = %q\nwant = %q", first, want)
	}
	if first != second {
		t.Fatalf("相同输入结果不稳定: %q != %q", first, second)
	}
	if rebuilt != rebuiltBefore || orig != origBefore {
		t.Fatal("preserveSummarySections 修改了入参")
	}
}

// TestPreserveSummarySectionsKeyIsRegistered 证明开关键已进入统一注册表：既出现在全量
// 清单里，也能从 ARGENT_OPT_INS 的解析结果中被识别，未知键与缺失环境变量保持默认关闭。
func TestPreserveSummarySectionsKeyIsRegistered(t *testing.T) {
	defaults := DefaultOptIns()
	if enabled, ok := defaults[OptInPreserveSummarySections]; !ok || enabled {
		t.Fatalf("DefaultOptIns()[OptInPreserveSummarySections] = (%v, %v), want (false, true)", enabled, ok)
	}
	if !ParseOptInEnv(func(string) string { return "preserve_summary_sections" })[OptInPreserveSummarySections] {
		t.Fatal("ParseOptInEnv(preserve_summary_sections) 未开启该键")
	}
	if ParseOptInEnv(nil)[OptInPreserveSummarySections] {
		t.Fatal("env 为 nil 时该键应保持关闭")
	}
	if ParseOptInEnv(func(string) string { return "unknown_key" })[OptInPreserveSummarySections] {
		t.Fatal("未知键不应开启该键")
	}
}
