package cli

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

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

	// -c overrides sit below env and flags. Leave compiled defaults out of
	// Options: an empty field means "unset" and lets app.Resolve prefer the
	// config file / profile layer over the compiled default.
	opts.Model = firstNonEmpty(opts.Model, overrideValue(&overrides, "model"))
	opts.BaseURL = firstNonEmpty(opts.BaseURL, overrideValue(&overrides, "baseUrl", "base-url", "base_url"))
	opts.APIKey = firstNonEmpty(opts.APIKey, overrideValue(&overrides, "apiKey", "api-key", "api_key"))
	opts.Protocol = firstNonEmpty(opts.Protocol, overrideValue(&overrides, "protocol"))
	opts.Provider = firstNonEmpty(opts.Provider, overrideValue(&overrides, "provider"))
	opts.Locale = firstNonEmpty(opts.Locale, overrideValue(&overrides, "locale"))
	opts.SandboxMode = firstNonEmpty(opts.SandboxMode, overrideValue(&overrides, "sandboxMode", "sandbox-mode"))
	opts.Approval = firstNonEmpty(opts.Approval, overrideValue(&overrides, "approval"))
	opts.Mode = firstNonEmpty(opts.Mode, overrideValue(&overrides, "mode"))

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
	// Empty means "unset": the compiled default is applied by app.Resolve,
	// after the config file and profile layers have had their say.
	switch o.Mode {
	case "", ModeText, ModeJSON:
	default:
		return fmt.Errorf("--mode must be text or json, got %q", o.Mode)
	}
	// Both the short CLI spellings and the config wire names are accepted;
	// app.Resolve canonicalizes them before choosing the chat client.
	switch strings.ToLower(strings.TrimSpace(o.Protocol)) {
	case "", ProtocolAnthropic, ProtocolOpenAI, ProtocolResponses,
		"anthropic-messages", "openai-chat-completions", "openai-responses", "messages":
	default:
		return fmt.Errorf("--protocol must be anthropic, openai or responses, got %q", o.Protocol)
	}
	switch o.SandboxMode {
	case "", SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
	default:
		return fmt.Errorf("--sandbox-mode must be read-only, workspace-write or danger-full-access, got %q", o.SandboxMode)
	}
	switch o.Approval {
	case "", ApprovalAsk, ApprovalNever, ApprovalAuto:
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

// serveAddress combines --serve-address and --serve-port into a bind address.
// A colon alone does not mean "host:port": a bare IPv6 literal (::1) has no
// port yet and must be joined with --serve-port. Only an address that already
// carries an explicit port is returned unchanged.
func serveAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	portText := strconv.Itoa(port)

	// Bracketed IPv6: [::1] still needs the port, [::1]:8080 already has one.
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end >= 0 {
			inner, rest := host[1:end], host[end+1:]
			switch {
			case rest == "":
				return net.JoinHostPort(inner, portText)
			case strings.HasPrefix(rest, ":"):
				return host
			}
		}
	}

	// A parsed IP literal is always a bare host, never host:port. Zone-scoped
	// IPv6 (fe80::1%eth0) is not parsed by net.ParseIP but still has >1 colon,
	// so it falls through to JoinHostPort below, which brackets it correctly.
	if net.ParseIP(host) != nil {
		return net.JoinHostPort(host, portText)
	}

	// A hostname or IPv4 address with exactly one colon is already host:port.
	if strings.Count(host, ":") == 1 {
		return host
	}
	return net.JoinHostPort(host, portText)
}
