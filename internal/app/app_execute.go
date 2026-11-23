package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/bash"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/memory"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/skills"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// personaDir 是工作区存放具名子 agent 定义的位置。
const personaDir = ".argent/agents"

// personaCache 按工作区根目录只加载一次角色定义。Execute 在每个进程中会运行多次
// （TUI 提示、schema 重试、测试），因此每次提示都重新扫描目录会浪费 I/O。
var personaCache sync.Map // map[string]personaSnapshot

type personaSnapshot struct {
	personas map[string]agent.Persona
	err      error
}

// loadPersonasForRoot 返回为某个工作区根目录配置的角色。结果（包括第一个解析错误）会被缓存：
// 一个损坏的 agent 文件只报告一次，且绝不会阻塞其他有效的运行。
func loadPersonasForRoot(root string) (map[string]agent.Persona, error) {
	if cached, ok := personaCache.Load(root); ok {
		snap := cached.(personaSnapshot)
		return snap.personas, snap.err
	}
	personas, err := agent.LoadPersonas(filepath.Join(root, filepath.FromSlash(personaDir)))
	personaCache.Store(root, personaSnapshot{personas: personas, err: err})
	return personas, err
}

// PersonaSubagentConfig 把被委派任务请求的角色名解析为 `task` 工具应使用的子配置。
// 名称为空或未知时返回 false，不改动调用方的通用默认值。
// 它由 configureSubagents 注入到 agent.SubagentConfig.ResolvePersona，
// 使 internal/agent 保持对 internal/app 零依赖。
func PersonaSubagentConfig(root, name string) (agent.SubagentConfig, bool) {
	if strings.TrimSpace(name) == "" {
		return agent.SubagentConfig{}, false
	}
	personas, _ := loadPersonasForRoot(root)
	persona, ok := personas[name]
	if !ok {
		return agent.SubagentConfig{}, false
	}
	return persona.SubagentConfig(), true
}

// configureSubagents 把选择性启用的委派设置应用到根运行的选项上。启用信号刻意极简：
// 在 .argent/agents 下定义了至少一个角色的工作区，就是在请求委派。
// 不需要任何配置键、标志或 CLI 改动，且没有角色的工作区保持逐字节相同的行为
// （没有 `task` 工具，系统提示也不变）。
//
// 启用后，编排策略会追加到调用方的系统提示（绝不替换它），Subagent 使用
// --secondary-model 指定的次要模型；未配置时该值为空，agent 循环将其解释为
// "继承父模型"——以及默认深度限制 2。
// 各个 task 调用请求的角色名，通过注入的 ResolvePersona 回调回到这里解析，
// 因此 agent 包永远不需要导入本包。
//
// bg 是调用方为这一次根运行所拥有的后台结果队列。它绝不能是进程单例：
// 两个独立的会话/运行器（例如 TUI + --rpc，或两次连续的 Execute 调用）若都启用了角色，
// 否则会共享同一个队列，一个会话的结果会被抽取到另一个会话的记录中
// （所有根运行都位于深度 0）。按运行拥有队列也把有界容量淘汰限制在运行内部。
func configureSubagents(opts *agent.Options, res Resolved, bg *agent.BackgroundResults) {
	if opts == nil {
		return
	}
	personas, err := loadPersonasForRoot(res.Root)
	if err != nil || len(personas) == 0 {
		return
	}
	// 追加到调用方已经安装的系统提示（在启用信任的运行上是缓存稳定的静态那一半），
	// 而不是 res.System——后者仍带有逐运行的动态值。直接调用方把 opts.System 设为 res.System，
	// 因此其行为逐字节不变。
	opts.System = opts.System + "\n\n" + agent.OrchestratorPrompt()
	opts.Subagent = &agent.SubagentConfig{
		// 次要模型只赋给子代理，父级模型绝不改变；为空时保持零值以继承父级模型。
		Model:    res.Options.SecondaryModel,
		MaxDepth: agent.DefaultMaxDepth,
		ResolvePersona: func(name string) (agent.SubagentConfig, bool) {
			return PersonaSubagentConfig(res.Root, name)
		},
		Background: bg,
	}
}

// sharedDefs 持有进程级的 DefinitionCache。发现调用点（buildSystemWithTrust、contextPrompt
// 和 newToolset）都是包级函数，没有可共享生命周期的 App/Factory 接收者，
// 因此该缓存是延迟初始化的包级单例，而非结构体字段。DefinitionCache.Get/Refresh 受互斥锁保护，
// 所以并发运行器（TUI + --serve/--rpc）能安全地共享它。
//
// 目前还没有接入到 Refresh 的配置/文件变化钩子；指纹在定义文件发生磁盘变化时已会重新发现，
// 而在没有任何变化时，逐运行的 Get 调用会折叠为仅一次 stat 的命中。
var (
	defsOnce sync.Once
	defs     *DefinitionCache
)

func sharedDefs() *DefinitionCache {
	defsOnce.Do(func() { defs = &DefinitionCache{} })
	return defs
}

// contextPrompt 处理斜杠式的 /context 输入，方式与 reviewPrompt 处理 /review 相同：
// 它注入一份只读报告供模型总结。内置描述符通过 commands.NewSetWithBuiltins 注册，
// 报告本身来自 commands.ContextHook，使 app 和 commands 包共享同一个渲染器。
// 在路由之前会检查注册情况；如果内置项缺失，提示会保持原样不变。
func contextPrompt(res Resolved, stdio cli.IO, prompt string) (string, bool) {
	text := strings.TrimSpace(prompt)
	if text != "/context" && !strings.HasPrefix(text, "/context ") {
		return "", false
	}
	home := homeDir(stdio.Env)
	sources := commands.DefaultSources(res.Root, home)
	// 插件提供的命令会在其插件命名空间下加入命令集。该辅助函数应用与 MCP 和技能相同的
	// 用户/项目信任门槛，因此不受信任的检出无法向会话注入命令。
	sources = append(sources, pluginCommandSources(res)...)
	set := commands.NewSetWithBuiltins(sources)
	if _, ok := set.Get("context"); !ok {
		return "", false
	}

	docs := projectMemoryDocs(res.Root, stdio.Env, trustFromEnv(stdio.Env))
	var memoryDocs []commands.MemoryDoc
	for _, doc := range docs {
		memoryDocs = append(memoryDocs, commands.MemoryDoc{
			Path:      doc.Path,
			Bytes:     len(doc.Text),
			Truncated: doc.Truncated,
			Reason:    "per-file or total size cap",
		})
	}
	// 这里只需要缓存发现结果中的技能部分。上面的命令集不做缓存：
	// NewSetWithBuiltins 会就地修改它的集合。
	_, skillRegistry := sharedDefs().Get(nil, skills.DefaultRoots(res.Root, home))
	input := commands.ContextInput{
		MemoryDocs:   memoryDocs,
		SkillCount:   skillRegistry.Len(),
		CommandCount: set.Len(),
		SandboxMode:  res.Sandbox.String(),
		ApprovalMode: res.Approval,
		Ignore:       ignoreStatus(res.Root),
	}
	report, err := commands.ContextHook{Input: input}.Run(strings.TrimSpace(strings.TrimPrefix(text, "/context")))
	if err != nil {
		return "The /context command failed: " + err.Error() + ". Answer the user directly.", true
	}
	return "Read-only /context report:\n" + report +
		"\nSummarise the findings; do not modify files.", true
}

// ignoreStatus 报告哪些 .argentignore 文件会影响本工作区。规则数量刻意保持为零：
// IgnoreRules 把其模式保持为私有，而 /context 只需要一行尽力而为的状态信息。
func ignoreStatus(root string) commands.IgnoreStatus {
	var files []string
	dir := filepath.Clean(root)
	for depth := 0; depth < 32; depth++ {
		path := filepath.Join(dir, memory.IgnoreFileName)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			files = append(files, path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return commands.IgnoreStatus{Files: files}
}

// planModeEnabled 报告本次运行是否启用只规划模式。ARGENT_PLAN_MODE="1"（允许首尾
// 空白）时开启，默认与未知值关闭（fail-closed）。它只读取注入的 env，不改全局状态。
func planModeEnabled(env func(string) string, res Resolved) bool {
	_ = res // 预留配置接入点；当前仅由环境变量门控
	return env != nil && strings.TrimSpace(env("ARGENT_PLAN_MODE")) == "1"
}

// wireExitPlan 在 plan 模式开启时，为本次运行的每个工具注册表安装解锁回调，
// 并注册 exit_plan 工具。回调是一个共享闭包，持有本次运行的 chain 并调用
// EnablePlanMode(false) 解除只规划守卫；因此任意一个注册表触发 exit_plan
// 都会解锁整次运行。
//
// 默认（plan 模式关闭）时它直接返回，不触碰任何注册表、不注册工具，
// Specs()/公布的工具目录逐字节不变。
//
// 并发语义：chain 的 planMode 是原子标志，切换与守卫检查均并发安全；
// 解锁按"下一步生效"——同批并行工具可能在解锁之前就通过了守卫检查，
// 只有后续的工具调用才不再被 plan 守卫拦截。
func wireExitPlan(enabled bool, chain *security.Chain, regs []*tools.Registry) {
	if !enabled {
		return
	}
	unlock := func() { chain.EnablePlanMode(false) }
	for _, r := range regs {
		if r == nil {
			continue
		}
		r.SetPlanUnlock(unlock)
		tools.RegisterExitPlan(r)
	}
}

// checkpointWorkspaceDiffEnabled 报告是否启用"工作区差异检查点"增强：把 shell
// 等命令造成的间接文件改动也纳入检查点，使 /undo 可还原。默认关闭（fail-closed），
// 关闭时检查点与会话字节与启用前逐字节相同。
func checkpointWorkspaceDiffEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_CHECKPOINT_WORKSPACE_DIFF")) == "1"
}

// streamRecoveryEnabled 报告是否启用"流中断后非流式回补"恢复路径：提供方在流式
// 答复中途报错且已输出部分文本时，用同一 model/messages/tools 再发一次非流式补全。
// 默认关闭，关闭时 agent.Options.EnableStreamRecovery 为零值，请求与事件逐字节不变。
func streamRecoveryEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_STREAM_RECOVERY")) == "1"
}

