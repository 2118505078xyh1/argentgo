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

// 本文件是休眠中的 internal/lsp 客户端与 internal/tools 中六个消费侧 lsp_* 工具之间的桥梁。
// tools 从不导入 lsp（archguard 测试会强制检查），因此每种类型都以中性的 tools.Lsp* DTO 跨越该边界，
// 由对应的 lsp 类型逐字段映射而来。
//
// 激活策略（懒加载、按需）
// -----------------------
// 启动时或构建工具集时不会派生任何进程。接好适配器只是让这六个工具*可调用*；
// 语言服务进程会在某次 lsp_* 工具首次带路径被调用时才启动，且只针对该路径的语言（Registry.ForPath）。
// 单一管理器是进程级的（通过 sync.Once 创建一次），因此客户端会在多个运行和工具集之间复用。
// 缺失的服务会被缓存为负结果，而不是每次调用都重试，
// 使没有 gopls/pyright 的环境优雅降级为清晰的错误，且绝不会拖住运行。
//
// ARGENT_LSP=0 是显式的关闭开关：此时 NewLspIdeAdapter 返回 nil，
// 工具继续报告"LSP IDE integration is not available"。默认（未设置或任何其他值）会激活适配器。

const (
	lspCallTimeout  = 10 * time.Second // 限制单次 IDE 请求
	lspStartTimeout = 15 * time.Second // 限制派生进程的初始化握手
)

// startFn 是 lspManager 使用的进程派生器。它是包级变量，使测试可以替换为离线伪实现；
// 生产环境始终使用 lsp.Start。具体的返回类型保持公共接线不变，
// 同时允许在没有真实语言服务的情况下测试缓存行为。
var startFn func(context.Context, lsp.Server, string) (*lsp.Client, error) = lsp.Start

// lspManager 为整个进程拥有语言服务客户端。它刻意是进程级的（见上面的激活策略）：
// 客户端以 root + "|" + server.Name 为键，因此多个根目录和语言能在一个管理器中共存，
// 并在各次运行之间保留。
type lspManager struct {
	mu       sync.Mutex
	reg      *lsp.Registry
	clients  map[string]*lsp.Client   // 键：root + "|" + server.Name
	failures map[string]error         // 负缓存：不在每次调用时重试派生
	starting map[string]chan struct{} // 按键设置的屏障，使每个键最多派生一次
	ctx      context.Context
	cancel   context.CancelFunc
}

// managerOnce/sharedManager 使管理器成为进程级单例。选择单例（而非每个适配器一个管理器）
// 是为了让客户端能在多次运行之间、以及为多根工作区创建的多个注册表之间保留；
// 根目录由适配器携带，而非管理器。
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

// newLspManager 构建一个隔离的管理器。测试直接使用它，从而绝不会触碰进程单例。
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

// Close 关闭所有缓存的客户端并取消在途工作。进程单例刻意不会在每次运行时拆除；
// Close 用于显式关闭和测试。
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

// clientFor 返回某个服务/根目录的缓存客户端，首次使用时派生它。
// 按键设置的屏障保证即使多个 lsp_* 工具并发触发，每个键也恰好只派生一次，且不会阻塞其他键。
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
			continue // 在途派生结束后重新检查缓存
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

// spawn 限制握手时间，使故障服务不会挂住管理器。失败会被包装为可操作的消息返回，
// 供 clientFor 做负缓存。
func (m *lspManager) spawn(server lsp.Server, root string) (*lsp.Client, error) {
	ctx, cancel := context.WithTimeout(m.ctx, lspStartTimeout)
	defer cancel()
	client, err := startFn(ctx, server, root)
	if err != nil {
		return nil, fmt.Errorf("LSP server %s not available (not installed?): %w", server.Name, err)
	}
	return client, nil
}

// lspIdeAdapter 用进程级管理器和用于解析相对路径的工作区根目录，实现 tools.LspIde。
type lspIdeAdapter struct {
	mgr  *lspManager
	root string
}

var _ tools.LspIde = (*lspIdeAdapter)(nil)

// NewLspIdeAdapter 返回 app 通过 SetLspIde 安装的适配器。ARGENT_LSP=0 时返回 nil，
// 使工具保持在"不可用"状态。这里把根目录变为绝对路径，
// 因为 lsp 客户端会相对于进程工作目录解析文档路径。
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

// clientForPath 把路径的语言解析为服务，并返回（或懒启动）该适配器根目录对应的客户端。
func (a *lspIdeAdapter) clientForPath(path string) (*lsp.Client, error) {
	server, ok := a.mgr.reg.ForPath(path)
	if !ok {
		return nil, fmt.Errorf("no LSP server configured for %q (unsupported extension %q)", path, filepath.Ext(path))
	}
	return a.mgr.clientFor(server, a.root)
}

// resolvePath 把工作区相对路径转换为绝对路径。lsp 客户端的 Open 读取文件并从绝对路径推导其 URI。
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

// WorkspaceSymbols 没有路径参数，因此它复用已为该根目录派生的任意客户端；若没有则尝试一次 gopls。
// 未安装服务时，它优雅降级为空结果且无错误，而不是让工具调用失败。
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

// anyClient 返回一个确定的、已在运行的客户端，或按需为该适配器根目录尝试启动 gopls。
// 启动失败会被管理器缓存，并向调用方报告为"无客户端"。
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

// ---- 纯类型映射（lsp -> tools 中立 DTO）----

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

// mapWorkspaceEdit 同时保留按文件的变更映射和有序的 documentChanges。
// lsp.WorkspaceEdit 已经把 documentChanges 合并进 Changes，
// 因此如实映射这两个字段能保持该合并视图不变。
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
