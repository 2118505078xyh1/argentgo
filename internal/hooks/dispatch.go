package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	// maxCapture 限制从钩子捕获的单个流。
	maxCapture = 64 * 1024
	// summaryClip 限制 Result.Message 中引用的输出。
	summaryClip = 400
	// waitDelay 限制 Wait 在钩子进程退出后保持被捕获的 stdout/stderr 管道
	// 打开的时间。若钩子生成一个后台
	// 打开的时间。若钩子生成一个继承这些管道的后台
	// 进程，否则它们会保持打开越过命令超时，拖慢整个（并行）分发。该延迟
	// 仍足够长，让普通输出完成刷新。
	waitDelay = 500 * time.Millisecond
)

// Payload 是写入钩子 stdin 的 JSON 文档。
type Payload struct {
	Event     string          `json:"event"`
	Tool      string          `json:"tool,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	// CallID 可选地标识触发此钩子的工具调用。它
	// 被带入 PanicError，使错误的回调能与正在分发的
	// 调用关联起来。
	CallID string `json:"callId,omitempty"`
	// Prompt 是 UserPromptSubmit 和 Stop 的用户消息。
	Prompt string `json:"prompt,omitempty"`
	// Output 是 PostToolUse 的工具结果。
	Output string `json:"output,omitempty"`
	// Error 是 PostToolUseFailure 的工具失败。
	Error string `json:"error,omitempty"`
	// Path 是 FileChanged 事件影响的文件。其他事件为空；修改多个文件的调用方
	// 每个路径触发一个事件。
	Path string `json:"path,omitempty"`
	// HookPayload 是回合结束事件的可选事件专属细节
	// （EventInterrupt、EventStopFailure、EventTurnEnd）：分类
	// 原因、最后一条助手消息或错误文本。其他所有事件为空，因此
	// 现有 payload JSON 不变。
	HookPayload string `json:"hookPayload,omitempty"`
}

// Result 是一次分发的聚合决策。
//
// Blocked 仅当 PreToolUse 钩子以状态 2 退出时为 true。Message
// 携带拦截钩子的输出，或未拦截但失败的钩子的警告。
// 当 Dispatcher.StopBlocks 开启时，Stop 钩子以状态 2 退出也会置 Blocked，
// 并把续跑提示填入 Continuation；默认关闭时该字段恒为空串。
type Result struct {
	Blocked bool
	Message string
	// Continuation 是 Stop 钩子要求注入下一轮的续跑提示。
	// 仅在 StopBlocks 开启且 Stop 钩子以退出码 2 阻断时非空。
	Continuation string
	// AdditionalContexts 是本次分发中所有钩子声明的附加上下文，按 outcome 顺序聚合。
	// 由 Summarize 收集各 Outcome.AdditionalContext 的非空值并去重；零值为 nil，
	// 因此未使用附加上下文的现有路径逐字节不变。上层（app）据此入队，供 agent 在轮次边界注入。
	AdditionalContexts []string
}

// Outcome 表示某个钩子命令做了什么。
type Outcome struct {
	Event    Event
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	Blocked  bool
	// Continuation 保存 Stop 钩子的续跑提示，供 Summarize 聚合进 Result。
	Continuation string
	// AdditionalContext 保存钩子 stdout 中声明的附加上下文。它由
	// AdditionalContextFromOutcome 在 Outcome 产出时提取，已脱敏、去空白并按字节上限截断；
	// 供上层注入下一轮模型上下文。零值表示钩子未提供，现有行为逐字节不变。
	AdditionalContext string
	Elapsed           time.Duration
	Err               error
}

// detail 提取钩子输出中最有信息量的一段：优先 stdout，为空时回退 stderr，
// 并去除两端空白。摘要与 Stop 续跑判定共用同一提取规则。
func (o Outcome) detail() string {
	if text := strings.TrimSpace(o.Stdout); text != "" {
		return text
	}
	return strings.TrimSpace(o.Stderr)
}

// Message 是对结果的一行摘要。
func (o Outcome) Message() string {
	detail := clip(o.detail())

	var prefix string
	switch {
	case o.TimedOut:
		prefix = fmt.Sprintf("%s hook %q timed out after %s", o.Event, clipCommand(o.Command), o.Elapsed.Round(time.Millisecond))
	case o.Err != nil:
		prefix = fmt.Sprintf("%s hook %q could not run: %v", o.Event, clipCommand(o.Command), o.Err)
	case o.Blocked:
		prefix = fmt.Sprintf("%s hook %q blocked the call (exit 2)", o.Event, clipCommand(o.Command))
	case o.ExitCode != 0:
		prefix = fmt.Sprintf("%s hook %q failed (exit %d)", o.Event, clipCommand(o.Command), o.ExitCode)
	default:
		prefix = fmt.Sprintf("%s hook %q succeeded", o.Event, clipCommand(o.Command))
	}
	if detail == "" {
		return prefix
	}
	return prefix + ": " + detail
}

// CommandRunner 执行一条钩子命令。零值 Dispatcher 使用
// RunCommand，它会启动 shell。测试注入 CommandRunner 以观察
// 执行而不触碰进程表。runner 必须遵守 ctx 的
// 取消和截止时间，使 dispatcher 的超时仍能限制钩子。
type CommandRunner func(ctx context.Context, command Command, payload Payload) Outcome

// Dispatcher 运行钩子命令。
type Dispatcher struct {
	// Timeout 在命令自身未设置超时时限制单条命令；零
	// 表示 30 秒。
	Timeout time.Duration
	// Shell 覆盖命令解释器，用于测试和小众平台。
	// 默认是 bash -c（没有 bash 的 Windows 上为 cmd /C）。
	Shell func(command string) (name string, args []string)

	// Telemetry 非 nil 时，对 Dispatch/Run 执行的每个钩子调用一次，
	// 传入其耗时、退出状态和失败原因。它
	// 在钩子自身的 goroutine 中运行（钩子并行时），因此
	// 回调必须并发安全。留空（默认）
	// 回调必须并发安全。留空（默认）不会给运行增加任何遥测工作。
	Telemetry TelemetryFunc
	// SlowThreshold 在钩子耗时达到该值时将 HookTelemetry.Slow 置 true。
	// 达到该值时置为 true。零（默认）禁用标记。它从不改变
	// 零（默认）禁用该标记。它从不改变分发或计时；调用方决定如何对慢记录告警。
	SlowThreshold time.Duration

	// Runner 非 nil 时，替换所有钩子（同步或异步）的内置进程 runner。
	// 钩子，同步或异步。它是测试用于
	// 它是测试在不启动真实 shell 的情况下观察钩子的注入接缝。
	Runner CommandRunner

	// StopBlocks 选择性启用 Stop 钩子的阻断/续跑能力。默认 false：
	// Stop 仍只是事后通知，行为与既有版本逐字节一致。显式置 true 后，
	// 以退出码 2 退出且给出非空消息的 Stop 钩子会阻断本轮完成，并把消息
	// 作为续跑提示经 Result.Continuation 暴露给调用方。
	StopBlocks bool

	// Middlewares 是可选的中间件链，默认 nil。为空时 Dispatch 直接走
	// dispatchBase，与引入中间件之前逐字节相同；非空时它们按声明顺序
	// 包成洋葱模型，位于实际钩子执行之前。中间件可以改写 Payload、
	// 短路分发，或改写返回的 Result。
	Middlewares []Middleware

	// asyncWG 跟踪 Run 启动的后台钩子。Run 从不在其上等待：
	// 异步钩子不得延迟触发它的决策。WaitAsync
	// 让调用方排空它们（测试、优雅关闭）。
	asyncWG sync.WaitGroup
}

// dispatchBase 执行既有的 Run + Summarize 聚合路径。Middlewares 为空时
// Dispatch 直接调用它，代码路径与引入中间件之前逐字节一致。
func (d *Dispatcher) dispatchBase(ctx context.Context, config *Config, payload Payload) (Result, error) {
	outcomes, err := d.Run(ctx, config, payload)
	if err != nil {
		return Result{}, err
	}
	return Summarize(outcomes), nil
}

// Dispatch 为 payload.Event 运行所有匹配的钩子并返回
// 聚合决策。Middlewares 为空时直接走 dispatchBase；非空时先把本次调用的
// payload 复制一份，使 middleware 对 args 的改写只作用于本次分发，且并发
// 分发之间互不干扰，再把中间件链包在 base 外层执行。
func (d *Dispatcher) Dispatch(ctx context.Context, config *Config, payload Payload) (Result, error) {
	if len(d.Middlewares) == 0 {
		return d.dispatchBase(ctx, config, payload)
	}
	args := payload
	base := func() (Result, error) { return d.dispatchBase(ctx, config, args) }
	chain := composeMiddlewares(ctx, Event(payload.Event), &args, d.Middlewares, base)
	return chain()
}

// DispatchDocument 运行受信任文档中的钩子。
func (d *Dispatcher) DispatchDocument(ctx context.Context, document *Document, payload Payload) (Result, error) {
	if document == nil || document.Config == nil {
		return Result{}, ErrUntrusted
	}
	return d.Dispatch(ctx, document.Config, payload)
}

// FireFileChanged 为某个变更路径分发 FileChanged 生命周期事件。
// 它是便捷包装器，固定事件名和 payload
// 字段，使每个调用方产生相同形状的钩子文档。需要上下文的调用方
// 字段，使每个调用方产生相同形状的钩子文档。需要会话/cwd 上下文的调用方
// 在分发之前于掌握运行上下文的 app 侧桥接处填写这些字段。
func (d *Dispatcher) FireFileChanged(ctx context.Context, config *Config, path string) (Result, error) {
	return d.Dispatch(ctx, config, Payload{Event: string(FileChanged), Path: path})
}

// Run 为某个事件执行同步钩子，Priority 高的先执行。
// Priority 相同的钩子保持声明顺序（稳定排序），因此
// 文档钩子（优先级均为 0）的行为完全不变。异步钩子
// 在后台启动且永不进入返回的决策：决定性的
// 在后台启动且永不进入返回的决策：决定性钩子必须同步回答。带 StopPropagation
// 钩子必须同步回答。带 StopPropagation 的命令执行后结束运行，保护后面的钩子。
//
// 的命令执行后结束运行，保护后面的钩子。
// 同一优先级的钩子并发运行；各层级按优先级顺序排空。聚合延迟从 O(N×T) 降到每层约
// O(T)，而执行的集合和返回结果的顺序与
// 之前的串行循环完全一致。StopPropagation 是静态标志，因此 planRun 可以在任何命令启动前
// 计算短路边界。
func (d *Dispatcher) Run(ctx context.Context, config *Config, payload Payload) ([]Outcome, error) {
	if config == nil {
		return nil, nil
	}
	event, ok := ParseEvent(payload.Event)
	if !ok {
		return nil, fmt.Errorf("hooks: unknown event %q", payload.Event)
	}

	// FileChanged 匹配器描述受影响的文件（"*.go"），而非
	// 改变它的工具。FireFileChanged 和其他直接调用方只携带
	// Payload.Path；ForEvent 在匹配值为空时跳过带匹配器的钩子，
	// 否则文档记载的文件匹配器永不触发。
	matchValue := payload.Tool
	if event == FileChanged && payload.Path != "" {
		matchValue = payload.Path
	}
	commands := config.ForEvent(event, matchValue)
	sort.SliceStable(commands, func(i, j int) bool {
		return commands[i].Priority > commands[j].Priority
	})

	// 在同步层级之前启动后台钩子，使缓慢或
	// 被取消的层级无法阻止它们启动。Run 从不等待它们。
	d.launchAsync(ctx, planAsync(commands), payload)

	tiers := planRun(commands)
	total := 0
	for _, tier := range tiers {
		total += len(tier)
	}
	outcomes := make([]Outcome, 0, total)
	for _, tier := range tiers {
		outcomes = append(outcomes, d.runTier(ctx, tier, payload)...)
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
	}
	// Stop 的阻断/续跑是可选能力：仅在显式开启时作用于 Stop 事件。
	// 放在所有同步层级排空之后，因此既覆盖真实进程 runner，也覆盖注入的 runner。
	if d.StopBlocks && event == Stop {
		applyStopVerdict(outcomes)
	}
	return outcomes, nil
}

// applyStopVerdict 对 Stop 事件的结果应用续跑判定。每个未阻断、未超时的
// 结果都交给纯函数 stopHookVerdict；命中的结果被标记为阻断并携带续跑提示，
// 随后由 Summarize 聚合进 Result。context 已被取消时 Run 会提前返回，
// 不会走到这里，因此取消的回合不会被 Stop 钩子拦住。
func applyStopVerdict(outcomes []Outcome) {
	for i := range outcomes {
		if outcomes[i].Blocked || outcomes[i].TimedOut {
			continue
		}
		if block, continuation := stopHookVerdict(outcomes[i].ExitCode, outcomes[i].detail()); block {
			outcomes[i].Blocked = true
			outcomes[i].Continuation = continuation
		}
	}
}

// claimedCommands 遍历排序后的命令列表一次，返回实际
// 执行的命令：重复的命令字符串被丢弃（按优先级/声明顺序第一次
// 出现者胜出），StopPropagation 命令结束遍历，
// 结束遍历，保护后面每个钩子。重复项仍遵守其自身的停止边界。
// 保护后面每个钩子。重复项仍遵守其自身的停止边界。planRun 和 planAsync 都从这单次遍历派生，
// 它们不会就认领哪些命令产生分歧：每条命令只进入
// 因此它们不会就哪些命令被认领产生分歧：每条命令只进入
// 同步或后台路径之一，任一路径上的停止都会保护两条路径中后面的钩子。
func claimedCommands(commands []Command) []Command {
	seen := make(map[string]struct{}, len(commands))
	claimed := make([]Command, 0, len(commands))
	for _, command := range commands {
		if _, duplicate := seen[command.Command]; duplicate {
			if command.StopPropagation {
				break
			}
			continue
		}
		seen[command.Command] = struct{}{}
		claimed = append(claimed, command)
		if command.StopPropagation {
			break
		}
	}
	return claimed
}

// planRun 使用认领集合镜像旧串行循环的控制流：
// 同步命令按优先级分组，每组包含
// 同步命令按优先级分组，每组包含可并发执行的同优先级命令，按执行顺序排列。
func planRun(commands []Command) [][]Command {
	claimed := claimedCommands(commands)
	tiers := make([][]Command, 0, len(claimed))
	var current []Command
	haveTier := false
	var tierPriority int
	for _, command := range claimed {
		if command.Async {
			continue
		}
		if !haveTier || command.Priority != tierPriority {
			if haveTier && len(current) > 0 {
				tiers = append(tiers, current)
			}
			current = nil
			tierPriority = command.Priority
			haveTier = true
		}
		current = append(current, command)
	}
	if haveTier && len(current) > 0 {
		tiers = append(tiers, current)
	}
	return tiers
}

// resultCollector 从并发钩子运行中收集结果。互斥锁保证
// 收集无竞态；每个 goroutine 按其原始索引记录，使
// ordered() 能重建声明顺序而不受完成顺序影响。
type resultCollector struct {
	mu      sync.Mutex
	results map[int]Outcome
}

func (c *resultCollector) record(index int, outcome Outcome) {
	c.mu.Lock()
	c.results[index] = outcome
	c.mu.Unlock()
}

func (c *resultCollector) ordered(n int) []Outcome {
	outcomes := make([]Outcome, n)
	for index, outcome := range c.results {
		outcomes[index] = outcome
	}
	return outcomes
}

// runTier 并发运行一个优先级层级。被取消的 context 阻止新
// 钩子启动；已启动的钩子在其自身超时下完成并仍被收集。
// 钩子启动；已启动的钩子在其自身超时下完成并仍被收集。结果按层级声明顺序返回。
func (d *Dispatcher) runTier(ctx context.Context, tier []Command, payload Payload) []Outcome {
	collector := &resultCollector{results: make(map[int]Outcome, len(tier))}
	var wg sync.WaitGroup
	launched := 0
	for index, command := range tier {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		launched++
		go func(index int, command Command) {
			defer wg.Done()
			collector.record(index, d.guardedRunCommand(ctx, command, payload))
		}(index, command)
	}
	wg.Wait()
	return collector.ordered(launched)
}

// commandRunner 返回所有钩子使用的 runner。零值保持
// 原有的进程启动行为；只有需要注入接缝的测试才覆盖它。
func (d *Dispatcher) commandRunner() CommandRunner {
	if d.Runner != nil {
		return d.Runner
	}
	return d.RunCommand
}

// timeoutFor 解析命令的有效超时：先命令自身的值，
// 再 dispatcher 的值，再 defaultTimeout，并限制到 minTimeout。
// 同步进程路径和后台路径都使用它，使钩子无论以何种方式启动
// 都受到相同的限制。
func (d *Dispatcher) timeoutFor(command Command) time.Duration {
	timeout := command.Timeout
	if timeout <= 0 {
		timeout = d.Timeout
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout < minTimeout {
		timeout = minTimeout
	}
	return timeout
}

// guardedRunCommand 运行一个带 panic 隔离的钩子。钩子能 panic 穿出的唯一进程内
// 回调是 Dispatcher.Shell 和 Runner；一个 panic 的命令变成带 PanicError 的 Outcome，
// 的命令变成带 PanicError 的 Outcome，循环仍会到达
// 下一个命令。调用方未提供工具调用 ID 时，命令字符串是回退标识。
// 循环仍会到达下一个命令。当调用方未提供工具调用 ID 时，命令字符串是回退标识。
func (d *Dispatcher) guardedRunCommand(ctx context.Context, command Command, payload Payload) Outcome {
	// 仅在启用遥测时才取时钟，使默认路径除 RunCommand 已有开销外
	// 不支付额外计时成本。
	var started time.Time
	if d.Telemetry != nil {
		started = time.Now()
	}
	outcome := Outcome{Event: command.Event, Command: command.Command, ExitCode: -1}
	callID := payload.CallID
	if callID == "" {
		callID = command.Command
	}
	runner := d.commandRunner()
	err := guard(callID, func() error {
		outcome = runner(ctx, command, payload)
		return outcome.Err
	})
	if err != nil && outcome.Err == nil {
		outcome.Err = err
	}
	if d.Telemetry != nil {
		elapsed := time.Since(started)
		d.reportTelemetry(HookTelemetry{
			Name:     command.Command,
			Event:    command.Event,
			Elapsed:  elapsed,
			Err:      outcome.Err,
			ExitCode: outcome.ExitCode,
			TimedOut: outcome.TimedOut,
			Blocked:  outcome.Blocked,
			Slow:     d.SlowThreshold > 0 && elapsed >= d.SlowThreshold,
		})
	}
	// 在 Outcome 产出的统一收口处提取附加上下文：无论来自真实进程还是注入的 Runner，
	// 都走同一个纯函数，保证脱敏与字节上限一致。零值时字段保持空串，对现有路径无影响。
	outcome.AdditionalContext = AdditionalContextFromOutcome(outcome)
	return outcome
}

// RunCommand 用 stdin 上的 payload 运行一条钩子命令。
func (d *Dispatcher) RunCommand(ctx context.Context, command Command, payload Payload) Outcome {
	started := time.Now()
	outcome := Outcome{Event: command.Event, Command: command.Command, ExitCode: -1}

	if len(bytes.TrimSpace(payload.Args)) == 0 {
		payload.Args = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		outcome.Err = err
		outcome.Elapsed = time.Since(started)
		return outcome
	}

	timeout := d.timeoutFor(command)

	// 调用方提供的 Shell 已经选好解释器。先解析默认解释器
	// 调用方提供的 Shell 已经选好了解释器。先解析默认解释器会运行 findBash 的探测
	// （在 Windows 上最多五个进程，每个两秒超时），即使结果会被
	// 丢弃 —— 而且这发生在 runCtx 存在之前，因此不在钩子超时
	// 不在钩子超时的覆盖范围内。
	var name string
	var args []string
	if d.Shell != nil {
		name, args = d.Shell(command.Command)
	} else {
		name, args = shellCommand(command.Command)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(runCtx, name, args...)
	// 覆盖范围内。
	// 超时或被杀死的进程可能留下持有管道写端的孙进程；限制 Wait 等待这些管道的时间，使
	// 上面的超时真正限制挂钟时间。
	process.WaitDelay = waitDelay
	// 在进程自身退出后，context 仍可能在 Wait 排空被捕获管道期间
	// 在进程自身退出后 —— 留下后台子进程持有 stdout 的钩子
	// 到期 —— 留下持有 stdout 的后台子进程的钩子会让管道保持打开越过截止时间。os/exec 在
	// 这种情况下仍调用 Cancel，但 Kill 返回 ErrProcessDone：钩子没有
	// 超时。只跟踪对存活进程的 Kill，使已完成的钩子
	// 不被误标为超时（那也会丢弃其 exit-2 拦截）。
	var killedByDeadline atomic.Bool
	process.Cancel = func() error {
		err := process.Process.Kill()
		if err == nil && runCtx.Err() == context.DeadlineExceeded {
			killedByDeadline.Store(true)
		}
		return err
	}
	if payload.CWD != "" {
		process.Dir = payload.CWD
	}
	process.Env = append(os.Environ(), "ARGENT_HOOK_EVENT="+string(command.Event))
	if payload.CWD != "" {
		process.Env = append(process.Env,
			"ARGENT_PROJECT_DIR="+payload.CWD,
			"CLAUDE_PROJECT_DIR="+payload.CWD,
		)
	}
	process.Stdin = bytes.NewReader(body)
	stdout := &limitedBuffer{}
	stderr := &limitedBuffer{}
	process.Stdout = stdout
	process.Stderr = stderr

	runErr := process.Run()
	outcome.Elapsed = time.Since(started)
	outcome.Stdout = stdout.String()
	outcome.Stderr = stderr.String()

	outcome.ExitCode, outcome.TimedOut, outcome.Err = classifyRunResult(runErr, process.ProcessState, killedByDeadline.Load())
	// 直接调用 RunCommand 的调用方不经过 guardedRunCommand，也应在产出 Outcome 时填充该字段。
	// AdditionalContextFromOutcome 只读 Stdout 与 TimedOut，重复填充是幂等的。
	outcome.AdditionalContext = AdditionalContextFromOutcome(outcome)

	// 退出状态 2 是钩子协议的"拦截"信号，且只有 PreToolUse
	// 可以拦截动作。
	if command.Event == PreToolUse && outcome.ExitCode == 2 && !outcome.TimedOut && command.BlocksOnFailure() {
		outcome.Blocked = true
	}
	return outcome
}

// 可以拦截动作。
// classifyRunResult 将完成的进程等待转换为 Outcome 的状态字段。killedByDeadline 仅当超时确实杀死了
// 存活的钩子时为 true。否则，在 Wait 排空后台子进程遗留管道期间到期的 context
// 后台子进程遗留的，不得改写进程的真实退出
// 不得改写进程的真实退出状态：钩子已经完成，其 exit-2 拦截必须保留。
func classifyRunResult(runErr error, state *os.ProcessState, killedByDeadline bool) (exitCode int, timedOut bool, err error) {
	switch {
	case killedByDeadline:
		return -1, true, nil
	case runErr == nil:
		return 0, false, nil
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return exitErr.ExitCode(), false, nil
		}
		if state != nil {
			code := state.ExitCode()
			// ErrWaitDelay 和迟到的 context 错误只表示被捕获的管道比进程活得久；它们不是钩子失败。
			// 管道比进程活得久；它们不是钩子失败。
			if errors.Is(runErr, exec.ErrWaitDelay) ||
				errors.Is(runErr, context.Canceled) ||
				errors.Is(runErr, context.DeadlineExceeded) {
				return code, false, nil
			}
			return code, false, runErr
		}
		return -1, false, runErr
	}
}

// Summarize 将结果转换为拦截/警告决策。
func Summarize(outcomes []Outcome) Result {
	var result Result
	var messages []string
	// 附加上下文按 outcome 顺序聚合、去重；仅在实际遇到非空值时惰性分配 map，
	// 使绝大多数不带附加上下文的分发走零分配路径，行为逐字节不变。
	var seenContext map[string]bool
	for _, outcome := range outcomes {
		if outcome.Continuation != "" {
			result.Continuation = outcome.Continuation
		}
		if outcome.AdditionalContext != "" {
			if seenContext == nil {
				seenContext = make(map[string]bool, len(outcomes))
			}
			if !seenContext[outcome.AdditionalContext] {
				seenContext[outcome.AdditionalContext] = true
				result.AdditionalContexts = append(result.AdditionalContexts, outcome.AdditionalContext)
			}
		}
		if outcome.Blocked {
			result.Blocked = true
			messages = append(messages, outcome.Message())
			continue
		}
		if outcome.ExitCode != 0 || outcome.TimedOut || outcome.Err != nil {
			messages = append(messages, outcome.Message())
		}
	}
	result.Message = strings.Join(messages, "\n")
	return result
}

// shellCommand 为钩子命令选择解释器。钩子是 shell
// 脚本，因此安装了可用的 bash 时优先使用。
func shellCommand(command string) (string, []string) {
	if bash := findBash(); bash != "" {
		return bash, []string{"-c", command}
	}
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "/bin/sh", []string{"-c", command}
}

var (
	bashOnce sync.Once
	bashPath string
)

// findBash 返回一个确实能运行的 bash。在 Windows 上 PATH 中第一个 "bash"
// 通常是 System32 里的 WSL 别名，未安装发行版时会失败，因此每个候选探测一次。
// 未安装时会失败，因此每个候选探测一次。
func findBash() string {
	bashOnce.Do(func() {
		var candidates []string
		if found, err := exec.LookPath("bash"); err == nil {
			candidates = append(candidates, found)
		}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				`C:\Program Files\Git\bin\bash.exe`,
				`C:\Program Files\Git\usr\bin\bash.exe`,
				`C:\Program Files (x86)\Git\bin\bash.exe`,
				`C:\msys64\usr\bin\bash.exe`,
			)
		}
		for _, candidate := range candidates {
			if bashRuns(candidate) {
				bashPath = candidate
				return
			}
		}
	})
	return bashPath
}

// bashRuns 探测一个候选解释器。它是变量，使测试能在不启动进程的情况下
// 统计探测次数。
var bashRuns = func(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "-c", "exit 0").Run() == nil
}

// limitedBuffer 最多捕获 maxCapture 字节。
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxCapture - b.buf.Len(); room > 0 {
		if len(p) > room {
			_, _ = b.buf.Write(p[:room])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "\n…(truncated)"
	}
	return b.buf.String()
}

// clipRunes 将文本限制在 limit 字节内且不切开 UTF-8 rune。
// 末尾的省略号取代被丢弃的尾部。回退
// 末尾的省略号取代被丢弃的尾部。回退从 limit-1 向前走到前一个 RuneStart，
// 因此切点总落在 rune 边界（最坏情况只得到省略号）。
func clipRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit - 1 // leave room for the ellipsis
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func clip(text string) string {
	return clipRunes(text, summaryClip)
}

func clipCommand(command string) string {
	return clipRunes(command, 120)
}
