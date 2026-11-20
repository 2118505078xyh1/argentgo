package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Problem 描述一条格式错误的 mcpServers 条目或字段。DecodeConfig
// 会跳过坏条目（并忽略坏字段），而不是让整个配置失败，
// 并在这里报告它们，供想打印警告的调用方使用。
type Problem struct {
	// Server 是服务器名称，或诸如 "#2" 的位置标签。
	Server string
	// Field 是有问题的键，条目级问题为空。
	Field string
	// Reason 是易读的解释。
	Reason string
}

// Error 实现 error，使 Problem 可以原样传给应用的 fail-soft
// 提示写入器。
func (p Problem) Error() string {
	if p.Field == "" {
		return fmt.Sprintf("mcpServers %q: %s", p.Server, p.Reason)
	}
	return fmt.Sprintf("mcpServers %q.%s: %s", p.Server, p.Field, p.Reason)
}

// DecodeConfig 把合并后配置的 `mcpServers` 对象（也接受列表形式）
// 解码为 spec。权限字段同时读取 camelCase 和 snake_case 拼写。
// spec 和 problem 按名称/字段排序，使输出稳定，
// 不受 Go map 迭代顺序影响。
//
// 不是对象的条目或没有名称的条目会被跳过并报告
// 在 problems 中。类型错误的字段会被忽略、报告，而该条目的
// 其余部分仍会被解码。既没有 command 也没有 URL 的条目仍是
// 一个 spec；使用服务器时 Connect 会报告缺失的目标。
func DecodeConfig(raw map[string]any) ([]Spec, []Problem) {
	var specs []Spec
	var problems []Problem
	if raw == nil {
		return nil, nil
	}
	value, exists := raw["mcpServers"]
	if !exists {
		return nil, nil
	}
	problem := func(server, field, reason string) {
		problems = append(problems, Problem{Server: server, Field: field, Reason: reason})
	}
	add := func(name string, body map[string]any) {
		name = strings.TrimSpace(name)
		if name == "" {
			problem(name, "", "missing server name; entry skipped")
			return
		}
		spec := Spec{Name: name}
		if v, key, ok := pick(body, "command"); ok {
			if text, valid := v.(string); valid {
				spec.Command = strings.TrimSpace(text)
			} else {
				problem(name, key, fmt.Sprintf("want a string, got %T; ignored", v))
			}
		}
		if v, key, ok := pick(body, "url"); ok {
			if text, valid := v.(string); valid {
				spec.URL = strings.TrimSpace(text)
			} else {
				problem(name, key, fmt.Sprintf("want a string, got %T; ignored", v))
			}
		}
		if v, key, ok := pick(body, "transport"); ok {
			if text, valid := v.(string); valid {
				spec.Transport = strings.TrimSpace(text)
			} else {
				problem(name, key, fmt.Sprintf("want a string, got %T; ignored", v))
			}
		}
		if v, key, ok := pick(body, "args"); ok {
			list, valid := decodeStrings(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an array of strings, got %T; ignored", v))
			} else {
				spec.Args = list
			}
		}
		if v, key, ok := pick(body, "env"); ok {
			list, valid := decodeEnv(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an object or array of strings, got %T; ignored", v))
			} else {
				spec.Env = list
			}
		}
		if v, key, ok := pick(body, "headers"); ok {
			headers, valid := decodeHeaders(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an object of strings, got %T; ignored", v))
			} else {
				spec.Headers = headers
			}
		}
		if v, key, ok := pick(body, "timeout"); ok {
			seconds, valid := decodeSeconds(v)
			if !valid || seconds <= 0 {
				problem(name, key, "want a positive number of seconds; ignored")
			} else {
				spec.Timeout = time.Duration(seconds * float64(time.Second))
			}
		}
		if v, key, ok := pick(body, "discoveryTimeout", "discovery_timeout"); ok {
			seconds, valid := decodeSeconds(v)
			if !valid || seconds <= 0 {
				problem(name, key, "want a positive number of seconds; ignored")
			} else {
				// 原样存储；钳制在 EffectiveDiscoveryTimeout 中进行，使
				// 编程式构造的 Spec 与解码得到的 Spec 有相同的界限。
				spec.DiscoveryTimeout = time.Duration(seconds * float64(time.Second))
			}
		}
		if v, key, ok := pick(body, "enabled", "Enabled"); ok {
			flag, valid := decodeBool(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want a boolean, got %T; ignored", v))
			} else {
				spec.Enabled = &flag
			}
		}
		if v, key, ok := pick(body, "readOnly", "read_only"); ok {
			flag, valid := decodeBool(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want a boolean, got %T; ignored", v))
			} else {
				spec.ReadOnly = flag
			}
		}
		if v, key, ok := pick(body, "allowedTools", "allowed_tools"); ok {
			list, valid := decodeStrings(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an array of strings, got %T; ignored", v))
			} else {
				spec.AllowedTools = list
			}
		}
		if v, key, ok := pick(body, "deniedTools", "denied_tools"); ok {
			list, valid := decodeStrings(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an array of strings, got %T; ignored", v))
			} else {
				spec.DeniedTools = list
			}
		}
		specs = append(specs, spec)
	}

	switch value := value.(type) {
	case map[string]any:
		for name, entry := range value {
			body, ok := entry.(map[string]any)
			if !ok {
				problem(name, "", fmt.Sprintf("entry must be an object, got %T; skipped", entry))
				continue
			}
			add(name, body)
		}
	case []any:
		for i, entry := range value {
			body, ok := entry.(map[string]any)
			if !ok {
				problem(fmt.Sprintf("#%d", i+1), "", fmt.Sprintf("entry must be an object, got %T; skipped", entry))
				continue
			}
			var name string
			if v, _, found := pick(body, "name"); found {
				name, _ = v.(string)
			}
			if strings.TrimSpace(name) == "" {
				name = fmt.Sprintf("server-%d", i+1)
			}
			add(name, body)
		}
	default:
		problem("", "", fmt.Sprintf("mcpServers must be an object or array, got %T; skipped", value))
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Server != problems[j].Server {
			return problems[i].Server < problems[j].Server
		}
		if problems[i].Field != problems[j].Field {
			return problems[i].Field < problems[j].Field
		}
		return problems[i].Reason < problems[j].Reason
	})
	return specs, problems
}

