package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// DiagnosticKind 对配置诊断进行分类。三种类型对应
// 参考实现报告的：无法执行的值（invalid）、一致但冲突的两个键
// （conflict），以及可识别但不受支持的旧设置
// （unsupported）。
type DiagnosticKind string

const (
	DiagnosticInvalid     DiagnosticKind = "invalid"
	DiagnosticConflict    DiagnosticKind = "conflict"
	DiagnosticUnsupported DiagnosticKind = "unsupported"
)

// Diagnostic 是检查已解析 Config 时发现的一个问题。Path 是
// 类似 "$.sandboxMode" 或 "$.providers.anthropic.baseURL" 的 JSON 路径。
// 与 Validate 不同，Diagnostics 不会在第一个问题处停止。
type Diagnostic struct {
	Path    string         `json:"path"`
	Kind    DiagnosticKind `json:"kind"`
	Message string         `json:"message"`
}

// Diagnostics 一次报告它能在 c 中发现的所有问题。三组
// 保持固定顺序：Validate 一直报告的旧值级失败、Load/启动强制执行的
// fail-closed 不变量，以及最后在合并后的 Raw 树中发现的
// 提示性问题。这是 CLI doctor 和启动可以消费的唯一
// "fail-closed 摘要"。
func (c Config) Diagnostics() []Diagnostic {
	out := c.validateDiagnostics()
	out = append(out, c.failClosedDiagnostics()...)
	return append(out, treeDiagnostics(c.Raw)...)
}

// failClosedDiagnostics 报告调用方不得忽略的安全关键不变量。
// 与 treeDiagnostics（仅解释原始键如何被解读）不同，
// 这里的每个问题都在加载时由 LoadWithOverrides 强制执行，
// 因此 doctor、启动和显式 Validate 对何种配置不可接受
// 保持一致。集中收集保持了 fail-closed 姿态：
// 报告一个坏值的同时不会掩盖其他问题。
func (c Config) failClosedDiagnostics() []Diagnostic {
	var out []Diagnostic

	// privacyMode 会导致加载失败，因为安全层对未知姿态采取 fail closed。
	// 手工构造或在 Load 之后被修改的 Config 绕过了
	// 该关卡，因此这里实施完全相同的规则。
	if err := validatePrivacyMode(c.PrivacyMode); err != nil {
		out = append(out, Diagnostic{
			Path:    "$.privacyMode",
			Kind:    DiagnosticInvalid,
			Message: err.Error(),
		})
	}

	// 程序其余部分无法执行的数值范围。百分比超过
	// 100 会悄悄禁用压缩，负值则总是拼写错误。
	if c.AutoCondenseContextPercent < -1 || c.AutoCondenseContextPercent > 100 {
		out = append(out, Diagnostic{
			Path:    "$.autoCondenseContextPercent",
			Kind:    DiagnosticInvalid,
			Message: fmt.Sprintf("autoCondenseContextPercent must be -1 or 0..100, got %d", c.AutoCondenseContextPercent),
		})
	}
	if c.ContextWindow < 0 {
		out = append(out, Diagnostic{
			Path:    "$.contextWindow",
			Kind:    DiagnosticInvalid,
			Message: fmt.Sprintf("contextWindow must be >= 0, got %d", c.ContextWindow),
		})
	}
	if c.VerifyTimeoutMS < 0 {
		out = append(out, Diagnostic{
			Path:    "$.verifyTimeoutMS",
			Kind:    DiagnosticInvalid,
			Message: fmt.Sprintf("verifyTimeoutMS must be >= 0, got %d", c.VerifyTimeoutMS),
		})
	}
	if c.VerifyMaxReflections < 0 {
		out = append(out, Diagnostic{
			Path:    "$.verifyMaxReflections",
			Kind:    DiagnosticInvalid,
			Message: fmt.Sprintf("verifyMaxReflections must be >= 0, got %d", c.VerifyMaxReflections),
		})
	}

	return out
}

// validateDiagnostics 是 Validate 一直报告的硬性失败的精确集合。
// 将其独立保存，可防止新增的提示性诊断改变
// Validate 的兼容性约定。
func (c Config) validateDiagnostics() []Diagnostic {
	var out []Diagnostic

	switch c.SandboxMode {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
	default:
		out = append(out, Diagnostic{
			Path: "$.sandboxMode",
			Kind: DiagnosticInvalid,
			Message: fmt.Sprintf("sandboxMode %q must be one of %s, %s, %s",
				c.SandboxMode, SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess),
		})
	}
	if strings.TrimSpace(c.Model) == "" {
		out = append(out, Diagnostic{
			Path: "$.model", Kind: DiagnosticInvalid, Message: "model must not be empty",
		})
	}
	if c.MaxTurns < 0 {
		out = append(out, Diagnostic{
			Path:    "$.maxTurns",
			Kind:    DiagnosticInvalid,
			Message: fmt.Sprintf("maxTurns must be >= 0, got %d", c.MaxTurns),
		})
	}

	return out
}

