package config

import "strings"

// 本文件解析分层配置树里的 `net` 块。配置层保持零依赖：这里只产出中性的
// 数据结构，由 app 装配层转换成 security.NetPolicy / NetworkGuard 注入。
//
// 解析刻意宽松（fail-closed）：类型不符或无法识别判定值的规则被跳过，绝不
// 让一份拼写错误的规则集使整个 agent 启动失败，也绝不放宽默认策略。七层
// 优先级由 config.Load 的 mergeTree 负责，本文件只消费最终合并的 Raw 树。

// NetRuleConfig 是一条声明式网络出口规则的中性配置形态。字段与
// security.NetRule 对齐，但 Decision 保持字符串，避免 config 依赖 security。
// 空 HostGlob 是通配符；Port 为零表示不限端口。
type NetRuleConfig struct {
	Decision     string
	HostGlob     string
	Port         int
	Protocol     string
	Method       string
	RequireHuman bool
}

// NetConfig 是 `net` 块的解析结果。Rules 为空时等价于未配置：只保留内置
// 的私网/云元数据拒绝（与历史行为一致）。
type NetConfig struct {
	Rules []NetRuleConfig
}

// ParseNetConfig 从合并后的配置树里解析 `net` 块。raw 为 nil 或缺少该键时
// 返回零值，因此未配置网络的运行逐字节不变。
//
// 接受两种写法：
//
//	"net": { "rules": [ { "decision": "deny", "hostGlob": "*.evil.example" } ] }
//	"net": [ { "decision": "deny", "hostGlob": "*.evil.example" } ]  // 简写
func ParseNetConfig(raw map[string]any) NetConfig {
	var out NetConfig
	if raw == nil {
		return out
	}
	block, ok := raw["net"]
	if !ok || block == nil {
		return out
	}
	// net: false / true 都等价于未配置（规则为空），布尔形态不含规则。
	if _, ok := block.(bool); ok {
		return out
	}
	var rules []any
	switch x := block.(type) {
	case map[string]any:
		if v, ok := x["rules"]; ok {
			rules, _ = v.([]any)
		}
	case []any:
		rules = x
	}
	for _, item := range rules {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		decision := strings.ToLower(strings.TrimSpace(stringValue(entry, "decision")))
		// 只接受 allow / ask / deny；其它值（含空）丢弃该规则，fail-closed。
		if decision != "allow" && decision != "ask" && decision != "deny" {
			continue
		}
		out.Rules = append(out.Rules, NetRuleConfig{
			Decision:     decision,
			HostGlob:     stringValue(entry, "hostGlob", "host_glob", "host"),
			Port:         intValue(entry, "port"),
			Protocol:     strings.ToLower(strings.TrimSpace(stringValue(entry, "protocol"))),
			Method:       strings.ToUpper(strings.TrimSpace(stringValue(entry, "method"))),
			RequireHuman: boolValue(entry, "requireHuman", "require_human"),
		})
	}
	return out
}

// stringValue 从 map 里读取第一个存在的字符串值。
func stringValue(tree map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := tree[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// intValue 从 map 里读取整数值（兼容 JSON 的 float64 与 Go 的 int）。
func intValue(tree map[string]any, key string) int {
	switch v := tree[key].(type) {
	case float64:
		if v == float64(int(v)) {
			return int(v)
		}
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// boolValue 从 map 里读取布尔值（兼容真值字符串）。
func boolValue(tree map[string]any, keys ...string) bool {
	for _, key := range keys {
		if v, ok := tree[key]; ok {
			return truthy(v)
		}
	}
	return false
}
