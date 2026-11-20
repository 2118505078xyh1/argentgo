package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidScript 标记脚本化 provider 无法按原样重放的
// ErrInvalidScript 标记脚本化 provider 无法按原样重放的场景。调用方可用 errors.Is 分类加载时失败。
var ErrInvalidScript = errors.New("invalid scripted step")

// Validate 在运行开始前检查场景的脚本化模型轮次。
// Validate 在运行开始前检查场景的脚本化模型轮次。Parse（因此 Load）调用它，使手写文档中的拼写错误被报告时带有场景名和轮号，而不是稍后
// 报告时带有场景名和轮号，而不是稍后浮现为
// 以混乱的工具错误或失步的脚本形式浮现。
func (s Scenario) Validate() error {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = "unnamed"
	}
	for i, turn := range s.Provider {
		if err := validateTurn(turn); err != nil {
			return fmt.Errorf("scenario %q: turn %d: %w", name, i+1, err)
		}
	}
	return nil
}

// validateTurn 检查一条 ScriptTurn。当轮次无法产生可用的模型回复，或其两个缓存期望相互矛盾永远无法同时成立时，该轮次无效。
// 可用的模型回复，或其两个缓存期望相互矛盾
// 而永远无法同时成立。
func validateTurn(turn ScriptTurn) error {
	if turn.ExpectRequestCached && turn.ExpectRequestCacheFree {
		return fmt.Errorf("%w: expect_request_cached and expect_request_cache_free are mutually exclusive", ErrInvalidScript)
	}
	for i, call := range turn.Respond.ToolCalls {
		tool := strings.TrimSpace(call.Name)
		if tool == "" {
			return fmt.Errorf("%w: tool call %d has an empty name", ErrInvalidScript, i+1)
		}
		// ArgumentsString 已经解开内含 JSON 的 JSON 字符串，
		// 并把缺失值默认为 {}。其余必须是对象：
		// ArgumentsString 已经解开内含 JSON 的 JSON 字符串，并把缺失值默认为 {}。其余必须是对象：agent 的工具 runner 会把参数解码到结构体中。
		var args map[string]json.RawMessage
		if err := json.Unmarshal([]byte(call.ArgumentsString()), &args); err != nil {
			return fmt.Errorf("%w: tool call %d (%s): arguments are not a JSON object: %v",
				ErrInvalidScript, i+1, tool, err)
		}
	}
	return nil
}