// configAliases 列出所有可识别的键家族。第一项是
// decode() 优先使用的规范拼写，其余为可接受的别名。
var configAliases = map[string][]string{
	"model":       {"model", "modelName"},
	"baseURL":     {"baseURL", "baseUrl", "base_url"},
	"apiKey":      {"apiKey", "api_key"},
	"protocol":    {"protocol"},
	"locale":      {"locale"},
	"sandboxMode": {"sandboxMode", "sandbox_mode"},
	"maxTurns":    {"maxTurns", "max_turns"},
	"yolo":        {"yolo"},
	"storageDir":  {"storageDir", "storage_dir"},
	"provider":    {"provider"},
}

// unsupportedKeys 是 argentgo 有意忽略的可识别旧设置。
// 未知键会保留在 Raw 中并保持有效；只把这些键
// 标记出来，与参考实现的 unsupported 列表一致。
var unsupportedKeys = []string{"logLevel", "subagent_depth", "layout", "server"}

func treeDiagnostics(raw map[string]any) []Diagnostic {
	if len(raw) == 0 {
		return nil
	}
	var out []Diagnostic

	canonical := make([]string, 0, len(configAliases))
	for key := range configAliases {
		canonical = append(canonical, key)
	}
	sort.Strings(canonical)

	for _, field := range canonical {
		aliases := configAliases[field]
		present := make([]string, 0, len(aliases))
		for _, key := range aliases {
			if _, ok := raw[key]; ok {
				present = append(present, key)
			}
		}
		if len(present) == 0 {
			continue
		}

		// decode() 按顺序读取别名并返回第一个类型正确的值，
		// 跳过类型错误的规范键。这里照搬该规则，使
		// "using X" 消息指明确实进入 Config 的值；
		// present[0] 仅当所有别名类型都错误时作为回退。
		winner := present[0]
		for _, key := range present {
			if configValueTypeOK(field, raw[key]) {
				winner = key
				break
			}
		}
		winnerValue := raw[winner]
		for _, key := range present {
			if !configValueTypeOK(field, raw[key]) {
				out = append(out, Diagnostic{
					Path:    "$." + key,
					Kind:    DiagnosticInvalid,
					Message: fmt.Sprintf("%s must be %s, got %T", key, expectedType(field), raw[key]),
				})
			}
		}
		if len(present) > 1 {
			for _, key := range present {
				if key == winner {
					continue
				}
				if !reflect.DeepEqual(raw[key], winnerValue) {
					out = append(out, Diagnostic{
						Path: "$." + field,
						Kind: DiagnosticConflict,
						Message: fmt.Sprintf("conflicting values for %s; using %s",
							strings.Join(present, ", "), winner),
					})
					break
				}
			}
		}
	}

	for _, key := range unsupportedKeys {
		if _, ok := raw[key]; ok {
			// 对已知废弃键给出可操作的迁移建议（改成什么/删除什么），
			// 而不是只说 "ignored"；表中未登记时保留旧的兜底文案。
			message := "unsupported legacy setting; ignored"
			if hint := deprecatedHint(key); hint != "" {
				message = hint
			}
			out = append(out, Diagnostic{
				Path:    "$." + key,
				Kind:    DiagnosticUnsupported,
				Message: message,
			})
		}
	}
	return out
}

func expectedType(field string) string {
	switch field {
	case "maxTurns":
		return "an integer"
	case "yolo":
		return "a boolean"
	default:
		return "a string"
	}
}

func configValueTypeOK(field string, value any) bool {
	switch field {
	case "maxTurns":
		switch typed := value.(type) {
		case float64, int, int64, json.Number:
			return true
		case string:
			_, err := strconv.Atoi(strings.TrimSpace(typed))
			return err == nil
		}
		return false
	case "yolo":
		if _, ok := value.(bool); ok {
			return true
		}
		if text, ok := value.(string); ok {
			_, err := parseBool(text)
			return err == nil
		}
		return false
	default:
		_, ok := value.(string)
		return ok
	}
}
