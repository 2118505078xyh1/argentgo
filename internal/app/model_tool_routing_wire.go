package app

import (
	"strings"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
)

// 本文件实现 #49：按 provider/model/模式动态收紧工具目录。它在工具目录装配后、
// 系统提示生成前应用（接线在 Options.FilterSpecs），只做减法，绝不新增工具。

// ToolRoutingRule 是一条基于 provider/model 子串与模式的工具收紧规则。
// ProviderIncludes / ModelIncludes 大小写不敏感；任一命中即视为该规则适用。
// Mode 为空表示任意模式，否则需与当前模式相等（大小写不敏感）。
type ToolRoutingRule struct {
	ProviderIncludes []string
	ModelIncludes    []string
	Mode             string
	EnableTools      []string
	DisableTools     []string
}

// defaultToolRoutingRules 是内置规则表。当前只有一条：OpenAI 原生/gpt/codex 在 act
// 模式下偏好 apply_patch，禁用 editor。规则只收紧目录，apply_patch 仍须由 opt-in
// 注册后才存在（不在本文件里新增工具）。
var defaultToolRoutingRules = []ToolRoutingRule{
	{
		ProviderIncludes: []string{"openai-native", "codex", "openai"},
		ModelIncludes:    []string{"gpt", "codex"},
		Mode:             "act",
		EnableTools:      []string{"apply_patch"},
		DisableTools:     []string{"editor"},
	},
}

// ResolveModelToolRouting 按规则表过滤工具目录。返回的新切片只含输入 spec，顺序不变；
// 没有规则命中时返回原切片（逐字节不变）。EnableTools 中的名字永远不会因规则被移除，
// 但本函数也从不会凭空加入不存在的工具——这就是「只收紧不新增」。
func ResolveModelToolRouting(specs []llm.ToolSpec, providerID, modelID, mode string) []llm.ToolSpec {
	if len(specs) == 0 || len(defaultToolRoutingRules) == 0 {
		return specs
	}
	var enabled, disabled map[string]bool
	for _, rule := range defaultToolRoutingRules {
		if !ruleMatches(rule, providerID, modelID, mode) {
			continue
		}
		if enabled == nil {
			enabled, disabled = make(map[string]bool), make(map[string]bool)
		}
		for _, name := range rule.EnableTools {
			enabled[strings.ToLower(name)] = true
		}
		for _, name := range rule.DisableTools {
			disabled[strings.ToLower(name)] = true
		}
	}
	if len(disabled) == 0 {
		return specs
	}
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		name := strings.ToLower(spec.Function.Name)
		if disabled[name] && !enabled[name] {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// ruleMatches 判断一条规则是否对当前 provider/model/mode 生效。
func ruleMatches(rule ToolRoutingRule, providerID, modelID, mode string) bool {
	if rule.Mode != "" && !strings.EqualFold(strings.TrimSpace(rule.Mode), strings.TrimSpace(mode)) {
		return false
	}
	if len(rule.ProviderIncludes) == 0 && len(rule.ModelIncludes) == 0 {
		return false
	}
	provider := strings.ToLower(providerID)
	model := strings.ToLower(modelID)
	for _, part := range rule.ProviderIncludes {
		if part != "" && strings.Contains(provider, strings.ToLower(part)) {
			return true
		}
	}
	for _, part := range rule.ModelIncludes {
		if part != "" && strings.Contains(model, strings.ToLower(part)) {
			return true
		}
	}
	return false
}

// wireModelToolRouting 是 FilterSpecs 里的接线点：act/plan 取自既有 plan mode 判定，
// provider/model 取自本次 Resolved。父级在 filterVisibleSpecs 之后调用它即可。
func wireModelToolRouting(specs []llm.ToolSpec, res Resolved, stdio cli.IO) []llm.ToolSpec {
	mode := "act"
	if planModeEnabled(stdio.Env, res) {
		mode = "plan"
	}
	return ResolveModelToolRouting(specs, res.Provider, res.Model, mode)
}