// recoveryDetailEnabled 报告是否在恢复公告末尾追加 RecoveryPlan 细节后缀。
// 与 replayRecoveryEnabled 同构：默认关闭（fail-closed），只有 ARGENT_RECOVERY_DETAIL=1
// （允许首尾空白）才启用；其他任何值都保持历史公告逐字节不变。该开关已在
// cli/optin_metadata.go 登记（键 recovery_detail）。它只控制公告文本，
// 绝不触碰 NeedsConfirmation 或其它安全语义。
func recoveryDetailEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_RECOVERY_DETAIL")) == "1"
}

// guidanceDedupeEnabled 报告是否启用失败指引的签名去重：仅 ARGENT_GUIDANCE_DEDUPE=1
// （允许首尾空白）开启，默认关闭时 Execute 写回 false（与零值一致），每轮仍按历史
// 注入指引，消息流逐字节不变。与 retainGuidance 正交：后者决定指引记录的载荷形状，
// 本开关决定要不要写入这条记录。
func guidanceDedupeEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_GUIDANCE_DEDUPE")) == "1"
}

// imageInputMaxBytes 是 @图片路径 单张文件读入的上限（20 MiB）。
// 超限文件被 llm.ImageInputsFromPrompt 静默跳过，避免把超大文件塞进提示。
const imageInputMaxBytes = int64(20 << 20)

// imageInputEnabled 报告是否启用图像输入：从用户原文中扫描 @图片路径 并作为
// 图像 part 注入首条用户消息。仅 ARGENT_IMAGE_INPUT=1（允许首尾空白）开启；
// 默认关闭时不调用 llm.ImageInputsFromPrompt，agent.Options.InitialImages 保持
// 零值 nil，首条用户消息与接线之前逐字节相同。
func imageInputEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_IMAGE_INPUT")) == "1"
}

// toolArgCoercionEnabled 判定工具参数保守强转是否启用：默认开启（让 AI 更强），
// 仅 ARGENT_TOOL_ARG_COERCION=0 时紧急关闭。env 为 nil 时按默认处理。
func toolArgCoercionEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env("ARGENT_TOOL_ARG_COERCION")) != "0"
}

// progressRecoveryEnabled 判定"轮次内进度检查点"是否启用：默认开启（让崩溃后
// 有可恢复的中间输出，对用户是纯增益），仅 ARGENT_PROGRESS_RECOVERY=0 紧急关闭。
// 与 toolArgCoercionEnabled 同构，不进"默认关"的集中键体系。
func progressRecoveryEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env("ARGENT_PROGRESS_RECOVERY")) != "0"
}

// progressHintText 把恢复出的进度渲染为一段有界的中文公告文本，供 EventNotice
// 使用。它只用于用户可见的恢复公告，绝不注入模型历史（见 session.Progress 注释）。
func progressHintText(p session.Progress) string {
	var b strings.Builder
	b.WriteString("（崩溃恢复：上次中断于第 ")
	b.WriteString(strconv.Itoa(p.Turn))
	b.WriteString(" 轮")
	if text := strings.TrimSpace(p.AssistantText); text != "" {
		b.WriteString("，未完成输出：")
		b.WriteString(text)
	}
	if len(p.ToolCalls) > 0 {
		b.WriteString("，进行中的工具：")
		b.WriteString(strings.Join(p.ToolCalls, "、"))
	}
	b.WriteString("）")
	return b.String()
}

