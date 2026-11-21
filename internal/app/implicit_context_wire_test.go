package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// implicitDropped 构造一份"被丢弃消息"夹具。每条内容的行首都带有 marker，
// 因此无论 session 的提取策略选中哪些尾部消息或失败条目，提取文本都会包含 marker，
// 让 app 侧测试无需依赖提取细节即可判断"隐式内容是否并入摘要"。
func implicitDropped(marker string) []session.Entry {
	return []session.Entry{
		{ID: "n1", Role: "user", Content: marker + " 目标：修复登录"},
		{ID: "n2", Role: "assistant", Content: marker + " 正在排查鉴权逻辑"},
		{ID: "n3", Role: "tool", Name: "shell", Content: "failed: " + marker + " 命令执行失败"},
		{ID: "n4", Role: "user", Content: marker + " 继续排查"},
		{ID: "n5", Role: "assistant", Content: marker + " 最后回复"},
	}
}

// implicitCloneDropped 深拷贝一份 dropped，用于验证 enrichWithImplicit 不修改入参。
func implicitCloneDropped(in []session.Entry) []session.Entry {
	if in == nil {
		return nil
	}
	out := make([]session.Entry, len(in))
	copy(out, in)
	for i := range out {
		if in[i].Data != nil {
			out[i].Data = append([]byte(nil), in[i].Data...)
		}
	}
	return out
}

// implicitOn 返回一个仅开启隐式上下文开关的注册表。
func implicitOn() *OptInRegistry {
	r := new(OptInRegistry)
	r.Set(OptInKeepImplicitContext, true)
	return r
}

