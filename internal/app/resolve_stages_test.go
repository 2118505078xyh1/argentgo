package app

import (
	"reflect"
	"testing"
)

func TestClassifyResolveField(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		stage ResolveStage
	}{
		// provider：模型、提供方、凭据、协议与线路。
		{"model", "Model", StageProvider},
		{"provider", "Provider", StageProvider},
		{"provider name camel", "providerName", StageProvider},
		{"provider name kebab", "provider-name", StageProvider},
		{"base url camel", "BaseURL", StageProvider},
		{"base url kebab", "base-url", StageProvider},
		{"api key", "APIKey", StageProvider},
		{"api key env", "apiKeyEnv", StageProvider},
		{"protocol", "Protocol", StageProvider},
		{"lowercase protocol", "protocol", StageProvider},
		{"fallback model", "FallbackModel", StageProvider},
		{"secondary model", "SecondaryModel", StageProvider},
		{"chatter", "Chatter", StageProvider},
		{"endpoint", "Endpoint", StageProvider},

		// pricing：价格与预算上限。
		{"price input", "PriceInput", StagePricing},
		{"price output", "PriceOutput", StagePricing},
		{"prices", "Prices", StagePricing},
		{"pricing word", "pricing", StagePricing},
		{"max cost", "MaxCost", StagePricing},
		{"max cost kebab", "max-cost", StagePricing},
		{"max cost usd", "MaxCostUSD", StagePricing},
		{"cost", "Cost", StagePricing},

		// session：工作区、会话存储与会话产物。
		{"session", "Session", StageSession},
		{"cwd", "Cwd", StageSession},
		{"root", "Root", StageSession},
		{"home", "Home", StageSession},
		{"storage", "Storage", StageSession},
		{"storage dir", "storageDir", StageSession},
		{"workspace", "Workspace", StageSession},
		{"add dirs", "AddDirs", StageSession},
		{"add dirs kebab", "add-dirs", StageSession},
		{"write roots", "WriteRoots", StageSession},
		{"output schema", "OutputSchema", StageSession},
		{"system", "System", StageSession},
		{"resume", "Resume", StageSession},
		{"title", "Title", StageSession},

		// hooks：钩子与各类事件；sessionStart/sessionEnd 的优先级高于 session。
		{"hook", "Hook", StageHooks},
		{"hooks", "Hooks", StageHooks},
		{"pre tool use", "PreToolUse", StageHooks},
		{"pre tool use kebab", "pre-tool-use", StageHooks},
		{"post tool use", "PostToolUse", StageHooks},
		{"notification", "Notification", StageHooks},
		{"stop", "Stop", StageHooks},
		{"pre compact", "PreCompact", StageHooks},
		{"subagent stop", "SubagentStop", StageHooks},
		{"session start", "SessionStart", StageHooks},
		{"session end", "SessionEnd", StageHooks},

		// config：其余字段与未知字段一律回退到 config。
		{"profile", "Profile", StageConfig},
		{"literal config", "config", StageConfig},
		{"max turns", "MaxTurns", StageConfig},
		{"max tokens", "MaxTokens", StageConfig},
		{"max parallel", "MaxParallel", StageConfig},
		{"sandbox mode", "SandboxMode", StageConfig},
		{"approval", "Approval", StageConfig},
		{"yolo", "Yolo", StageConfig},
		{"locale", "Locale", StageConfig},
		{"thinking", "Thinking", StageConfig},
		{"trust project", "TrustProject", StageConfig},
		{"plugins", "Plugins", StageConfig},
		{"overrides", "Overrides", StageConfig},
		{"cache tier", "cacheTier", StageConfig},
		{"auto compact tokens", "AutoCompactTokens", StageConfig},
		{"no auto compact", "NoAutoCompact", StageConfig},
		{"serve", "Serve", StageConfig},
		{"unknown", "bogus", StageConfig},
		{"empty", "", StageConfig},

		// 归一化：空白与 CJK。
		{"surrounding whitespace", "  Model  ", StageProvider},
		{"CJK price is unknown", "价格", StageConfig},
		{"CJK suffix after known prefix", "provider名称", StageProvider},
		{"CJK suffix after hook prefix", "Hook配置", StageHooks},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyResolveField(tt.key); got != tt.stage {
				t.Fatalf("ClassifyResolveField(%q) = %q, want %q", tt.key, got, tt.stage)
			}
		})
	}
}

func TestClassifyResolveFieldIsDeterministic(t *testing.T) {
	// 同一个键重复调用必须得到相同结果，且不依赖调用顺序或外部状态。
	for _, key := range []string{"Model", "价格", "SessionStart", "bogus"} {
		first := ClassifyResolveField(key)
		ClassifyResolveField("Hook")
		ClassifyResolveField("")
		if got := ClassifyResolveField(key); got != first {
			t.Fatalf("ClassifyResolveField(%q) 非确定性：%q != %q", key, got, first)
		}
	}
}

func TestGroupByStageGroupsAndPreservesOrder(t *testing.T) {
	keys := []string{
		"Profile",      // config
		"Model",        // provider
		"PriceInput",   // pricing
		"Session",      // session
		"Hook",         // hooks
		"MaxTurns",     // config
		"BaseURL",      // provider
		"MaxCost",      // pricing
		"Cwd",          // session
		"SessionStart", // hooks（优先级高于 session）
		"bogus",        // config
	}
	want := map[ResolveStage][]string{
		StageConfig:   {"Profile", "MaxTurns", "bogus"},
		StageProvider: {"Model", "BaseURL"},
		StagePricing:  {"PriceInput", "MaxCost"},
		StageSession:  {"Session", "Cwd"},
		StageHooks:    {"Hook", "SessionStart"},
	}
	got := GroupByStage(keys)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupByStage() = %#v, want %#v", got, want)
	}
}

func TestGroupByStageEmptyInput(t *testing.T) {
	if got := GroupByStage(nil); len(got) != 0 {
		t.Fatalf("GroupByStage(nil) = %#v, want empty map", got)
	}
	if got := GroupByStage([]string{}); len(got) != 0 {
		t.Fatalf("GroupByStage([]) = %#v, want empty map", got)
	}
}

func TestGroupByStageDoesNotModifyInput(t *testing.T) {
	keys := []string{"Model", "Profile", "价格", "Hook"}
	snapshot := append([]string(nil), keys...)

	got := GroupByStage(keys)
	if !reflect.DeepEqual(keys, snapshot) {
		t.Fatalf("GroupByStage 改动了入参：%#v, want %#v", keys, snapshot)
	}

	// 修改结果切片不应回写入参，也不应影响后续调用。
	got[StageProvider][0] = "MUTATED"
	if !reflect.DeepEqual(keys, snapshot) {
		t.Fatalf("修改分组结果影响了入参：%#v, want %#v", keys, snapshot)
	}
	again := GroupByStage(snapshot)
	if again[StageProvider][0] != "Model" {
		t.Fatalf("修改一次结果污染了后续调用：%#v", again)
	}
}

func TestStageOrder(t *testing.T) {
	want := []ResolveStage{StageConfig, StageProvider, StagePricing, StageSession, StageHooks}
	if got := StageOrder(); !reflect.DeepEqual(got, want) {
		t.Fatalf("StageOrder() = %#v, want %#v", got, want)
	}
}

func TestStageOrderReturnsFreshSlice(t *testing.T) {
	first := StageOrder()
	first[0] = "MUTATED"
	second := StageOrder()
	if second[0] != StageConfig {
		t.Fatalf("StageOrder 返回了共享切片：%#v", second)
	}
}
