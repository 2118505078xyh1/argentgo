package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
)

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

// Overrides holds repeated `-c key=value` / `--config key=value` settings.
// Dotted keys build nested objects, e.g. `-c tools.include='["read"]'`.
type Overrides struct {
	values map[string]string
}

// Set parses one key=value override. It implements flag.Value.
func (o *Overrides) Set(spec string) error {
	key, value, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("expected key=value, got %q", spec)
	}
	key = strings.TrimSpace(key)
	if err := validateDottedKey(key); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("override %q has an empty value", key)
	}
	if o.values == nil {
		o.values = make(map[string]string)
	}
	o.values[key] = strings.TrimSpace(value)
	return nil
}

// String implements flag.Value.
func (o *Overrides) String() string {
	if o == nil || len(o.values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(o.values))
	for key := range o.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+o.values[key])
	}
	return strings.Join(parts, ",")
}

// Get reports the raw value of one override key.
func (o *Overrides) Get(key string) (string, bool) {
	if o == nil {
		return "", false
	}
	value, ok := o.values[key]
	return value, ok
}

// lookup returns the first override matching any of the given keys.
func (o *Overrides) lookup(keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := o.Get(key); ok {
			return value, true
		}
	}
	return "", false
}

// All returns a copy of the raw override map.
func (o *Overrides) All() map[string]string {
	out := make(map[string]string, len(o.values))
	for key, value := range o.values {
		out[key] = value
	}
	return out
}

// Nested turns dotted overrides into nested maps, parsing values as JSON when
// possible. It is what the config file writer consumes.
func (o *Overrides) Nested() map[string]any {
	out := make(map[string]any, len(o.values))
	for key, value := range o.values {
		setDotted(out, key, parseValue(value))
	}
	return out
}

func validateDottedKey(key string) error {
	if key == "" {
		return errors.New("override has an empty key")
	}
	for _, segment := range strings.Split(key, ".") {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("invalid dotted key %q", key)
		}
	}
	return nil
}

// parseValue decodes a CLI/config value as JSON, falling back to a string.
func parseValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return raw
}

// stringList is a repeatable string flag (--add-dir, --plugins).
type stringList struct {
	values []string
}

func (l *stringList) String() string { return strings.Join(l.values, ",") }

func (l *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("expected a non-empty value")
	}
	l.values = append(l.values, value)
	return nil
}

// optionalString is a flag whose value is optional: `--worktree` alone means
// "auto", `--worktree=name` names the worktree.
type optionalString struct {
	value string
}

func (o *optionalString) String() string { return o.value }

func (o *optionalString) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || value == "true" {
		value = "auto"
	}
	o.value = value
	return nil
}

// IsBoolFlag lets the flag package accept a bare --worktree.
func (o *optionalString) IsBoolFlag() bool { return true }

// flagValues holds one pointer per registered flag. It exists so the flag
// surface has exactly one definition, which the parity test inspects.
type flagValues struct {
	model    *string
	baseURL  *string
	apiKey   *string
	protocol *string
	provider *string
	profile  *string

	yolo        *bool
	maxTurns    *int
	maxParallel *int
	mode        *string
	cwd         *string

	sandboxMode *string
	approval    *string
	session     *string
	resume      *bool
	undo        *bool
	retry       *bool
	title       *string
	locale      *string

	jsonLines     *bool
	listProfiles  *bool
	listSessions  *bool
	listModels    *bool
	noColor       *bool
	tokens        *bool
	trustProject  *bool
	thinking      *string
	noAutoCompact *bool
	noPromptCache *bool
	theme         *string
	plugins       *stringList

	maxTokens      *int
	maxCost        *float64
	maxSeconds     *int
	priceInput     *float64
	priceOutput    *float64
	fallbackModel  *string
	secondaryModel *string
	addDirs        *stringList
	outputSchema   *string

	background      *bool
	worktree        *optionalString
	enableBrowser   *bool
	enableDesktop   *bool
	enableService   *bool
	workflowMode    *string
	workflowResume  *bool
	workflowJournal *string
	gui             *bool
	tui             *bool

	serve *bool
	rpc   *bool
	acp   *bool
	addr  *string
	port  *int
	token *string

	version *bool
	help    *bool

	overrides *Overrides
}