// TestImplicitContextEnabledStates 覆盖开关键谓词的各态：nil、零值、仅其他键开启、
// 显式开启、显式关闭与 Reset，确认只有显式开启才返回 true。
func TestImplicitContextEnabledStates(t *testing.T) {
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInContextPressure, true)
	turnedOff := new(OptInRegistry)
	turnedOff.Set(OptInKeepImplicitContext, true)
	turnedOff.Set(OptInKeepImplicitContext, false)
	reset := new(OptInRegistry)
	reset.Set(OptInKeepImplicitContext, true)
	reset.Reset()

	tests := []struct {
		name string
		reg  *OptInRegistry
		want bool
	}{
		{"nil 注册表", nil, false},
		{"零值注册表", new(OptInRegistry), false},
		{"仅其他键开启", otherOnly, false},
		{"显式开启", implicitOn(), true},
		{"显式关闭", turnedOff, false},
		{"Reset 后", reset, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := implicitContextEnabled(tc.reg); got != tc.want {
				t.Fatalf("implicitContextEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEnrichWithImplicitOffIsByteIdentical 确认关闭状态下摘要逐字节不变。
// 覆盖 nil、零值、仅其他键开启、显式关闭、Reset，以及有/无 dropped 两种输入。
func TestEnrichWithImplicitOffIsByteIdentical(t *testing.T) {
	summary := "## Compaction\n- dropped 5 message(s)\n\n## Goal\n- 修复登录\n"
	// 末尾放多字节 CJK 与换行，确保比较是字节级的而非按行/按符文。
	summary += "\n## 失败\n- 权限不足失败\n\n"

	regs := map[string]*OptInRegistry{
		"nil": nil,
		"零值":  new(OptInRegistry),
		"仅其他键开启": func() *OptInRegistry {
			r := new(OptInRegistry)
			r.Set(OptInAttemptIsolation, true)
			return r
		}(),
		"显式关闭": func() *OptInRegistry {
			r := new(OptInRegistry)
			r.Set(OptInKeepImplicitContext, false)
			return r
		}(),
		"Reset 后": func() *OptInRegistry {
			r := new(OptInRegistry)
			r.Set(OptInKeepImplicitContext, true)
			r.Reset()
			return r
		}(),
	}
	droppedVariants := map[string][]session.Entry{
		"有 dropped": implicitDropped("IMPLICIT"),
		"无 dropped": nil,
	}

	for rn, reg := range regs {
		for dn, dropped := range droppedVariants {
			t.Run(rn+"/"+dn, func(t *testing.T) {
				got := enrichWithImplicit(summary, dropped, reg)
				if got != summary {
					t.Fatalf("关闭状态下摘要被修改:\n got = %q\nwant = %q", got, summary)
				}
				if len(got) != len(summary) {
					t.Fatalf("关闭状态下摘要长度变化: got %d, want %d", len(got), len(summary))
				}
			})
		}
	}
}

// TestEnrichWithImplicitOnIncludesImplicit 确认开启后被丢弃消息中的隐式内容并入摘要。
func TestEnrichWithImplicitOnIncludesImplicit(t *testing.T) {
	const marker = "IMPLICIT_UNIQUE_TOKEN"
	summary := "## Compaction\n- dropped 5 message(s)\n"
	got := enrichWithImplicit(summary, implicitDropped(marker), implicitOn())
	if got == summary {
		t.Fatal("开启状态下摘要未发生变化，隐式内容未并入")
	}
	if !strings.Contains(got, marker) {
		t.Fatalf("开启状态下摘要未包含隐式内容 %q:\n%s", marker, got)
	}
}

// TestEnrichWithImplicitCJK 确认中文隐式内容在开启时同样被并入，且关闭时逐字节不变。
func TestEnrichWithImplicitCJK(t *testing.T) {
	const marker = "隐式上下文中文标记"
	summary := "## Compaction\n- 已丢弃若干消息\n"

	off := enrichWithImplicit(summary, implicitDropped(marker), nil)
	if off != summary {
		t.Fatalf("中文场景关闭状态下摘要被修改: %q", off)
	}

	on := enrichWithImplicit(summary, implicitDropped(marker), implicitOn())
	if !strings.Contains(on, marker) {
		t.Fatalf("中文隐式内容未并入摘要:\n%s", on)
	}
}

// TestEnrichWithImplicitDoesNotMutateInput 确认纯函数契约：不修改 dropped 切片及其元素，
// 也不修改 summary 字符串。
func TestEnrichWithImplicitDoesNotMutateInput(t *testing.T) {
	summary := "## Compaction\n- 基线摘要\n"
	dropped := implicitDropped("IMPLICIT")
	before := implicitCloneDropped(dropped)
	summaryBefore := summary

	_ = enrichWithImplicit(summary, dropped, implicitOn())

	if !reflect.DeepEqual(dropped, before) {
		t.Fatalf("enrichWithImplicit 修改了 dropped 入参:\n got = %+v\nwant = %+v", dropped, before)
	}
	if summary != summaryBefore {
		t.Fatalf("enrichWithImplicit 修改了 summary 入参: %q", summary)
	}
}

// TestEnrichWithImplicitStable 锁定确定性：相同输入反复调用得到相同输出，
// 开关各态下都成立。
func TestEnrichWithImplicitStable(t *testing.T) {
	summary := "## Compaction\n- 基线摘要\n"
	dropped := implicitDropped("IMPLICIT")
	regs := map[string]*OptInRegistry{
		"关闭": nil,
		"开启": implicitOn(),
	}
	for name, reg := range regs {
		t.Run(name, func(t *testing.T) {
			first := enrichWithImplicit(summary, dropped, reg)
			for i := 0; i < 5; i++ {
				got := enrichWithImplicit(summary, dropped, reg)
				if got != first {
					t.Fatalf("第 %d 次调用结果不稳定:\n got = %q\nwant = %q", i+2, got, first)
				}
			}
		})
	}
}

// TestImplicitContextKeyIsRegistered 证明开关键已进入统一注册表：默认关闭、可被
// ARGENT_OPT_INS 解析识别，未知键与缺失 env 均保持默认关闭。
func TestImplicitContextKeyIsRegistered(t *testing.T) {
	defaults := DefaultOptIns()
	if enabled, ok := defaults[OptInKeepImplicitContext]; !ok || enabled {
		t.Fatalf("DefaultOptIns()[OptInKeepImplicitContext] = (%v, %v), want (false, true)", enabled, ok)
	}
	if !ParseOptInEnv(func(string) string { return "keep_implicit_context" })[OptInKeepImplicitContext] {
		t.Fatal("ParseOptInEnv(keep_implicit_context) 未开启该键")
	}
	if ParseOptInEnv(nil)[OptInKeepImplicitContext] {
		t.Fatal("env 为 nil 时该键应保持关闭")
	}
	if ParseOptInEnv(func(string) string { return "unknown_key" })[OptInKeepImplicitContext] {
		t.Fatal("未知键不应开启该键")
	}
}
