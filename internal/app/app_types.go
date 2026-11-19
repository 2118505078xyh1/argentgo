package app

import (
	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Resolved is everything one run needs after merging flags, config and
// provider templates.
type Resolved struct {
	Options  cli.Options
	Cfg      config.Config
	Root     string
	Home     string
	Storage  string
	Model    string
	Provider string
	Profile  string
	BaseURL  string
	APIKey   string
	Protocol string
	Locale   string
	Sandbox  security.SandboxMode
	Approval string
	MaxTurns int
	Mutating bool
	System   string

	// MaxParallel is the worker count for read-only tool batches. Values <= 1
	// keep batches sequential; zero means "unset" and resolves to
	// defaultMaxParallel in Resolve.
	MaxParallel int

	// Budget and pricing.
	MaxTokens  int
	MaxCostUSD float64
	MaxSeconds int
	Prices     map[string]agent.Price

	// Extra workspace directories, an output schema request and the reasoning
	// effort hint.
	AddDirs      []string
	WriteRoots   []string
	OutputSchema string
	Thinking     string
	TrustProject bool
	Plugins      []string

	// Post-run compaction. Zero selects the default threshold; a negative
	// value disables the estimate-based trigger.
	AutoCompactTokens int
	NoAutoCompact     bool

	// Approve overrides the interactive approver; the TUI installs a modal
	// approver here.
	Approve security.Approver

	// HookDispatcher overrides the shell dispatcher used by the hooks bridge;
	// tests inject a fake so no hook process is spawned.
	HookDispatcher HookDispatcher

	Chatter llm.Chatter
}

// providerChoice is the resolved provider endpoint without the template type.
type providerChoice struct {
	Name         string
	Protocol     string
	BaseURL      string
	DefaultModel string
	APIKeyEnv    string
}
