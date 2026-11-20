package cli

// 输出模式。
const (
	ModeText = "text"
	ModeJSON = "json"
)

// 审批模式。
const (
	ApprovalAsk   = "ask"
	ApprovalNever = "never"
	ApprovalAuto  = "auto"
)

// 沙箱模式，与 Rust CLI 保持一致。
const (
	SandboxReadOnly         = "read-only"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxDangerFullAccess = "danger-full-access"
)

// --protocol 接受的协议名称。
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
	ProtocolResponses = "responses"
)

// Options 是交给 runner 工厂的、完全解析后的命令行。
type Options struct {
	Model    string
	Provider string
	Profile  string
	BaseURL  string
	APIKey   string
	Protocol string
	Yolo     bool
	// Yes 表示在非交互环境下自动确认提示；默认 false 时不改变任何现有行为。
	Yes           bool
	MaxTurns      int
	MaxParallel   int
	Mode          string
	Cwd           string
	SandboxMode   string
	Approval      string
	Session       string
	Resume        bool
	Undo          bool
	Retry         bool
	Title         string
	Locale        string
	NoPromptCache bool

	// 输出与列举开关。
	JSONLines     bool
	ListProfiles  bool
	ListSessions  bool
	ListModels    bool
	NoColor       bool
	Tokens        bool
	TrustProject  bool
	Thinking      string
	NoAutoCompact bool
	Theme         string
	Plugins       []string

	// 预算与价格。
	MaxTokens      int
	MaxCost        float64
	MaxSeconds     int
	PriceInput     float64
	PriceOutput    float64
	FallbackModel  string
	SecondaryModel string
	AddDirs        []string
	OutputSchema   string

	// 为兼容性而解析的 flag。Background、Worktree、EnableBrowser、
	// EnableDesktop、EnableService、GUI 以及 workflow 三件套背后的子系统
	// 尚未迁移；runMain 会显式拒绝它们。
	Background      bool
	Worktree        string
	EnableBrowser   bool
	EnableDesktop   bool
	EnableService   bool
	WorkflowMode    string
	WorkflowResume  bool
	WorkflowJournal string
	GUI             bool
	TUI             bool

	Overrides Overrides

	// 传输方式。
	Serve      bool
	RPC        bool
	ACP        bool
	ServeAddr  string
	ServeToken string

	Help    bool
	Version bool
}