// newFlags registers the whole flag surface on fs.
func newFlags(fs *flag.FlagSet, v *flagValues) {
	v.model = fs.String("m", "", "model id")
	v.baseURL = fs.String("base-url", "", "provider base URL")
	v.apiKey = fs.String("api-key", "", "provider API key")
	v.protocol = fs.String("protocol", "", "protocol: anthropic, openai or responses")
	v.provider = fs.String("provider", "", "provider template id (see `argent models`)")
	v.profile = fs.String("profile", "", "named config profile")
	v.yolo = fs.Bool("yolo", false, "auto-approve every tool call")
	v.maxTurns = fs.Int("max-turns", 12, "maximum model turns")
	v.maxParallel = fs.Int("max-parallel", 0, "read-only tool workers per batch (0 uses the default, 1 disables)")
	v.mode = fs.String("mode", ModeText, "output mode: text or json")
	v.cwd = fs.String("cwd", "", "workspace root (default: current directory)")
	v.sandboxMode = fs.String("sandbox-mode", "", "read-only, workspace-write or danger-full-access")
	v.approval = fs.String("approval", "", "approval mode: ask, never or auto")
	v.session = fs.String("session", "", "resume the session with this id")
	v.resume = fs.Bool("resume", false, "resume the most recent session")
	v.undo = fs.Bool("undo", false, "restore the latest checkpoint of --session and exit")
	v.retry = fs.Bool("retry", false, "fork --session at its latest checkpoint before the prompt")
	v.title = fs.String("title", "", "override the generated session title")
	v.locale = fs.String("locale", "", "output locale")

	v.jsonLines = fs.Bool("json-lines", false, "one-shot strict JSONL on stdout")
	v.listProfiles = fs.Bool("list-profiles", false, "list config profiles and exit")
	v.listSessions = fs.Bool("list-sessions", false, "list stored sessions and exit")
	v.listModels = fs.Bool("list-models", false, "list provider templates and exit")
	v.noColor = fs.Bool("no-color", false, "disable ANSI colours")
	v.tokens = fs.Bool("tokens", false, "print the request payload breakdown and exit")
	v.trustProject = fs.Bool("trust-project", false, "trust project-local resources for this run")
	v.thinking = fs.String("thinking", "", "reasoning effort hint")
	v.noAutoCompact = fs.Bool("no-auto-compact", false, "disable automatic compaction for this run")
	v.noPromptCache = fs.Bool("no-prompt-cache", false, "disable provider prompt-cache markers for this run")
	v.theme = fs.String("theme", "", "UI theme name")
	v.plugins = &stringList{}
	fs.Var(v.plugins, "plugins", "load this plugin by name, repeatable; `all` loads every one")

	v.maxTokens = fs.Int("max-tokens", 0, "stop after this many billable tokens")
	v.maxCost = fs.Float64("max-cost", 0, "stop after this estimated dollar spend")
	v.maxSeconds = fs.Int("max-seconds", 0, "stop after this many wall-clock seconds")
	v.priceInput = fs.Float64("price-input", 0, "USD per million prompt tokens")
	v.priceOutput = fs.Float64("price-output", 0, "USD per million completion tokens")
	v.fallbackModel = fs.String("fallback-model", "", "model to retry once with when the primary fails")
	v.secondaryModel = fs.String("secondary-model", "", "model for delegated subagent tasks")
	v.addDirs = &stringList{}
	fs.Var(v.addDirs, "add-dir", "extra writable workspace directory, repeatable")
	v.outputSchema = fs.String("output-schema", "", "JSON Schema file path, or inline schema text starting with {")

	v.background = fs.Bool("background", false, "run in the background (not migrated)")
	v.worktree = &optionalString{}
	fs.Var(v.worktree, "worktree", "run in a git worktree (not migrated; bare flag names it automatically)")
	v.enableBrowser = fs.Bool("enable-browser", false, "enable the browser tool (not migrated)")
	v.enableDesktop = fs.Bool("enable-desktop", false, "enable desktop control (not migrated)")
	v.enableService = fs.Bool("enable-service", false, "enable the pty service tool (not migrated)")
	v.workflowMode = fs.String("workflow-mode", "", "dynamic workflow mode (not migrated)")
	v.workflowResume = fs.Bool("workflow-resume", false, "resume a workflow journal (not migrated)")
	v.workflowJournal = fs.String("workflow-journal", "", "workflow journal path (not migrated)")
	v.gui = fs.Bool("gui", false, "launch the desktop window (not migrated)")
	v.tui = fs.Bool("tui", false, "run the full-screen terminal UI")

	v.serve = fs.Bool("serve", false, "serve HTTP + SSE")
	v.rpc = fs.Bool("rpc", false, "serve newline-delimited JSON-RPC on stdio")
	v.acp = fs.Bool("acp", false, "serve ACP JSON-RPC on stdio")
	v.addr = fs.String("serve-address", "", "HTTP bind address (default 127.0.0.1)")
	v.port = fs.Int("serve-port", 7777, "HTTP bind port")
	v.token = fs.String("serve-token", "", "bearer token required by --serve")
	v.version = fs.Bool("version", false, "print version and exit")
	v.help = fs.Bool("help", false, "print this help")
	v.overrides = &Overrides{}
	fs.Var(v.overrides, "c", "config override key=value (repeatable)")

	// Aliases share the same storage, so either spelling is equivalent.
	fs.StringVar(v.model, "model", "", "model id")
	fs.StringVar(v.cwd, "C", "", "workspace root")
	fs.StringVar(v.session, "session-id", "", "resume the session with this id")
	fs.BoolVar(v.resume, "continue", false, "resume the most recent session")
	fs.BoolVar(v.version, "V", false, "print version and exit")
	fs.BoolVar(v.help, "h", false, "print this help")
	fs.StringVar(v.token, "token", "", "alias for --serve-token")
	fs.StringVar(v.sandboxMode, "sandbox", "", "alias for --sandbox-mode")
	fs.BoolVar(v.jsonLines, "continue-session", false, "alias for --continue")
	fs.Var(v.overrides, "config", "config override key=value (repeatable)")
}

