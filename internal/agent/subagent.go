package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// TaskToolName 是内置的委派工具。
	TaskToolName = "task"
	// DefaultMaxDepth 是调用方未设置限制时，子 agent 可以嵌套的深度。
	// 根为深度 0，因此默认值允许两层子级。
	DefaultMaxDepth = 2
	// DefaultSubagentTurns 在父级没有更小预算时，限制子运行的轮次数。
	DefaultSubagentTurns = 16
	// MaxVerifyRetries 是 task 调用中 verify_max_retries 的硬性上限。
	// 模型无法请求无界的验证循环：超过该值的都会被收紧，
	// 因此每个启用验证的任务最多执行 1+MaxVerifyRetries 次子运行
	// （每次尝试再加一个验证子运行）。
	MaxVerifyRetries = 3
	// DefaultBackgroundResultsCap 是共享后台队列保留的、未被抽取的结果的默认数量。
	DefaultBackgroundResultsCap = 50
	// DefaultMaxBackgroundTasks 限制在一个根运行及其所有后代中，
	// 同时运行的后台子任务数量。
	DefaultMaxBackgroundTasks = 8
	// BackgroundResultPrefix 既标记已完成的后台任务推送的合成结果文本，
	// 也标记对应的父级 EventNotice。
	BackgroundResultPrefix = "task result: "
)

const subagentTemplate = `You are a subagent working for another agent. Task: %s
Do only the work the task asks for, using the tools you have. Do not ask questions: make the most reasonable assumption and report the result. Finish with a concise final answer; the parent agent receives only that text.`

// VerifyFailInfo 携带一次验证 FAIL 的可序列化旁路信息，交给 OnVerifyFail 回调做分叉副作用。
// 字段刻意保持为基础类型，使 agent 不需要导入 session 等任何内部包。
type VerifyFailInfo struct {
	// Label 是被验证任务的标签（description 或提示首行）。
	Label string
	// Attempt 是本次尝试的序号，从 1 开始，与 FAIL 通知中的 attempt 一致。
	Attempt int
	// Retries 是收紧到 [0, MaxVerifyRetries] 后的最大重试次数。
	Retries int
	// Verdict 是验证子级返回的 FAIL 结论首行。
	Verdict string
	// AttemptID 是本次验证尝试的稳定标识，接线层可用它作为分叉记录的 id 前缀。
	AttemptID string
}

// SubagentConfig 启用内置的 `task` 工具。
type SubagentConfig struct {
	// Model 若设置，是子循环使用的模型（例如 --secondary-model）。
	// 父级的模型绝不会被改变。为空则继承父级的模型。
	Model string
	// AllowedTools 限制子级的工具集。为空则继承父级的工具
	// （子级仍不能递归超过 MaxDepth）。
	AllowedTools []string
	// MaxDepth 是可以再派生子 agent 的最深子级层数（默认 2）。
	MaxDepth int
	// MaxTurns 覆盖子级的轮次上限。
	MaxTurns int
	// Budget 若设置，是子级的完整预算；否则取父级预算的一半。
	Budget Budget
	// System 替换子级的系统提示。
	System string
	// systemInherited / toolsInherited 标记 System / AllowedTools 是子级从父级
	// （某个角色或祖先）继承的值，而非在本层编写的覆盖。继承的值只是基线：
	// 嵌套的 task(persona=...) 必须可以自由替换它。零值（false）保留旧规则，
	// 即直接设置这些字段的调用方优先于角色。包内私有：只有 childOptions 会设置它们。
	systemInherited bool
	toolsInherited  bool
	// OnEvent 接收子级的事件。为 nil 时，事件会送往父级的 Emit，并设置 Event.Depth。
	OnEvent func(Event)
	// OnVerifyFail 在验证结论为 FAIL 且仍有重试余量时被调用，供接线层做旁路分叉等副作用。
	// 它为 nil 时完全关闭，事件与记录保持逐字节不变；回调返回的错误只会转成一条父级通知，
	// 不会改变验证结果、返回文本或重试决策。
	OnVerifyFail func(ctx context.Context, info VerifyFailInfo) error
	// BeginAttempt 在启用验证的任务开始前调用，返回本次验证尝试的前置根记录 id。
	// 返回的 rootID 会成为 VerifyFailInfo.AttemptID，使接线层能把后续分叉归属于同一尝试。
	// 它为 nil 时整段跳过，AttemptID 保持原有的合成串，事件与记录逐字节不变；
	// 回调返回错误（或返回空 id）时同样回落到合成串，错误被吞掉。
	BeginAttempt func(reason string) (string, error)
	// EndAttempt 在验证得出结论后调用，rootID 来自 BeginAttempt，verdict 为 succeeded 或 failed。
	// 它为 nil、或 BeginAttempt 未给出有效 rootID 时整段跳过，默认路径逐字节不变；
	// 回调返回的错误被吞掉，不影响验证结果、返回文本或重试决策。
	EndAttempt func(rootID, verdict string) error
	// ResolvePersona 把 task 调用请求的角色/身份名映射到该角色定义的子配置。
	// 它是可选的，由接线层（internal/app）注入，因此本包从不导入它。
	// 为 nil 或名称未知时，委派回退到上面的通用配置。
	// 返回 true 的解析器以角色的配置为基础，随后把本配置中显式设置的字段叠加其上。
	ResolvePersona func(string) (SubagentConfig, bool)
	// Background 若设置，会在 task 调用中启用 `background` 字段。它是属于一个根运行
	// 及其所有后代的有界结果队列；接线层绝不能在不同会话间共享它。
	// 每次运行只抽取带有自身 owner 标记的结果（见 BackgroundResults 的说明）。
	// 未配置队列时的后台调用会被拒绝，而不是默默丢失其结果。
	Background *BackgroundResults
	// NewChild 可选地覆盖被委派子运行器的构建方式。nil 表示 &Runner{Opts: opts}。
	// 它主要是一个测试接缝：注入伪实现可让测试在没有真实模型的情况下控制子级执行。
	NewChild func(opts Options) ChildRunner
	// SeedContext 把带有保留标记的父级记录带入每个子级的上下文，每条记录占一行格式化文本。
	// 它是附加性的且默认关闭：为空时子级系统提示与之前完全一致。
	// app 用 session.SelectForChildren(records) 计算这些行并格式化；
	// agent 包从不导入 session（见 AGENTS.md 的分层规则）。
	SeedContext []string
	// Instruction 把父会话已解析的指令快照传递给子级。设置后，子级会收到一份继承副本
	// （深拷贝，且恰好带一个继承标记），在 SeedContext 块之后渲染进其系统提示。
	// 为 nil 时子级系统提示逐字节不变，因此这也保持默认关闭。
	// 快照由 app 接线层负责构建；本包只负责携带和渲染它。
	Instruction *InstructionSnapshot
}

