package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/app"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/gui"
	"github.com/2118505078xyh1/argentgo/internal/launcher"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/nativegui"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func isGUIRequest(args []string) bool {
	for _, arg := range args {
		if arg == "gui" || arg == "--gui" {
			return true
		}
	}
	return false
}

func runGUI(stdio cli.IO, args []string) int {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "gui" || arg == "--gui" {
			continue
		}
		filtered = append(filtered, arg)
	}

	flags := flag.NewFlagSet("argent gui", flag.ContinueOnError)
	flags.SetOutput(stdio.Err)
	var model, baseURL, apiKey, provider, protocol, locale, cwd string
	var web bool
	flags.StringVar(&model, "m", "", "model id")
	flags.StringVar(&model, "model", "", "model id")
	flags.StringVar(&baseURL, "base-url", "", "OpenAI-compatible base URL")
	flags.StringVar(&apiKey, "api-key", "", "API key")
	flags.StringVar(&provider, "provider", "", "provider template")
	flags.StringVar(&protocol, "protocol", "", "wire protocol")
	flags.StringVar(&locale, "locale", "", "ui locale")
	flags.StringVar(&cwd, "C", "", "workspace root")
	flags.StringVar(&cwd, "cwd", "", "workspace root")
	flags.BoolVar(&web, "web", false, "use the embedded web UI instead of the native window")
	if err := flags.Parse(filtered); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stdio.Err, "argent gui: unexpected argument:", flags.Arg(0))
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := cli.Options{
		Cwd: cwd, Model: model, BaseURL: baseURL, APIKey: apiKey,
		Protocol: protocol, Locale: locale,
	}
	resolved, err := app.Resolve(stdio, opts)
	if err != nil {
		fmt.Fprintln(stdio.Err, "error:", err)
		return 1
	}
	store, err := session.Open(filepath.Join(resolved.Storage, "sessions"))
	if err != nil {
		fmt.Fprintln(stdio.Err, "error: open sessions:", err)
		return 1
	}

	history := &guiHistory{sessions: store.Dir(), root: resolved.Root}
	if !web && runtime.GOOS == "windows" {
		return nativeRun(ctx, newGUIRunner(ctx, stdio, resolved, store), resolved, history)
	}

	guiOpts := gui.Options{
		Dir:        resolved.Root,
		Version:    cli.Version,
		Model:      resolved.Model,
		Provider:   providerName(resolved, provider),
		Protocol:   resolved.Protocol,
		Locale:     locale,
		StorageDir: resolved.Storage,
		Sessions:   &guiSessions{dir: store.Dir(), cwd: resolved.Root},
		History:    history,
		NewRunner: func() gui.Runner {
			return newGUIRunner(ctx, stdio, resolved, store)
		},
		OnReady: func(url string) {
			profile := filepath.Join(resolved.Storage, "gui-profile")
			if err := launcher.Open(ctx, url, profile); err != nil {
				fmt.Fprintf(stdio.Err, "argent: %v\n", err)
				fmt.Fprintln(stdio.Err, "open this URL manually:", url)
			} else if errors.Is(err, launcher.ErrFallback) {
				fmt.Fprintln(stdio.Err, "opened in a browser tab (no Edge/Chrome app mode found)")
			} else {
				fmt.Fprintln(stdio.Err, "desktop UI:", url)
			}
		},
	}

	if err := gui.Run(ctx, guiOpts); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stdio.Err, "error:", err)
		return 1
	}
	return 0
}

