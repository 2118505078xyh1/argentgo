package cli

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// listedOptInKeys 从 FormatOptIns 的输出中提取以 "- " 开头的行里的键名，
// 便于直接断言"列出了哪些项"及其顺序，而不依赖标题文案。
func listedOptInKeys(out string) []string {
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- ") {
			keys = append(keys, strings.TrimPrefix(line, "- "))
		}
	}
	return keys
}

func TestFormatOptInsAllOff(t *testing.T) {
	tests := []struct {
		name    string
		enabled map[string]bool
	}{
		{"nil map", nil},
		{"empty map", map[string]bool{}},
		{"all false", map[string]bool{"refusal_reason": false, "attempt_isolation": false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatOptIns(tt.enabled); got != optInAllOffMessage {
				t.Fatalf("FormatOptIns() = %q, want %q", got, optInAllOffMessage)
			}
		})
	}
}

func TestFormatOptInsListsEnabledSorted(t *testing.T) {
	// 两份等价映射以不同顺序写入；输出必须一致且按 key 排序，并过滤掉 false 项。
	a := map[string]bool{
		"untrusted_result":  true,
		"attempt_isolation": true,
		"context_pressure":  true,
		"refusal_reason":    false,
	}
	b := map[string]bool{}
	b["context_pressure"] = true
	b["attempt_isolation"] = true
	b["untrusted_result"] = true
	b["refusal_reason"] = false

	gotA := FormatOptIns(a)
	if gotB := FormatOptIns(b); gotA != gotB {
		t.Fatalf("FormatOptIns depends on map order: %q vs %q", gotA, gotB)
	}
	want := []string{"attempt_isolation", "context_pressure", "untrusted_result"}
	if got := listedOptInKeys(gotA); !reflect.DeepEqual(got, want) {
		t.Fatalf("listed keys = %v, want %v (output %q)", got, want, gotA)
	}
	if strings.Contains(gotA, "refusal_reason") {
		t.Fatalf("FormatOptIns listed a disabled key: %q", gotA)
	}
}

func TestFormatOptInsAllKnownKeys(t *testing.T) {
	enabled := map[string]bool{
		"refusal_reason":            true,
		"untrusted_result":          true,
		"context_pressure":          true,
		"untrusted_boundary":        true,
		"attempt_isolation":         true,
		"compaction_fidelity":       true,
		"keep_implicit_context":     true,
		"preserve_summary_sections": true,
		"apply_patch":               true,
		"grep_ex":                   true,
		"cli_hints":                 true,
		"tool_error_reflection":     true,
		"model_summary":             true,
		"auto_verify":               true,
	}
	want := []string{
		"apply_patch", "attempt_isolation", "auto_verify", "cli_hints", "compaction_fidelity",
		"context_pressure", "grep_ex", "keep_implicit_context", "model_summary",
		"preserve_summary_sections", "refusal_reason", "tool_error_reflection",
		"untrusted_boundary", "untrusted_result",
	}
	if got := listedOptInKeys(FormatOptIns(enabled)); !reflect.DeepEqual(got, want) {
		t.Fatalf("listed keys = %v, want %v", got, want)
	}
}

// TestKnownOptInKeysStable 锁定 cli 侧词汇表与 app 权威 14 键一致，防止新增键时
// 只改一处造成三处分叉。
func TestKnownOptInKeysStable(t *testing.T) {
	want := []string{
		"refusal_reason", "untrusted_result", "context_pressure", "untrusted_boundary",
		"attempt_isolation", "compaction_fidelity", "keep_implicit_context",
		"preserve_summary_sections", "apply_patch", "grep_ex", "cli_hints", "tool_error_reflection", "model_summary", "auto_verify",
	}
	if len(knownOptInKeys) != len(want) {
		t.Fatalf("knownOptInKeys 键数 = %d, want %d: %v", len(knownOptInKeys), len(want), knownOptInKeys)
	}
	for _, key := range want {
		if _, ok := knownOptInKeys[key]; !ok {
			t.Errorf("knownOptInKeys 缺少权威键 %q", key)
		}
	}
}

func TestFormatOptInsCJKKey(t *testing.T) {
	enabled := map[string]bool{"拒绝理由": true, "untrusted_result": true}
	out := FormatOptIns(enabled)
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
	if !strings.Contains(out, "拒绝理由") {
		t.Fatalf("output lost CJK key: %q", out)
	}
	// UTF-8 字节序下 ASCII 的 'u'(0x75) 排在 CJK 首字节(0xE6)之前。
	want := []string{"untrusted_result", "拒绝理由"}
	if got := listedOptInKeys(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("listed keys = %v, want %v (output %q)", got, want, out)
	}
}

func TestFormatOptInsDoesNotMutateInput(t *testing.T) {
	enabled := map[string]bool{"refusal_reason": true, "untrusted_result": false}
	before := make(map[string]bool, len(enabled))
	for key, value := range enabled {
		before[key] = value
	}
	FormatOptIns(enabled)
	if !reflect.DeepEqual(enabled, before) {
		t.Fatalf("FormatOptIns mutated input: %v, want %v", enabled, before)
	}
}

