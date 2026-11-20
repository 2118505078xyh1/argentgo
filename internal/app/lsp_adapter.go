package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// This file is the bridge between the dormant internal/lsp client and the six
// consumer-side lsp_* tools in internal/tools. tools never imports lsp (the
// archguard test enforces that), so every type crosses this boundary as a
// neutral tools.Lsp* DTO mapped field-by-field from the corresponding lsp type.
//
// ACTIVATION POLICY (lazy, on-demand)
// ----------------------------------
// Nothing is spawned at startup or when a toolset is built. Wiring the adapter
// only makes the six tools *callable*; a language server process is launched the
// first time an lsp_* tool is invoked with a path, and only for that path's
// language (Registry.ForPath). The single manager is process-scoped (created
// once via sync.Once) so the client is reused across runs and toolsets. A
// missing server is cached as a negative result instead of retrying on every
// call, so an environment without gopls/pyright degrades to a clear error and
// never stalls the run.
//
// ARGENT_LSP=0 is an explicit kill switch: NewLspIdeAdapter then returns nil
// and the tools keep reporting "LSP IDE integration is not available". The
// default (unset or any other value) activates the adapter.

const (
	lspCallTimeout  = 10 * time.Second // bounds one IDE request
	lspStartTimeout = 15 * time.Second // bounds the initialize handshake of a spawn
)

// startFn is the process spawner used by lspManager. It is a package variable
// so tests can substitute an offline fake; production always uses lsp.Start.
// The concrete return type keeps the public wiring unchanged while allowing
// cache behaviour to be tested without a real language server.
var startFn func(context.Context, lsp.Server, string) (*lsp.Client, error) = lsp.Start

// lspManager owns the language-server clients for the whole process. It is
// deliberately process-scoped (see activation policy above): clients are keyed
// by root + "|" + server.Name, so several roots and languages coexist in one
// manager and survive individual runs.
type lspManager struct {
	mu       sync.Mutex
	reg      *lsp.Registry
	clients  map[string]*lsp.Client   // key: root + "|" + server.Name
	failures map[string]error         // negative cache: no retry-spawn on every call
	starting map[string]chan struct{} // per-key barrier so one key spawns at most once
	ctx      context.Context
	cancel   context.CancelFunc
}

// managerOnce/sharedManager make the manager a process-level singleton. A
// singleton (rather than one manager per adapter) is chosen so clients survive
// across runs and across the multiple registries created for multi-root
// workspaces; the root is carried by the adapter, not the manager.
var (
	managerOnce   sync.Once
	sharedManager *lspManager
)

func getSharedManager() *lspManager {
	managerOnce.Do(func() {
		sharedManager = newLspManager(context.Background())
	})
	return sharedManager
}