// SpecsFromConfig 是应用层调用的薄解码器，取代其
// 原先本地的 mcpSpecsFromConfig 辅助函数。它只返回 spec；当被跳过条目的
// 问题需要呈现给用户时，使用 DecodeConfig。
func SpecsFromConfig(raw map[string]any) []Spec {
	specs, _ := DecodeConfig(raw)
	return specs
}

// pick 返回第一个存在的键，使配置同时声明两种拼写时
// camelCase 胜过 snake_case。
func pick(body map[string]any, keys ...string) (any, string, bool) {
	for _, key := range keys {
		if v, ok := body[key]; ok {
			return v, key, true
		}
	}
	return nil, "", false
}

func decodeBool(v any) (bool, bool) {
	switch value := v.(type) {
	case bool:
		return value, true
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func decodeStrings(v any) ([]string, bool) {
	switch value := v.(type) {
	case []string:
		return append([]string(nil), value...), true
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, strings.TrimSpace(text))
		}
		return out, true
	case string:
		return []string{strings.TrimSpace(value)}, true
	}
	return nil, false
}

func decodeEnv(v any) ([]string, bool) {
	switch value := v.(type) {
	case map[string]any:
		out := make([]string, 0, len(value))
		for key, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, key+"="+text)
		}
		sort.Strings(out)
		return out, true
	case map[string]string:
		out := make([]string, 0, len(value))
		for key, text := range value {
			out = append(out, key+"="+text)
		}
		sort.Strings(out)
		return out, true
	case []any, []string:
		return decodeStrings(v)
	}
	return nil, false
}

func decodeHeaders(v any) (map[string]string, bool) {
	entries, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(entries))
	for key, item := range entries {
		text, valid := item.(string)
		if !valid {
			return nil, false
		}
		out[key] = text
	}
	return out, true
}

func decodeSeconds(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		n, err := value.Float64()
		return n, err == nil
	case string:
		var n float64
		_, err := fmt.Sscanf(strings.TrimSpace(value), "%f", &n)
		return n, err == nil
	}
	return 0, false
}