func TestFormatOptInsStableAcrossCalls(t *testing.T) {
	enabled := map[string]bool{
		"compaction_fidelity": true,
		"attempt_isolation":   true,
		"context_pressure":    true,
		"untrusted_result":    false,
	}
	first := FormatOptIns(enabled)
	for i := 0; i < 20; i++ {
		if got := FormatOptIns(enabled); got != first {
			t.Fatalf("unstable output at call %d: %q vs %q", i, got, first)
		}
	}
}

func TestOptInFromEnv(t *testing.T) {
	tests := []struct {
		name   string
		nilEnv bool
		value  string
		want   map[string]bool
	}{
		{"nil env is empty map", true, "", map[string]bool{}},
		{"empty value", false, "", map[string]bool{}},
		{"only commas", false, ",,", map[string]bool{}},
		{"single key", false, "refusal_reason", map[string]bool{"refusal_reason": true}},
		{"two keys", false, "refusal_reason,untrusted_result",
			map[string]bool{"refusal_reason": true, "untrusted_result": true}},
		{"whitespace trimmed", false, "  context_pressure , untrusted_boundary  ",
			map[string]bool{"context_pressure": true, "untrusted_boundary": true}},
		{"unknown key ignored", false, "bogus,refusal_reason",
			map[string]bool{"refusal_reason": true}},
		{"only unknown keys", false, "nope,nada", map[string]bool{}},
		{"duplicate key", false, "refusal_reason,refusal_reason",
			map[string]bool{"refusal_reason": true}},
		{"empty segments ignored", false, "refusal_reason,,untrusted_result,",
			map[string]bool{"refusal_reason": true, "untrusted_result": true}},
		{"CJK token ignored", false, "拒绝理由,refusal_reason",
			map[string]bool{"refusal_reason": true}},
		{"case sensitive", false, "REFUSAL_REASON", map[string]bool{}},
		{"all known keys", false,
			"refusal_reason,untrusted_result,context_pressure,untrusted_boundary,attempt_isolation,compaction_fidelity,keep_implicit_context,preserve_summary_sections,apply_patch,grep_ex,cli_hints,tool_error_reflection,model_summary,auto_verify",
			map[string]bool{
				"refusal_reason": true, "untrusted_result": true, "context_pressure": true,
				"untrusted_boundary": true, "attempt_isolation": true, "compaction_fidelity": true,
				"keep_implicit_context": true, "preserve_summary_sections": true,
				"apply_patch": true, "grep_ex": true, "cli_hints": true, "tool_error_reflection": true, "model_summary": true,
				"auto_verify": true,
			}},
		{"new keys cli_hints/apply_patch/grep_ex", false,
			"cli_hints,  apply_patch , grep_ex",
			map[string]bool{"cli_hints": true, "apply_patch": true, "grep_ex": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]bool
			if tt.nilEnv {
				got = OptInFromEnv(nil)
			} else {
				got = OptInFromEnv(func(string) string { return tt.value })
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("OptInFromEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOptInFromEnvNilReturnsNonNilEmptyMap(t *testing.T) {
	got := OptInFromEnv(nil)
	if got == nil {
		t.Fatal("OptInFromEnv(nil) returned nil map")
	}
	if len(got) != 0 {
		t.Fatalf("OptInFromEnv(nil) = %v, want empty", got)
	}
}

func TestOptInFromEnvIsPure(t *testing.T) {
	// 记录 env 被查询的名字与次数，证明只读固定的单个变量且只读一次。
	var queried []string
	env := func(name string) string {
		queried = append(queried, name)
		return "refusal_reason,context_pressure"
	}
	first := OptInFromEnv(env)
	if len(queried) != 1 || queried[0] != optInEnvVar {
		t.Fatalf("env queried %v, want exactly [%s]", queried, optInEnvVar)
	}

	// 修改第一次结果不应影响第二次：每次调用都新建映射。
	first["untrusted_result"] = true
	delete(first, "refusal_reason")
	second := OptInFromEnv(env)
	want := map[string]bool{"refusal_reason": true, "context_pressure": true}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("second OptInFromEnv() = %v, want %v", second, want)
	}
}

func TestOptInFromEnvIgnoresProcessEnvironment(t *testing.T) {
	t.Setenv("ARGENT_OPT_INS", "refusal_reason")
	got := OptInFromEnv(func(string) string { return "" })
	if len(got) != 0 {
		t.Fatalf("OptInFromEnv() read process environment: %v", got)
	}
}

func TestOptInFromEnvToFormatOptIns(t *testing.T) {
	// 组合两个原语：未知键被过滤，启用项按排序展示。
	enabled := OptInFromEnv(func(string) string {
		return "compaction_fidelity,attempt_isolation,unknown"
	})
	want := []string{"attempt_isolation", "compaction_fidelity"}
	if got := listedOptInKeys(FormatOptIns(enabled)); !reflect.DeepEqual(got, want) {
		t.Fatalf("listed keys = %v, want %v", got, want)
	}
}
