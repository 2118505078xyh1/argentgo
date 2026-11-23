package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 本文件实现 #47 的 switch_model 工具。工具本身只做白名单校验与结果回灌，
// 真正的模型切换由宿主注入的 SwitchModelApplier 完成（tools 不依赖 app/llm）。
// 默认（未显式注册）时工具不在目录里，逐字节不变。

// SwitchModelApplier 把具名 profile 切换为当前运行的主模型，返回切换后的实际模型名。
// 宿主负责审计事件与失败回滚；本包只消费返回值。
type SwitchModelApplier func(profileName, reason string) (activeModel string, err error)

// switchModelTool 是 switch_model 的实现。profiles 为宿主配置的 profile 白名单。
type switchModelTool struct {
	profiles []string
	apply    SwitchModelApplier
}

func (switchModelTool) Name() string { return "switch_model" }

func (switchModelTool) Description() string {
	return "Switch the active model to a configured profile for the next step. Provide profile_name and explain why that profile is better suited. Only profiles declared in the session configuration are accepted."
}

func (switchModelTool) Schema() string {
	return `{"type":"object","properties":{"profile_name":{"type":"string","description":"name of a configured profile to switch to"},"reason":{"type":"string","description":"why this profile is more suitable for the next step"}},"required":["profile_name","reason"]}`
}

// Mutating 返回 false：切换模型不改变工作区文件，不需要走文件修改审批链。
func (switchModelTool) Mutating() bool { return false }

func (t switchModelTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		ProfileName string `json:"profile_name"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("switch_model: bad arguments: %w", err)
	}
	profile := strings.TrimSpace(args.ProfileName)
	if profile == "" {
		return "", fmt.Errorf("switch_model: profile_name is required")
	}
	if strings.TrimSpace(args.Reason) == "" {
		return "", fmt.Errorf("switch_model: reason is required (explain why this profile fits the next step)")
	}
	if !t.allowed(profile) {
		return "", fmt.Errorf("switch_model: profile %q is not in the allowlist (available: %s)", profile, strings.Join(t.sortedProfiles(), ", "))
	}
	if t.apply == nil {
		return "", fmt.Errorf("switch_model: host did not configure a model switcher")
	}
	active, err := t.apply(profile, args.Reason)
	if err != nil {
		return "", fmt.Errorf("switch_model: %w", err)
	}
	out, _ := json.Marshal(map[string]string{
		"profile_name": profile,
		"active_model": active,
		"reason":       strings.TrimSpace(args.Reason),
	})
	return string(out), nil
}

// allowed 以大小写不敏感的方式匹配白名单。
func (t switchModelTool) allowed(name string) bool {
	for _, profile := range t.profiles {
		if strings.EqualFold(strings.TrimSpace(profile), name) {
			return true
		}
	}
	return false
}

func (t switchModelTool) sortedProfiles() []string {
	out := make([]string, 0, len(t.profiles))
	for _, profile := range t.profiles {
		out = append(out, strings.TrimSpace(profile))
	}
	sort.Strings(out)
	return out
}

// RegisterSwitchModel 把 switch_model 工具显式追加到 r（默认注册，调用方决定是否注册）。
// 它是幂等的：重复调用或已存在同名工具时不再插入；nil 注册表为空操作。
func RegisterSwitchModel(r *Registry, profiles []string, apply SwitchModelApplier) {
	if r == nil {
		return
	}
	const name = "switch_model"
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = switchModelTool{profiles: append([]string(nil), profiles...), apply: apply}
	r.order = append(r.order, name)
}