// Execute 用全新接线的工具注册表、策略和 agent 循环运行一个提示。
// 注册表是按运行划分的，因此安全链可以携带会话 id，而无需可变的全局状态。
func Execute(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, prompt string, onText func(string)) (agent.Result, error) {
	// 循环检测开关（opt-in ARGENT_LOOP_DETECTION）：每次运行都按本次 env 显式设置，
	// 使进程级原子开关不受上次运行残留影响；默认关闭时零副作用。
	configureLoopDetection(stdio.Env)
	configureSubagentSharedPrompt(stdio.Env)
	configureCompactDrain(stdio.Env)

	// 请求字节快照（opt-in ARGENT_REQUEST_SNAPSHOT）：开启时把实际发送的线协议
	// 字节追加到会话；关闭时 ctx 不变，llm 侧钩子零副作用。
	ctx = withRequestSnapshot(ctx, stdio.Env, sess)

	// 字节回放（opt-in ARGENT_RESUME_BYTE_REPLAY）：在恢复分类可用时计算候选，
	// 供下方 Runner 构造使用；校验失败时为 nil，回退普通 Chat 路径。
	var replay *replayCandidate

	// 在下方追加 review/context/动态上下文前缀之前，先保留调用方自己的原文。
	// 预算声明必须只来自用户：出现在注入前缀中的 "+500k" 不算声明。
	rawPrompt := prompt

	// 恢复分类：当历史被中断或降级时，在进入 agent 循环之前宣告这一情况；
	// 需要人工决定时则停止。干净的会话（常见情况）不产生通知，直接原样继续。
	if sess != nil {
		decision := recoveryDecisionFor(recoveryMessagesForClassification(sess.Messages()))
		// 字节回放候选：仅在中断提示、快照有效、协议/模型一致时非空。
		replay = replayFromSession(sess, stdio.Env, decision.Category, res.Model, res.Protocol)
		// 重放诊断是 opt-in 的纯增量信息：只在已有恢复公告且开关开启时，
		// 才读取记录流并追加诊断后缀。开关关闭时连 Records() 都不调用，
		// 决策与公告逐字节不变；诊断也绝不触碰 NeedsConfirmation（安全语义）。
		// 读取失败时保持原公告不变，诊断永不阻断运行。
		if replayRecoveryEnabled(stdio.Env) && decision.Notice != "" {
			if records, recErr := sess.Records(); recErr == nil {
				decision.Notice += replayDiagnosticsSuffix(records)
			}
		}
		// 恢复细节（opt-in ARGENT_RECOVERY_DETAIL）：与重放诊断同构的纯增量后缀。
		// 开启时把 recoveryDecisionFor 一次分类得到的 RecoveryPlan 细节作为文本
		// 追加到公告末尾；关闭时零调用、公告逐字节不变。与重放诊断一样，
		// 它只加信息，绝不触碰 NeedsConfirmation（安全语义由分类唯一决定）。
		if recoveryDetailEnabled(stdio.Env) && decision.Notice != "" {
			decision.Notice += recoveryDetailSuffix(decision)
		}
		// 进度恢复（默认开）：RecoverProgress 带两道过期检查，只在确有"未提交的中途
		// 进度"时返回有效值。把有界的进度文本作为用户可见的 EventNotice 公告追加，
		// 绝不注入模型历史、绝不触碰 NeedsConfirmation（安全语义仍由分类唯一决定）。
		// 纯进度崩溃可能被分类为干净会话（Notice 为空），此时单独发一条 progress 公告。
		if progressRecoveryEnabled(stdio.Env) {
			if p, ok := session.RecoverProgress(sess); ok {
				suffix := progressHintText(p)
				if decision.Notice != "" {
					decision.Notice += " " + suffix
				} else {
					EmitEvent(ctx, agent.Event{Kind: agent.EventNotice, Text: suffix})
					if onText != nil {
						onText(suffix)
					}
				}
			}
		}
		if err := announceRecovery(ctx, stdio, onText, decision); err != nil {
			return agent.Result{}, err
		}
	}

	// 加载角色是尽力而为的：格式错误的 agent 定义不得使运行失败。
	// configureSubagents 复用缓存结果，决定本次运行是否启用委派，并解析逐任务角色。
	_, _ = loadPersonasForRoot(res.Root)
	if injection, ok := reviewPrompt(res.Root, prompt); ok {
		prompt = injection + "\n\nUser request: " + strings.TrimSpace(prompt)
	}
	if injection, ok := contextPrompt(res, stdio, prompt); ok {
		prompt = injection + "\n\nUser request: " + strings.TrimSpace(prompt)
	}
	// Resolve 为每个调用方构建旧版系统提示。当解析出的信任决定启用时，
	// 通过感知边界的加载器重建项目记忆块，使 res.TrustProject 真正生效
	// （该标志可覆盖 ARGENT_TRUST_PROJECT=0 环境变量）。未启用时，
	// 保持 res.System 逐字节不变，使现有调用方不受影响。
	//
	// 在信任启用的路径上，系统消息只携带提示的静态那一半。cwd、推理强度和输出 schema 是
	// 逐运行的值：它们随下方第一条用户消息一起发送，因此目录或思考级别的改变
	// 不再使提供方缓存的系统前缀失效。buildStaticSystem(true) 保留对
	// ARGENT_TRUST_PROJECT=0 环境变量的显式标志覆盖。
	trustedSystem := res.System
	dynamicContext := ""
	// 按提示自动决策是选择性启用的性能旋钮。默认关闭时 decision 保持零值、
	// thinking 直接取 res.Thinking，下面不会切换模型，主路径逐字节不变。
	var decision PromptDecision
	autoDecide := autoDecideEnabled(stdio.Env)
	thinking := res.Thinking
	if autoDecide {
		// 在构建 dynamicContext 之前完成判定；只分析调用方原文，
		// review/context 等注入前缀不参与（它们也不是用户本人的请求）。
		decision = DecidePrompt(rawPrompt, promptHasCode(rawPrompt), promptMutatingSignal(rawPrompt))
		// 只提供"缺失时的默认"思考档位：用户已显式指定的档位始终优先。
		// MaxTurns 建议刻意不接入预算，避免覆盖用户/配置显式声明的上限。
		thinking = autoDecideThinking(res.Thinking, decision.Thinking)
	}
	if res.TrustProject {
		trustedSystem = buildStaticSystem(res.Root, stdio.Env, true, res.Cfg)
		dynamicContext = buildDynamicContext(res.Root, thinking, res.OutputSchema)
	}
	if dynamicContext != "" {
		prompt = dynamicContext + "\n\nUser request: " + strings.TrimSpace(prompt)
	}
	if res.MaxSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(res.MaxSeconds)*time.Second)
		defer cancel()
	}
	// 对超大工具输出选择性启用产物落盘。未启用时它原样返回 ctx，
	// 因此 tools 包保持普通的 clipTo 路径，每个现有运行都逐字节不变。
	// 派生的 ctx 会通过 agent 循环的 callTool(ctx, ...) 到达每个工具。
	ctx = configureOutputSpill(ctx, stdio.Env, res.Storage)

	// 统一 opt-in 激活面：Execute 是 runner/eval/tui 三条生产路径的唯一汇聚点，
	// 因此在这里单点解析 ARGENT_OPT_INS，并与配置的 optins 块取并集（env OR config）。
	// 默认全关时：所有 setter 写入 false（与零值一致）、reg 全 false、观察器为 nil，
	// 系统提示/事件序列/会话 JSONL 逐字节不变。
	optIns := mergeOptIns(ParseOptInEnv(stdio.Env), configOptIns(res.Cfg.Raw))
	agent.SetRefusalReason(optIns[OptInRefusalReason])
	agent.SetToolResultWrapping(optIns[OptInUntrustedResult])
	// 畸形参数纠错回灌（独立门控 ARGENT_TOOL_ARG_FEEDBACK=1，默认关闭）：
	// 模型发出的参数非空却不是合法 JSON 时不执行工具，把原始片段作为错误结果回灌。
	agent.SetToolArgFeedback(toolArgFeedbackOptInEnabled(stdio.Env))
	// 带图消息 token 估算（独立门控 ARGENT_MULTIMODAL_TRIM=1，默认关闭）：
	// 把 ContentParts 的文本/图像 part 计入上下文估算，避免低估压力。
	agent.SetMultimodalTrim(multimodalTrimOptInEnabled(stdio.Env))
	// 重试前持久上下文省略（opt-in ARGENT_RETRY_OMIT，默认关闭）：开启时注入
	// 按会话的回调，把失败尝试的助手消息及其工具结果以 ContextEdit 省略；
	// 关闭时恢复 nil 钩子，重试路径不读取，逐字节不变。
	if retryOmitEnabled(stdio.Env) {
		agent.SetRetryOmitHook(func(int) error { return omitLastAssistantAttempt(sess) })
	} else {
		agent.SetRetryOmitHook(nil)
	}
	agent.SetOutputRedaction(optIns[OptInOutputRedaction])
	agent.SetToolErrorReflection(optIns[OptInToolErrorReflection])
	// 工具参数保守强转默认开启：模型常把 schema 声明的数字/布尔写成字符串
	// （如 {"limit":"5"}），开启后仅在无损且 schema 明确时改写，减少工具调用失败，
	// 让 AI 能力更强。强转后的字节同时用于审批与执行（见 agent.effectiveToolArgs），
	// 无"批准 A 执行 B"语义差。ARGENT_TOOL_ARG_COERCION=0 可紧急关闭。
	agent.SetToolArgumentCoercion(toolArgCoercionEnabled(stdio.Env))
	// 纠正提示的压缩存活标记：独立 env 开关，默认关闭时写回 false（与零值一致），
	// nudge 记录形状逐字节不变。
	agent.SetRetainGuidance(os.Getenv("ARGENT_RETAIN_GUIDANCE") == "1")
	// 失败指引签名去重（opt-in ARGENT_GUIDANCE_DEDUPE）：默认关闭时写回 false
	// （与零值一致），逐轮注入指引的既有行为与会话记录逐字节不变。
	agent.SetGuidanceDedupe(guidanceDedupeEnabled(stdio.Env))
	SetContextPressure(optIns[OptInContextPressure])
	// 观察器必须在 SetContextPressure 之后构造，并用 replace 无条件覆盖 runner.Prompt
	// 基于进程级旧开关挂接的观察器：关闭时 newContextPressureObserver 返回 typed nil，
	// contextPressureOf 将其解读为 nil，轮末采集与上报都是空操作（比 with 多一次覆盖，
	// 但修复了"上次开、本次关"时过期观察器残留的错误通知）。
	ctx = replaceContextPressure(ctx, newContextPressureObserver())
	// 供 attempt_isolation 与三个压缩 wire 使用的注册表；关闭的键未 Set，Enabled 为 false。
	reg := &OptInRegistry{}
	for key, on := range optIns {
		reg.Set(key, on)
	}

	// 启动拓扑校验（选择性启用，默认关闭）：ARGENT_BOOT_TOPO=1 时用依赖图推导
	// 构造顺序并与历史顺序逐项比对，漂移则 fail-closed；默认不调用 TopoSort。
	// 下方 boot 八步仍按历史字面顺序执行，因此零行为漂移。
	if err := bootOrder(stdio.Env); err != nil {
		return agent.Result{}, err
	}

	bridge := newHookBridge(res, sess.ID(), stdio.Err)
	// hook 附加上下文队列：每次根运行一个。它既通过 ctx 交给 agent 在轮次边界抽取，
	// 也交给 hookBridge，使本次运行中所有生命周期/工具钩子声明的 additionalContext
	// 汇入同一队列。接线早于 bridge.start，连 SessionStart/UserPromptSubmit 的上下文也不丢失。
	// 没有钩子声明上下文时队列恒为空，注入为空操作，默认运行逐字节不变。
	hookCtxQ := &agent.HookContextQueue{}
	ctx = agent.WithHookContextQueue(ctx, hookCtxQ)
	if bridge != nil {
		bridge.hookCtxQ = hookCtxQ
	}
	bridge.start(ctx, prompt)
	defer bridge.end()

	approver := res.Approve
	if approver == nil {
		approver = approverFor(stdio, res)
		// accept-edits 模式：会话级批准一次后同类工具不再重复询问。默认（ask/
		// 未知值）解析为 ModeAsk，WrapForMode 原样返回 base，行为逐字节不变。
		approver = security.WrapForMode(approver, security.ParseApprovalMode(res.Approval))
	}
	// 持久化审批规则（opt-in ARGENT_APPROVAL_PERSIST，默认关闭，TODO #127）：
	// 在作用域审批器之上叠加跨会话的 always/deny 规则，落盘 <storage>/approvals.jsonl。
	// 作用域由 security.BashScopeFor 按空白切词生成分层通配前缀（#27）：
	// npm install lodash → "npm *" / "npm install *" / "npm install lodash"；
	// 非 shell 工具仍回落到工具名。只影响持久化的 scope 文本，不记自由文本参数。
	// 关闭时不构造该审批器，默认路径零文件 I/O、审批行为逐字节不变。
	if approvalPersistEnabled(stdio.Env) {
		approver = security.NewFileBackedScopedApproverHierarchical(approver, res.Storage,
			func(name, args string) []string { return security.BashScopeFor(name, args) })
	}
	audit := security.NewAuditLog(filepath.Join(res.Storage, "audit"))
	policy := policyFor(res)
	chain := security.NewChain(policy, approver, audit, sess.ID())
	// 分阶段审批（opt-in ARGENT_STAGED_APPROVAL，默认关闭）：用 stdio 交互的
	// StagedApprover 取代普通审批器，启用 once/always/reject/esc 状态机与
	// "始终允许"作用域记忆。关闭时不触碰链，逐字节不变。
	if stagedApprovalEnabled(stdio.Env) {
		approver = newStagedApprover(stdio.In, stdio.Err)
		chain.Approver = approver
		chain.EnableStagedApproval(true)
	}
	// 审计命令脱敏（opt-in ARGENT_AUDIT_SANITIZE，默认关闭）：用 bash 的
	// SanitizeForLog 在写入前脱敏审计 reason 中的 VAR=值/口令状串。
	if auditSanitizeEnabled(stdio.Env) {
		chain.SanitizeAudit = bash.SanitizeForLog
	}
	// 审批 diff 预览（opt-in ARGENT_APPROVAL_PREVIEW，默认关闭）：写类工具的
	// 审批提示附带脱敏的 +N/-M 行数摘要；不改变判定，关闭时提示逐字节不变。
	if approvalPreviewEnabled(stdio.Env) {
		chain.Preview = approvalDiffPreview
	}

	// 把内建 shell 的安全咨询接到本次运行的"策略 -> 审批 -> 审计"链上。
	// shell 包受分层规则约束不能 import security，因此通过进程级注入点把
	// "转调 chain" 的谓词交给它。内建 shell 现默认开启（只有 ARGENT_BUILTIN_SHELL=0
	// 才紧急关闭），因此受支持子集内的命令全部经解释器执行；当展开后的真实 argv
	// 与外层守卫审过的原始命令不同时，还会再经 oracle 复核（相同则跳过，避免同一
	// 审批器被重复询问）。仅当命令含不支持语法、整条回退系统 shell 时不经过解释器、
	// 也不读取 oracle。未注入时 CurrentSafety 默认放行。
	//
	// 并发安全：闭包只捕获不可变的 chain 指针与 ctx，每次调用用 json.Marshal
	// 生成全新的参数（无共享可变暂存区）；chain.Check 自身可并发调用。
	bash.SetSafetyOracle(func(cmd string) bool {
		args, err := json.Marshal(map[string]string{"command": cmd})
		if err != nil {
			// map[string]string 不可能序列化失败；一旦发生则 fail-closed。
			return false
		}
		return chain.Check(ctx, "bash", args) == nil
	})

	// plan 模式（可选）：启用后修改类命令在审批前被拒并给模型可读的恢复提示。
	// 默认关闭时 EnablePlanMode(false) 与零值等价，chain 决策逐字节不变。
	planMode := planModeEnabled(stdio.Env, res)
	chain.EnablePlanMode(planMode)

	// 命令风险分级（opt-in ARGENT_OPT_INS=command_risk，默认关闭）：
	// 开启时安全链同时对 shell 命令做静态风险分级，并把"高危命令需要显式确认"
	// 施加到声明 RequiresExplicitYes 的审批器上。关闭时该字段为零值，
	// Chain.check 中的整段风险门控逐字节跳过，原有决策不变。
	chain.EnableCommandRisk = optIns[OptInCommandRisk] || CommandRiskOptInEnabled(stdio.Env)

	// 检查层是链前面一个选择性启用的额外门控；门控关闭时 guard 就是链本身，
	// 因此每个现有运行都逐字节不变（策略 -> 审批 -> 审计）。Guardian 由
	// inspectionGuardFor 在总开关开启时按需追加，两开关为"与"关系。
	// Guardian 审阅从占位切到真实次要模型：非流式短答、3s 超时、严格 JSON，
	// 任何失败都 fail-closed 到 RequireApproval。未开启 ARGENT_GUARDIAN 时
	// guardianInspectorFor 返回 nil，这段审阅函数根本不会被调用。
	var guard tools.Guard = inspectionGuardFor(chain, approver, stdio, res.Options.SecondaryModel, guardianModelReview(res.Chatter, res.Options.SecondaryModel))
	// apply_patch 目标路径策略校验（opt-in ARGENT_PATCH_POLICY_PATHS，默认关闭）：
	// 补丁把目标藏在 input 正文里，安全链默认看不到其中的文件，用户配置的
	// denyPaths/allowPaths 与带 Path 的规则会静默失效。开启时在原有 guard 之前
	// 解析真实目标并逐条走策略；关闭时 guard 就是原链，逐字节不变。
	guard = patchPolicyGuardFor(guard, chain, stdio.Env)

	toolset := newToolset(ctx, stdio, res, sess, guard, reg)
	defer toolset.Close()
	// LSP 工作区诊断快照（opt-in ARGENT_LSP_DIAGNOSTICS=1）：运行结束时用已启动
	// 的 client 拉一次诊断写快照，供下一次 doctor 进程读取；不懒启动、不联网。
	defer CaptureLSPWorkspaceDiagnostics(stdio.Env, res.Storage, res.Root)

	// 技能触发器 opt-in（ARGENT_SKILL_TRIGGERS / 配置 skillTriggers）：默认关闭时
	// 不调用 SetTriggerContext，注册表不收紧、队列不新增条目，逐字节不变。
	// 开启时把当前上下文（提示中的路径、用户原文、模型家族）解析为 active 技能集：
	//   ① 收紧 use_skill —— 只有 active 技能可按名称加载（系统提示前缀不动）；
	//   ② 在首个轮次边界经 hookCtxQ 注入 active 索引，让模型知道哪些技能真正可用。
	// 该队列是既有的 additional-context 注入机制，无需改动 agent 循环或缓存前缀。
	if skillTriggersEnabled(res.Cfg.Raw, stdio.Env) && toolset.skills != nil {
		model := res.Model
		if strings.TrimSpace(model) == "" {
			model = res.Options.Model
		}
		active := toolset.skills.SetTriggerContext(
			skillTriggerPath(rawPrompt, res.Root), rawPrompt, skillTriggerModelFamily(model))
		// 只有确实收窄（有技能被触发器排除）时才注入；无触发器技能全部 active，
		// 注入会是系统提示索引的重复，故跳过以保持逐字节不变。
		if len(active) < toolset.skills.Len() {
			if index := skills.IndexTextFor(active); index != "" {
				hookCtxQ.Add(strings.TrimSpace(index) +
					"\n\n以上为当前上下文实际可用的技能；其余技能本次不能通过 use_skill 加载。")
			}
		}
	}

	// plan 模式开启时把解锁回调接到每个工具注册表并注册 exit_plan。
	// 关闭时 wireExitPlan 直接返回，不触碰注册表，逐字节不变。
	wireExitPlan(planMode, chain, toolset.base.regs)

	// 后台作业注册表（opt-in ARGENT_JOBS / ARGENT_OPT_INS=jobs）：默认关闭时
	// wireJobs 无副作用，jobRegistry 仍为 nil，background_after_ms 被静默忽略。
	// 开启时把注册表接到本次运行的工具注册表，退出钩子在 toolset.Close 之前
	// （defer LIFO）回收仍存活的后台进程。
	_, closeJobs, jobsErr := wireJobs(
		stdio.Env,
		filepath.Join(res.Storage, "sessions", sess.ID()),
		toolset.base.regs,
	)
	if jobsErr != nil {
		return agent.Result{}, jobsErr
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = closeJobs(shutdownCtx)
	}()

	// 持久化 shell 会话（opt-in ARGENT_PERSISTENT_SHELL=1）：默认关闭时
	// wireShellSessions 无副作用，bash 仍走原有系统 shell 路径。开启时把
	// 会话管理器接到本次运行的全部注册表。defer 按 LIFO 先于 closeJobs 与
	// toolset.Close 关闭会话：先关长驻 shell，再停后台作业，最后释放工具集。
	defer wireShellSessions(stdio.Env, toolset)()

	var runner agent.ToolRunner = toolset
	if bridge != nil {
		// auto_verify opt-in：开启时让编辑类工具成功后附带 LSP 诊断回灌，帮助模型
		// 自我修正。默认关闭时 autoDiagnostics 为零值，诊断路径逐字节不变。
		runner = &hookedTools{
			inner: toolset, bridge: bridge, mutationPaths: toolset.base.mutationPaths,
			autoDiagnostics: optIns[OptInAutoVerify],
			preApproval:     hookVerdictEnabled(stdio.Env), // ARGENT_HOOK_VERDICT=1
			rewrite:         hookRewriteEnabled(stdio.Env), // ARGENT_HOOK_REWRITE=1
			lspFileNotifier: lspWatchNotifier(stdio.Env),   // ARGENT_LSP_WATCH=0 时注入 nil，完全关闭。
		}
	}
	// 来源 URL 标注（默认启用）：web_fetch/web_search 成功结果前加一行已脱敏
	// 来源，让模型能在答案里明确引用抓取自哪个页面（aider 审计项）。
	// 独立于 hooks bridge，因此无 hook 文档时也生效；其它工具逐字节不变。
	runner = &sourceAnnotatingTools{inner: runner}

	// 读后写新鲜度校验（opt-in ARGENT_READ_BEFORE_WRITE，默认关闭）：
	// 用会话级账本包装 runner，把 read 快照注入工具 ctx；账本未注入时所有工具
	// 直接跳过检查，默认路径逐字节不变。
	if readBeforeWriteEnabled(stdio.Env) {
		runner = &freshnessTools{inner: runner, ledger: tools.NewFileFreshness()}
	}

	tracker := newCheckpointTracker(res.Root, sess)
	tracker.workspaceDiff = checkpointWorkspaceDiffEnabled(stdio.Env)
	var evidence agent.CompactionEvidence
	compactor := newSessionCompactorWithOpts(res, sess, reg)
	// 后台预压缩（opt-in ARGENT_BACKGROUND_COMPACT）：包装 compactor 并把压力
	// 观察者接到 agent 循环。关闭时返回原 compactor 与 nil observer，同步路径不变。
	wrappedCompactor, compactObserver := newBackgroundCompactor(compactor, stdio.Env)
	compactor.env = stdio.Env
	// 压缩事件钩子（opt-in ARGENT_COMPACTION_HOOK，默认关闭）。
	if bridge != nil && compactionHookEnabled(stdio.Env) {
		compactor.fireHook = bridge.fire
		compactor.compactionHook = true
	}
	// 持久性屏障：在每个开销较大的步骤之前刷写 JSONL 会话。
	// 这些是按 Runner 划分的字段，因此并发的 Execute 调用（TUI + RPC，或多个会话）
	// 不会清除彼此的屏障，也不会同步绑定到更早运行的过期会话。
	flush := func() error { return sess.Sync() }

	// 受 schema 约束的运行会缓冲根答案（文本增量 + 停止），直到它验证通过。
	// 之后一次纠正轮次可以在客户端替换该答案，而不是追加第二份副本。
	// 没有 --output-schema 的运行保留原有的实时流式路径，逐字节不变。
	schemaMode := strings.TrimSpace(res.OutputSchema) != ""
	var pendingTexts []agent.Event
	var pendingStop *agent.Event
	mutatingRan := false

	// 上下文压力观察器由 runner.Prompt 按开关附着。关闭时为 nil，下面的采集与
	// 轮末上报都是空操作，不构造 MessageLike、不发事件，既有事件字节完全不变。
	observer := contextPressureOf(ctx)

	lifecycleHooksOn := hookLifecycleEnabled(stdio.Env)
	forwardEvent := func(event agent.Event) {
		// 生命周期 hook 映射（opt-in ARGENT_HOOK_LIFECYCLE，默认关闭）：
		// 把轮次边界/中断/子 agent 结束事件投递给 hook bridge。
		fireLifecycleHook(bridge, lifecycleHooksOn, event)
		// 发布到按运行划分的事件 hub，使 --serve / --rpc 的订阅者
		// （SSE /v1/event、event/subscribe）看到真实的 agent 事件。
		// 接收器由 runner.Prompt 注入 ctx；不存在时为空操作。
		EmitEvent(ctx, event)
		// 供给实时用量工具，使其报告真实的 token 总量。
		if event.Kind == agent.EventUsage {
			toolset.UpdateUsage(event.Turn, event.Usage, event.TotalUsage)
		}
		if event.Kind == agent.EventTextDelta && event.Depth == 0 && onText != nil {
			onText(event.Text)
		}
		// 选择性启用：关闭时 observer 为 nil，collectEvent 立即返回，不构造 MessageLike。
		observer.collectEvent(event)
	}
	// 模型摘要（opt-in model_summary）依赖注入：用 secondary-model 发起、复用本次 ctx
	// 与事件通道。wire 内部按 reg 门控 + summaryModel 非空判断，默认全关时零调用、零事件。
	compactor.ctx = ctx
	compactor.chatter = res.Chatter
	compactor.summaryModel = res.Options.SecondaryModel
	compactor.emit = forwardEvent
	flushAnswer := func() {
		for _, event := range pendingTexts {
			forwardEvent(event)
		}
		pendingTexts = nil
		if pendingStop != nil {
			forwardEvent(*pendingStop)
			pendingStop = nil
		}
	}

	// 用户声明的单轮上限是一个额外的累计限制；在构建循环之前，
	// 从原始提示（绝不是注入的前缀）中解析它。
	budget := agent.Budget{
		MaxTurns:       res.MaxTurns,
		MaxTotalTokens: res.MaxTokens,
		MaxCostUSD:     res.MaxCostUSD,
	}
	applyDeclaredTurnBudget(rawPrompt, &budget)

	// 图像输入（opt-in ARGENT_IMAGE_INPUT）：仅开启时从用户原文（绝不是注入的
	// 前缀）扫描 @图片路径并读取图片。关闭时零调用，initialImages 保持 nil，
	// 首条用户消息走原纯文本路径，逐字节不变。
	var initialImages []llm.ImageInput
	if imageInputEnabled(stdio.Env) {
		initialImages = llm.ImageInputsFromPrompt(rawPrompt, res.Root, imageInputMaxBytes)
	}

	loop := &agent.Runner{Opts: agent.Options{
		BeforeRequest: flush,
		BeforeTool:    flush,
		Model:         res.Model,
		MaxTurns:      res.MaxTurns,
		MaxParallel:   res.MaxParallel,
		Root:          res.Root,
		System:        trustedSystem,
		Chatter:       res.Chatter,
		Tools:         runner,
		// 字节回放候选：nil 时三个字段为零值，agent 首请求走逐字节不变的普通 Chat。
		ReplayBody:     replayBodyOf(replay),
		ReplayModel:    replayModelOf(replay),
		ReplayProtocol: replayProtocolOf(replay),
		// 图像输入（opt-in ARGENT_IMAGE_INPUT）：nil 时首条用户消息逐字节不变。
		InitialImages: initialImages,
		// 绝不公布沙箱/拒绝规则禁止的工具。Run() 仍会对每次调用做门控；
		// 这只是避免模型浪费轮次。
		FilterSpecs: func(specs []llm.ToolSpec) []llm.ToolSpec {
			visible := filterVisibleSpecs(specs, res, toolset, policy)
			// 精简目录（默认自动启用，用户零配置）：只公布核心工具 + tool_search，
			// 其余工具由模型自己经 tool_search 按需发现。仅在显式设置调试逃生舱
			// ARGENT_FULL_TOOLS 时原样返回全部。
			visible = tools.LazyFilterSpecs(visible)
			// 网关模式（opt-in）：mcp 不在 lazy 核心集里会被过滤掉，但它是唯一的
			// MCP 入口，必须始终公布。每个工具 spec 本身从 toolset.Specs() 里
			// 取出（它与会话开始时的固定 schema 逐字节一致），避免在此重建。
			if toolset.gateway {
				for _, spec := range visible {
					if spec.Function.Name == mcpGatewayToolName {
						return visible
					}
				}
				visible = append(visible, mcpGatewaySpec(toolset.gatewayServers))
			}
			return visible
		},
		Approve: approver,
		Record:  agent.RecorderFunc(sess.Append),
		// 进度写侧（默认开）：agent 侧按字节节流后调用，这里把累积的文本/工具调用
		// 持久化为 progress 记录。进度是纯优化，SyncProgress 失败被忽略，绝不影响运行。
		ProgressSink: func() agent.ProgressSink {
			if !progressRecoveryEnabled(stdio.Env) || sess == nil {
				return nil
			}
			return agent.ProgressSinkFunc(func(turn int, text string, toolCalls []string) {
				_ = sess.SyncProgress(session.Progress{Turn: turn, AssistantText: text, ToolCalls: toolCalls})
			})
		}(),
		Budget:      budget,
		Prices:      res.Prices,
		SkillAuthor: skillWriter{},
		SkillLister: skillLister{toolset.skills},
		Checkpoint: func(cp agent.Checkpoint) {
			tracker.capture(cp, prompt)
		},
		MutationPaths: toolset.base.mutationPaths,
		// 选择性启用的有界验证。没有配置验证命令时，两个辅助函数都返回零值，
		// 使默认运行逐字节不变。
		Verify:               verifyFuncFor(res, optIns[OptInAutoVerify]),
		MaxVerifyReflections: verifyReflectionsFor(res),
		// 轮次中压缩由 app 层注入，使 agent 永不导入 session。
		// 禁用/负的阈值会得到 0，从而成为空操作。
		Compactor:          wrappedCompactor,
		CompactObserver:    compactObserver,
		ContextCalibration: contextCalibrationEnabled(),
		CompactThreshold:   compactor.CompactThreshold(),
		// 流中断回补（opt-in ARGENT_STREAM_RECOVERY）：默认关闭时为零值，
		// agent 的恢复分支整体短路，请求体与事件逐字节不变。
		EnableStreamRecovery: streamRecoveryEnabled(stdio.Env),
		// TTSR 流式规则注入（opt-in ARGENT_TTSR_RULES=<json>）：默认无配置为 nil，
		// agent 侧观察器为 nil，流式路径逐字节不变。配置存在时命中 Retry 会中止流、
		// 注入提醒并从同点重发一次。加载失败静默降级为 nil 并经事件发一条 warn。
		TTSRRules: ttsrRulesForRun(stdio.Env, res.Root, func(msg string) {
			EmitEvent(ctx, agent.Event{Kind: agent.EventNotice, Text: msg})
		}),
		// 上下文溢出时允许一次有界的"压缩后重放"：仅在提供方明确报告 context
		// overflow、配置了 compactor 且压缩确实缩短时才重试；其它情况走原失败路径
		// 逐字节不变。这是上下文管理要素的优雅降级，不增加正常路径的开销。
		EnableCompactRetry: true,
		OnCompactionEvidence: func(e agent.CompactionEvidence) {
			evidence = e
		},
		Emit: func(event agent.Event) {
			if schemaMode {
				switch {
				case event.Kind == agent.EventTextDelta && event.Depth == 0:
					pendingTexts = append(pendingTexts, event)
					return
				case event.Kind == agent.EventStop:
					stop := event
					pendingStop = &stop
					return
				case event.Kind == agent.EventCheckpointReady:
					// agent 会在包含变更型工具的批次之前立即发出这个事件，
					// 因此它是副作用已经发生、绝不能重复的可靠信号。
					mutatingRan = true
				}
			}
			forwardEvent(event)
		},
	}}
	// workspace-diff opt-in：批次后把 shell 间接改动并入检查点。仅在开启时挂接，
	// 默认 AfterBatch 保持 nil，runTools 的 defer 不注册，逐字节不变。
	if tracker.workspaceDiff {
		loop.Opts.AfterBatch = tracker.afterBatch
	}
	// 后台结果队列优先由 runner 注入（per-session 持久）：后台子任务可能比触发它的
	// 提示活得久，持久队列让它的结果能在后续提示里被抽取，而不是投递到一个已被
	// 丢弃的旧队列。直接调用 Execute（eval/测试）时 ctx 无队列，回退到一次性队列，
	// 行为与接线前一致。换会话时 runner 会注入新队列，杜绝跨会话串味。
	bg := agent.BackgroundResultsFrom(ctx)
	if bg == nil {
		bg = agent.NewBackgroundResults(0, 0)
	}
	configureSubagents(&loop.Opts, res, bg)
	// attempt_isolation opt-in：验证失败回调把失败尝试分叉为旁支会话，并通过
	// wireAttemptRoot 在任务运行前写前置尝试根、把真实 rootID 带进失败信息。
	// 关闭时两者都返回 nil：OnVerifyFail/BeginAttempt/EndAttempt 保持未装配，
	// runTaskVerified 的新分支全部跳过，AttemptID 仍为合成串，逐字节不变。
	if loop.Opts.Subagent != nil {
		loop.Opts.Subagent.OnVerifyFail = wireVerifyFail(sess, reg)
		loop.Opts.Subagent.BeginAttempt, loop.Opts.Subagent.EndAttempt = wireAttemptRoot(sess, reg)
	}
	// 仅性能旋钮：判定建议使用次级模型时切换根运行的主模型；FallbackModels 的
	// 顺序语义由 applyAutoDecideModel 保持（次级模型成为新的第 0 项，原回退项后移），
	// app 层基于 fallbackChatter 的单回退链同样保持"先主后回退"。
	// 默认关闭时 autoDecide 为 false，模型字段保持 res.Model，逐字节不变。
	if autoDecide && decision.UseSecondary {
		applyAutoDecideModel(&loop.Opts, res.Options.SecondaryModel)
	}
	// 按模型的编辑格式指引是选择性启用的，因此字节精确的系统提示断言，
	// 以及锁定自己提示的调用方，默认都保持字节不变。设置 ARGENT_EDIT_GUIDE=1 即可追加。
	// 在这里（configureSubagents 之后）追加，可保持编排器追加顺序不变。
	if env := stdio.Env; env != nil && env("ARGENT_EDIT_GUIDE") == "1" {
		loop.Opts.System = agent.AppendEditFormat(loop.Opts.System, res.Model)
	}
	// magic keywords（opt-in ARGENT_MAGIC_KEYWORDS=1）：用户原话里出现的
	// ultrathink/chillax 等词只在 prose 中生效（代码块/路径不触发），注入隐藏指令。
	// 关闭或未命中时系统提示逐字节不变。
	loop.Opts.System = applyMagicKeywords(loop.Opts.System, rawPrompt, stdio.Env)
	// repo map 只读声明同样是选择性启用的：默认不追加，以免改变字节精确的系统提示断言。
	// 设置 ARGENT_REPO_MAP_HINT=1 时，明确告诉模型"文件出现在 repo map 中不等于获得编辑授权"。
	if repoMapHintEnabled(stdio.Env) {
		loop.Opts.System += "\n" + repoMapReadOnlyHint()
	}
	// codegraph opt-in（ARGENT_OPT_INS=codegraph）：默认关闭时不建索引、不注入工具，
	// 系统提示/tool 列表逐字节不变。开启时提示模型可用 codegraph_query，并把本地
	// 索引注入工具注册表；索引构建失败只写 warn 并降级为不可用，不影响本次运行。
	if codeGraphHintEnabled(reg) {
		loop.Opts.System += "\n" + codeGraphCapabilityHint()
	}
	wireCodeGraph(stdio, res.Root, toolset, reg)
	// ask_user 仅在交互式终端安装：让模型遇到歧义时能向用户提问而非靠猜。
	// 非 TTY（--rpc/headless）不注册工具、不读 stdin，工具列表与接线前逐字节不变。
	wireAskUser(stdio, toolset)
	// worktree 三件套默认安装（ARGENT_WORKTREE_TOOLS=0 关闭）：让模型能创建
	// 隔离工作区做改动，不碰当前工作树。显式注册，默认工具清单逐字节不变。
	wireWorktrees(stdio, toolset)
	// models 工具默认安装（ARGENT_MODELS_TOOL=0 关闭）：把 provider 模板表
	// 暴露给模型，方便其选择正确的 provider id。显式注册，默认清单逐字节不变。
	wireModels(stdio, toolset)
	// DAP 调试工具默认注册（只公布工具、不启动进程）；ARGENT_DAP=0 时紧急关闭。
	wireDap(stdio, toolset)
	// 浏览器工具默认注册（只公布工具、不启动 Chrome）；ARGENT_BROWSER=0 时关闭。
	// 与 web_fetch/web_search 分层：静态页用 web_fetch/web_fastfetch，需 JS/交互才用 browser。
	wireBrowser(stdio, toolset)
	// computer 桌面工具默认注册（截图+坐标操作 Windows 桌面）；ARGENT_COMPUTER=0 关闭。
	// 与 browser 分层：Web 用 browser 的文本 refs，原生桌面应用才用 computer。
	wireComputer(stdio, toolset)
	// memory_consolidate 默认关（会改写记忆日志）：ARGENT_MEMORY_CONSOLIDATE=1
	// 才注册。显式注册，默认工具清单逐字节不变。
	wireMemoryConsolidate(stdio, toolset)
	// ast_edit 默认安装（只做结构预览 diff，写盘交给 apply_patch）；ARGENT_AST_EDIT=0 关闭。
	wireAstEdit(stdio, toolset)
	// 统一 exec 会话（opt-in ARGENT_EXEC_SESSION）：注册 write_stdin 工具。
	wireExecSession(stdio, toolset)
	// verify 自动运行声明是选择性启用的：只有配置了验证命令且显式设置
	// ARGENT_VERIFY_AUTORUN_HINT=1 时才追加，默认保持字节精确的系统提示不变。
	// 用 verifyConfigured 判断配置，未配置时提示为空，不会多出多余的空行。
	if verifyAutoRunHintEnabled(stdio.Env) {
		if hint := verifyAutoRunHint(verifyConfigured(res)); hint != "" {
			loop.Opts.System += "\n" + hint
		}
	}
	// 会话续接摘要（opt-in ARGENT_CONTINUATION_BRIEF）：resume/追问时把上一段的
	// 目标/改动文件/失败/下一步作为有界的 system 段注入，补齐跨 Execute 的历史断裂。
	// 默认关闭时不追加，system 字节与接线前一致。无历史时原语返回空串，同样不追加。
	if continuationBriefEnabled(stdio.Env) && sess != nil {
		if brief := BuildContinuationBrief(sess.Messages(), 2000); brief != "" {
			loop.Opts.System += "\n" + brief
		}
	}
	// 压缩摘要的扩展字段续接段（opt-in ARGENT_SUMMARY_FIELDS_V2=1）：用 V2 只读投影
	// 把 Objective/Requirements/Decisions/Work State/Relevant Files/Important Context
	// 作为独立段注入，与默认四段互补。默认关闭时零调用、system 逐字节不变。
	if summaryFieldsV2Enabled(stdio.Env) && sess != nil {
		if brief := BuildContinuationBriefV2(sess.Messages(), 2000); brief != "" {
			loop.Opts.System += "\n" + brief
		}
	}
	system := loop.Opts.System

	result, err := loop.Run(ctx, prompt)
	if schemaMode && err == nil {
		if invalid := validateOutputText(res.OutputSchema, result.Text); invalid != nil {
			switch {
			case mutatingRan:
				// 变更型工具已经运行过。重新运行提示会重复其副作用，
				// 因此应暴露失败，而不是重试。
				err = fmt.Errorf("output schema validation failed (auto-retry skipped: a mutating tool already ran): %w", invalid)
			default:
				// 没有可重复的工具副作用：在*同一*对话中追加一条纠正性的用户轮次，
				// 再请模型回答一次。这完全绕过 agent 循环——不公布工具，也不发出工具事件——
				// 因此客户端既不会重复文本，也不会重复工具工作。
				corrected, cerr := correctSchemaAnswer(ctx, loop.Opts.Chatter, loop.Opts.Model, system, result.Messages, res.OutputSchema, invalid)
				if cerr != nil {
					err = fmt.Errorf("output schema retry failed: %w", cerr)
				} else if again := validateOutputText(res.OutputSchema, corrected); again != nil {
					err = fmt.Errorf("output schema validation failed after one retry: %w", again)
				} else {
					// 替换（而不是追加到）已缓冲的第一个答案。
					result.Text = corrected
					result.Turns++
					pendingTexts = []agent.Event{{Kind: agent.EventTextDelta, Turn: result.Turns, Depth: 0, Text: corrected}}
					pendingStop = &agent.Event{Kind: agent.EventStop, Reason: agent.StopComplete}
					_ = sess.Append("user", schemaCorrectionUserTurn)
					_ = sess.Append("assistant", corrected)
				}
			}
		}
	}
	flushAnswer()
	// 轮末（全部既有事件含被缓冲的 stop 已转发完毕）再观察压力：仅在 opt-in 且
	// 告警非空时追加一条 EventNotice；关闭时 observeStop 是空操作，事件流不变。
	observer.observeStop(func(ev agent.Event) { EmitEvent(ctx, ev) })
	bridge.stop(ctx, prompt, result.Text)
	tracker.finish(err != nil || ctx.Err() != nil)
	recordAttempt(sess, ctx, err)
	maybeAutoCompact(res, sess, evidence, reg, compactor)
	_ = sess.Append("turn", map[string]any{
		"turns":   result.Turns,
		"stopped": result.Stopped,
		"usage":   result.Usage,
	})
	return result, err
}