// flagSurface reports every long flag name the CLI accepts, sorted. It builds a
// throwaway flag set so the list can never drift from the parser.
func flagSurface() []string {
	fs := flag.NewFlagSet("argent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	newFlags(fs, &flagValues{})
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		if len(f.Name) > 1 {
			names = append(names, f.Name)
		}
	})
	sort.Strings(names)
	return names
}

// parseArgs parses the flag surface, returning resolved options plus the
// positional prompt words. Flags may appear before or between positional words.
func parseArgs(args []string, env func(string) string, stderr io.Writer) (*Options, []string, error) {
	before, after := splitTerminator(args)
	if hasHelpFlag(before) {
		return &Options{Help: true}, nil, nil
	}

	fs := flag.NewFlagSet("argent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { writeUsage(stderr) }
	values := &flagValues{}
	newFlags(fs, values)

	positional, err := parseInterleaved(fs, before)
	if err != nil {
		return nil, nil, err
	}
	positional = append(positional, after...)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	overrides := *values.overrides
	maxParallel := intSetting(set, "max-parallel", *values.maxParallel, env, "ARGENT_MAX_PARALLEL",
		overrideInt(&overrides, "maxParallel", "max-parallel"))
	opts := &Options{
		Overrides:   overrides,
		Mode:        *values.mode,
		Cwd:         *values.cwd,
		Session:     *values.session,
		Resume:      *values.resume,
		Undo:        *values.undo,
		Retry:       *values.retry,
		Title:       *values.title,
		Serve:       *values.serve,
		RPC:         *values.rpc,
		ACP:         *values.acp,
		Help:        hasHelpFlag(before),
		Version:     *values.version,
		MaxTurns:    *values.maxTurns,
		MaxParallel: maxParallel,
		TUI:         *values.tui,
		ServeToken:  firstNonEmpty(*values.token, env("ARGENT_SERVE_TOKEN"), env("ARGENT_TOKEN")),
		ServeAddr:   serveAddress(*values.addr, *values.port),
		SandboxMode: firstNonEmpty(*values.sandboxMode, env("ARGENT_SANDBOX_MODE")),
		Approval:    firstNonEmpty(*values.approval, env("ARGENT_APPROVAL")),
		Protocol:    firstNonEmpty(*values.protocol, env("ARGENT_PROTOCOL")),
		Locale:      firstNonEmpty(*values.locale, env("ARGENT_LOCALE")),
		Model:       firstNonEmpty(*values.model, env("ARGENT_MODEL")),
		Provider:    firstNonEmpty(*values.provider, env("ARGENT_PROVIDER")),
		Profile:     firstNonEmpty(*values.profile, env("ARGENT_PROFILE")),
		BaseURL:     firstNonEmpty(*values.baseURL, env("ARGENT_BASE_URL"), env("OPENAI_BASE_URL")),
		APIKey:      firstNonEmpty(*values.apiKey, env("ARGENT_API_KEY"), env("OPENAI_API_KEY")),
	}

	opts.JSONLines = *values.jsonLines
	opts.ListProfiles = *values.listProfiles
	opts.ListSessions = *values.listSessions
	opts.ListModels = *values.listModels
	opts.NoColor = *values.noColor
	opts.Tokens = *values.tokens
	opts.TrustProject = *values.trustProject
	opts.NoAutoCompact = *values.noAutoCompact
	opts.NoPromptCache = *values.noPromptCache
	opts.Plugins = append([]string(nil), values.plugins.values...)
	opts.Background = *values.background
	opts.Worktree = values.worktree.value
	opts.EnableBrowser = *values.enableBrowser
	opts.EnableDesktop = *values.enableDesktop
	opts.EnableService = *values.enableService
	opts.WorkflowMode = *values.workflowMode
	opts.WorkflowResume = *values.workflowResume
	opts.WorkflowJournal = *values.workflowJournal
	opts.GUI = *values.gui
	opts.AddDirs = append([]string(nil), values.addDirs.values...)
	opts.OutputSchema = *values.outputSchema

	// --json-lines is the strict one-shot JSONL mode.
	if opts.JSONLines {
		opts.Mode = ModeJSON
	}
	// --continue-session, --continue and --resume all mean "resume the latest".
	if set["continue-session"] || set["continue"] || set["resume"] {
		opts.Resume = true
	}

	opts.Thinking = firstNonEmpty(*values.thinking, env("ARGENT_THINKING"))
	opts.Theme = firstNonEmpty(*values.theme, env("ARGENT_THEME"))
	opts.FallbackModel = *values.fallbackModel
	opts.SecondaryModel = *values.secondaryModel

	opts.MaxTokens = intSetting(set, "max-tokens", *values.maxTokens, env, "ARGENT_MAX_TOKENS",
		overrideInt(&overrides, "maxTokens", "max-tokens"))
	opts.MaxCost = floatSetting(set, "max-cost", *values.maxCost, env, "ARGENT_MAX_COST",
		overrideFloat(&overrides, "maxCost", "max-cost"))
	opts.MaxSeconds = intSetting(set, "max-seconds", *values.maxSeconds, env, "ARGENT_MAX_SECONDS",
		overrideInt(&overrides, "maxSeconds", "max-seconds"))
	opts.PriceInput = floatSetting(set, "price-input", *values.priceInput, env, "ARGENT_PRICE_INPUT",
		overrideFloat(&overrides, "priceInput", "price-input"))
	opts.PriceOutput = floatSetting(set, "price-output", *values.priceOutput, env, "ARGENT_PRICE_OUTPUT",
		overrideFloat(&overrides, "priceOutput", "price-output"))

	// Config-file overrides sit below env and flags.
	opts.Model = firstNonEmpty(opts.Model, overrideValue(&overrides, "model"), "gpt-4o-mini")
	opts.BaseURL = firstNonEmpty(opts.BaseURL, overrideValue(&overrides, "baseUrl", "base-url", "base_url"), "https://api.openai.com/v1")
	opts.APIKey = firstNonEmpty(opts.APIKey, overrideValue(&overrides, "apiKey", "api-key", "api_key"))
	opts.Protocol = firstNonEmpty(opts.Protocol, overrideValue(&overrides, "protocol"), ProtocolOpenAI)
	opts.Provider = firstNonEmpty(opts.Provider, overrideValue(&overrides, "provider"))
	opts.Locale = firstNonEmpty(opts.Locale, overrideValue(&overrides, "locale"))
	opts.SandboxMode = firstNonEmpty(opts.SandboxMode, overrideValue(&overrides, "sandboxMode", "sandbox-mode"), SandboxWorkspaceWrite)
	opts.Approval = firstNonEmpty(opts.Approval, overrideValue(&overrides, "approval"), ApprovalAsk)
	opts.Mode = firstNonEmpty(opts.Mode, overrideValue(&overrides, "mode"), ModeText)

	if !set["max-turns"] {
		if value := env("ARGENT_MAX_TURNS"); value != "" {
			if n, err := strconv.Atoi(value); err == nil {
				opts.MaxTurns = n
			}
		} else if value, ok := overrideValueOK(&overrides, "maxTurns", "max-turns"); ok {
			if n, err := strconv.Atoi(value); err == nil {
				opts.MaxTurns = n
			}
		}
	}
	opts.Yolo = *values.yolo || env("ARGENT_YOLO") == "1" || strings.EqualFold(overrideValue(&overrides, "yolo"), "true")

	if err := opts.validate(); err != nil {
		return nil, nil, &usageError{msg: err.Error()}
	}
	return opts, positional, nil
}

// intSetting resolves a numeric flag as flag > env > override > zero.
func intSetting(set map[string]bool, name string, flagValue int, env func(string) string, envKey string, override int) int {
	if set[name] {
		return flagValue
	}
	if value := env(envKey); value != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return n
		}
	}
	if override != 0 {
		return override
	}
	return flagValue
}

