package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// codeModeEnabled 默认开启；ARGENT_CODE_MODE=0 紧急回退。
func codeModeEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_CODE_MODE")) != "0"
}

// Code Mode DSL（清单 #63）：模型输出一段受约束脚本，一次调用里顺序执行
// 多个工具，减少往返。它不是通用脚本语言——没有循环、条件、函数或任意
// 代码执行；只有有序的 tool 调用序列，可用 {{steps.N.output}} 引用前序结果。
//
// 所有执行都经由注入的 run 回调（agent 的 callTool），因此每一步仍走
// 完整的审批/策略/审计链。步数、引用深度与输出总长都有硬上限。

var (
	ErrCodeMode     = errors.New("code mode error")
	ErrCodeModeStep = fmt.Errorf("%w: invalid step", ErrCodeMode)
)

const (
	codeModeMaxSteps      = 32
	codeModeMaxOutput     = 256 * 1024
	codeModeMaxStepOutput = 64 * 1024
)

// CodeModeStep 是脚本里的一步：调一个已注册工具，参数里的字符串支持
// {{steps.N.output}} 插值。
type CodeModeStep struct {
	Tool string `json:"tool"`
	Args any    `json:"args"`
	// As 给该步结果一个名字，可用 {{vars.name}} 引用（可选）。
	As string `json:"as,omitempty"`
}

// parseCodeModeScript 把工具参数解析为步骤序列。
// 接受两种形态：
//   - {"steps":[{tool,args,as},...]}
//   - 直接数组 [{tool,args,as},...]
//
// 限制在纯函数里完成，不执行任何工具。
func parseCodeModeScript(raw string) ([]CodeModeStep, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty script", ErrCodeMode)
	}
	var steps []CodeModeStep
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &steps); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCodeMode, err)
		}
	} else {
		var wrapper struct {
			Steps []CodeModeStep `json:"steps"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCodeMode, err)
		}
		steps = wrapper.Steps
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: script has no steps", ErrCodeMode)
	}
	if len(steps) > codeModeMaxSteps {
		return nil, fmt.Errorf("%w: too many steps (%d > %d)", ErrCodeMode, len(steps), codeModeMaxSteps)
	}
	for i, step := range steps {
		if strings.TrimSpace(step.Tool) == "" {
			return nil, fmt.Errorf("%w %d: empty tool name", ErrCodeModeStep, i)
		}
	}
	return steps, nil
}

// stepOutput 保存一步的结果，供后续插值。
type stepOutput struct {
	Output string
	As     string
}

// interpolateCodeMode 把 {{steps.N.output}} / {{vars.name}} 替换为实际值。
// 未识别的占位符原样保留（让工具/模型看到并纠正），不报错、不执行代码。
func interpolateCodeMode(s string, steps []stepOutput) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		open := strings.Index(s[i:], "{{")
		if open < 0 {
			b.WriteString(s[i:])
			break
		}
		open += i
		b.WriteString(s[i:open])
		close := strings.Index(s[open:], "}}")
		if close < 0 {
			b.WriteString(s[open:])
			break
		}
		close += open
		key := strings.TrimSpace(s[open+2 : close])
		value, ok := lookupCodeModeRef(key, steps)
		if ok {
			b.WriteString(truncateCodeModeValue(value))
		} else {
			b.WriteString(s[open : close+2]) // 未识别占位符原样保留
		}
		i = close + 2
	}
	return b.String()
}

func lookupCodeModeRef(key string, steps []stepOutput) (string, bool) {
	if strings.HasPrefix(key, "steps.") && strings.HasSuffix(key, ".output") {
		mid := strings.TrimSuffix(strings.TrimPrefix(key, "steps."), ".output")
		var n int
		if _, err := fmt.Sscanf(mid, "%d", &n); err != nil || n < 0 || n >= len(steps) {
			return "", false
		}
		return steps[n].Output, true
	}
	if strings.HasPrefix(key, "vars.") {
		name := strings.TrimPrefix(key, "vars.")
		for _, step := range steps {
			if step.As == name {
				return step.Output, true
			}
		}
	}
	return "", false
}

// truncateCodeModeValue 限制单个插值值的长度，避免展开炸弹。
func truncateCodeModeValue(s string) string {
	if len(s) <= codeModeMaxStepOutput {
		return s
	}
	return s[:codeModeMaxStepOutput] + "\n[code mode: interpolated value truncated]"
}

// interpolateArgs 递归插值参数里的字符串。
func interpolateArgs(value any, steps []stepOutput) any {
	switch v := value.(type) {
	case string:
		return interpolateCodeMode(v, steps)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = interpolateArgs(item, steps)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = interpolateArgs(item, steps)
		}
		return out
	default:
		return value
	}
}
