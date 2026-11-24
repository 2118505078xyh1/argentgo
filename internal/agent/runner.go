// Package agent 运行模型/工具循环。
//
// 本包刻意不依赖 session、config、tools 和 security 等包：它从程序其他部分所需的一切，
// 都在这里以小型接口（ToolRunner、Approver、Recorder）声明，并由调用方提供。
// 这样循环可以用伪实现进行测试，也让集成者能在不产生依赖环的情况下接入真实的注册表。
package agent

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/prof"
)

const (
	systemTemplate = `You are argentgo, a concise coding assistant working on the user's machine.
Working directory: %s
The working directory is already the workspace root: use relative paths and never run cd (it fails on Windows paths). Read before writing, batch independent calls, and stop once the request is satisfied.`

	// DefaultMaxTurns 是调用方未设置时的轮次预算。
	DefaultMaxTurns = 100

	// defaultContextTokens 是调用方未设置 MaxContextTokens 时，裁剪所瞄准的估算提示大小。
	defaultContextTokens = 32000

	// defaultMaxRepeats 是在重复防护改为以错误回应之前，允许真正运行的相同（工具, 参数）调用次数。
	defaultMaxRepeats = 2

	// StopOutputLimit 表示模型连续两次因输出长度而结束回复，且最近一次没有请求工具，
	// 因此循环停止要求它继续。其他 Stop* 原因位于 budget.go；这里声明这个是因为
	// 它属于循环限制而非预算限制。
	StopOutputLimit = "output_limit"

	// StopVerifyReflections 表示编辑后验证持续失败，直到反思预算耗尽。
	// 与 StopOutputLimit 一样，它是循环限制而非预算限制，因此在这里声明。
	StopVerifyReflections = "verify_reflections"

	// DefaultMaxVerifyReflections 是当 Options.MaxVerifyReflections 为零时，
	// 一次运行中编辑后验证反馈轮次的硬性上限。它对应 aider 的 max_reflections = 3。
	DefaultMaxVerifyReflections = 3

	// compactionRecoveryBand 是 omp 风格的迟滞带：轮次中压缩以触发阈值的这个比例为目标。
	// 落在触发线以下但仍在带内，就足以释放压缩压力，
	// 因此运行绝不会为了省几个 token 而在每个轮次都重新压缩。
	compactionRecoveryBand = 0.8
	// summaryReserveTokens 是当压缩窗口由触发阈值推导时，为生成的摘要预留的余量。
	summaryReserveTokens = 2000
	// minCompactWindow 是推导出的压缩窗口的下限。
	minCompactWindow = 1000
)

// defaultMutatingTools 是当调用方既不提供 Mutating 也不提供 MutatingTools 时，
// 使用的保守审批集合。未知工具一律视为只读：会做修改的子 agent 或插件工具应被显式列出。
var defaultMutatingTools = []string{
	"write", "edit", "multi_edit", "apply_patch", "patch",
	"bash", "shell", "exec", "run",
	"delete", "remove", "move", "copy", "mkdir",
}

// ToolRunner 是循环所驱动的工具注册表子集。
type ToolRunner interface {
	// Specs 是为下一次请求向模型公布的工具列表。
	// 每个轮次都会重新读取它，使运行中被解锁的工具能立即出现。
	Specs() []llm.ToolSpec
	// Run 执行一次工具调用并返回其输出文本。
	Run(ctx context.Context, name, args string) (string, error)
}

// ToolMeta 是工具可以附加到一次结果上的进程内元数据。它刻意不属于线上协议，
// 在恢复时会丢失；它只在运行期间驱动廉价的上下文回收。
type ToolMeta struct {
	// SupersedeKey 标识一个可复用的事实（例如 "read|/path/to/file"）。当后续结果携带相同键时，
	// 较旧的结果就被取代。
	SupersedeKey string
	// Useless 标记某个结果没有携带新信息（例如空搜索），
	// 这样一旦后续助手轮次消费了它，就可以将其省略。
	Useless bool
}