// recoveryMessagesForClassification 为 ClassifyRecovery 准备 Session.Messages。
// Session.Messages 只投影对话记录，但真实记录会在它们之间夹杂元数据记录（usage/stats/turn）。
// 因此一条消息的 Parent 常常指向投影中不存在元数据记录，而朴素的链接检查
// 会把每个恢复的会话都标记为降级。只把这些悬空的 Parent 改写为前一条消息；
// 两条消息之间真实的链接保持不变。输入会被复制，绝不会被修改。
func recoveryMessagesForClassification(messages []session.Entry) []session.Entry {
	out := make([]session.Entry, len(messages))
	copy(out, messages)
	ids := make(map[string]struct{}, len(out))
	for _, m := range out {
		if m.ID != "" {
			ids[m.ID] = struct{}{}
		}
	}
	prevID := ""
	for i := range out {
		if i > 0 && out[i].Parent != "" && out[i].Parent != prevID {
			if _, ok := ids[out[i].Parent]; !ok {
				out[i].Parent = prevID
			}
		}
		if out[i].ID != "" {
			prevID = out[i].ID
		}
	}
	return out
}

// announceRecovery 通过 Execute 已用于用户可见文本的两个通道发布恢复通知
// （给调用方的 onText，给 --serve/--rpc 订阅者的 EventNotice）。
// 对于降级的历史，它刻意拒绝继续：Execute 没有确认通道，因此它不会凭空造一个 UI 提示，
// 而是向 stderr 写一条明确的警告并返回错误，让记录保持原样供操作人员检查。
//
// 干净或仅被中断的记录会立即返回 nil，使恢复的运行与接线之前逐字节一致。
func announceRecovery(ctx context.Context, stdio cli.IO, onText func(string), decision recoveryDecision) error {
	if decision.Notice == "" {
		return nil
	}
	EmitEvent(ctx, agent.Event{Kind: agent.EventNotice, Text: decision.Notice})
	if onText != nil {
		onText(decision.Notice)
	}
	if !decision.NeedsConfirmation {
		return nil
	}

	warning := "Session recovery paused: automatic continuation is disabled for degraded history; please confirm before resuming. " + decision.Notice
	if stdio.Err != nil {
		fmt.Fprintln(stdio.Err, warning)
	}
	EmitEvent(ctx, agent.Event{Kind: agent.EventNotice, Text: warning})
	if onText != nil {
		onText(warning)
	}
	return fmt.Errorf("session recovery paused: degraded history requires operator confirmation before continuing")
}

