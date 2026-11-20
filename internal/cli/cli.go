// Package cli is the testable command layer of the argentgo binary: flag
// parsing, subcommand dispatch and the entry points for every transport.
//
// The agent loop is injected. The integrator wires internal/agent by setting
// DefaultRunner (or App.NewRunner), which keeps this package free of the
// tools/session/security packages while the rest of the migration lands.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/server"
)

// Version is the version reported by --version, /v1/health and ACP initialize.
const Version = "0.6.0"

// IO carries the process streams so commands can be tested without os.Stdin.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	Env func(string) string
}

// OSIO returns the real process streams.
func OSIO() IO {
	return IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Env: os.Getenv}
}

// Runner is the agent slice every transport drives.
type Runner = server.Runner

// RunnerFactory builds a runner from resolved options.
type RunnerFactory func(Options) (Runner, error)

// DefaultRunner is the process-wide runner factory. It stays nil until the
// integrator wires internal/agent into this package.
var DefaultRunner RunnerFactory

// SessionStore is the local session view used by `sessions` and the RPC/ACP
// transports.
type SessionStore interface {
	server.SessionStore
	Delete(id string) error
}

// EvalReport is what one eval scenario is graded against.
type EvalReport struct {
	Text  string
	Tools []string
}

// EvalSuiteRunner executes a whole scripted suite file. RunEval prefers it
// when available; the narrow EvalRunner stays for simple injected fakes.
type EvalSuiteRunner interface {
	RunSuite(ctx context.Context, path string, out io.Writer) (passed, failed int, err error)
}

// EvalRunner executes one eval scenario.
type EvalRunner interface {
	RunScenario(ctx context.Context, prompt string) (EvalReport, error)
}

// TUIFunc runs the full-screen terminal UI for one set of resolved options.
type TUIFunc func(ctx context.Context, opts Options) error

// ProfileLister reports the profile names visible to a workspace and the one
// currently selected.
type ProfileLister func(opts Options) (names []string, active string, err error)

// TokenReporter renders the request payload breakdown printed by --tokens.
type TokenReporter func(opts Options) (string, error)

// App bundles the injected dependencies of the command layer.
type App struct {
	IO        IO
	Version   string
	NewRunner RunnerFactory
	Sessions  SessionStore
	// Eval accepts either an EvalRunner or an EvalSuiteRunner; runEval selects
	// the richer suite interface at runtime when it is available.
	Eval        any
	TUI         TUIFunc
	Profiles    ProfileLister
	TokenReport TokenReporter
	ConfigPath  string
	StorageDir  string
	// UpdateOps overrides the updater seams for tests; nil calls internal/update.
	UpdateOps *UpdateOps
}

// Run executes the CLI and returns the process exit code.
func Run(args []string, stdio IO) int {
	return RunWith(App{IO: stdio}, args)
}

// RunWith executes the CLI with an explicit App, which is what tests use.
func RunWith(app App, args []string) int {
	app = app.withDefaults()
	ctx, stop := commandContext()
	defer stop()

	if len(args) > 0 {
		if handler, ok := subcommands[args[0]]; ok {
			return handler(app, ctx, args[1:])
		}
	}
	return app.runMain(ctx, args)
}

var subcommands = map[string]func(App, context.Context, []string) int{
	"sessions":   runSessions,
	"replay":     runReplay,
	"usage":      runUsage,
	"doctor":     runDoctor,
	"login":      runLogin,
	"logout":     runLogout,
	"auth":       runAuth,
	"models":     runModels,
	"mcp":        runMCP,
	"skills":     runSkills,
	"commands":   runCommands,
	"hooks":      runHooks,
	"policy":     runPolicy,
	"audit":      runAudit,
	"update":     runUpdate,
	"completion": runCompletion,
	"tui":        runTUI,
	"bug":        runBug,
	"eval":       runEval,
	"config":     runConfig,
	"workflow":   runWorkflow,
	"telemetry":  runTelemetry,
	"attach":     runAttach,
	"logs":       runLogs,
	"jobs":       runJobs,
	"follow":     runFollow,
	"stop":       runStop,
	"board":      runBoard,
	"help":       runHelp,
	"version":    runVersion,
}

