package app

import (
	"reflect"
	"testing"
)

// 本文件守护配置激活面的纯函数契约：configOptIns 只产出 app 与 config 交集的键，
// 默认全关、未知键忽略、不改入参；mergeOptIns 是环境面与配置面的逻辑或并集，且
// 返回新映射。所有用例不联网、不读进程环境、不用真实模型。

// configOptInKeys 是独立于实现写死的交集清单，作为测试的预期事实来源：
// config 白名单与 app OptInKey 清单的重叠部分。
var configOptInKeys = []OptInKey{
	OptInCliHints,
	OptInApplyPatch,
	OptInGrepEx,
	OptInContextPressure,
	OptInAttemptIsolation,
	OptInKeepImplicitContext,
	OptInPreserveSummarySections,
	OptInToolErrorReflection,
	OptInModelSummary,
}

// configOptInWant 以「9 键全 false」为基线，仅把 on 中的键设为 true，
// 让表驱动用例只需声明「哪些配置开关应开启」。
func configOptInWant(on ...OptInKey) map[OptInKey]bool {
	m := make(map[OptInKey]bool, len(configOptInKeys))
	for _, key := range configOptInKeys {
		m[key] = false
	}
	for _, key := range on {
		m[key] = true
	}
	return m
}

// TestConfigOptInsAllKeysEnabled 覆盖交集内全部 9 键：嵌套 optins 块中显式 true
// 时应全部开启，且结果恰好只含这 8 个键。
func TestConfigOptInsAllKeysEnabled(t *testing.T) {
	raw := map[string]any{"optins": map[string]any{
		"cli_hints":                 true,
		"apply_patch":               true,
		"grep_ex":                   true,
		"context_pressure":          true,
		"attempt_isolation":         true,
		"keep_implicit_context":     true,
		"preserve_summary_sections": true,
		"tool_error_reflection":     true,
		"model_summary":             true,
	}}
	got := configOptIns(raw)
	want := configOptInWant(configOptInKeys...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configOptIns() = %v, want %v", got, want)
	}
}

// TestConfigOptInsDirectForm 覆盖直接形式：raw 本身即 opt-in 映射时按白名单取交集。
func TestConfigOptInsDirectForm(t *testing.T) {
	raw := map[string]any{
		"cli_hints":        true,
		"grep_ex":          true,
		"context_pressure": false,
	}
	got := configOptIns(raw)
	want := configOptInWant(OptInCliHints, OptInGrepEx)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configOptIns(direct) = %v, want %v", got, want)
	}
}

// TestConfigOptInsDefaultClosed 覆盖默认关闭：nil、空映射、空块、块类型不符、
// 非布尔值都不得开启任何开关，结果恒为 9 键全 false。
func TestConfigOptInsDefaultClosed(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{"nil raw", nil},
		{"empty raw", map[string]any{}},
		{"empty optins block", map[string]any{"optins": map[string]any{}}},
		{"malformed optins block", map[string]any{"optins": "not-a-map"}},
		{"non-bool value", map[string]any{"optins": map[string]any{
			"cli_hints":        "true",
			"grep_ex":          1,
			"context_pressure": nil,
		}}},
	}
	want := configOptInWant()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := configOptIns(tc.raw)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("configOptIns(%s) = %v, want all false %v", tc.name, got, want)
			}
		})
	}
}

// TestConfigOptInsIgnoresUnknownKeys 验证未知键被忽略：CJK 键、无关键，以及 app 独有
// 但不在 config 白名单里的 4 个键，都不能通过配置激活，也不会污染结果映射。
func TestConfigOptInsIgnoresUnknownKeys(t *testing.T) {
	raw := map[string]any{"optins": map[string]any{
		"bogus":               true,
		"另一个键":                true,
		"refusal_reason":      true,
		"untrusted_result":    true,
		"untrusted_boundary":  true,
		"compaction_fidelity": true,
		"cli_hints":           true,
	}}
	got := configOptIns(raw)
	want := configOptInWant(OptInCliHints)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configOptIns() = %v, want %v", got, want)
	}
	for _, key := range []OptInKey{
		OptInRefusalReason, OptInUntrustedResult,
		OptInUntrustedBoundary, OptInCompactionFidelity,
	} {
		if _, leaked := got[key]; leaked {
			t.Errorf("配置不应激活 app 独有键 %q: %v", key, got)
		}
	}
}

// TestConfigOptInsDoesNotMutateInput 验证纯函数约定：不修改入参（含嵌套块），
// 且修改一次结果不影响下一次调用。
func TestConfigOptInsDoesNotMutateInput(t *testing.T) {
	raw := map[string]any{"optins": map[string]any{
		"cli_hints": true,
		"unknown":   "x",
	}}
	before := map[string]any{"optins": map[string]any{
		"cli_hints": true,
		"unknown":   "x",
	}}
	first := configOptIns(raw)
	if !reflect.DeepEqual(raw, before) {
		t.Fatalf("configOptIns 修改了入参: got %+v, want %+v", raw, before)
	}
	first[OptInCliHints] = false
	delete(first, OptInGrepEx)
	first[OptInRefusalReason] = true

	second := configOptIns(raw)
	if !reflect.DeepEqual(second, configOptInWant(OptInCliHints)) {
		t.Fatalf("第二次 configOptIns 被第一次结果污染: %v", second)
	}
}

