package provider

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed model_policy.json
var modelPolicyJSON []byte

// modelPolicy 是数据驱动的模型策略覆盖表。
type modelPolicy struct {
	Exact  map[string]int `json:"exact"`
	Family map[string]int `json:"family"`
}

var embeddedModelPolicy = loadModelPolicy(modelPolicyJSON)

// loadModelPolicy 解析 JSON；非法内容回退为空策略（不影响内置表）。
func loadModelPolicy(data []byte) modelPolicy {
	var p modelPolicy
	if json.Unmarshal(data, &p) != nil {
		return modelPolicy{}
	}
	if p.Exact == nil {
		p.Exact = map[string]int{}
	}
	if p.Family == nil {
		p.Family = map[string]int{}
	}
	return p
}

// policyWindow 按 exact > family 优先级查询数据驱动窗口；未命中返回 0。
func policyWindow(model string) int {
	name := normalizeWindowModel(model)
	if name == "" {
		return 0
	}
	if w, ok := embeddedModelPolicy.Exact[name]; ok && w > 0 {
		return w
	}
	// family 前缀按最长匹配，避免短前缀抢先命中。
	best, bestLen := 0, 0
	for prefix, w := range embeddedModelPolicy.Family {
		if w > 0 && strings.HasPrefix(name, prefix) && len(prefix) > bestLen {
			best, bestLen = w, len(prefix)
		}
	}
	return best
}