// floatSetting resolves a float flag as flag > env > override > zero.
func floatSetting(set map[string]bool, name string, flagValue float64, env func(string) string, envKey string, override float64) float64 {
	if set[name] {
		return flagValue
	}
	if value := env(envKey); value != "" {
		if n, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return n
		}
	}
	if override != 0 {
		return override
	}
	return flagValue
}

func overrideInt(o *Overrides, keys ...string) int {
	value, ok := o.lookup(keys...)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}

func overrideFloat(o *Overrides, keys ...string) float64 {
	value, ok := o.lookup(keys...)
	if !ok {
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return n
}

func (o *Options) validate() error {
	switch o.Mode {
	case ModeText, ModeJSON:
	default:
		return fmt.Errorf("--mode must be text or json, got %q", o.Mode)
	}
	switch o.Protocol {
	case ProtocolAnthropic, ProtocolOpenAI, ProtocolResponses:
	default:
		return fmt.Errorf("--protocol must be anthropic, openai or responses, got %q", o.Protocol)
	}
	switch o.SandboxMode {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
	default:
		return fmt.Errorf("--sandbox-mode must be read-only, workspace-write or danger-full-access, got %q", o.SandboxMode)
	}
	switch o.Approval {
	case ApprovalAsk, ApprovalNever, ApprovalAuto:
	default:
		return fmt.Errorf("--approval must be ask, never or auto, got %q", o.Approval)
	}
	if o.MaxTurns < 1 {
		return fmt.Errorf("--max-turns must be at least 1, got %d", o.MaxTurns)
	}
	if o.MaxParallel < 0 {
		return fmt.Errorf("--max-parallel must be >= 0, got %d", o.MaxParallel)
	}
	if o.MaxTokens < 0 {
		return fmt.Errorf("--max-tokens must be >= 0, got %d", o.MaxTokens)
	}
	if o.MaxSeconds < 0 {
		return fmt.Errorf("--max-seconds must be >= 0, got %d", o.MaxSeconds)
	}
	if o.MaxCost < 0 {
		return fmt.Errorf("--max-cost must be >= 0, got %v", o.MaxCost)
	}
	if o.PriceInput < 0 || o.PriceOutput < 0 {
		return fmt.Errorf("--price-input and --price-output must be >= 0")
	}
	return nil
}

// parseInterleaved parses flags that may be separated by positional words.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		tail := fs.Args()
		if len(tail) == 0 {
			return positional, nil
		}
		positional = append(positional, tail[0])
		rest = tail[1:]
	}
}

