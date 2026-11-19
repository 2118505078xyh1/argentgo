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
	Model       string
	BaseURL     string
	APIKey      string
	Protocol    string
	Yolo        bool
	MaxTurns    int
	Mode        string
	Cwd         string
	SandboxMode string
	Approval    string
	Session     string
	Resume      bool
	Locale      string

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

	var (
		model    = fs.String("m", "", "model id")
		baseURL  = fs.String("base-url", "", "provider base URL")
		apiKey   = fs.String("api-key", "", "provider API key")
		protocol = fs.String("protocol", "", "protocol: anthropic, openai or responses")
		yolo     = fs.Bool("yolo", false, "auto-approve every tool call")
		maxTurns = fs.Int("max-turns", 12, "maximum model turns")
		mode     = fs.String("mode", ModeText, "output mode: text or json")
		cwd      = fs.String("cwd", "", "workspace root (default: current directory)")
		sandbox  = fs.String("sandbox-mode", "", "read-only, workspace-write or danger-full-access")
		approval = fs.String("approval", "", "approval mode: ask, never or auto")
		session  = fs.String("session", "", "resume the session with this id")
		resume   = fs.Bool("resume", false, "resume the most recent session")
		locale   = fs.String("locale", "", "output locale")
		serve    = fs.Bool("serve", false, "serve HTTP + SSE")
		rpc      = fs.Bool("rpc", false, "serve newline-delimited JSON-RPC on stdio")
		acp      = fs.Bool("acp", false, "serve ACP JSON-RPC on stdio")
		addr     = fs.String("serve-address", "", "HTTP bind address (default 127.0.0.1)")
		port     = fs.Int("serve-port", 7777, "HTTP bind port")
		token    = fs.String("serve-token", "", "bearer token required by --serve")
		version  = fs.Bool("version", false, "print version and exit")
	)
	var overrides Overrides
	fs.Var(&overrides, "c", "config override key=value (repeatable)")
	fs.Var(&overrides, "config", "config override key=value (repeatable)")
	fs.StringVar(model, "model", "", "model id")
	fs.StringVar(cwd, "C", "", "workspace root")
	fs.StringVar(session, "session-id", "", "resume the session with this id")
	fs.BoolVar(resume, "continue", false, "resume the most recent session")
	fs.BoolVar(version, "V", false, "print version and exit")
	fs.StringVar(token, "token", "", "alias for --serve-token")

	positional, err := parseInterleaved(fs, before)
	if err != nil {
		return nil, nil, err
	}
	positional = append(positional, after...)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	opts := &Options{
		Overrides:   overrides,
		Mode:        *mode,
		Cwd:         *cwd,
		Session:     *session,
		Resume:      *resume,
		Serve:       *serve,
		RPC:         *rpc,
		ACP:         *acp,
		Help:        hasHelpFlag(before),
		Version:     *version,
		MaxTurns:    *maxTurns,
		ServeToken:  firstNonEmpty(*token, env("ARGENT_SERVE_TOKEN"), env("ARGENT_TOKEN")),
		ServeAddr:   serveAddress(*addr, *port),
		SandboxMode: firstNonEmpty(*sandbox, env("ARGENT_SANDBOX_MODE")),
		Approval:    firstNonEmpty(*approval, env("ARGENT_APPROVAL")),
		Protocol:    firstNonEmpty(*protocol, env("ARGENT_PROTOCOL")),
		Locale:      firstNonEmpty(*locale, env("ARGENT_LOCALE")),
		Model:       firstNonEmpty(*model, env("ARGENT_MODEL")),
		BaseURL:     firstNonEmpty(*baseURL, env("ARGENT_BASE_URL"), env("OPENAI_BASE_URL")),
		APIKey:      firstNonEmpty(*apiKey, env("ARGENT_API_KEY"), env("OPENAI_API_KEY")),
	}

	// Config-file overrides sit below env and flags.
	opts.Model = firstNonEmpty(opts.Model, overrideValue(&overrides, "model"), "gpt-4o-mini")
	opts.BaseURL = firstNonEmpty(opts.BaseURL, overrideValue(&overrides, "baseUrl", "base-url", "base_url"), "https://api.openai.com/v1")
	opts.APIKey = firstNonEmpty(opts.APIKey, overrideValue(&overrides, "apiKey", "api-key", "api_key"))
	opts.Protocol = firstNonEmpty(opts.Protocol, overrideValue(&overrides, "protocol"), ProtocolOpenAI)
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
	opts.Yolo = *yolo || env("ARGENT_YOLO") == "1" || strings.EqualFold(overrideValue(&overrides, "yolo"), "true")

	if err := opts.validate(); err != nil {
		return nil, nil, &usageError{msg: err.Error()}
	}
	return opts, positional, nil
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
  doctor                        check config, storage, provider, git and Go
  login <provider>              store an API key in the config file
  eval <file>                   run scripted JSON eval scenarios
  config get|set                read or write dotted config keys

flags:
  -m, --model <id>              model id (default gpt-4o-mini)
      --base-url <url>          provider base URL
      --api-key <key>           provider API key
      --protocol <name>         anthropic | openai | responses (default openai)
      --yolo                    auto-approve every tool call
      --max-turns <n>           maximum model turns (default 12)
      --mode <mode>             text | json (default text)
  -C, --cwd <dir>               workspace root (default: current directory)
      --sandbox-mode <mode>     read-only | workspace-write | danger-full-access
      --approval <mode>         ask | never | auto (default ask)
      --session <id>            resume the session with this id
      --resume                  resume the most recent session
      --locale <locale>         output locale
  -c, --config <key=value>      config override, dotted keys nest (repeatable)
      --serve                   serve HTTP + SSE (/v1/prompt, /v1/abort, /v1/health)
      --serve-address <host>    HTTP bind address (default 127.0.0.1)
      --serve-port <port>       HTTP bind port (default 7777)
      --serve-token <token>     bearer token; mandatory for --serve
      --rpc                     newline-delimited JSON-RPC on stdio
      --acp                     ACP JSON-RPC on stdio
  -V, --version                 print version and exit
  -h, --help                    print this help
`)
}