// subcommandOrder mirrors the dispatch map. It exists so completion does not
// reference the map at package initialisation (which would be an import cycle
// through runCompletion); parity_test asserts the two stay identical.
var subcommandOrder = []string{
	"sessions", "replay", "usage", "doctor", "login", "logout", "auth",
	"models", "mcp", "skills", "commands", "hooks", "policy", "audit",
	"update", "completion", "tui", "bug", "eval", "config",
	"workflow", "telemetry", "attach", "logs", "jobs", "follow", "stop", "board",
	"help", "version",
}

// subcommandNames lists every dispatched command, sorted. Completion reads it
// and the parity test cross-checks it against the dispatch map, so the two
// cannot drift.
func subcommandNames() []string {
	names := append([]string(nil), subcommandOrder...)
	sort.Strings(names)
	return names
}

func runHelp(app App, _ context.Context, _ []string) int {
	writeUsage(app.IO.Out)
	return 0
}

func runVersion(app App, _ context.Context, _ []string) int {
	fmt.Fprintf(app.IO.Out, "argentgo %s\n", app.Version)
	return 0
}

// usageError marks validation failures that still need a message after the
// flag package has printed its own diagnostics.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func (a App) runMain(ctx context.Context, args []string) int {
	opts, positional, err := parseArgs(args, a.env, a.IO.Err)
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(a.IO.Err, "argent:", ue.msg)
			writeUsage(a.IO.Err)
		}
		return 2
	}
	if opts.Help {
		writeUsage(a.IO.Out)
		return 0
	}
	if opts.Version {
		fmt.Fprintf(a.IO.Out, "argentgo %s\n", a.Version)
		return 0
	}
	if opts.ListProfiles {
		return a.listProfiles(*opts)
	}
	if opts.ListModels {
		return runModels(a, ctx, nil)
	}
	if opts.ListSessions {
		return sessionsList(a, a.sessionStore())
	}
	if opts.Tokens {
		return a.printTokens(*opts)
	}
	if reason := unsupportedFlag(opts); reason != "" {
		fmt.Fprintln(a.IO.Err, "argent:", reason)
		return 1
	}
	if opts.TUI {
		return a.runTUI(ctx, opts)
	}
	a.warnPartial(opts)

	root := opts.Cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error: resolve workspace:", err)
		return 1
	}
	opts.Cwd = absRoot

	if code, handled := a.applySessionFlags(opts); handled {
		return code
	}

	switch {
	case opts.Serve:
		if strings.TrimSpace(opts.ServeToken) == "" {
			fmt.Fprintln(a.IO.Err, "error: --serve requires a bearer token (--serve-token or ARGENT_SERVE_TOKEN)")
			return 1
		}
		runner, err := a.newRunner(opts)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1
		}
		return a.serve(ctx, opts, runner)
	case opts.RPC:
		runner, err := a.newRunner(opts)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1
		}
		return a.rpc(ctx, opts, runner)
	case opts.ACP:
		runner, err := a.newRunner(opts)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1
		}
		return a.acp(ctx, opts, runner)
	}

	prompt := strings.TrimSpace(strings.Join(positional, " "))
	if prompt == "" {
		data, err := io.ReadAll(a.IO.In)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error: read stdin:", err)
			return 1
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		writeUsage(a.IO.Err)
		return 2
	}

	runner, err := a.newRunner(opts)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return a.runPrompt(ctx, opts, runner, prompt)
}