// MetaToolRunner 是 ToolRunner 的可选扩展。实现它的运行器能让循环在不调用模型的情况下，
// 回收被取代或无用的结果。未实现它的运行器保持原样工作。
type MetaToolRunner interface {
	RunWithMeta(ctx context.Context, name, args string) (string, ToolMeta, error)
}

// callTool 优先使用感知元数据的路径，并透明地回退到原始 ToolRunner。
// 它放在这里是为了让 ToolRunner 本身保持不变。
//
// 本函数是每个工具调用的 panic 围栏：注入的非 Registry ToolRunner（如 eval/fake）
// 若发生 panic，会被 safeToolPanic 规范成普通的工具错误结果，循环继续运行，
// 而不会击穿 Run。tools.Registry 自身已有内部 recover，这里是独立的第二道保险。
// 正常路径不触碰任何返回值，事件与会话记录逐字节不变；只有真正发生 panic 时
// 才会产生一条 is_error 结果。
func (s *runState) callTool(ctx context.Context, name, args string) (out string, meta ToolMeta, err error) {
	// 所有工具执行的统一汇聚点：一个埋点覆盖全部工具（含 MCP/插件/内建），
	// 使 profiler 能回答"这一轮时间花在 LLM 还是工具"。
	defer prof.Region(prof.RegionToolRun).End()
	defer func() {
		if p := recover(); p != nil {
			out = ""
			meta = ToolMeta{}
			err = safeToolPanic(p)
		}
	}()
	if metaRunner, ok := s.tools.(MetaToolRunner); ok {
		return metaRunner.RunWithMeta(ctx, name, args)
	}
	out, err = s.tools.Run(ctx, name, args)
	return out, ToolMeta{}, err
}

// CompactWindow 按 agent 的迟滞带与摘要预留量计算压缩目标窗口。
// app 层后台预压缩（TODO #105）复用它，避免在两个包里重复窗口公式。
func CompactWindow(threshold int) int {
	window := int(float64(threshold)*compactionRecoveryBand) - summaryReserveTokens
	if window < minCompactWindow {
		window = minCompactWindow
	}
	return window
}

// Compactor 是可选的轮次中压缩钩子。app 层注入一个拥有会话的实现；
// agent 包保持不依赖任何 session。window 是保留的后缀应当适配的 token 预算。
type Compactor interface {
	MaybeCompact(messages []llm.Message, window int) ([]llm.Message, bool)
}

// Approver 对变更型工具做门控。nil 的 Approver 表示允许所有调用。
type Approver interface {
	Ask(ctx context.Context, name, args string) (bool, error)
}

// Recorder 持久化循环想要保留的一条记录项。
type Recorder interface {
	Append(kind string, payload any) error
}

// RecorderFunc 把普通函数适配为 Recorder。
type RecorderFunc func(kind string, payload any) error

// Append 实现 Recorder。
func (f RecorderFunc) Append(kind string, payload any) error {
	if f == nil {
		return nil
	}
	return f(kind, payload)
}