// newLspManager builds an isolated manager. Tests use it directly so they never
// touch the process singleton.
func newLspManager(ctx context.Context) *lspManager {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	return &lspManager{
		reg:      lsp.NewRegistry(),
		clients:  make(map[string]*lsp.Client),
		failures: make(map[string]error),
		starting: make(map[string]chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// close shuts down every cached client and cancels in-flight work. The process
// singleton is intentionally not torn down per run; Close is for explicit
// shutdown and for tests.
func (m *lspManager) Close() error {
	m.mu.Lock()
	clients := m.clients
	m.clients = make(map[string]*lsp.Client)
	m.mu.Unlock()

	var first error
	for _, client := range clients {
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	m.cancel()
	return first
}

// clientFor returns the cached client for a server/root, spawning it on first
// use. The per-key barrier guarantees exactly one spawn per key even when
// several lsp_* tools fire concurrently, without blocking other keys.
func (m *lspManager) clientFor(server lsp.Server, root string) (*lsp.Client, error) {
	key := root + "|" + server.Name
	for {
		m.mu.Lock()
		if client := m.clients[key]; client != nil {
			m.mu.Unlock()
			return client, nil
		}
		if err := m.failures[key]; err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if wait, ok := m.starting[key]; ok {
			m.mu.Unlock()
			<-wait
			continue // re-check the caches after the in-flight spawn settles
		}
		wait := make(chan struct{})
		m.starting[key] = wait
		m.mu.Unlock()

		client, err := m.spawn(server, root)

		m.mu.Lock()
		if err != nil {
			m.failures[key] = err
		} else {
			m.clients[key] = client
		}
		delete(m.starting, key)
		close(wait)
		m.mu.Unlock()
		return client, err
	}
}

// spawn bounds the handshake so a broken server cannot hang the manager. A
// failure is wrapped into an actionable message and returned for negative
// caching by clientFor.
func (m *lspManager) spawn(server lsp.Server, root string) (*lsp.Client, error) {
	ctx, cancel := context.WithTimeout(m.ctx, lspStartTimeout)
	defer cancel()
	client, err := startFn(ctx, server, root)
	if err != nil {
		return nil, fmt.Errorf("LSP server %s not available (not installed?): %w", server.Name, err)
	}
	return client, nil
}

// lspIdeAdapter implements tools.LspIde with a process-level manager and the
// workspace root used to resolve relative paths.
type lspIdeAdapter struct {
	mgr  *lspManager
	root string
}

var _ tools.LspIde = (*lspIdeAdapter)(nil)

// NewLspIdeAdapter returns the adapter the app installs with SetLspIde. It
// returns nil when ARGENT_LSP=0, leaving the tools in their "not available"
// state. The root is made absolute here because the lsp client resolves
// document paths against the process working directory.
func NewLspIdeAdapter(root string) tools.LspIde {
	if os.Getenv("ARGENT_LSP") == "0" {
		return nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil || absolute == "" {
		absolute = root
	}
	return &lspIdeAdapter{mgr: getSharedManager(), root: absolute}
}

// clientForPath resolves a path's language to a server and returns (or lazily
// starts) the client for the adapter's root.
func (a *lspIdeAdapter) clientForPath(path string) (*lsp.Client, error) {
	server, ok := a.mgr.reg.ForPath(path)
	if !ok {
		return nil, fmt.Errorf("no LSP server configured for %q (unsupported extension %q)", path, filepath.Ext(path))
	}
	return a.mgr.clientFor(server, a.root)
}

// resolvePath turns a workspace-relative path into an absolute one. The lsp
// client's Open reads the file and derives its URI from the absolute path.
func (a *lspIdeAdapter) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(a.root, path)
}

func (a *lspIdeAdapter) requestCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.mgr.ctx, lspCallTimeout)
}

func (a *lspIdeAdapter) DocumentSymbol(path string) ([]tools.LspSymbol, error) {
	client, err := a.clientForPath(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.requestCtx()
	defer cancel()
	items, err := client.DocumentSymbolAt(ctx, a.resolvePath(path))
	if err != nil {
		return nil, err
	}
	out := make([]tools.LspSymbol, 0, len(items))
	for _, item := range items {
		out = append(out, mapSymbol(item))
	}
	return out, nil
}

func (a *lspIdeAdapter) References(path string, line, character int, includeDecl bool) ([]tools.LspLocation, error) {
	client, err := a.clientForPath(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.requestCtx()
	defer cancel()
	position := lsp.Position{Line: line, Character: character}
	items, err := client.ReferencesAt(ctx, a.resolvePath(path), position, includeDecl)
	if err != nil {
		return nil, err
	}
	out := make([]tools.LspLocation, 0, len(items))
	for _, item := range items {
		out = append(out, mapLocation(item))
	}
	return out, nil
}

func (a *lspIdeAdapter) CodeActions(path string, startLine, startChar, endLine, endChar int) ([]tools.LspCodeAction, error) {
	client, err := a.clientForPath(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.requestCtx()
	defer cancel()
	rng := lsp.Range{
		Start: lsp.Position{Line: startLine, Character: startChar},
		End:   lsp.Position{Line: endLine, Character: endChar},
	}
	items, err := client.CodeActionsAt(ctx, a.resolvePath(path), rng, nil)
	if err != nil {
		return nil, err
	}
	out := make([]tools.LspCodeAction, 0, len(items))
	for _, item := range items {
		out = append(out, mapCodeAction(item))
	}
	return out, nil
}

func (a *lspIdeAdapter) Rename(path string, line, char int, newName string) (*tools.LspWorkspaceEdit, error) {
	client, err := a.clientForPath(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.requestCtx()
	defer cancel()
	position := lsp.Position{Line: line, Character: char}
	edit, err := client.RenameAt(ctx, a.resolvePath(path), position, newName)
	if err != nil {
		return nil, err
	}
	return mapWorkspaceEdit(edit), nil
}

func (a *lspIdeAdapter) Formatting(path string, tabSize int, insertSpaces bool) ([]tools.LspTextEdit, error) {
	client, err := a.clientForPath(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.requestCtx()
	defer cancel()
	options := lsp.FormattingOptions{TabSize: tabSize, InsertSpaces: insertSpaces}
	items, err := client.FormattingAt(ctx, a.resolvePath(path), options)
	if err != nil {
		return nil, err
	}
	return mapTextEdits(items), nil
}

// WorkspaceSymbols has no path, so it reuses any client already spawned for
// this root; if none exists it tries gopls once. When no server is installed it
// degrades to an empty result with no error rather than failing the tool call.
func (a *lspIdeAdapter) WorkspaceSymbols(query string) ([]tools.LspSymbolInfo, error) {
	client := a.anyClient()
	if client == nil {
		return []tools.LspSymbolInfo{}, nil
	}
	ctx, cancel := a.requestCtx()
	defer cancel()
	items, err := client.WorkspaceSymbols(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]tools.LspSymbolInfo, 0, len(items))
	for _, item := range items {
		out = append(out, mapSymbolInfo(item))
	}
	return out, nil
}

// anyClient returns a deterministic already-running client, or attempts to
// start gopls for the adapter root on demand. A start failure is cached by the
// manager and reported as "no client" to the caller.
func (a *lspIdeAdapter) anyClient() *lsp.Client {
	m := a.mgr
	m.mu.Lock()
	var bestKey string
	var client *lsp.Client
	for key, candidate := range m.clients {
		if client == nil || key < bestKey {
			bestKey, client = key, candidate
		}
	}
	m.mu.Unlock()
	if client != nil {
		return client
	}
	if server, ok := m.reg.Lookup("go"); ok {
		c, err := m.clientFor(server, a.root)
		if err == nil {
			return c
		}
	}
	return nil
}

// ---- Pure type mappings (lsp -> tools neutral DTOs) ----

func mapPosition(p lsp.Position) tools.LspPos {
	return tools.LspPos{Line: p.Line, Character: p.Character}
}

func mapRange(r lsp.Range) tools.LspRange {
	return tools.LspRange{Start: mapPosition(r.Start), End: mapPosition(r.End)}
}

func mapSymbol(s lsp.Symbol) tools.LspSymbol {
	return tools.LspSymbol{
		Name:           s.Name,
		Kind:           s.Kind,
		Range:          mapRange(s.Range),
		SelectionRange: mapRange(s.SelectionRange),
		URI:            s.URI,
		Detail:         s.Detail,
		Depth:          s.Depth,
	}
}

func mapLocation(loc lsp.Location) tools.LspLocation {
	return tools.LspLocation{URI: loc.URI, Range: mapRange(loc.Range)}
}

func mapTextEdit(edit lsp.TextEdit) tools.LspTextEdit {
	return tools.LspTextEdit{Range: mapRange(edit.Range), NewText: edit.NewText}
}

func mapTextEdits(edits []lsp.TextEdit) []tools.LspTextEdit {
	out := make([]tools.LspTextEdit, 0, len(edits))
	for _, edit := range edits {
		out = append(out, mapTextEdit(edit))
	}
	return out
}

// mapWorkspaceEdit preserves both the per-file changes map and the ordered
// documentChanges. lsp.WorkspaceEdit already merges documentChanges into
// Changes, so mapping both fields faithfully keeps that merged view intact.
func mapWorkspaceEdit(edit *lsp.WorkspaceEdit) *tools.LspWorkspaceEdit {
	if edit == nil {
		return nil
	}
	out := &tools.LspWorkspaceEdit{
		Changes: make(map[string][]tools.LspTextEdit, len(edit.Changes)),
	}
	for uri, edits := range edit.Changes {
		out.Changes[uri] = mapTextEdits(edits)
	}
	for _, doc := range edit.DocumentChanges {
		out.DocumentChanges = append(out.DocumentChanges, tools.LspDocumentEdit{
			URI:   doc.URI,
			Edits: mapTextEdits(doc.Edits),
		})
	}
	return out
}

func mapDiagnostic(d lsp.Diagnostic) tools.LspDiagnostic {
	return tools.LspDiagnostic{
		Range:    mapRange(d.Range),
		Severity: d.Severity,
		Message:  d.Message,
		Source:   d.Source,
	}
}

func mapCommand(cmd *lsp.Command) *tools.LspCommand {
	if cmd == nil {
		return nil
	}
	return &tools.LspCommand{
		Title:     cmd.Title,
		Command:   cmd.Command,
		Arguments: cmd.Arguments,
	}
}

func mapCodeAction(action lsp.CodeAction) tools.LspCodeAction {
	out := tools.LspCodeAction{
		Title:       action.Title,
		Kind:        action.Kind,
		Edit:        mapWorkspaceEdit(action.Edit),
		Command:     mapCommand(action.Command),
		IsPreferred: action.IsPreferred,
	}
	for _, d := range action.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, mapDiagnostic(d))
	}
	if action.Disabled != nil {
		out.Disabled = &tools.LspCodeActionDisabled{Reason: action.Disabled.Reason}
	}
	return out
}

func mapSymbolInfo(info lsp.SymbolInformation) tools.LspSymbolInfo {
	return tools.LspSymbolInfo{
		Name:          info.Name,
		Kind:          info.Kind,
		Location:      mapLocation(info.Location),
		ContainerName: info.ContainerName,
	}
}
