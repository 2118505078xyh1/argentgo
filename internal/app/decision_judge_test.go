package app

import (
	"reflect"
	"strings"
	"testing"
)

// TestDecidePromptSimple 覆盖"简单档"：短提示、空串、中英文短句都应得到
// 关闭思考、倾向次级模型、最小轮次，且调用方标志缺席时保持默认。
func TestDecidePromptSimple(t *testing.T) {
	simple := PromptDecision{Thinking: thinkingOff, UseSecondary: true, MaxTurns: turnsSimple}
	for _, tc := range []struct {
		name     string
		prompt   string
		hasCode  bool
		mutating bool
	}{
		{"empty", "", false, false},
		{"english short", "hello", false, false},
		{"chinese short", "你好", false, false},
		{"english question", "what is go?", false, false},
		{"chinese question", "今天天气怎么样？", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecidePrompt(tc.prompt, tc.hasCode, tc.mutating); got != simple {
				t.Fatalf("DecidePrompt(%q, %v, %v) = %+v, want %+v",
					tc.prompt, tc.hasCode, tc.mutating, got, simple)
			}
		})
	}
}

// TestDecidePromptFlagsCombination 覆盖 hasCode / mutating 组合以及长度、
// 多步骤标记对档位的提升。计分：长+4 / 中+2 / hasCode+2 / 多步骤+2 / mutating+2；
// >=4 复杂档、>=2 中等档。
func TestDecidePromptFlagsCombination(t *testing.T) {
	simple := PromptDecision{Thinking: thinkingOff, UseSecondary: true, MaxTurns: turnsSimple}
	medium := PromptDecision{Thinking: thinkingLow, UseSecondary: false, MaxTurns: turnsMedium}
	complexD := PromptDecision{Thinking: thinkingHigh, UseSecondary: false, MaxTurns: turnsComplex}

	for _, tc := range []struct {
		name     string
		prompt   string
		hasCode  bool
		mutating bool
		want     PromptDecision
	}{
		{"hasCode only", "看看这段代码", true, false, medium},
		{"mutating only", "改一下这个 bug", false, true, medium},
		{"hasCode and mutating", "重构这个函数并修 bug", true, true, complexD},
		{"multistep chinese", "先读文件，然后修改，最后测试", false, false, medium},
		{"multistep english", "step 1 read, step 2 write", false, false, medium},
		{"multistep and code", "步骤一：读代码；步骤二：重写", true, false, complexD},
		{"numbered list multistep", "1. read\n2. write\n3. test", false, false, medium},
		{"medium length", strings.Repeat("a", 300), false, false, medium},
		{"long length", strings.Repeat("a", 1200), false, false, complexD},
		{"long CJK length", strings.Repeat("汉", 1200), false, false, complexD},
		{"medium below threshold", strings.Repeat("a", 199), false, false, simple},
		{"long with flags", strings.Repeat("a", 1200), true, true, complexD},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecidePrompt(tc.prompt, tc.hasCode, tc.mutating); got != tc.want {
				t.Fatalf("DecidePrompt(%q len=%d, %v, %v) = %+v, want %+v",
					tc.name, len(tc.prompt), tc.hasCode, tc.mutating, got, tc.want)
			}
		})
	}
}