// schemaCorrectionUserTurn 是当 --output-schema 验证失败时，追加到现有对话的唯一一条用户轮次。
// schema 和确切的失败原因会放进系统消息（correctiveInstruction）；这个轮次只告诉模型
// 期望它给出一个更正后的答案。
const schemaCorrectionUserTurn = "Your previous reply did not satisfy the required output schema. Reply once more with a single JSON document that conforms exactly to the schema and nothing else."

// correctSchemaAnswer 在现有对话上请模型给出一个更正后的答案。
// 它刻意绕过 agent 循环：chatter 被直接调用且不传工具规格，
// 因此重试无法调用工具、发出工具事件或重新运行原始提示。
// 由调用方负责把返回的文本作为唯一的替换答案发布。
func correctSchemaAnswer(ctx context.Context, chatter llm.Chatter, model, system string, messages []llm.Message, schema string, invalid error) (string, error) {
	if chatter == nil {
		return "", fmt.Errorf("no chatter configured: %w", invalid)
	}
	instruction := correctiveInstruction(schema, invalid)
	request := make([]llm.Message, 0, len(messages)+2)
	systemFound := false
	for _, message := range messages {
		if message.Role == "system" && !systemFound {
			message.Content = system + instruction
			systemFound = true
		}
		request = append(request, message)
	}
	if !systemFound {
		request = append([]llm.Message{{Role: "system", Content: system + instruction}}, request...)
	}
	request = append(request, llm.Message{Role: "user", Content: schemaCorrectionUserTurn})

	var streamed strings.Builder
	reply, err := chatter.Chat(ctx, model, request, nil, func(chunk string) {
		streamed.WriteString(chunk)
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Content) != "" {
		return reply.Content, nil
	}
	return streamed.String(), nil
}

// dropMutatingSpecs 在只读沙箱下用统一的 mutating 判定再过滤一次待公布的 spec。
// 这是目录过滤之外的纵深防御：即使注册表未识别某个带命名空间的 MCP 工具名
// （如 mcp__server__write_file），它也不会被公布给模型。其他沙箱模式原样返回。
func dropMutatingSpecs(mode security.SandboxMode, specs []llm.ToolSpec) []llm.ToolSpec {
	if mode != security.SandboxReadOnly {
		return specs
	}
	kept := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if security.MutatingTool(spec.Function.Name) {
			continue
		}
		kept = append(kept, spec)
	}
	return kept
}