// Options 配置一个 Runner。
type Options struct {
	// 当 FallbackModels 为空时，Model 是主模型。为兼容 API，该旧字段按原样生效；
	// 一旦设置了 FallbackModels 则以其为准，其第一项为主模型。
	Model string
	// FallbackModels 是有序的模型列表：第 0 项为主模型，当当前模型以可重试的方式失败时
	// （HTTP 429/5xx、限流或网络错误），其余模型会被依次尝试。
	// 为空表示不回退，保留 wave 4 之前的单模型行为。
	FallbackModels []string

	MaxTurns    int // 0 表示回退到 Budget.MaxTurns，再到 DefaultMaxTurns
	MaxParallel int // <=1（默认）表示串行运行工具批次
	System      string
	Root        string
	// SystemNodes 是要追加到系统提示尾部的节点。非空时经 llm.StableSystemPrompt
	// 以 base 为前缀追加，保住 prompt cache 前缀；为空时系统提示逐字节不变。
	SystemNodes []string

	// InitialImages 是随首条用户消息一起发送的图片输入（纯数据，不含读取逻辑）。
	// 非空时 newRunState 用 llm.BuildImageMessage 把首条用户消息构造成带图 part 的消息；
	// 为零值 nil 时首条用户消息与接线前逐字节相同（纯文本 Content）。
	InitialImages []llm.ImageInput

	Chatter llm.Chatter
	Tools   ToolRunner
	Approve Approver

	// FilterSpecs 若设置，会在每个轮次收窄向模型公布的工具目录。
	// app 层根据策略和沙箱模式提供它（例如 tools.Registry.FilterSpecs）。
	// 它只改变模型看到的内容：每次调用仍会经过 Tools.Run，其防护器是最终防线。
	FilterSpecs func(specs []llm.ToolSpec) []llm.ToolSpec

	// NoFinalAnswer 禁用轮次预算耗尽时发起的那一次无工具请求。
	// 需要断言精确模型调用次数的测试框架可以选择退出；正常运行保留这个优雅的收尾答案。
	NoFinalAnswer bool

	// Record 接受 Recorder、func(kind string, payload any) error 或
	// func(kind string, payload any)。宽松的类型使 Recorder 出现之前编写的调用方仍能编译；
	// 新代码应传入 Recorder。
	Record any

	// BeforeRequest 和 BeforeTool 是按 Runner 注入的可选持久性接缝。
	// BeforeRequest 在每次模型请求之前立即运行；BeforeTool 在工具批次派发之前立即运行。
	// 非 nil 错误会中止相应动作：请求钩子错误以提供方错误结束运行，
	// 工具钩子错误会用错误结果填满每个调用而不派发任何一个。nil 钩子会被跳过。
	//
	// 它们是实例字段（而非进程全局注册表），因此并发运行不会清除彼此的钩子。
	// app 层在这里注入会话的 Sync 屏障，使本包保持不依赖任何存储。
	BeforeRequest func() error
	BeforeTool    func() error

	// Emit 接收运行中的每一个可观察步骤。Emit 为 nil 是可以的。
	Emit func(Event)

	// Checkpoint 在包含变更型工具调用的批次之前被调用一次，同步执行，
	// 且在该批次中任何工具运行之前。会话层在这里记录轮次前的字节和摘要。
	// nil 钩子不改变循环行为；EventCheckpointReady 仍会被发出。
	Checkpoint func(Checkpoint)

	// ProgressSink 在流式输出累积到阈值时接收轮次内进度检查点，由 app 持久化为
	// progress 记录。nil 时整段是空操作，逐字节不变。它不进入模型可见历史。
	ProgressSink ProgressSink
	// AfterBatch 在每个工具批次派发并返回后调用一次，turn 是该批次的轮次号。
	// 它由 agent 在 runTools 出口用 defer 触发，因此 id 非法、取消、熔断等提前
	// 返回路径也会触发；捕获时没有检查点的批次会因 tracker 查表落空而成为空操作。
	// agent 本身不读取它，nil 时循环行为与历史上逐字节一致。注意 childOptions
	// 刻意不复制该字段，子 agent 批次不会触发根运行注入的钩子。
	AfterBatch func(turn int)
	// MutationPaths 提供变更型调用可能改动的工作区相对路径。它会覆盖 Tools 上可选的
	// MutationReporter。
	MutationPaths func(name, args string) []string

	// Verify 若非 nil，则启用有界的编辑后反思循环。它在每个包含变更型工具的工具批次之后
	// 运行一次，并附带该批次可能改动的工作区相对路径（可用时来自批次检查点）。
	// label 是检查的名称（"go test"、"lint" 等）；passed 是其结论；output 是原始检查输出；
	// 当检查根本无法运行（缺少工具、命令被取消）时 ok 为 false，此时不会注入任何反馈。
	// 循环会限制并脱敏它回灌的反馈，但回调自身仍必须保持其工作有界。
	// nil 的 Verify 会禁用整个功能，使现有运行保持逐字节不变。
	Verify func(ctx context.Context, changedPaths []string) (label string, passed bool, output string, ok bool)
	// MaxVerifyReflections 限制一次运行可注入的 FAIL 反馈轮次数量。
	// 设置 Verify 时，零值选用 DefaultMaxVerifyReflections（3）。PASS 从不消耗一次反思；
	// 该计数按运行计算，且从不重置。
	MaxVerifyReflections int

	// OnCompactionEvidence 接收压缩器必须保留的失败记录和后续步骤行。它在循环结束时运行一次。
	OnCompactionEvidence func(CompactionEvidence)

	// Compactor 若设置，会在估算上下文超过 CompactThreshold 时于工具批次边界被调用。
	// 它由 app 层提供，因此本包从不导入 session。
	Compactor Compactor
	// CompactThreshold 是轮次中压缩的估算 token 触发阈值。零值（或 nil 的 Compactor）会禁用它。
	CompactThreshold int

	// EnableCompactRetry 启用选择性的"上下文溢出后压缩并重放"恢复路径。
	// 默认 false：未设置的运行与引入该路径之前逐字节一致。
	// 开启后仍需要 Compactor 且重试预算未用尽才会真正重放；每次运行最多重放一次，
	// 避免同一份超长请求在同一轮次里反复空转。
	EnableCompactRetry bool

	// EnableStreamRecovery 启用选择性的"流中断后非流式回补"恢复路径。
	// 默认 false：未设置的运行与引入该路径之前逐字节一致。
	// 开启后仍须同时满足提供方流错误、已捕获部分文本、context 未取消，且 Chatter
	// 实现了 streamOnceChatter，才会用同一 model/messages/tools 再发一次非流式补全；
	// 每轮最多一次，补不到就沿用原有失败路径。
	EnableStreamRecovery bool

	// TTSRRules 是流式规则注入（TTSR）已编译的规则集。nil/空时 TTSR 完全关闭，
	// 流式循环走原有 s.chat 路径逐字节不变。非空时每次模型请求把正文增量喂给
	// 规则观察器：命中 Retry 动作会中止当前流、注入一条提醒用户消息并从同点重发
	// 一次（每轮最多一次）；Warn/Inject 只发可观察事件，不改变流。
	TTSRRules []CompiledTTSRRule

	// Steer 在轮次之间被抽取；每个值都会成为一条用户消息。
	Steer <-chan string
	// FollowUp 是"本应停止后再继续"的消息通道：模型无工具调用、即将结束时，
	// 排队的用户消息会开启一个新轮次（而不是像 Steer 那样插在当前轮的边界）。
	// nil 时完全关闭，零分配、原样结束（项目 TODO #122）。
	FollowUp <-chan string

	// ReplayBody 是 resume 时首请求要原样发送的线协议字节（来自请求快照）。
	// 仅当本次运行被判定为"中断待重发"且协议/模型/配置校验全过时由 app 层注入。
	// 三个 Replay* 字段全为零值时字节回放完全关闭，请求走正常 Chat 路径。
	ReplayBody     []byte
	ReplayModel    string
	ReplayProtocol string

	// CompactObserver 是可选的压力观察钩子（app 层接线，agent 保持无 session 依赖）。
	// 它在每次工具批次后、压缩判定之前被调用，携带当前消息与压缩阈值；app 可用它
	// 在接近阈值时提前在后台准备摘要（ARGENT_BACKGROUND_COMPACT）。nil 时零调用。
	CompactObserver func(messages []llm.Message, threshold int)

	// ContextCalibration 启用上下文计量校准（app 层 ARGENT_CONTEXT_CALIBRATION）：
	// 压缩重试窗口按 ShrinkWindow 逐级缩小。默认 false 时重试窗口逐字节不变。
	ContextCalibration bool
	// Budget 限制本次运行。限制会在每个轮次之前、工具批次之前和之后检查。
	Budget Budget
	// Prices 把模型 id 映射到其每百万 token 的价格。若存在 "" 条目，
	// 它作为没有对应条目模型的回退价格。
	Prices map[string]Price

	// MaxContextTokens 是裁剪所瞄准的估算提示大小（默认 32000）。
	// Budget.MaxPromptTokens 若更小则优先。
	MaxContextTokens int
	// KeepTurns 是从不被裁剪的末尾助手轮次数量（默认 6）。
	KeepTurns int
	// KeepToolTokens 是从不被裁剪的最近工具输出量（默认 2000 估算 token）。
	KeepToolTokens int
	// NoTrimToolCallIDs 是不允许被裁剪或回收的 ToolCallID 集合，由调用方注入以保护携带
	// output-spill 恢复标记的工具结果。nil（默认）使裁剪行为与未引入该字段时逐字节一致。
	// agent 包不依赖 session/tools，因此保护集合必须从这里注入。
	NoTrimToolCallIDs map[string]bool
	// MaxRepeats 是在重复防护以错误回应之前，允许真正运行的相同调用次数
	// （默认 2；0 表示使用默认值）。
	MaxRepeats int

	// NoProgress 启用选择性的无进展检测。nil（默认值）使检测器完全关闭且运行不变。
	// 设置后，持续发出相同调用并得到相同结果的批次会以 StopStuck 提前结束运行。
	// 这是建议性的循环保护，并非重复防护的替代——后者仍会独立地阻断重复调用。
	NoProgress *NoProgressConfig

	// MutatingTools 列出需要审批的工具。设置 Mutating 时以其为准。两者都没有时使用
	// defaultMutatingTools。
	MutatingTools []string
	Mutating      func(name string) bool

	// Subagent 若设置，会公布内置 `task` 工具并为其运行嵌套循环。
	Subagent *SubagentConfig
	// Depth 是当前子 agent 的嵌套深度（根运行为 0）。
	Depth int

	// bgOwner 是本次运行在 Subagent.Background 中的身份。它由 agent 包设置（绝不由调用方设置），
	// 并通过 childOptions 按值复制，因此后台结果能被精确投递回委派它的那次运行。
	// 仅靠 depth 做不到这一点：两个同级子 agent 共享相同深度，会窃取彼此的结果。
	// 零表示"首次使用时分配"（见 newRunState 和 BackgroundResults.RootOwner）。
	bgOwner uint64

	// SkillAuthor 若设置，会公布内置 `save_skill` 工具，并把其调用路由到注入的 writer。
	// nil（默认值）使现有运行完全不变。
	SkillAuthor SkillWriter
	// SkillLister 若设置，会公布只读的 `list_skills` 工具。
	SkillLister SkillLister

	// Out 接收流式输出的答案文本。已弃用：请配合 Emit 使用 EventTextDelta；
	// 保留它是为了让 wave 2 之前的调用方继续工作。
	Out io.Writer
}