// nativeRun hands the engine's three channels to the Win32 window. With no
// browser in the loop there is no token, no HTTP server and no web asset load.
func nativeRun(ctx context.Context, runner gui.Runner, resolved app.Resolved, history gui.TurnHistory) int {
	err := nativegui.Run(nativegui.Options{
		Cwd:       resolved.Root,
		Model:     resolved.Model,
		Provider:  resolved.Cfg.ProviderName,
		Context:   ctx,
		History:   history,
		Events:    runner.Events(),
		Approvals: runner.Approvals(),
		Commands:  runner.Commands(),
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func providerName(resolved app.Resolved, flagValue string) string {
	if strings.TrimSpace(flagValue) != "" {
		return flagValue
	}
	if resolved.Cfg.ProviderName != "" {
		return resolved.Cfg.ProviderName
	}
	return resolved.Protocol
}

// guiRunner is one tab's worker. All agent state is per-tab; the HTTP bridge
// only sees the three channels.
type guiRunner struct {
	ctx       context.Context
	stdio     cli.IO
	resolved  app.Resolved
	store     *session.Store
	events    chan gui.Event
	approvals chan gui.Approval
	commands  chan gui.Command

	mu        sync.Mutex
	model     string
	level     string
	current   *session.Session
	steer     chan string
	cancelRun context.CancelFunc
}

func newGUIRunner(ctx context.Context, stdio cli.IO, resolved app.Resolved, store *session.Store) *guiRunner {
	r := &guiRunner{
		ctx:       ctx,
		stdio:     stdio,
		resolved:  resolved,
		store:     store,
		events:    make(chan gui.Event, 256),
		approvals: make(chan gui.Approval, 1),
		commands:  make(chan gui.Command, 16),
		model:     resolved.Model,
		level:     "medium",
		steer:     make(chan string, 16),
	}
	r.events <- gui.Event{Kind: gui.EventReady, Ready: &gui.Ready{
		Model:      r.model,
		Level:      r.level,
		ContextMax: 32000,
		Models:     []gui.ModelInfo{{ID: r.model, ContextLength: 32000}},
		Thinking:   []string{"auto", "off", "low", "medium", "high", "max"},
		Cwd:        resolved.Root,
		Provider:   providerName(resolved, ""),
		Protocol:   resolved.Protocol,
		Theme:      "dark",
	}}
	go r.loop()
	return r
}

func (r *guiRunner) Events() <-chan gui.Event       { return r.events }
func (r *guiRunner) Approvals() <-chan gui.Approval { return r.approvals }
func (r *guiRunner) Commands() chan<- gui.Command   { return r.commands }

func (r *guiRunner) loop() {
	defer close(r.events)
	for {
		select {
		case <-r.ctx.Done():
			return
		case command := <-r.commands:
			if command.Kind == gui.CommandShutdown {
				return
			}
			r.handle(command)
		}
	}
}

func (r *guiRunner) handle(command gui.Command) {
	switch command.Kind {
	case gui.CommandPrompt:
		r.mu.Lock()
		if r.cancelRun != nil {
			r.cancelRun()
		}
		ctx, cancel := context.WithCancel(r.ctx)
		r.cancelRun = cancel
		r.mu.Unlock()
		go func() {
			defer cancel()
			r.prompt(ctx, command.Text)
		}()
	case gui.CommandAbort:
		r.mu.Lock()
		cancel := r.cancelRun
		r.mu.Unlock()
		if cancel != nil {
			cancel()
			r.emit(gui.Event{Kind: gui.EventAgent, Agent: &gui.AgentEvent{Type: gui.AgentAborted}})
		}
	case gui.CommandSteer:
		select {
		case r.steer <- command.Text:
		default:
		}
	case gui.CommandSetModel:
		if command.Model != "" {
			r.mu.Lock()
			r.model = command.Model
			r.mu.Unlock()
		}
	case gui.CommandSetThinking:
		if command.Level != "" {
			r.mu.Lock()
			r.level = command.Level
			r.mu.Unlock()
		}
	case gui.CommandNewSession:
		r.mu.Lock()
		r.current = nil
		r.mu.Unlock()
		r.emit(gui.Event{Kind: gui.EventSessionCleared})
	case gui.CommandOpenSession:
		sess, err := session.OpenSession(r.store.Dir(), command.Path)
		if err != nil {
			r.fail(err)
			return
		}
		r.mu.Lock()
		r.current = sess
		r.mu.Unlock()
		r.emit(gui.Event{Kind: gui.EventSession, Path: filepath.Join(sess.Dir(), sess.ID()+".jsonl")})
	case gui.CommandRenameSession, gui.CommandDeleteSession, gui.CommandCompact, gui.CommandRewind, gui.CommandPause, gui.CommandResume:
		r.emit(gui.Event{Kind: gui.EventError, Message: fmt.Sprintf("%s is not available in the Go build", command.Kind)})
	}
}

func (r *guiRunner) prompt(ctx context.Context, text string) {
	sess, err := r.sessionFor(text)
	if err != nil {
		r.fail(err)
		return
	}

	registry := tools.NewWithLimit(r.resolved.Root, r.resolved.Mutating, 0)
	registry.SetSessionDir(filepath.Join(r.resolved.Storage, "sessions", sess.ID()))
	audit := security.NewAuditLog(filepath.Join(r.resolved.Storage, "audit"))
	approver := &guiApprover{approvals: r.approvals}
	chain := security.NewChain(security.DefaultPolicy(r.resolved.Root), approver, audit, sess.ID())
	registry.SetGuard(chain)

	r.mu.Lock()
	model := r.model
	r.mu.Unlock()

	loop := &agent.Runner{Opts: agent.Options{
		Model:    model,
		MaxTurns: r.resolved.MaxTurns,
		Root:     r.resolved.Root,
		System:   r.resolved.System,
		Chatter:  r.resolved.Chatter,
		Tools:    guardedToolRunner{registry},
		Approve:  approver,
		Record:   agent.RecorderFunc(sess.Append),
		Budget:   agent.Budget{MaxTurns: r.resolved.MaxTurns},
		Steer:    r.steer,
		Emit: func(event agent.Event) {
			r.emit(fromAgentEvent(event))
		},
	}}

	result, err := loop.Run(ctx, text)
	if err != nil && !errors.Is(err, context.Canceled) {
		r.fail(err)
	}
	r.emit(gui.Event{Kind: gui.EventTurnDone, TurnDone: &gui.TurnDone{
		Reason:           result.Stopped,
		Requests:         result.Turns,
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.PromptTokens + result.Usage.CompletionTokens,
	}})
	_ = sess.Append("turn", map[string]any{"turns": result.Turns, "stopped": result.Stopped})
}

func (r *guiRunner) sessionFor(first string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current, nil
	}
	title := strings.TrimSpace(strings.SplitN(first, "\n", 2)[0])
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60])
	}
	if title == "" {
		title = "session"
	}
	sess, err := r.store.NewSession(title)
	if err != nil {
		return nil, err
	}
	r.current = sess
	return sess, nil
}

