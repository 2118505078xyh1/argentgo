// Package eval 对脚本化模型运行确定性的 agent 场景。
//
// 单元测试证明函数返回它应返回的内容。它不证明 agent、其工具和其策略在修改后仍彼此一致。
// 这正是 eval 的用途：一组场景，每个带有脚本化模型，
// 针对真实循环运行并检查实际发生的事情。
//
// 这正是 eval 的用途：一组场景，每个带有脚本化模型，针对真实循环运行并检查实际发生的事情。
// 模型是脚本化的，因此 eval 是确定的且无成本。循环及其工具是真实的，因此损坏的工具会让 eval 失败。
//
//	{
//	  "scenarios": [{
//	    "name": "reads a file before answering",
//	    "prompt": "what is in notes.txt?",
//	    "files": {"notes.txt": "the release notes are here\n"},
//	    "provider": [
//	      {"respond": {"tool_calls": [{"name": "read", "arguments": {"path": "notes.txt"}}]}},
//	      {"expect_request_contains": ["release notes"],
//	       "respond": {"text": "notes.txt says the release notes are here"}}
//	    ],
//	    "expect": {"tool_calls": ["read"], "text_contains": ["release notes"], "turns": 2},
//	    "check": {"files_exist": ["notes.txt"], "file_contains": {"notes.txt": "release"}}
//	  }]
//	}
//
// 本包不导入 agent：调用方注入 RunFunc，因此同一场景可以驱动任何循环实现。
// 因此同一场景可以驱动任何循环实现。
package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"example.com/argentgo/internal/llm"
)

// ToolCall 是脚本化模型请求的一次工具调用。
type ToolCall struct {
	Name string `json:"name"`
	// Arguments 是原始 JSON。也接受内含 JSON 的 JSON 字符串（"{\"path\":\"a\"}"），因为脚本是手写的。
	// 也接受，因为脚本是手写的。
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// UnmarshalJSON 接受对象或 JSON 字符串形式的参数。
func (c *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Name = raw.Name
	c.Arguments = raw.Arguments
	if trimmed := bytes.TrimSpace(c.Arguments); len(trimmed) > 0 && trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(trimmed, &inner); err == nil {
			c.Arguments = json.RawMessage(inner)
		}
	}
	return nil
}

// ArgumentsString 返回原始参数，默认为空对象。
func (c ToolCall) ArgumentsString() string {
	trimmed := string(bytes.TrimSpace(c.Arguments))
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

// Respond 是脚本化模型为一轮返回的消息。
type Respond struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Usage 是这一轮的计费用量，包括任何 cached_tokens 计数。
	Usage *llm.Usage `json:"usage,omitempty"`
}

// ScriptTurn 是一条脚本化模型回复以及关于触发它的请求的可选期望。
// 触发它的请求。
type ScriptTurn struct {
	// ExpectRequestContains 列出必须出现在发给模型的对话中的子串：消息文本、工具名、工具参数
	// 发给模型的对话中的子串：消息文本、工具名、工具参数
	// 和工具结果。这是场景断言工具结果确实
	// 和工具结果。这是场景断言工具结果确实回到上下文的方式。
	ExpectRequestContains []string `json:"expect_request_contains,omitempty"`
	// ExpectRequestCacheFree 当请求中任何消息带有稳定前缀缓存标记时失败（提示缓存选择退出情形）。
	// 稳定前缀缓存标记时失败（提示缓存选择退出情形）。
	ExpectRequestCacheFree bool `json:"expect_request_cache_free,omitempty"`
	// ExpectRequestCached 当没有消息带有缓存标记时失败。
	ExpectRequestCached bool    `json:"expect_request_cached,omitempty"`
	Respond             Respond `json:"respond"`
}

// Expect 是场景期望已发生的事情。
type Expect struct {
	// ToolCalls 是精确的工具序列，按顺序。nil 表示"不检查"；空（非 nil）列表表示"不得运行任何工具"。
	// "不检查"；空（非 nil）列表表示"不得运行任何工具"。
	ToolCalls []string `json:"tool_calls"`
	// TextContains 列出最终答案必须包含的子串。
	TextContains []string `json:"text_contains,omitempty"`
	// TextNotContains 列出最终答案不得包含的子串。
	TextNotContains []string `json:"text_not_contains,omitempty"`
	// Turns 是期望的模型轮数，由 harness 从脚本化 provider 计数，而非由循环报告。
	// 由脚本化 provider 计数，而非由循环报告。
	Turns *int `json:"turns,omitempty"`
	// Stopped 说明运行是否被模型完成以外的事情停止：轮数上限、策略拦截、取消。
	// 模型完成以外的事情：轮数上限、策略拦截、取消。
	Stopped *bool `json:"stopped,omitempty"`
	// CachedTokens 是期望的缓存提示 token 总数，由
	// CachedTokens 是脚本化 provider 报告的缓存提示 token 期望总数。nil 表示"不检查"。
	CachedTokens *int `json:"cached_tokens,omitempty"`
}