// Result 是一次 Run 的结果。
type Result struct {
	// Text 是最后产生的助手文本。
	Text string
	// Messages 是完整对话，包括工具结果。
	Messages []llm.Message
	// Usage 是本次运行累积的 token 用量。
	Usage llm.Usage
	// Turns 是完成的模型请求数量。
	Turns int
	// Stopped 是运行结束的原因：StopComplete、StopMaxTurns、
	// StopMaxPromptTokens、StopMaxTotalTokens、StopMaxCost、StopCancelled、
	// StopProviderError、StopOutputLimit 或 StopVerifyReflections。
	Stopped string
}

// Runner 驱动循环。
type Runner struct {
	// Client 和 Tools 是 wave 2 之前的构造接缝。设置后，Options.Chatter 和 Options.Tools 优先。
	Client llm.Chatter
	Tools  ToolRunner
	Opts   Options

	// summaryCache 在该 Runner 的所有 Run 之间缓存已完成的压缩视图（服务器通过一个实例驱动多个提示）。
	// 它采用首次使用时延迟创建，因为调用方用带键的结构体字面量构造 Runner；零值无需修改构造函数。
	// 缓存本身可安全并发访问，该互斥锁只保护延迟初始化。按 Runner 划分作用域可避免跨 agent 污染；
	// 持久的跨会话缓存属于 app/session 层。
	summaryCacheMu sync.Mutex
	summaryCache   *SummaryCache
}