// filterVisibleSpecs 收窄向模型公布的工具目录：先按沙箱剔除修改类工具，
// 再按策略与注册表可见性过滤，最后按隐私姿态移除被阻止的 Web 工具。
// 这是 Execute 中 FilterSpecs 闭包的核心逻辑，拆成函数以便在末尾统一叠加
// opt-in 的精简目录过滤（LazyFilterSpecs）。
func filterVisibleSpecs(specs []llm.ToolSpec, res Resolved, toolset *toolset, policy *security.Policy) []llm.ToolSpec {
	specs = dropMutatingSpecs(res.Sandbox, specs)
	if len(toolset.base.regs) == 0 {
		return specs
	}
	filtered := toolset.base.regs[0].FilterSpecs(policy, res.Sandbox, specs)
	blocked, perr := ApplyPrivacyPosture(res.Cfg.PrivacyMode, res.BaseURL, []string{"web_search", "web_fetch"})
	if perr != nil {
		return nil
	}
	if len(blocked) == 0 {
		return filtered
	}
	drop := make(map[string]bool, len(blocked))
	for _, name := range blocked {
		drop[name] = true
	}
	kept := filtered[:0]
	for _, spec := range filtered {
		if !drop[spec.Function.Name] {
			kept = append(kept, spec)
		}
	}
	return kept
}