// Check 在运行后检查工作区。
type Check struct {
	// FilesExist 列出必须存在的、相对于工作区的路径。
	FilesExist []string `json:"files_exist,omitempty"`
	// FileContains 将路径映射到其内容必须包含的 needle。
	FileContains map[string]string `json:"file_contains,omitempty"`
	// FileEquals 将路径映射到其运行后内容必须逐字节精确相等的值。
	// 与 FileContains 的子串判定不同，这里连长度和结尾换行都要求一致。
	FileEquals map[string]string `json:"file_equals,omitempty"`

	// Exec 设置且 AllowExec 为 true 时，在其他检查之后运行于种入的临时工作区（见 RunExecCheck / GradeExecCheck）。
	// 种入的临时工作区在其他检查之后运行（见 RunExecCheck / GradeExecCheck）。
	Exec *ExecCheck `json:"exec,omitempty"`
	// AllowExec 是对 Exec 的显式选择加入。命令执行
	// AllowExec 是对 Exec 的显式选择加入。命令执行默认关闭：没有 AllowExec 的 Exec 永不运行，因此场景文档无法执行
	// 未明确点名的命令。
	AllowExec bool `json:"allow_exec,omitempty"`
}

// Scenario 是一个脚本化 eval 用例。
type Scenario struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Model 和 System 原样传给循环；Model 默认为
	// DefaultModel.
	Model  string `json:"model,omitempty"`
	System string `json:"system,omitempty"`
	Prompt string `json:"prompt"`
	// Provider 是假模型重放的脚本，每轮一条。
	Provider []ScriptTurn `json:"provider"`
	Expect   Expect       `json:"expect"`
	// Files 在运行前种入工作区，路径 → 内容。
	Files map[string]string `json:"files,omitempty"`
	Check Check             `json:"check"`

	// 下面的字段是给调用方 RunFunc 的提示。harness 本身
	// 下面的字段是给调用方 RunFunc 的提示。harness 本身只强制 Repeat（通过脚本化 provider）；其余描述场景想让循环做什么，使 runner 不必
	// 场景想让循环做什么，使 runner 不必
	// 按名称对场景做特例处理。
	//
	// MaxTurns 是运行的轮数预算（0 = 循环的默认值）。
	MaxTurns int `json:"max_turns,omitempty"`
	// CancelAfter 要求 runner 在那么多轮后取消运行。
	CancelAfter int `json:"cancel_after,omitempty"`
	// Approve 是审批门应给出的答案（nil = runner 的默认值）。
	// default).
	Approve *bool `json:"approve,omitempty"`
	// DenyPaths 列出策略必须拒绝的路径。
	DenyPaths []string `json:"deny_paths,omitempty"`
	// Repeat 循环播放脚本，使模型永不自行停止，只有上限结束运行。
	// 且只有上限结束运行。
	Repeat bool `json:"repeat,omitempty"`
	// PromptCache 是 runner 提示：当 runner 组装支持缓存的请求时标记稳定的系统前缀。
	// runner 组装支持缓存的请求时标记稳定的系统前缀。
	PromptCache bool `json:"prompt_cache,omitempty"`
	// NoPromptCache 镜像 --no-prompt-cache：runner 不得发出缓存标记，即使协议支持它们。
	// 缓存标记，即使协议支持它们。
	NoPromptCache bool `json:"no_prompt_cache,omitempty"`
}

// DefaultModel 是场景未指定时交给循环的模型 id。
const DefaultModel = "scripted"

// Load 从磁盘读取场景文档。
func Load(path string) ([]Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	scenarios, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return scenarios, nil
}

// Parse 解码场景文档：{"scenarios": [...]} 或裸数组。
func Parse(data []byte) ([]Scenario, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var document struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(data, &document.Scenarios); err != nil {
			return nil, fmt.Errorf("decode scenarios: %w", err)
		}
	} else if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode scenarios: %w", err)
	}
	if len(document.Scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios in document")
	}
	for i := range document.Scenarios {
		document.Scenarios[i].normalize(i)
	}
	// 规范化之后再验证，使错误消息能点名场景，
	// 规范化之后再验证，使错误消息能点名场景，包括那些继承了生成名称的场景。
	for _, scenario := range document.Scenarios {
		if err := scenario.Validate(); err != nil {
			return nil, err
		}
	}
	return document.Scenarios, nil
}

// normalize 应用手写文档可能省略的默认值。
func (s *Scenario) normalize(index int) {
	if s.Name == "" {
		s.Name = fmt.Sprintf("scenario %d", index+1)
	}
	if s.Model == "" {
		s.Model = DefaultModel
	}
	if s.MaxTurns < 0 {
		s.MaxTurns = 0
	}
	if s.CancelAfter < 0 {
		s.CancelAfter = 0
	}
}
