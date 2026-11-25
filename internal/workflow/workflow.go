// Package workflow 运行脚本化的多步骤任务。
//
// 定义是一个小的 JSON 文档：
//
//	{"name":"review","steps":[
//	  {"name":"scan","prompt":"list the changed files"},
//	  {"name":"review","prompt":"review {{steps.scan.output}} for {{input}}"},
//	  {"name":"report","prompt":"write the report","when":{"previous_contains":"BUG"}}
//	]}
//
// 步骤按依赖顺序运行：读取 {{steps.NAME.output}} 的步骤在产生它的步骤之后运行。每个步骤要么提示模型要么调用
// 在产生它的步骤之后运行。每个步骤要么提示模型要么调用
// 工具，并可携带对上一步输出的条件、超时、
// 带指数退避的重试以及 on_failure 策略。
package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"example.com/argentgo/internal/security"
)

// 失败策略。
const (
	// OnFailureStop 结束运行；其余步骤被记录为跳过。
	OnFailureStop = "stop"
	// OnFailureContinue 继续执行下一步。
	OnFailureContinue = "continue"
	// OnFailureRetry 重试该步骤（至少一次），当尝试耗尽时停止。
	// 尝试耗尽。
	OnFailureRetry = "retry"
)

// Duration 是 JSON 友好的 time.Duration：它编码为字符串（"30s"）
// 并在解码时接受时长字符串或秒数。
type Duration time.Duration

// Std 以 time.Duration 形式返回值。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String 按 time.Duration 的方式渲染时长。
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON 将时长写为字符串。
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON 接受 "45s"、90（秒）、"" 和 null。
func (d *Duration) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		*d = 0
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			*d = 0
			return nil
		}
		v, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("workflow: duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("workflow: duration must be a string like \"30s\" or a number of seconds: %w", err)
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// When 根据上一步的输出为步骤设置门槛。
type When struct {
	// PreviousContains 仅在上一步输出包含它时运行步骤。
	PreviousContains string `json:"previous_contains,omitempty"`
	// PreviousNotContains 仅在上一步输出不包含它时运行步骤。
	// 包含它。
	PreviousNotContains string `json:"previous_not_contains,omitempty"`
}

// matches 报告条件是否成立。prev 是最后一个实际运行的步骤的输出；被跳过的步骤永远不会成为前一个。
// 实际运行的步骤；被跳过的步骤永远不会成为前一个。
func (w When) matches(prev string, havePrev bool) bool {
	if !havePrev {
		return w.PreviousContains == "" && w.PreviousNotContains == ""
	}
	if w.PreviousContains != "" && !strings.Contains(prev, w.PreviousContains) {
		return false
	}
	if w.PreviousNotContains != "" && strings.Contains(prev, w.PreviousNotContains) {
		return false
	}
	return true
}

// StepCheck 是步骤的客观 shell 检查。退出码零表示步骤达成了目标；其他任何情况（包括超时）都是步骤失败
// 步骤达成了目标；其他任何情况（包括超时）都是步骤失败
// 经由正常的重试路径处理。
type StepCheck struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// Step 是一个工作单元。
type Step struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt,omitempty"`
	// Tool 指定要调用的工具，而非提示模型。
	Tool string `json:"tool,omitempty"`
	// Args 是工具参数的 JSON；它支持与 Prompt 相同的模板。
	Args string `json:"args,omitempty"`
	When When   `json:"when,omitzero"`
	// Retries 是失败后额外尝试的次数。
	Retries int `json:"retries,omitempty"`
	// Timeout 限制单次尝试。
	Timeout   Duration `json:"timeout,omitzero"`
	OnFailure string   `json:"on_failure,omitempty"`
	// SuccessCheck 设置后必须在主动作成功后通过。
	SuccessCheck *StepCheck `json:"success_check,omitempty"`
	// CleanupCommand 是步骤失败时运行一次的 shell 命令，就在
	// 应用 on_failure 策略之前。其输出被追加到
	// 失败诊断中，其退出码从不改变决策。它是
	// 失败诊断中，其退出码从不改变决策。它是独立字段，因为 OnFailure 已经命名了失败策略。
	CleanupCommand string `json:"cleanup,omitempty"`
	// Guard 是可选的执行守卫（前置条件与失败回退）。它只在 RunGuarded
	// 路径生效；默认的 Run/RunWithParams 完全忽略它。
	Guard *StepGuard `json:"guard,omitempty"`
}