// compactionCache 返回该 Runner 的摘要缓存，首次使用时以默认容量创建它。
func (r *Runner) compactionCache() *SummaryCache {
	r.summaryCacheMu.Lock()
	defer r.summaryCacheMu.Unlock()
	if r.summaryCache == nil {
		r.summaryCache = NewSummaryCache(0)
	}
	return r.summaryCache
}

// Run 执行提示并返回完整结果。
func (r *Runner) Run(ctx context.Context, prompt string) (Result, error) {
	return r.execute(ctx, prompt, r.Opts.Emit)
}

// RunText 是 wave 2 之前的形态：只返回最终的助手文本。
func (r *Runner) RunText(ctx context.Context, prompt string) (string, error) {
	res, err := r.Run(ctx, prompt)
	return res.Text, err
}

// Prompt 满足传输层运行器接口（server.Runner）：它运行一个提示，并把答案增量转发给 onText。
func (r *Runner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	base := r.Opts.Emit
	var emit func(Event)
	if base != nil || onText != nil {
		emit = func(e Event) {
			if e.Kind == EventTextDelta && onText != nil {
				onText(e.Text)
			}
			if base != nil {
				base(e)
			}
		}
	}
	res, err := r.execute(ctx, text, emit)
	return res.Text, err
}

// resolved 把旧版结构体字段与 Options 合并。
func (r *Runner) resolved() (Options, error) {
	opts := r.Opts
	if opts.Chatter == nil {
		opts.Chatter = r.Client
	}
	if opts.Tools == nil {
		opts.Tools = r.Tools
	}
	if opts.Chatter == nil {
		return opts, fmt.Errorf("agent: no chatter configured")
	}
	if opts.Tools == nil {
		opts.Tools = nopTools{}
	}
	return opts, nil
}