// TestMergeOptInsUnion 覆盖并集语义：环境或配置任一为 true 即为 true，
// 只出现在一侧的键（即使值为 false）也保留在结果中。
func TestMergeOptInsUnion(t *testing.T) {
	tests := []struct {
		name   string
		envOpt map[OptInKey]bool
		cfgOpt map[OptInKey]bool
		want   map[OptInKey]bool
	}{
		{
			name:   "env true wins over cfg false",
			envOpt: map[OptInKey]bool{OptInCliHints: true},
			cfgOpt: map[OptInKey]bool{OptInCliHints: false},
			want:   map[OptInKey]bool{OptInCliHints: true},
		},
		{
			name:   "cfg true wins over env false",
			envOpt: map[OptInKey]bool{OptInGrepEx: false},
			cfgOpt: map[OptInKey]bool{OptInGrepEx: true},
			want:   map[OptInKey]bool{OptInGrepEx: true},
		},
		{
			name:   "both true stays true",
			envOpt: map[OptInKey]bool{OptInApplyPatch: true},
			cfgOpt: map[OptInKey]bool{OptInApplyPatch: true},
			want:   map[OptInKey]bool{OptInApplyPatch: true},
		},
		{
			name:   "both false stays false",
			envOpt: map[OptInKey]bool{OptInContextPressure: false},
			cfgOpt: map[OptInKey]bool{OptInContextPressure: false},
			want:   map[OptInKey]bool{OptInContextPressure: false},
		},
		{
			name:   "env only and cfg only keys both kept",
			envOpt: map[OptInKey]bool{OptInRefusalReason: true, OptInUntrustedResult: false},
			cfgOpt: map[OptInKey]bool{OptInGrepEx: true, OptInApplyPatch: false},
			want: map[OptInKey]bool{
				OptInRefusalReason:   true,
				OptInUntrustedResult: false,
				OptInGrepEx:          true,
				OptInApplyPatch:      false,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeOptIns(tc.envOpt, tc.cfgOpt)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeOptIns() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMergeOptInsDoesNotMutateInput 验证 merge 返回新映射：修改结果不影响两个入参。
func TestMergeOptInsDoesNotMutateInput(t *testing.T) {
	envOpt := map[OptInKey]bool{OptInCliHints: true}
	cfgOpt := map[OptInKey]bool{OptInGrepEx: true}
	envBefore := map[OptInKey]bool{OptInCliHints: true}
	cfgBefore := map[OptInKey]bool{OptInGrepEx: true}

	got := mergeOptIns(envOpt, cfgOpt)
	got[OptInCliHints] = false
	got[OptInApplyPatch] = true
	delete(got, OptInGrepEx)

	if !reflect.DeepEqual(envOpt, envBefore) {
		t.Fatalf("mergeOptIns 修改了 env 入参: %v", envOpt)
	}
	if !reflect.DeepEqual(cfgOpt, cfgBefore) {
		t.Fatalf("mergeOptIns 修改了 cfg 入参: %v", cfgOpt)
	}
}

// TestMergeOptInsEmptyInputs 验证两个 nil 入参返回非 nil 空映射。
func TestMergeOptInsEmptyInputs(t *testing.T) {
	got := mergeOptIns(nil, nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("mergeOptIns(nil, nil) = %v, want non-nil empty map", got)
	}
}

// TestMergeOptInsAlignsWithDefaultWhenAllClosed 守护「默认关闭逐字节不变」总契约：
// 无 env 且无配置时，两条激活面合并的结果必须与 DefaultOptIns 逐键一致（全 false）。
func TestMergeOptInsAlignsWithDefaultWhenAllClosed(t *testing.T) {
	got := mergeOptIns(ParseOptInEnv(nil), configOptIns(nil))
	want := DefaultOptIns()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeOptIns(nil env, nil cfg) = %v, want DefaultOptIns %v", got, want)
	}
}

// TestMergeOptInsConfigKeyActivates 逐个验证 8 个配置键都能经合并真正激活，
// 证明「无配置全关」不是「接线未生效」的假绿。
func TestMergeOptInsConfigKeyActivates(t *testing.T) {
	for _, key := range configOptInKeys {
		t.Run(string(key), func(t *testing.T) {
			cfg := configOptIns(map[string]any{"optins": map[string]any{
				string(key): true,
			}})
			got := mergeOptIns(ParseOptInEnv(nil), cfg)
			want := optInWant(key)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("配置键 %q 未生效: got %v, want %v", key, got, want)
			}
		})
	}
}

// TestMergeOptInsKeepsEnvOnlyKeys 验证 app 独有的 4 个键只能由 env 激活，
// 合并配置面后它们保持原值（配置无法覆盖也无法关闭它们）。
func TestMergeOptInsKeepsEnvOnlyKeys(t *testing.T) {
	env := ParseOptInEnv(func(string) string {
		return "refusal_reason,compaction_fidelity"
	})
	got := mergeOptIns(env, configOptIns(nil))
	want := optInWant(OptInRefusalReason, OptInCompactionFidelity)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeOptIns() = %v, want %v", got, want)
	}
}