// TaskToolSpec 向模型描述委派工具。
func TaskToolSpec() llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = TaskToolName
	spec.Function.Description = "Delegate a self-contained task to a subagent with its own context and a restricted tool set. " +
		"The subagent returns only its final answer, so give it everything it needs in the prompt. " +
		"Set background to true to start without waiting: the call returns an ack immediately and the result is injected into the conversation when it is ready."
	spec.Function.Parameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {"type": "string", "description": "Short label for the task, shown in the UI"},
    "prompt": {"type": "string", "description": "Complete, self-contained instructions for the subagent"},
    "task": {"type": "string", "description": "Alias for prompt, accepted for compatibility"},
    "persona": {"type": "string", "description": "Optional persona/role name defined in .argent/agents; the named persona's system prompt and tool allow-list seed the subagent"},
    "role": {"type": "string", "description": "Alias for persona, accepted for compatibility"},
    "verify": {"type": "string", "description": "Optional verification instruction. When set, the parent validates the subagent's result against it before accepting: it issues a short verification subagent that replies PASS or FAIL. On FAIL the original task is retried up to verify_max_retries times."},
    "verify_max_retries": {"type": "integer", "description": "How many times to retry the original task when verification fails (default 0, capped at 3). Ignored when verify is absent."},
    "background": {"type": "boolean", "description": "When true, start the task in the background and return immediately with an ack; the finished result is injected into the conversation when ready. Takes precedence over verify."}
  },
  "required": ["prompt"]
}`)
	return spec
}

// newToolset 在启用子 agent 时，用委派工具包装调用方的工具。
// 达到深度上限时会隐藏该工具的规格，使子级不会想去递归。
func newToolset(inner ToolRunner, opts Options) ToolRunner {
	if inner == nil {
		inner = nopTools{}
	}
	// 技能工具是选择性启用的，并在委派工具之前包装调用方的运行器，
	// 因此子 agent 会通过 taskTools 继承同样的公布集合。
	if opts.SkillAuthor != nil || opts.SkillLister != nil {
		inner = newSkillTools(inner, opts)
	}
	if opts.Subagent == nil {
		return inner
	}
	cfg := *opts.Subagent
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = DefaultMaxDepth
	}
	specs := make([]llm.ToolSpec, 0, len(inner.Specs())+1)
	for _, spec := range inner.Specs() {
		if spec.Function.Name == TaskToolName {
			continue
		}
		specs = append(specs, spec)
	}
	if opts.Depth < cfg.MaxDepth {
		specs = append(specs, TaskToolSpec())
	}
	return &taskTools{inner: inner, specs: specs, parent: opts, depth: opts.Depth, owner: opts.bgOwner, cfg: &cfg}
}

// taskTools 在基础工具运行器之上公布并运行委派工具。
type taskTools struct {
	inner  ToolRunner
	specs  []llm.ToolSpec
	parent Options
	depth  int
	// owner 是拥有该 taskTools 的运行所属的投递身份。它派生的后台任务会在该 owner 下
	// 推回结果，因此委派运行的 injectSteering 能精确收集属于它自己的结果。
	owner uint64
	cfg   *SubagentConfig
	// emitMu 串行化父级层面的子 agent 事件。后台子级从各自的 goroutine 发出事件，
	// 因此用锁来保持开始/结束/通知的顺序合理。
	emitMu sync.Mutex
	// costMu 串行化被取消子级的成本补记。后台子级可能同时被取消，
	// 多个 goroutine 会同时向父级 Recorder 追加 stats 记录。
	costMu sync.Mutex
}

func (t *taskTools) Specs() []llm.ToolSpec { return t.specs }

func (t *taskTools) Run(ctx context.Context, name, args string) (string, error) {
	if name != TaskToolName {
		return t.inner.Run(ctx, name, args)
	}
	return t.runTask(ctx, args)
}

// taskArgs 是委派工具的参数结构。
type taskArgs struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Task        string `json:"task"`
	// Persona 可选地选择一个具名的子 agent 定义。Role 是其别名。
	Persona string `json:"persona"`
	Role    string `json:"role"`
	// Verify 若设置，会使父级在接受子级结果之前先对其进行验证（见 runTaskVerified）。
	// 为空则保留旧有行为。
	Verify string `json:"verify,omitempty"`
	// VerifyMaxRetries 限制 FAIL 结论对原任务的重试次数。负值收紧到 0，
	// 高于 MaxVerifyRetries 的值会被收紧下来。Verify 为空时忽略该字段。
	VerifyMaxRetries int `json:"verify_max_retries,omitempty"`
	// Background 为 true 时，会在 goroutine 中启动子级并立即返回确认。
	// 完成的结果会被排队，等待注入父级。缺省或为 false 时保留旧有的同步行为。
	Background bool `json:"background,omitempty"`
}

// runTask 是 task 工具的入口。它解析参数，要么运行子级一次（原始的、向后兼容的路径），
// 要么在存在验证指令时驱动有界的验证/重试循环。
//
// 验证设计：父级复核（PARENT-RECHECK）。单个子级的自检无法表达父级可据以行动的 FAIL 结论，
// 因此验证是一个独立的简短 task 调用，回复 PASS 或 FAIL。后续调用本身也经过 runTask，
// 所以它继承深度防护；它自身不带 verify 字段，因此验证不会递归。重试次数由 MaxVerifyRetries 限制。
// 这样把改动限制在 subagent.go 内。
func (t *taskTools) runTask(ctx context.Context, raw string) (string, error) {
	if t.depth >= t.cfg.MaxDepth {
		return "", fmt.Errorf("task depth limit reached: subagents may nest at most %d level(s)", t.cfg.MaxDepth)
	}
	var args taskArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("task: invalid arguments: %w", err)
	}
	if args.Background {
		return t.runTaskBackground(ctx, raw, args)
	}
	if verify := strings.TrimSpace(args.Verify); verify != "" {
		return t.runTaskVerified(ctx, raw, args, verify)
	}
	return t.runTaskOnce(ctx, raw, args)
}

// runTaskOnce 运行一次子循环，并把其最终答案作为工具结果返回。
// 这是原始的委派路径，在没有 verify 时保持不变。
func (t *taskTools) runTaskOnce(ctx context.Context, raw string, args taskArgs) (string, error) {
	task := strings.TrimSpace(args.Prompt)
	if task == "" {
		task = strings.TrimSpace(args.Task)
	}
	if task == "" {
		task = strings.TrimSpace(args.Description)
	}
	if task == "" {
		return "", fmt.Errorf("task: prompt is required")
	}
	label := strings.TrimSpace(args.Description)
	if label == "" {
		label = firstLine(task)
	}
	persona := strings.TrimSpace(args.Persona)
	if persona == "" {
		persona = strings.TrimSpace(args.Role)
	}

	t.parentEmit(Event{Kind: EventSubagentStart, Text: label, ToolName: TaskToolName, Args: raw})
	child := t.newChild(label, persona)
	res, err := child.Run(ctx, task)
	// 结果回灌：被取消的子级会跳过自身的 stats 记录（见 loop.go 的 execute），
	// 这里把它已经产生的部分成本补记到父级统计，避免漏账。
	t.reconcileCancelledChildCost(label, t.childModel(persona), res)
	if err != nil {
		t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Err: err.Error(), IsError: true})
		return "", fmt.Errorf("subagent failed: %w", err)
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		text = "(subagent finished without an answer)"
	}
	t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Output: text})
	return text, nil
}

// verifyFailSeq 为每次启用验证的运行分配进程内唯一序号，使 VerifyFailInfo.AttemptID
// 可作为接线层分叉记录的稳定前缀；它只影响旁路信息，不改变任何事件或记录。
var verifyFailSeq atomic.Uint64

// runTaskVerified 运行原任务，然后询问一个简短的验证子级，根据验证指令评判结果。
// PASS 返回结果；FAIL 会按 verify_max_retries（收紧到 [0, MaxVerifyRetries]）重试原任务。
// 尝试次数和验证递归（每次尝试一个非验证的后续调用）都是有界的，
// 因此任何配置都无法产生无界循环。
func (t *taskTools) runTaskVerified(ctx context.Context, raw string, args taskArgs, verify string) (string, error) {
	label := strings.TrimSpace(args.Description)
	if label == "" {
		label = firstLine(strings.TrimSpace(args.Prompt))
	}
	retries := args.VerifyMaxRetries
	if retries < 0 {
		retries = 0
	}
	if retries > MaxVerifyRetries {
		retries = MaxVerifyRetries
	}
	// 仅为旁路信息分配一个稳定 id；nil 回调路径不读取它，因此默认行为逐字节不变。
	runID := verifyFailSeq.Add(1)
	// 前置尝试根：在第一次子运行之前写入一次，rootID 由分叉信息与结束记录共享。
	// 默认回调为 nil、返回错误或空 id 时 rootID 为空，回落到此前的合成串，逐字节不变。
	rootID := ""
	if cfg := t.cfg; cfg.BeginAttempt != nil {
		if id, err := cfg.BeginAttempt("task_verify"); err == nil {
			rootID = id
		}
	}

	for attempt := 0; ; attempt++ {
		text, err := t.runTaskOnce(ctx, raw, args)
		if err != nil {
			return "", err
		}

		// 后续调用复用 runTask，因此继承深度防护。它的 JSON 刻意省略 verify，
		// 所以验证绝不会递归。
		verdict, err := t.runTask(ctx, verifyArgsJSON(label, verify, text))
		if err != nil {
			// 没有结论时，我们无法把结果当作已验证而接受。
			t.verifyNotice(fmt.Sprintf("subagent verification error for %q: %v", label, err))
			t.endAttempt(rootID, "failed")
			return "", fmt.Errorf("subagent verification failed for %q: %w", label, err)
		}
		if !verificationFailed(verdict) {
			t.verifyNotice(fmt.Sprintf("subagent verification PASS: %s", label))
			t.endAttempt(rootID, "succeeded")
			return text, nil
		}
		t.verifyNotice(fmt.Sprintf("subagent verification FAIL (attempt %d/%d): %s - %s", attempt+1, retries+1, label, firstLine(verdict)))
		if attempt >= retries {
			// 重试已耗尽：返回最后的结果而不是丢失工作，FAIL 结果已在上方记录。
			// 提前返回使耗尽时的最后一次 FAIL 不触发旁路回调。
			t.endAttempt(rootID, "failed")
			return text, nil
		}
		// 旁路分叉副作用：仅在仍有重试余量时触发，且不改变下面的循环走向。
		// 回调为 nil 时整段跳过，默认路径与旧实现逐字节一致。
		if cfg := t.cfg; cfg.OnVerifyFail != nil {
			// rootID 有效时以真实前置根作为稳定标识，否则保留旧的合成串。
			attemptID := fmt.Sprintf("verifyfail-%d-%d", runID, attempt+1)
			if rootID != "" {
				attemptID = rootID
			}
			info := VerifyFailInfo{
				Label:     label,
				Attempt:   attempt + 1,
				Retries:   retries,
				Verdict:   firstLine(verdict),
				AttemptID: attemptID,
			}
			if err := cfg.OnVerifyFail(ctx, info); err != nil {
				// 回调失败只转成一条父级通知，绝不影响验证结论或重试决策。
				t.verifyNotice(fmt.Sprintf("on verify fail hook error for %q: %v", label, err))
			}
		}
	}
}

// verifyNotice 把可审计的验证结果作为父级通知事件（EventNotice）发出，
// 复用现有的事件词汇。
func (t *taskTools) verifyNotice(text string) {
	t.parentEmit(Event{Kind: EventNotice, Text: text, ToolName: TaskToolName})
}

// endAttempt 在验证得出结论后记录本次尝试的结论。rootID 为空（BeginAttempt 未配置或失败）
// 或 EndAttempt 为 nil 时整段跳过，默认路径逐字节不变；回调错误一律吞掉。
func (t *taskTools) endAttempt(rootID, verdict string) {
	if rootID == "" || t.cfg == nil || t.cfg.EndAttempt == nil {
		return
	}
	_ = t.cfg.EndAttempt(rootID, verdict)
}

// verifyArgsJSON 构建用于评判结果的后续 task 调用。验证指令和结果都嵌入在提示中；
// 没有设置 verify 字段，因此后续调用只是一次普通的单次子运行。
func verifyArgsJSON(label, verify, result string) string {
	prompt := fmt.Sprintf("Verify the following result against these criteria:\n%s\n\nResult to verify:\n%s\n\nReply with PASS if the result satisfies the criteria, otherwise reply with FAIL followed by concise reasons.",
		verify, result)
	b, _ := json.Marshal(taskArgs{
		Description: "verify: " + label,
		Prompt:      prompt,
	})
	return string(b)
}

// verificationFailed 报告验证结论是否为 FAIL。后续调用被要求以 PASS 或 FAIL 开头，
// 因此第一个词优先；另有回退逻辑处理只提到这两个词之一的冗长回复。
func verificationFailed(verdict string) bool {
	v := strings.TrimSpace(verdict)
	if v == "" {
		return true
	}
	first := strings.ToUpper(strings.Fields(v)[0])
	if strings.HasPrefix(first, "FAIL") {
		return true
	}
	if strings.HasPrefix(first, "PASS") {
		return false
	}
	upper := strings.ToUpper(v)
	return strings.Contains(upper, "FAIL") && !strings.Contains(upper, "PASS")
}

// configForPersona 以具名角色作为子配置的起点，然后叠加父级显式设置的字段。
// 父级自身继承来的值（由 childOptions 标记）不算显式覆盖，因此嵌套的角色切换可以替换它们。
// 解析器缺失、名称为空或名称未知时，通用父级配置保持不变，从而保持委派的向后兼容性。
func (t *taskTools) configForPersona(name string) SubagentConfig {
	cfg := *t.cfg
	name = strings.TrimSpace(name)
	if name == "" || t.cfg.ResolvePersona == nil {
		cfg.Instruction = inheritForChild(cfg.Instruction)
		return cfg
	}
	persona, ok := t.cfg.ResolvePersona(name)
	if !ok {
		cfg.Instruction = inheritForChild(cfg.Instruction)
		return cfg
	}

	merged := persona
	if v := strings.TrimSpace(t.cfg.Model); v != "" {
		merged.Model = v
	}
	if !t.cfg.toolsInherited && len(t.cfg.AllowedTools) > 0 {
		merged.AllowedTools = t.cfg.AllowedTools
	}
	if t.cfg.MaxDepth > 0 {
		merged.MaxDepth = t.cfg.MaxDepth
	}
	if t.cfg.MaxTurns > 0 {
		merged.MaxTurns = t.cfg.MaxTurns
	}
	if t.cfg.Budget != (Budget{}) {
		merged.Budget = t.cfg.Budget
	}
	if !t.cfg.systemInherited && strings.TrimSpace(t.cfg.System) != "" {
		merged.System = t.cfg.System
	}
	if t.cfg.OnEvent != nil {
		merged.OnEvent = t.cfg.OnEvent
	}
	// 与 OnEvent 一样显式透传验证失败回调：按角色选定的子级同样需要触发旁路分叉，
	// 角色自身的配置不会覆盖父级注入的钩子。
	if t.cfg.OnVerifyFail != nil {
		merged.OnVerifyFail = t.cfg.OnVerifyFail
	}
	// 与 OnVerifyFail 一样显式透传尝试根钩子：按角色选定的子级同样需要在验证时
	// 写入前置根并记录结论；仅当父级显式注入时才覆盖角色自带的实现。
	if t.cfg.BeginAttempt != nil {
		merged.BeginAttempt = t.cfg.BeginAttempt
	}
	if t.cfg.EndAttempt != nil {
		merged.EndAttempt = t.cfg.EndAttempt
	}
	// 把共享的后台状态和子运行器接缝一并带过角色叠加过程，
	// 使按角色选定的子级保留父级的队列、信号量和可注入的运行器。
	if t.cfg.Background != nil {
		merged.Background = t.cfg.Background
	}
	if t.cfg.NewChild != nil {
		merged.NewChild = t.cfg.NewChild
	}
	if len(t.cfg.SeedContext) > 0 {
		merged.SeedContext = t.cfg.SeedContext
	}
	// 与 SeedContext 一样，父级已解析的快照优先于角色自带的快照：
	// 对于本次运行解析出的内容，父级才是权威。
	if t.cfg.Instruction != nil {
		merged.Instruction = t.cfg.Instruction
	}
	// 保留解析器，使嵌套任务也能选择自己的角色。
	merged.ResolvePersona = t.cfg.ResolvePersona
	// 在这里（构建子配置时）继承一次，而不是在 childOptions 中：继承副本存放在子级配置上，
	// 它会成为该子级的 Options.Subagent，因此它自己的后代也会继承该快照。
	merged.Instruction = inheritForChild(merged.Instruction)
	return merged
}

// inheritForChild 返回子配置应携带的快照。委派链中的第一个子级会在其 Thread 层追加
// Inherit() 的标记；更深的后代看到标记已存在，则收到普通的深拷贝。
// 这样无论链有多深，标记都恰好出现一次，同时每个子级都有独立的值。
// nil 的快照保持为 nil（默认关闭的路径）。
func inheritForChild(s *InstructionSnapshot) *InstructionSnapshot {
	if s == nil {
		return nil
	}
	for _, entry := range s.Thread {
		if entry == InheritanceNote {
			clone := s.Clone()
			return &clone
		}
	}
	inherited := s.Inherit()
	return &inherited
}

// childOptions 构建嵌套运行的配置：受限工具、子级系统提示、预算切片以及事件桥接。
func (t *taskTools) childOptions(label, persona string) Options {
	cfg := t.configForPersona(persona)
	// 该子级携带的 System/AllowedTools 都来自其父级（某个角色或祖先），而非本层代码。
	// 把它们标记为继承，使该子级自己的 task(persona=...) 能切换角色，
	// 而不是把父级的值重新叠加到新角色之上。
	cfg.systemInherited = true
	cfg.toolsInherited = true
	tools := t.inner
	if len(cfg.AllowedTools) > 0 {
		allowed := make(map[string]bool, len(cfg.AllowedTools))
		for _, name := range cfg.AllowedTools {
			allowed[name] = true
		}
		tools = restrictedTools{inner: t.inner, allowed: allowed}
	}

	system := strings.TrimSpace(cfg.System)
	if system == "" {
		system = fmt.Sprintf(subagentTemplate, label)
	}
	if len(cfg.SeedContext) > 0 {
		system += "\n\nRetained context from the parent thread:\n- " + strings.Join(cfg.SeedContext, "\n- ")
	}
	if cfg.Instruction != nil {
		// configForPersona 已经生成了继承副本（恰好带一个标记），因此这里渲染时不会再次调用
		// Inherit。All() 的顺序是 user -> thread -> project，因此各层按解析顺序出现。
		system += "\n\nInherited instructions from the parent session:\n- " + strings.Join(cfg.Instruction.All(), "\n- ")
	}

	budget := cfg.Budget
	if budget == (Budget{}) {
		budget = t.parent.Budget.sliceForChild()
	}
	if budget.MaxTurns <= 0 {
		turns := DefaultSubagentTurns
		if parentTurns := maxTurnsFor(t.parent); parentTurns > 1 && parentTurns/2 < turns {
			turns = max(1, parentTurns/2)
		}
		budget.MaxTurns = turns
	}
	if cfg.MaxTurns > 0 {
		budget.MaxTurns = cfg.MaxTurns
	}

	emit := func(event Event) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(event)
			return
		}
		if t.parent.Emit != nil {
			t.parent.Emit(event)
		}
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = t.parent.Model
	}
	// 把父级的回退链带入子级。FallbackModels[0] 是主模型，因此显式的子级模型
	// （--secondary-model 或角色选择）只替换第一项；父级的备用模型保持原有顺序。
	// 复制该切片，使同级子级绝不会与父级配置共享可变状态。
	fallback := t.parent.FallbackModels
	if model != "" && len(fallback) > 0 {
		fb := make([]string, len(fallback))
		copy(fb, fallback)
		fb[0] = model
		fallback = fb
	}
	return Options{
		Model:            model,
		FallbackModels:   fallback,
		MaxTurns:         budget.MaxTurns,
		MaxParallel:      t.parent.MaxParallel,
		System:           system,
		Root:             t.parent.Root,
		Chatter:          t.parent.Chatter,
		Tools:            tools,
		Approve:          t.parent.Approve,
		Record:           t.parent.Record,
		BeforeRequest:    t.parent.BeforeRequest,
		BeforeTool:       t.parent.BeforeTool,
		Emit:             emit,
		Budget:           budget,
		Prices:           t.parent.Prices,
		MaxContextTokens: t.parent.MaxContextTokens,
		KeepTurns:        t.parent.KeepTurns,
		KeepToolTokens:   t.parent.KeepToolTokens,
		MaxRepeats:       t.parent.MaxRepeats,
		MutatingTools:    t.parent.MutatingTools,
		Mutating:         t.parent.Mutating,
		Subagent:         &cfg,
		Depth:            t.depth + 1,
		// 为该子级在共享队列中分配自己的身份。它自己的后台任务会以该 owner 推送结果
		// （通过 newToolset），因此即使同级子级共享深度，该子级也只抽取自己的结果。
		bgOwner: t.nextOwner(),
	}
}

// nextOwner 从本树的队列中分配一个新的投递身份。未配置队列时返回零值，
// 因为那种情况下不可能有后台任务。
func (t *taskTools) nextOwner() uint64 {
	if t.cfg != nil && t.cfg.Background != nil {
		return t.cfg.Background.nextID()
	}
	return 0
}

// parentEmit 发送父级层面的子 agent 事件。后台子级从各自的 goroutine 发出事件，
// 因此用锁保持这些事件有序。
func (t *taskTools) parentEmit(event Event) {
	if t.parent.Emit == nil {
		return
	}
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
	event.Depth = t.depth
	t.parent.Emit(event)
}

// ---------------------------------------------------------------------------
// 被取消子级的成本回灌（R39）
// ---------------------------------------------------------------------------

// childCostContribution 描述一个子级对父级统计的成本贡献。
// 字段与 session.CostContribution 对齐；agent 无法 import session（session -> eval -> agent
// 会形成 import 环），因此在本地复刻这个纯数据类型，算术语义与 session 中的原语保持一致。
type childCostContribution struct {
	ChildID string
	CostUSD float64
	Tokens  int
}

// reconcileChildCost 是 session.ReconcileChildCost 的本地等价实现：过滤非法成本、
// 按 ChildID 稳定排序、按排序后的顺序确定性求和。它只做算术，不触碰任何记录或提示字节。
// 入参不会被修改。
func reconcileChildCost(parentCost float64, children []childCostContribution) (float64, []childCostContribution) {
	if !validChildCostUSD(parentCost) {
		parentCost = 0
	}
	breakdown := make([]childCostContribution, 0, len(children))
	for _, child := range children {
		if !validChildCostUSD(child.CostUSD) {
			continue
		}
		breakdown = append(breakdown, child)
	}
	// 稳定排序：同名 ChildID 的贡献保留输入次序，避免重复子级的明细顺序抖动。
	sort.SliceStable(breakdown, func(i, j int) bool {
		return breakdown[i].ChildID < breakdown[j].ChildID
	})
	total := parentCost
	for _, child := range breakdown {
		total += child.CostUSD
	}
	return total, breakdown
}

// validChildCostUSD 判定成本值是否可用于记账：必须是有限的非负数。
// 负数没有记账意义，NaN/±Inf 会污染累加结果，故一并拒绝。
func validChildCostUSD(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// childModel 复算某个子级实际使用的模型，用于按与子级相同的价格表折算成本。
// 它与 childOptions 中的取值顺序一致：角色显式模型优先，否则继承父级模型。
func (t *taskTools) childModel(persona string) string {
	cfg := t.configForPersona(persona)
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = t.parent.Model
	}
	return model
}

// reconcileCancelledChildCost 在子级结果回灌点补记被取消运行的成本。
//
// 被取消的运行在 loop.go 的 execute 中会跳过 recordRunStats，因此它已经产生的成本
// 不会进入会话的 stats 记录；而 Result 只携带 Usage，不携带 CostUSD，这里用与子级
// account() 相同的价格折算得到部分成本。只有在 StopCancelled 且成本为正时才追加一条
// stats 记录：无子代理、非取消子级（其自身已写 stats）或成本为 0 时完全不触碰 Recorder，
// 会话记录与提示字节保持逐字节不变。
func (t *taskTools) reconcileCancelledChildCost(childID, model string, res Result) {
	rec := normaliseRecorder(t.parent.Record)
	if rec == nil || res.Stopped != StopCancelled {
		return
	}
	price, ok := PriceFor(t.parent.Prices, model)
	if !ok {
		return
	}
	contribution := childCostContribution{
		ChildID: childID,
		CostUSD: price.Cost(res.Usage),
		Tokens:  res.Usage.PromptTokens + res.Usage.CompletionTokens,
	}
	total, breakdown := reconcileChildCost(0, []childCostContribution{contribution})
	if total <= 0 || len(breakdown) == 0 {
		return
	}
	t.costMu.Lock()
	defer t.costMu.Unlock()
	// 成本来源于注入的价格表推导，因此始终是估算值；与 recordRunStats 的口径一致。
	_ = rec.Append("stats", sessionStatsPayload{
		CostUSD:    total,
		CostSource: "estimated",
		Updated:    time.Now().UTC().Format(time.RFC3339),
	})
}

// restrictedTools 是子级对父级工具的过滤视图。
type restrictedTools struct {
	inner   ToolRunner
	allowed map[string]bool
}

func (r restrictedTools) Specs() []llm.ToolSpec {
	specs := r.inner.Specs()
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if r.allowed[spec.Function.Name] {
			out = append(out, spec)
		}
	}
	return out
}

func (r restrictedTools) Run(ctx context.Context, name, args string) (string, error) {
	if !r.allowed[name] {
		return "", fmt.Errorf("tool %q is not available to subagents", name)
	}
	return r.inner.Run(ctx, name, args)
}

// firstLine 缩短任务提示以用作标签。
func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	runes := []rune(text)
	if len(runes) > 80 {
		return string(runes[:77]) + "..."
	}
	return text
}

// ---------------------------------------------------------------------------
// 后台（即发即忘）任务
// ---------------------------------------------------------------------------

// ChildRunner 是委派所需的 Runner 子集。*Runner 满足它；测试通过 SubagentConfig.NewChild
// 注入伪实现，以在没有真实模型的情况下控制子级执行。
type ChildRunner interface {
	Run(ctx context.Context, prompt string) (Result, error)
}

// newChild 为一次被委派的任务构建子运行器。
func (t *taskTools) newChild(label, persona string) ChildRunner {
	opts := t.childOptions(label, persona)
	if t.cfg.NewChild != nil {
		return t.cfg.NewChild(opts)
	}
	return &Runner{Opts: opts}
}

// BackgroundResults 是属于一个根运行及其所有后代的、已完成后台任务结果的有界队列。
// 接线层为每个根运行创建一个实例，并存储在 SubagentConfig.Background 上；由于该字段是指针，
// 并通过每个 SubagentConfig 按值复制，根循环（通过 Options.Subagent）和树中的每个 taskTools
// 都看到同一个实例。该类型还拥有后台信号量、任务 id 计数器和按运行划分的 owner 计数器，
// 使这些限制在整个树中共享。所有方法都是并发安全的。
//
// 一个队列绝不能被两个独立的根运行共享：每个根运行都位于深度 0，
// 因此以深度为键的队列会让一个会话抽取另一个会话的结果（且 50 条上限会跨会话淘汰）。
// 在同一棵树内，深度同样不是投递键：两个同级子 agent 共享一个深度，且各自都可能派生后台子级，
// 因此结果以运行时分配的唯一 owner id 为键。深度仅作为诊断元数据携带。
//
// 循环接线（在本文件之外、loop.go 中完成）：在 runState.injectSteering 中，
// 抽取 s.steer 之后，追加发往本次运行 owner 的结果：
//
//	if s.opts.Subagent != nil && s.opts.Subagent.Background != nil {
//	    pending = append(pending, s.opts.Subagent.Background.DrainFor(s.bgOwner)...)
//	}
//
// injectSteering 已在每个轮次边界运行，因此已完成的结果无需其他改动，
// 就能在下一个边界变成一条合成用户消息。newRunState 通过 RootOwner() 分配根 owner；
// childOptions 通过 nextID() 为每个子级分配自己的 owner。由于 owner 缓存在队列上，
// 已恢复 Runner 的后续提示仍会抽取同一个 owner。
//
// Drain()（所有 owner）只用于诊断/测试。
type BackgroundResults struct {
	mu      sync.Mutex
	items   []bgResult
	cap     int
	dropped int
	seq     atomic.Uint64

	// rootOwner 是拥有该队列的根运行的稳定身份。它采用延迟分配，
	// 使仅被创建却从未使用的队列不会消耗 id，并使同一 Runner 上的重试提示共享它。
	rootOwner atomic.Uint64

	sem    chan struct{}
	maxRun int
}

// bgResult 是一条排队的结果，标记了应当接收它的 owner。
// depth 仅为诊断而保留，从不用于投递。
type bgResult struct {
	owner uint64
	depth int
	text  string
}

// NewBackgroundResults 构建一个后台队列。cap 限制未被抽取的结果（<=0 使用
// DefaultBackgroundResultsCap）；maxConcurrent 限制同时运行的后台子运行器
// （<=0 使用 DefaultMaxBackgroundTasks）。
func NewBackgroundResults(cap, maxConcurrent int) *BackgroundResults {
	if cap <= 0 {
		cap = DefaultBackgroundResultsCap
	}
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxBackgroundTasks
	}
	return &BackgroundResults{
		items:  make([]bgResult, 0, cap),
		cap:    cap,
		sem:    make(chan struct{}, maxConcurrent),
		maxRun: maxConcurrent,
	}
}

// RootOwner 返回该队列稳定的根运行身份。首次调用胜出；并发调用者都观察到相同的值。
// newRunState 对尚未分配 owner 的根 Options 只调用它一次。
func (b *BackgroundResults) RootOwner() uint64 {
	if id := b.rootOwner.Load(); id != 0 {
		return id
	}
	id := b.nextID()
	if b.rootOwner.CompareAndSwap(0, id) {
		return id
	}
	return b.rootOwner.Load()
}

// Push 把没有 owner 的文本入队，用于诊断测试。真实投递使用 PushFor，
// 并传入委派运行的 owner。
func (b *BackgroundResults) Push(text string) bool { return b.PushFor(0, 0, text) }

// PushFor 为 owner 把一条已完成的结果入队。depth 仅用于诊断。队列满时会丢弃最旧的条目
// （最新的结果与仍在运行的父级最相关）；布尔返回值报告是否发生了丢弃。
func (b *BackgroundResults) PushFor(owner uint64, depth int, text string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	dropped := false
	if len(b.items) >= b.cap {
		copy(b.items, b.items[1:])
		b.items = b.items[:len(b.items)-1]
		dropped = true
		b.dropped++
	}
	b.items = append(b.items, bgResult{owner: owner, depth: depth, text: text})
	return dropped
}

// Drain 按 FIFO 顺序移除并返回所有排队的结果。仅供诊断/测试使用：它忽略 owner。
func (b *BackgroundResults) Drain() []string { return b.drain(0, true) }

// DrainFor 按 FIFO 顺序只移除并返回发往 owner 的结果。
// 其他 owner 的结果继续排队，等待各自的运行来抽取。
func (b *BackgroundResults) DrainFor(owner uint64) []string { return b.drain(owner, false) }

func (b *BackgroundResults) drain(owner uint64, all bool) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return nil
	}
	out := make([]string, 0, len(b.items))
	kept := b.items[:0]
	for _, item := range b.items {
		if all || item.owner == owner {
			out = append(out, item.text)
			continue
		}
		kept = append(kept, item)
	}
	b.items = kept
	return out
}

// Len 报告当前排队的结果数量。
func (b *BackgroundResults) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// Dropped 报告因队列满而被淘汰的结果数量。
func (b *BackgroundResults) Dropped() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// tryAcquire 预留一个后台槽位，达到上限时返回 false。
func (b *BackgroundResults) tryAcquire() bool {
	select {
	case b.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (b *BackgroundResults) release() { <-b.sem }

func (b *BackgroundResults) nextID() uint64 { return b.seq.Add(1) }

func (b *BackgroundResults) maxConcurrent() int { return b.maxRun }

// runTaskBackground 在 goroutine 中启动子运行器并立即返回确认。子级完成时，其文本会被推送到
// 共享队列（供父循环注入），并作为父级 EventNotice 发出（便于 UI 展示）。
// 后台模式下不应用验证：background 字段优先于 verify。
// MaxDepth 由调用方（runTask）在到达本方法之前检查，且子级通过 childOptions 继承 Depth+1，
// 因此后台子级不能派生无界的后台孙级。
func (t *taskTools) runTaskBackground(ctx context.Context, raw string, args taskArgs) (string, error) {
	bg := t.cfg.Background
	if bg == nil {
		return "", fmt.Errorf("task: background execution requires SubagentConfig.Background to be configured")
	}
	if !bg.tryAcquire() {
		return "", fmt.Errorf("task: background task limit reached (%d concurrent); retry later or use a synchronous task", bg.maxConcurrent())
	}

	task := strings.TrimSpace(args.Prompt)
	if task == "" {
		task = strings.TrimSpace(args.Task)
	}
	if task == "" {
		task = strings.TrimSpace(args.Description)
	}
	if task == "" {
		bg.release()
		return "", fmt.Errorf("task: prompt is required")
	}
	label := strings.TrimSpace(args.Description)
	if label == "" {
		label = firstLine(task)
	}
	persona := strings.TrimSpace(args.Persona)
	if persona == "" {
		persona = strings.TrimSpace(args.Role)
	}
	if strings.TrimSpace(args.Verify) != "" {
		t.parentEmit(Event{Kind: EventNotice, ToolName: TaskToolName, Text: "background task ignores verify: " + label})
	}

	id := bg.nextID()
	t.parentEmit(Event{Kind: EventSubagentStart, Text: label, ToolName: TaskToolName, Args: raw})

	// 结果属于委派此任务的运行（即该 taskTools 的 owner），绝不属于共享当前深度的所有运行。
	owner, depth := t.owner, t.depth
	go func() {
		defer bg.release()
		child := t.newChild(label, persona)
		res, err := child.Run(ctx, task)
		// 与同步路径相同：后台子级被取消时补记其部分成本。
		t.reconcileCancelledChildCost(label, t.childModel(persona), res)
		if err != nil {
			t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Err: err.Error(), IsError: true})
			bg.PushFor(owner, depth, fmt.Sprintf("%s[task %d] %s failed: %v", BackgroundResultPrefix, id, label, err))
			return
		}
		text := strings.TrimSpace(res.Text)
		if text == "" {
			text = "(subagent finished without an answer)"
		}
		t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Output: text})
		result := fmt.Sprintf("%s[task %d] %s: %s", BackgroundResultPrefix, id, label, text)
		dropped := bg.PushFor(owner, depth, result)
		t.parentEmit(Event{Kind: EventNotice, ToolName: TaskToolName, Text: result})
		if dropped {
			t.parentEmit(Event{Kind: EventNotice, ToolName: TaskToolName,
				Text: "task result queue full: dropped an older result while storing task " + fmt.Sprint(id)})
		}
	}()

	return fmt.Sprintf("task %d started in background; result will be injected when ready", id), nil
}

// ---------------------------------------------------------------------------
// 共享任务板
// ---------------------------------------------------------------------------

// BoardMessage 是进程内任务板上的一条带类型消息。
type BoardMessage struct {
	Seq  int    `json:"seq"`
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// 任务板的消息种类和有界容量限制。
const (
	BoardKindInfo   = "info"
	BoardKindAsk    = "ask"
	BoardKindResult = "result"

	BoardMaxBodyBytes  = 4096
	BoardMaxMessages   = 1000
	BoardMaxTotalBytes = 2 << 20 // 2 MiB 的消息体字节数
)

var (
	ErrBoardSelfPost      = errors.New("task board: a task cannot post to itself")
	ErrBoardInvalidKind   = errors.New("task board: kind must be info, ask or result")
	ErrBoardMessageTooBig = errors.New("task board: message body exceeds 4096 bytes")
	ErrBoardFull          = errors.New("task board: capacity reached")
)

// TaskBoard 是一个小型、进程内、有界且并发安全的消息板，用于 agent 间协调。
// 它刻意不依赖任何外部包。
type TaskBoard struct {
	mu      sync.Mutex
	entries []BoardMessage
	seq     int
	total   int
}

// TaskBoardIO 是未来 board_post / board_read 工具应作为参数接收的窄接口。
// 它放在这里，使 internal/tools 可以依赖一个接口而不必导入 agent，且目前无需新建文件。
type TaskBoardIO interface {
	Post(from, to, kind, body string) (int, error)
	Read(since int, to string) []BoardMessage
}

// Post 添加一条消息并返回其单调递增的序号。它会拒绝自发消息、未知种类、超大消息体和已满的板。
func (b *TaskBoard) Post(from, to, kind, body string) (int, error) {
	if strings.TrimSpace(from) == strings.TrimSpace(to) {
		return 0, ErrBoardSelfPost
	}
	switch kind {
	case BoardKindInfo, BoardKindAsk, BoardKindResult:
	default:
		return 0, ErrBoardInvalidKind
	}
	if len(body) > BoardMaxBodyBytes {
		return 0, ErrBoardMessageTooBig
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) >= BoardMaxMessages || b.total+len(body) > BoardMaxTotalBytes {
		return 0, ErrBoardFull
	}
	b.seq++
	b.total += len(body)
	b.entries = append(b.entries, BoardMessage{Seq: b.seq, From: from, To: to, Kind: kind, Body: body})
	return b.seq, nil
}

// Read 返回 Seq > since 的消息，按序号排序。to 为空时返回所有收件人的消息；
// 否则只返回发给 to 的消息。
func (b *TaskBoard) Read(since int, to string) []BoardMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []BoardMessage
	for _, m := range b.entries {
		if m.Seq <= since {
			continue
		}
		if to != "" && m.To != to {
			continue
		}
		out = append(out, m)
	}
	return out
}