// WorkflowParam 声明一个模板参数。其值在运行时提供
// WorkflowParam 声明一个模板参数。其值在运行时提供（或取自 Default），并在提示和工具参数中作为 {{key}} 引用。
// 工具参数中。
type WorkflowParam struct {
	Key         string   `json:"key"`
	InputType   string   `json:"input_type"`
	Requirement string   `json:"requirement"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// 参数输入类型与必需性。
const (
	ParamTypeString  = "string"
	ParamTypeNumber  = "number"
	ParamTypeBoolean = "boolean"
	ParamTypeDate    = "date"
	ParamTypeFile    = "file"
	ParamTypeSelect  = "select"

	ParamRequired   = "required"
	ParamOptional   = "optional"
	ParamUserPrompt = "user_prompt"
)

// Definition 是解析后的工作流文档。
type Definition struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
	// Parameters 声明工作流接受的模板变量。
	Parameters []WorkflowParam `json:"parameters,omitempty"`
	// Warnings 报告在声明性内容中发现的不可见字符。
	// 它仅是元数据：从不被序列化，从不阻塞运行。
	Warnings []string `json:"-"`
}

// MaxRetries 限制重试预算，使拼写错误不会永远循环。
const MaxRetries = 10

// ParseDefinition 解码并验证工作流文档。
func ParseDefinition(data []byte) (*Definition, error) {
	var def Definition
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("workflow: invalid document: %w", err)
	}
	def.normalise()
	if err := def.Validate(); err != nil {
		return nil, err
	}
	def.Warnings = security.ScanArtifact(string(data)).Warnings()
	return &def, nil
}

// LoadDefinition 从磁盘读取工作流文档。
func LoadDefinition(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workflow: read %s: %w", path, err)
	}
	def, err := ParseDefinition(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return def, nil
}

func (d *Definition) normalise() {
	if strings.TrimSpace(d.Name) == "" {
		d.Name = "workflow"
	}
	for i := range d.Steps {
		step := &d.Steps[i]
		// 名称在这里以及 Validate/ValidateDAG 中都会被裁剪，使
		// executor 能用与引用解析到的完全相同的名称作为步骤输出键。
		// executor 能用与 {{steps.NAME.output}} 引用解析到的完全相同的名称作为步骤输出键。否则，
		// 带首尾空白的名称会通过验证，但其每个消费者
		// 带首尾空白的名称会通过验证，但其输出的每个消费者都会因"未定义依赖"错误被跳过，且
		// 占位符在提示中保持未替换。
		step.Name = strings.TrimSpace(step.Name)
		if strings.TrimSpace(step.OnFailure) == "" {
			step.OnFailure = OnFailureStop
		}
		step.OnFailure = strings.ToLower(strings.TrimSpace(step.OnFailure))
		if step.Retries < 0 {
			step.Retries = 0
		}
		if step.Retries > MaxRetries {
			step.Retries = MaxRetries
		}
	}
}

// Validate 检查名称、动作、条件和策略。
func (d *Definition) Validate() error {
	if d == nil {
		return fmt.Errorf("workflow: the definition is nil")
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("workflow: %q has no steps", d.Name)
	}
	seen := make(map[string]bool, len(d.Steps))
	for i, step := range d.Steps {
		name := strings.TrimSpace(step.Name)
		if name == "" {
			return fmt.Errorf("workflow: step %d needs a name", i+1)
		}
		if seen[name] {
			return fmt.Errorf("workflow: duplicate step name %q", name)
		}
		seen[name] = true
		if strings.TrimSpace(step.Prompt) == "" && strings.TrimSpace(step.Tool) == "" {
			return fmt.Errorf("workflow: step %q needs a prompt or a tool", name)
		}
		switch step.OnFailure {
		case "", OnFailureStop, OnFailureContinue, OnFailureRetry:
		default:
			return fmt.Errorf("workflow: step %q has an unknown on_failure %q (use stop, continue or retry)", name, step.OnFailure)
		}
		if step.Timeout.Std() < 0 {
			return fmt.Errorf("workflow: step %q has a negative timeout", name)
		}
	}
	// 步骤输出引用是依赖。在这里拒绝损坏的图，使
	// 定义不会带着环或悬空的 {{steps.NAME.output}} 引用到达 executor。
	if _, err := ValidateDAG(d.Steps); err != nil {
		return err
	}
	if err := ValidateParameters(*d); err != nil {
		return err
	}
	// 值与声明的 input_type 矛盾的默认值会在运行时被字符串化进提示或工具参数；在这里拒绝它，使
	// 会在运行时被字符串化进提示或工具参数；在这里拒绝它，使
	// 带畸形默认值的定义无法到达 executor。
	if err := ValidateParamDefaults(*d); err != nil {
		return err
	}
	return nil
}