func splitTerminator(args []string) (before, after []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func overrideValue(o *Overrides, keys ...string) string {
	value, _ := o.lookup(keys...)
	return value
}

func overrideValueOK(o *Overrides, keys ...string) (string, bool) {
	return o.lookup(keys...)
}

func serveAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// writeUsage prints the command surface to w.
func writeUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent [flags] <prompt>
       argent [flags] -            (prompt from stdin)
       argent <command> [args]

commands:
  sessions list|show|rm|find    inspect stored sessions
  replay <id>                   print a stored session transcript
  usage                         summarise turns and tokens per session
  doctor                        check config, storage, provider, git and Go
  login <provider>              store an API key in the config file
  auth                          show stored credential status
  logout [provider]             remove stored credentials
  models                        list provider templates
  mcp                           list configured MCP servers
  skills                        list discovered skills
  commands                      list discovered slash commands
  hooks                         list hook documents and trust state
  policy                        show sandbox, approval and policy
  audit                         tail the audit log
  update                        check GitHub releases for a newer build
  completion bash|zsh|powershell  print a shell completion script
  tui                           run the full-screen terminal UI
  bug                           write a diagnostics dump
  eval <file>                   run scripted JSON eval scenarios
  config get|set                read or write dotted config keys
   workflow list|run|show       inspect and run workflow definitions
   telemetry status|off         report the personal-build telemetry preference
   attach <id> [-n N]           print the last N lines of a job log
   logs [<id>]                  list background jobs and their logs
   jobs                         alias for logs
   follow <id> [-n N]           tail a job log until interrupted
   stop <id>                    kill a background job
   board                        read-only view of jobs and automations

flags:
  -m, --model <id>              model id (default gpt-4o-mini)
      --provider <name>         provider template id (see `+"`argent models`"+`)
      --profile <name>          named config profile
      --list-profiles           list config profiles and exit
      --base-url <url>          provider base URL
      --api-key <key>           provider API key
      --protocol <name>         anthropic | openai | responses (default openai)
      --yolo                    auto-approve every tool call
      --max-turns <n>           maximum model turns (default 12)
      --max-parallel <n>        read-only tool workers per batch (0 default, 1 serial)
      --max-tokens <n>          stop after this many billable tokens
      --max-cost <usd>          stop after this estimated dollar spend
      --max-seconds <n>         stop after this many wall-clock seconds
      --price-input <usd>       USD per million prompt tokens
      --price-output <usd>      USD per million completion tokens
      --fallback-model <id>     model to retry once with when the primary fails
      --secondary-model <id>    model for delegated subagent tasks
      --thinking <level>        reasoning effort hint (low | medium | high)
      --mode <mode>             text | json (default text)
      --json-lines              one-shot strict JSONL on stdout
      --tokens                  print the request payload breakdown and exit
  -C, --cwd <dir>               workspace root (default: current directory)
      --add-dir <dir>           extra writable workspace directory (repeatable)
      --sandbox-mode <mode>     read-only | workspace-write | danger-full-access
      --sandbox <mode>          alias for --sandbox-mode
      --approval <mode>         ask | never | auto (default ask)
      --trust-project           trust project-local resources for this run
      --session <id>            resume the session with this id
      --session-id <id>         alias for --session
      --resume                  resume the most recent session
      --undo                    restore files from the latest checkpoint and exit
      --retry                   fork the session at its latest checkpoint
      --title <text>            override the generated session title
      --continue                alias for --resume
      --continue-session        alias for --resume
      --list-sessions           list stored sessions and exit
      --list-models             list provider templates and exit
      --output-schema <file>    JSON Schema path, or inline schema text
      --plugins <name>          load this plugin by name (repeatable, `+"`all`"+`)
      --no-auto-compact         disable automatic compaction for this run
      --no-prompt-cache         disable provider prompt-cache markers for this run
      --no-color                disable ANSI colours
      --theme <name>            UI theme name
      --locale <locale>         output locale
      --tui                     run the full-screen terminal UI
      --worktree[=name]         run in a git worktree (not migrated)
      --background              run in the background (not migrated)
      --enable-browser          enable the browser tool (not migrated)
      --enable-desktop          enable desktop control (not migrated)
      --enable-service          enable the pty service tool (not migrated)
      --workflow-mode <mode>    dynamic workflow mode (not migrated)
      --workflow-resume         resume a workflow journal (not migrated)
      --workflow-journal <path> workflow journal path (not migrated)
      --gui                     launch the desktop window (not migrated)
  -c, --config <key=value>      config override, dotted keys nest (repeatable)
      --serve                   serve HTTP + SSE (/v1/prompt, /v1/abort, /v1/health)
      --serve-address <host>    HTTP bind address (default 127.0.0.1)
      --serve-port <port>       HTTP bind port (default 7777)
      --serve-token <token>     bearer token; mandatory for --serve
      --token <token>           alias for --serve-token
      --rpc                     newline-delimited JSON-RPC on stdio
      --acp                     ACP JSON-RPC on stdio
  -V, --version                 print version and exit
  -h, --help                    print this help
`)
}