func firstLine(text string, limit int) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

// autoApprover 批准除变更型 shell 命令之外的所有请求；它支撑 --yolo 和 approval=auto。
// 破坏性的 shell 命令是 auto 模式绝不能静默批准的唯一调用（对应 aider 的
// explicit_yes_required）：没有人工参与时，模型可能在不被察觉的情况下删除或覆盖文件。
// 这类调用会被委派给 fallback 做真实决定；没有 fallback 时会被拒绝（fail closed）。
// 只读工具和只读 shell 命令保留单次检查即批准，使 --yolo 在非命令路径上保持快捷方便。
// TUI/RPC 通过 Resolved.Approve 注入自己的审批器，从不使用这个类型，
// 因此交互式审批不变。
type autoApprover struct {
	fallback security.Approver
}

// InteractiveReply 声明 autoApprover 是机器自动批准，没有真人交互（#23）。
// 对敏感调用（配置/说明文件编辑、显式 Sensitive 元数据）链会据此拒绝它的
// 自动批准，而非静默放行。
func (autoApprover) InteractiveReply() bool { return false }

func (a autoApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// shellCommandOf 已经把它限制在 bash/powershell，并对其他一切（包括畸形负载）
	// 返回 ""（这是安全的假阴性），因此非 shell 工具无需付出命令分类或提示的开销。
	command := shellCommandOf(name, json.RawMessage(args))
	if command == "" || security.FindFileEditingCommand(command) == "" {
		return true, nil
	}
	if a.fallback == nil {
		// auto 模式无权自行批准破坏性命令。(false, nil) 会变成模型在 Chain.Check 中
		// 可以读到的拒绝。
		return false, nil
	}
	return a.fallback.Ask(ctx, name, args)
}