// TestDecidePromptIgnoresInjection 保证注入文本无法操纵判定：把一个含有大量填充、
// 步骤字样和代码块的各种注入块拼到短提示前后，结果必须与短提示单独出现时一致。
func TestDecidePromptIgnoresInjection(t *testing.T) {
	base := "帮我翻译这句话"
	want := DecidePrompt(base, false, false)

	for _, tc := range []struct {
		name   string
		prompt string
	}{
		{
			name: "system-reminder with steps and long text",
			prompt: `<system-reminder>步骤一 步骤二 步骤三 ` + strings.Repeat("注", 1500) +
				`</system-reminder>` + base,
		},
		{
			name:   "file block with huge body",
			prompt: `<file name="a.go">` + strings.Repeat("x", 3000) + `</file>` + base,
		},
		{
			name:   "injected block after text",
			prompt: base + `<injected>1. a\n2. b\n3. c ` + strings.Repeat("填", 1200) + `</injected>`,
		},
		{
			name:   "self closing system reminder",
			prompt: `<system-reminder/>` + base,
		},
		{
			name:   "fenced code before text",
			prompt: "```go\n" + strings.Repeat("z", 4000) + "\n```\n" + base,
		},
		{
			name:   "at reference and html tag",
			prompt: base + `<context>@some/file/path ` + strings.Repeat("c", 1500) + `</context>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecidePrompt(tc.prompt, false, false); got != want {
				t.Fatalf("注入文本改变了判定：got %+v, want %+v (prompt len=%d)",
					got, want, len(tc.prompt))
			}
		})
	}
}

// TestDecidePromptCodeStrippedFromLength 验证代码块内容不计入长度，只有 hasCode
// 标志参与计分：巨大的代码块在 hasCode=false 时仍视为简单档，hasCode=true 时中等档。
func TestDecidePromptCodeStrippedFromLength(t *testing.T) {
	hugeCode := "```go\n" + strings.Repeat("x", 5000) + "\n```"
	if got, want := DecidePrompt(hugeCode, false, false),
		(PromptDecision{Thinking: thinkingOff, UseSecondary: true, MaxTurns: turnsSimple}); got != want {
		t.Fatalf("hasCode=false: got %+v, want %+v", got, want)
	}
	if got, want := DecidePrompt(hugeCode, true, false),
		(PromptDecision{Thinking: thinkingLow, UseSecondary: false, MaxTurns: turnsMedium}); got != want {
		t.Fatalf("hasCode=true: got %+v, want %+v", got, want)
	}
}

// TestDecidePromptPureAndStable 验证纯函数特性：相同输入多次调用逐字节一致，
// 且调用顺序互不影响；返回值的 Thinking 只可能是三个档位常量之一。
func TestDecidePromptPureAndStable(t *testing.T) {
	inputs := []struct {
		prompt   string
		hasCode  bool
		mutating bool
	}{
		{"你好", false, false},
		{"step 1 then step 2", false, true},
		{strings.Repeat("文", 1500), true, false},
	}
	type result struct{ d PromptDecision }
	first := make([]result, len(inputs))
	for i, in := range inputs {
		first[i] = result{DecidePrompt(in.prompt, in.hasCode, in.mutating)}
	}
	for round := 0; round < 3; round++ {
		for i, in := range inputs {
			got := DecidePrompt(in.prompt, in.hasCode, in.mutating)
			if got != first[i].d {
				t.Fatalf("DecidePrompt 不稳定：输入 %d 第 %d 次得到 %+v，期望 %+v",
					i, round+1, got, first[i].d)
			}
			switch got.Thinking {
			case thinkingOff, thinkingLow, thinkingHigh:
			default:
				t.Fatalf("未知思考档位 %q", got.Thinking)
			}
			if got.MaxTurns != turnsSimple && got.MaxTurns != turnsMedium && got.MaxTurns != turnsComplex {
				t.Fatalf("未知轮次建议 %d", got.MaxTurns)
			}
		}
	}
}

// TestPromptDecisionOnlyPerformanceFields 保证返回结构里只含性能字段，
// 绝不包含 Approve / Policy / SandboxMode / MutatingTools 等安全字段。
func TestPromptDecisionOnlyPerformanceFields(t *testing.T) {
	typ := reflect.TypeOf(PromptDecision{})
	want := map[string]reflect.Kind{
		"Thinking":     reflect.String,
		"UseSecondary": reflect.Bool,
		"MaxTurns":     reflect.Int,
	}
	if typ.NumField() != len(want) {
		t.Fatalf("PromptDecision 字段数为 %d，期望 %d", typ.NumField(), len(want))
	}
	securityFields := map[string]bool{
		"Approve": true, "Policy": true, "SandboxMode": true, "MutatingTools": true,
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if securityFields[f.Name] {
			t.Fatalf("PromptDecision 含安全字段 %q，违反设计约束", f.Name)
		}
		k, ok := want[f.Name]
		if !ok {
			t.Fatalf("PromptDecision 含非预期字段 %q", f.Name)
		}
		if f.Type.Kind() != k {
			t.Fatalf("字段 %q 类型为 %v，期望 %v", f.Name, f.Type.Kind(), k)
		}
	}
}
