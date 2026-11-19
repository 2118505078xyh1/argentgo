package cli

// Output modes.
const (
	ModeText = "text"
	ModeJSON = "json"
)

// Approval modes.
const (
	ApprovalAsk   = "ask"
	ApprovalNever = "never"
	ApprovalAuto  = "auto"
)

// Sandbox modes, matching the Rust CLI.
const (
	SandboxReadOnly         = "read-only"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxDangerFullAccess = "danger-full-access"
)

// Protocol names accepted by --protocol.
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
	ProtocolResponses = "responses"
)

// Options is the fully resolved command line handed to the runner factory.
type Options struct {
	Model         string
	Provider      string
	Profile       string
	BaseURL       string
	APIKey        string
	Protocol      string
	Yolo          bool
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

	// Output and listing switches.
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

	// Budgets and prices.
	MaxTokens      int
	MaxCost        float64
	MaxSeconds     int
	PriceInput     float64
	PriceOutput    float64
	FallbackModel  string
	SecondaryModel string
	AddDirs        []string
	OutputSchema   string

	// Flags parsed for compatibility. The subsystems behind Background,
	// Worktree, EnableBrowser, EnableDesktop, EnableService, GUI and the
	// workflow trio are not migrated yet; runMain refuses them explicitly.
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

	// Transports.
	Serve      bool
	RPC        bool
	ACP        bool
	ServeAddr  string
	ServeToken string

	Help    bool
	Version bool
}