func approverFor(stdio cli.IO, res Resolved) security.Approver {
	switch res.Approval {
	case "never":
		return security.NeverApprover{}
	case "auto", "yolo":
		// auto 不提示即批准非 shell 工作，但变更型 shell 命令仍需人工：
		// PromptApprover 只接受 y/yes，且刻意没有"全部批准"的捷径。
		// 在无界面的 --rpc/--serve 运行中，调用方改为注入 Resolved.Approve，
		// 因此这个读取器只会在 stdio.In 是操作人员 tty 的直接 CLI 路径上被触达。
		return autoApprover{fallback: security.NewPromptApprover(stdio.In, stdio.Err)}
	default:
		inner := security.NewPromptApprover(stdio.In, stdio.Err)
		if ttl, on := ttlApprovalGate(stdio.Env); on {
			// TTL 批准会替换 CachedApprover，而不是叠加在其之上：
			// 叠加会让 TTL 批准过期后，被会话生命周期缓存无限期地重新批准。
			return &ttlApprover{
				cache: security.NewTTLCache(filepath.Join(res.Storage, "ttl-approvals"), ttl),
				inner: inner,
			}
		}
		return security.NewCachedApprover(inner)
	}
}

// inspectionEnabled 报告可选的安全失败检查层是否开启。默认关闭：只有 ARGENT_INSPECTION=1
// 才会启用；其他任何值都保留历史的 策略 -> 审批 -> 审计 行为不变。
func inspectionEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_INSPECTION")) == "1"
}

// toolArgFeedbackOptInEnabled 报告畸形参数纠错回灌是否开启。默认关闭：
// 只有 ARGENT_TOOL_ARG_FEEDBACK=1 才启用，其他值保持历史派发行为逐字节不变。
func toolArgFeedbackOptInEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_TOOL_ARG_FEEDBACK")) == "1"
}

// multimodalTrimOptInEnabled 报告带图 token 估算是否开启。默认关闭：
// 只有 ARGENT_MULTIMODAL_TRIM=1 才启用，其他值保持历史估算逐字节不变。
func multimodalTrimOptInEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_MULTIMODAL_TRIM")) == "1"
}

// compactionHookEnabled 报告压缩事件钩子是否开启。默认关闭：
// 只有 ARGENT_COMPACTION_HOOK=1 才启用，其他值压缩路径逐字节不变。
func compactionHookEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_COMPACTION_HOOK")) == "1"
}

// destructiveCommandInspector 是选择性启用层的内置检查器。它复用 security 的
// 感知 token 的命令分类器，对所有非变更型 shell 命令返回 Allow，
// 使普通只读调用不会因额外的判定而被延迟。检测到时映射到 RequireApproval（绝不是 Deny）：
// 最终决定仍由操作人员做出。
type destructiveCommandInspector struct{}

func (destructiveCommandInspector) Name() string { return "destructive-command" }

func (destructiveCommandInspector) Inspect(ctx context.Context, tool string, args json.RawMessage) (security.Verdict, error) {
	if err := ctx.Err(); err != nil {
		return security.Verdict{}, err
	}
	token := security.FindFileEditingCommand(shellCommandOf(tool, args))
	if token == "" {
		return security.Verdict{
			Action:     security.InspectAllow,
			Reason:     "no destructive shell command detected",
			Confidence: 1,
			Inspector:  "destructive-command",
		}, nil
	}
	return security.Verdict{
		Action:     security.InspectRequireApproval,
		Reason:     "destructive shell command: " + token,
		Confidence: 1,
		Inspector:  "destructive-command",
	}, nil
}

// shellCommandOf 从 shell 工具的 JSON 参数中提取命令文本。它对应 security 中未导出的
// guardShellCommand，使检查器无需修改 security 包即可复用 FindFileEditingCommand。
func shellCommandOf(tool string, args json.RawMessage) string {
	name := strings.ToLower(strings.TrimSpace(tool))
	if name != "bash" && name != "powershell" {
		return ""
	}
	var payload struct {
		Command string `json:"command"`
		Script  string `json:"script"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return ""
	}
	if strings.TrimSpace(payload.Command) == "" {
		return payload.Script
	}
	return payload.Command
}

// inspectionGuard 把一次安全失败的检查判定折叠进注册表已经调用的单一 tools.Guard。
// Deny 是硬性的 StopError；RequireApproval 在这里询问一次，并通过固定审批器重放到 Chain.Check，
// 使操作人员绝不会被提示两次；Allow（或未知动作）不增加门控，直接落到链上。
type inspectionGuard struct {
	manager  *security.InspectionManager
	chain    *security.Chain
	approver security.Approver
}

func (g *inspectionGuard) Check(ctx context.Context, tool string, args json.RawMessage) error {
	verdict := g.manager.Resolve(ctx, tool, args)
	switch verdict.Action {
	case security.InspectDeny:
		return &security.StopError{Tool: tool, Reason: verdict.Reason}
	case security.InspectRequireApproval:
		// 发现项的 reason/finding ID 只通过可选的原因感知审批器接缝传递；
		// 工具名和参数（缓存/权限/审计的输入）从不被修改，
		// 因此审批不会偏离实际被授权的那次调用。见 askInspectionApproval。
		ok, err := askInspectionApproval(ctx, g.approver, tool, args, verdict)
		if err != nil {
			return err
		}
		if !ok {
			return &security.StopError{Tool: tool, Reason: verdict.Reason, Refused: true}
		}
		// 通过按调用复制的链重放已记录的答案；共享链为不需要检查门控的调用
		// 保留其真实的审批器。
		callChain := *g.chain
		callChain.Approver = fixedApprover{granted: true}
		return callChain.Check(ctx, tool, args)
	default:
		return g.chain.Check(ctx, tool, args)
	}
}

// fixedApprover 重放一个已记录的答案。它只用于 RequireApproval 判定之后的那一次
// Chain.Check，因此不会在并发工具调用之间共享状态。
type fixedApprover struct{ granted bool }

func (a fixedApprover) Ask(ctx context.Context, _, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return a.granted, nil
}

// newInspectionGuard 用内置检查器构建选择性启用层。InspectionManager 的零值即可使用
// （该构建块不暴露构造函数），其错误处理器是安全失败的：损坏的检查器会被跳过，
// 只记录它的名字，绝不记录原始错误文本。
func newInspectionGuard(chain *security.Chain, approver security.Approver, stdio cli.IO, secondaryModel string, review guardianReviewFunc) *inspectionGuard {
	manager := &security.InspectionManager{}
	manager.Add(destructiveCommandInspector{})
	// Guardian 只在此处注册：它复用 stdio.Env 读取环境门，secondaryModel 与 review
	// 由调用方注入（生产走 defaultGuardianReview 的 fail-closed 默认实现）。
	// 任一接线条件不满足时 guardianInspectorFor 返回 nil，链保持不变。
	if ins := guardianInspectorFor(stdio.Env, secondaryModel, review); ins != nil {
		manager.Add(ins)
	}
	if stdio.Err != nil {
		manager.SetErrorHandler(func(name string, _ error) {
			fmt.Fprintf(stdio.Err, "inspection: inspector %q failed and was skipped\n", name)
		})
	}
	return &inspectionGuard{manager: manager, chain: chain, approver: approver}
}

// ttlApprovalGate 解析 ARGENT_APPROVAL_TTL 的启用设置。"0" 和空值使门控关闭。
// Go 时长格式（"15m"）设置精确的生命周期；裸的正整数按分钟解读；
// 任何其他真值都会以缓存的 30 分钟默认值启用门控。
func ttlApprovalGate(env func(string) string) (time.Duration, bool) {
	if env == nil {
		return 0, false
	}
	raw := strings.TrimSpace(env("ARGENT_APPROVAL_TTL"))
	if raw == "" || raw == "0" {
		return 0, false
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d, true
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Duration(n) * time.Minute, true
	}
	return 0, true
}

// ttlApprover 批准由 security.TTLCache 支撑的授权。当 TTL 门控开启时，它取代 CachedApprover：
// 批准会过期（并可通过磁盘缓存在重启后保留），而拒绝从不被缓存——
// 一次拒绝只会清除该确切 (tool, args) 键上的任何既有批准。
type ttlApprover struct {
	cache *security.TTLCache
	inner security.Approver
}

func (a *ttlApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	key := a.cache.Key(name, json.RawMessage(args))
	if a.cache.IsAllowed(key) {
		return true, nil
	}
	ok, err := a.inner.Ask(ctx, name, args)
	if err != nil || !ok {
		if err == nil {
			a.cache.Deny(key)
		}
		return ok, err
	}
	_ = a.cache.Allow(key, name+" "+args)
	return true, nil
}