// guardedToolRunner tells the agent loop that the security chain in
// tools.Registry already asked the approver. Without this marker one mutating
// call produces two GUI approval dialogs.
type guardedToolRunner struct{ *tools.Registry }

func (guardedToolRunner) Guarded() bool { return true }

type guiApprover struct {
	approvals chan<- gui.Approval
}

func (g *guiApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	reply := make(chan bool, 1)
	select {
	case g.approvals <- gui.Approval{Name: name, Arguments: args, Reply: reply}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case allowed := <-reply:
		return allowed, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func fromAgentEvent(event agent.Event) gui.Event {
	var e gui.AgentEvent
	switch event.Kind {
	case agent.EventTurnStart:
		e.Type, e.Index = gui.AgentTurnStart, event.Turn
	case agent.EventTurnEnd:
		e.Type, e.Index = gui.AgentTurnEnd, event.Turn
	case agent.EventTextDelta:
		e.Type, e.Text = gui.AgentText, event.Text
	case agent.EventReasoningDelta:
		e.Type, e.Text = gui.AgentThinking, event.Text
	case agent.EventToolStart:
		e.Type, e.ID, e.Name, e.Arguments = gui.AgentToolRequested, event.ToolCallID, event.ToolName, event.Args
	case agent.EventToolEnd:
		e.Type, e.ID, e.Name, e.Output, e.IsError, e.DurationMS = gui.AgentToolFinished, event.ToolCallID, event.ToolName, event.Output, event.IsError, event.DurationMS
	case agent.EventNotice:
		e.Type, e.Text = gui.AgentNotice, event.Reason
	default:
		return gui.Event{}
	}
	return gui.Event{Kind: gui.EventAgent, Agent: &e}
}

func (r *guiRunner) emit(event gui.Event) {
	if event.Kind == "" {
		return
	}
	select {
	case r.events <- event:
	case <-r.ctx.Done():
	}
}

func (r *guiRunner) fail(err error) {
	r.emit(gui.Event{Kind: gui.EventError, Message: err.Error()})
}

// guiSessions adapts the JSONL store to the bridge's read/search/import API.
type guiSessions struct {
	dir string
	cwd string
}

func (s *guiSessions) List() ([]gui.SessionRow, error) {
	metas, err := session.List(s.dir)
	if err != nil {
		return nil, err
	}
	rows := make([]gui.SessionRow, 0, len(metas))
	for _, meta := range metas {
		rows = append(rows, gui.SessionRow{
			Path:    filepath.Join(s.dir, meta.ID+".jsonl"),
			Title:   meta.Title,
			Updated: meta.Updated.UTC().Format(time.RFC3339),
			Cwd:     s.cwd,
		})
	}
	return rows, nil
}

func (s *guiSessions) Search(query string, limit int) ([]gui.SearchHit, error) {
	metas, err := session.Find(s.dir, query)
	if err != nil {
		return nil, err
	}
	hits := make([]gui.SearchHit, 0, limit)
	for _, meta := range metas {
		if limit > 0 && len(hits) >= limit {
			break
		}
		path := filepath.Join(s.dir, meta.ID+".jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
				continue
			}
			hits = append(hits, gui.SearchHit{
				Path: path, Title: meta.Title, Line: i + 1,
				Snippet: strings.TrimSpace(line),
			})
			if limit > 0 && len(hits) >= limit {
				break
			}
		}
	}
	return hits, nil
}

func (s *guiSessions) Import(text, title string) (string, error) {
	store, err := session.Open(s.dir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(title) == "" {
		title = "imported"
	}
	sess, err := store.NewSession(title)
	if err != nil {
		return "", err
	}
	if err := sess.Append("user", text); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, sess.ID()+".jsonl"), nil
}

var _ io.Writer = (io.Writer)(nil)
var _ = llm.Usage{}