func (a App) serve(ctx context.Context, opts *Options, runner Runner) int {
	cfg := server.Config{
		Addr:     opts.ServeAddr,
		Token:    opts.ServeToken,
		Version:  a.Version,
		Runner:   runner,
		Sessions: a.Sessions,
	}
	srv, ln, err := server.Listen(cfg)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(a.IO.Err, "argentgo listening on http://%s (bearer token required)\n", ln.Addr())
	if err := server.ServeListener(ctx, srv, ln); err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return 0
}

func (a App) rpc(ctx context.Context, opts *Options, runner Runner) int {
	cfg := server.Config{Version: a.Version, Runner: runner, Sessions: a.Sessions}
	if err := server.RunRPC(ctx, a.IO.In, a.IO.Out, cfg); err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return 0
}

func (a App) acp(ctx context.Context, opts *Options, runner Runner) int {
	cfg := server.Config{Version: a.Version, Runner: runner, Sessions: a.Sessions}
	if err := server.RunACP(ctx, a.IO.In, a.IO.Out, cfg); err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return 0
}

// runPrompt streams one turn to IO and returns the exit code.
func (a App) runPrompt(ctx context.Context, opts *Options, runner Runner, prompt string) int {
	jsonMode := opts.Mode == ModeJSON
	streamed := false
	onText := func(chunk string) {
		if chunk == "" {
			return
		}
		streamed = true
		if jsonMode {
			a.emit(map[string]any{"type": "text", "text": chunk})
			return
		}
		fmt.Fprint(a.IO.Out, chunk)
	}

	result, err := runner.Prompt(ctx, prompt, onText)
	if jsonMode {
		a.emit(map[string]any{"type": "result", "text": result, "error": errText(err)})
	}
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		writeActionableError(a.IO.Err, err)
		return 1
	}
	if !jsonMode {
		if !streamed {
			fmt.Fprint(a.IO.Out, result)
		}
		fmt.Fprintln(a.IO.Out)
	}
	return 0
}

// writeActionableError appends the normalized provider explanation and its
// next step to the terse "error: ..." line. It is additive on purpose: the
// original line is unchanged for callers that parse stderr, while the user
// gets a concrete recovery action. Classification is a no-op for nil errors
// and never changes the success path.
func writeActionableError(w io.Writer, err error) {
	if err == nil {
		return
	}
	actionable := llm.ClassifyAPIError(err)
	if actionable == nil {
		return
	}
	fmt.Fprintln(w, actionable.Message)
	fmt.Fprintln(w, actionable.NextStep)
}

func (a App) emit(event map[string]any) {
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintln(a.IO.Out, string(line))
}

func (a App) newRunner(opts *Options) (Runner, error) {
	factory := a.NewRunner
	if factory == nil {
		factory = DefaultRunner
	}
	if factory == nil {
		return nil, errors.New("no runner configured: set cli.DefaultRunner (integrator wires internal/agent)")
	}
	return factory(*opts)
}

func (a App) withDefaults() App {
	if a.IO.In == nil {
		a.IO.In = os.Stdin
	}
	if a.IO.Out == nil {
		a.IO.Out = os.Stdout
	}
	if a.IO.Err == nil {
		a.IO.Err = os.Stderr
	}
	if a.IO.Env == nil {
		a.IO.Env = os.Getenv
	}
	if a.Version == "" {
		a.Version = Version
	}
	return a
}

func (a App) env(key string) string {
	if a.IO.Env == nil {
		return ""
	}
	return a.IO.Env(key)
}

func (a App) storageDir() string {
	if a.StorageDir != "" {
		return a.StorageDir
	}
	if dir := firstNonEmpty(a.env("ARGENT_STORAGE_DIR"), a.env("ARGENT_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".argentgo"
	}
	return filepath.Join(home, ".argentgo")
}

func (a App) configPath() string {
	if a.ConfigPath != "" {
		return a.ConfigPath
	}
	return filepath.Join(a.storageDir(), "config.json")
}

func (a App) sessionDir() string {
	return filepath.Join(a.storageDir(), "sessions")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// commandContext is cancelled on Ctrl-C.
func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