// maxTurnsFor 确定轮次预算。
func maxTurnsFor(opts Options) int {
	if opts.Budget.MaxTurns > 0 {
		return opts.Budget.MaxTurns
	}
	if opts.MaxTurns > 0 {
		return opts.MaxTurns
	}
	return DefaultMaxTurns
}

// repeatsFor 确定重复防护的额度。
func repeatsFor(opts Options) int {
	if opts.MaxRepeats > 0 {
		return opts.MaxRepeats
	}
	return defaultMaxRepeats
}

// normaliseRecorder 接受 Options.Record 历史上采取过的所有形态。
func normaliseRecorder(value any) Recorder {
	switch r := value.(type) {
	case nil:
		return nil
	case Recorder:
		return r
	case func(kind string, payload any) error:
		return RecorderFunc(r)
	case func(kind string, payload any):
		return RecorderFunc(func(kind string, payload any) error {
			r(kind, payload)
			return nil
		})
	default:
		return nil
	}
}

// nopTools 是调用方未提供工具时使用的工具运行器。
type nopTools struct{}

func (nopTools) Specs() []llm.ToolSpec { return nil }

func (nopTools) Run(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("agent: no tools configured")
}

// RunWithMeta 通过技能包装器转发元数据。内置技能工具不携带回收元数据；
// 其他所有调用在包装的运行器具有元数据路径时会走该路径。
func (t *skillTools) RunWithMeta(ctx context.Context, name, args string) (string, ToolMeta, error) {
	switch name {
	case SaveSkillToolName, ListSkillsToolName:
		out, err := t.Run(ctx, name, args)
		return out, ToolMeta{}, err
	default:
		if meta, ok := t.inner.(MetaToolRunner); ok {
			return meta.RunWithMeta(ctx, name, args)
		}
		out, err := t.inner.Run(ctx, name, args)
		return out, ToolMeta{}, err
	}
}

// RunWithMeta 通过委派包装器转发元数据。内置 task 工具没有回收元数据；
// 其他所有调用都会到达被包装的运行器，使元数据在子 agent 接线后仍然保留。
func (t *taskTools) RunWithMeta(ctx context.Context, name, args string) (string, ToolMeta, error) {
	if name == TaskToolName {
		out, err := t.Run(ctx, name, args)
		return out, ToolMeta{}, err
	}
	if meta, ok := t.inner.(MetaToolRunner); ok {
		return meta.RunWithMeta(ctx, name, args)
	}
	out, err := t.inner.Run(ctx, name, args)
	return out, ToolMeta{}, err
}
