package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DidOpen 宣布一份文档及其当前文本。
func (c *Client) DidOpen(ctx context.Context, uri, languageID, text string) error {
	if languageID == "" {
		languageID = LanguageForPath(URIToPath(uri))
	}
	c.diagMu.Lock()
	state := c.docs[uri]
	if state == nil {
		state = &documentState{}
		c.docs[uri] = state
	}
	state.version = 1
	state.open = true
	state.languageID = languageID
	state.mark = c.seq
	c.diagMu.Unlock()

	return c.notify(ctx, "textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// DidChange 替换文档内容并增加其版本。未知文档会先被打开，因为服务器拒绝关闭文件的变更。
// 会先被打开，因为服务器拒绝关闭文件的变更。
func (c *Client) DidChange(ctx context.Context, uri, text string) error {
	c.diagMu.Lock()
	state := c.docs[uri]
	if state == nil || !state.open {
		c.diagMu.Unlock()
		return c.DidOpen(ctx, uri, LanguageForPath(URIToPath(uri)), text)
	}
	state.version++
	version := state.version
	state.mark = c.seq
	c.diagMu.Unlock()

	return c.notify(ctx, "textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": text}},
	})
}

// DidSave 报告文档已写入磁盘。
func (c *Client) DidSave(ctx context.Context, uri string) error {
	c.diagMu.Lock()
	if state := c.docs[uri]; state != nil {
		state.mark = c.seq
	}
	c.diagMu.Unlock()
	return c.notify(ctx, "textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// DidClose 告诉服务器不再跟踪该文档。
func (c *Client) DidClose(ctx context.Context, uri string) error {
	c.diagMu.Lock()
	if state := c.docs[uri]; state != nil {
		state.open = false
	}
	c.diagMu.Unlock()
	return c.notify(ctx, "textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// Version 报告某文档最后发送的版本，未知时为 0。
func (c *Client) Version(uri string) int {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if state := c.docs[uri]; state != nil {
		return state.version
	}
	return 0
}

// LanguageID 报告某文档打开时使用的语言 id。
func (c *Client) LanguageID(uri string) string {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if state := c.docs[uri]; state != nil {
		return state.languageID
	}
	return ""
}

// Open 读取文件，发送 didOpen（已打开时为 didChange），并返回其 URI。
// 并返回其 URI。
func (c *Client) Open(ctx context.Context, path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("lsp: resolve %s: %w", path, err)
	}
	text, err := os.ReadFile(absolute)
	if err != nil {
		return "", fmt.Errorf("lsp: read %s: %w", absolute, err)
	}
	uri := PathToURI(absolute)
	if c.Version(uri) > 0 {
		if err := c.DidChange(ctx, uri, string(text)); err != nil {
			return "", err
		}
		return uri, nil
	}
	if err := c.DidOpen(ctx, uri, LanguageForPath(absolute), string(text)); err != nil {
		return "", err
	}
	return uri, nil
}

// Refresh 从磁盘重新读取文件并发送 didChange。
func (c *Client) Refresh(ctx context.Context, path string) (string, error) {
	return c.Open(ctx, path)
}

// publishParams 是 textDocument/publishDiagnostics 的 payload。
type publishParams struct {
	URI         string          `json:"uri"`
	Version     *int            `json:"version,omitempty"`
	Diagnostics []rawDiagnostic `json:"diagnostics"`
}

type rawDiagnostic struct {
	Range struct {
		Start Position `json:"start"`
		End   Position `json:"end"`
	} `json:"range"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
	Source   string `json:"source"`
}

// storeDiagnostics 替换某个 URI 的诊断并唤醒其等待者。
func (c *Client) storeDiagnostics(params []byte) {
	var payload publishParams
	if err := json.Unmarshal(params, &payload); err != nil || payload.URI == "" {
		return
	}
	items := make([]Diagnostic, 0, len(payload.Diagnostics))
	for _, entry := range payload.Diagnostics {
		items = append(items, Diagnostic{
			URI: payload.URI,
			Range: Range{
				Start: entry.Range.Start,
				End:   entry.Range.End,
			},
			Severity: severityName(entry.Severity),
			Message:  entry.Message,
			Source:   entry.Source,
		})
	}

	c.diagMu.Lock()
	defer c.diagMu.Unlock()

	// LSP 允许服务器在较新版本之后发布较旧文档版本的
	// 诊断（延迟或乱序发布）。这样的结果是
	// 陈旧的：接受它会把 seq 推过调用方的编辑标记，使
	// LSP 允许服务器在较新版本之后发布较旧文档版本的诊断（延迟或乱序发布）。这样的结果是陈旧的：接受它会把 seq 推过调用方的编辑标记，使 Diagnostics 把旧诊断当当前诊断返回。应丢弃它
	// 而不触碰 seq、缓存或任何等待者。省略版本的服务器不受影响：它们的发布保持原有行为。
	// 省略版本的服务器不受影响：它们的发布保持原有行为。
	if payload.Version != nil {
		if state := c.docs[payload.URI]; state != nil && *payload.Version < state.version {
			return
		}
	}

	c.seq++
	c.published[payload.URI] = publication{seq: c.seq, items: items}
	for key, waiter := range c.waiters {
		if waiter.uri == payload.URI {
			close(waiter.ch)
			delete(c.waiters, key)
		}
	}
}

// Diagnostics 返回 uri 的诊断。
//
// 当服务器已经发布了比调用方上次编辑更新的
// 诊断时立即返回。否则最多等待 wait 时间等待
// 当服务器已经发布了比调用方上次编辑更新的诊断时立即返回。否则最多等待 wait 时间等待下一次发布，并遵守 ctx。超时报告为 ErrTimeout；context 取消作为 context 错误。两种情况下任何
// context 取消作为 context 错误。两种情况下任何诊断
// 已到达的诊断都随错误一起返回。
func (c *Client) Diagnostics(ctx context.Context, uri string, wait time.Duration) ([]Diagnostic, error) {
	baseline := c.documentMark(uri)
	if items, ok := c.publishedAfter(uri, baseline); ok {
		return items, nil
	}
	if wait <= 0 {
		items, _ := c.current(uri)
		return items, nil
	}

	ch, cancel := c.addWaiter(uri)
	defer cancel()
	// 发布可能在第一次检查与注册之间落地。
	if items, ok := c.publishedAfter(uri, baseline); ok {
		return items, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ch:
		items, _ := c.current(uri)
		return items, nil
	case <-timer.C:
		items, _ := c.current(uri)
		return items, fmt.Errorf("%w for %s after %s", ErrTimeout, uri, wait)
	case <-ctx.Done():
		items, _ := c.current(uri)
		return items, ctx.Err()
	case <-c.doneCh:
		items, _ := c.current(uri)
		return items, c.closedError("diagnostics")
	}
}

// DiagnosticsAll 返回服务器当前报告的所有诊断。
func (c *Client) DiagnosticsAll() []Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	var out []Diagnostic
	for _, entry := range c.published {
		out = append(out, entry.items...)
	}
	return out
}

// documentMark 是调用方上次编辑 uri 时的发布序号。
func (c *Client) documentMark(uri string) uint64 {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if state := c.docs[uri]; state != nil {
		return state.mark
	}
	return 0
}

// publishedAfter 在诊断比基线新时返回该诊断。
func (c *Client) publishedAfter(uri string, baseline uint64) ([]Diagnostic, bool) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	entry, ok := c.published[uri]
	if !ok || entry.seq <= baseline {
		return nil, false
	}
	return append([]Diagnostic(nil), entry.items...), true
}

// current 返回 uri 的缓存诊断（若已发布）。
func (c *Client) current(uri string) ([]Diagnostic, bool) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	entry, ok := c.published[uri]
	if !ok {
		return nil, false
	}
	return append([]Diagnostic(nil), entry.items...), true
}

// addWaiter 注册对 uri 下一次发布的关注。
func (c *Client) addWaiter(uri string) (chan struct{}, func()) {
	ch := make(chan struct{})
	c.diagMu.Lock()
	c.nextWaiter++
	key := c.nextWaiter
	c.waiters[key] = diagWaiter{uri: uri, ch: ch}
	c.diagMu.Unlock()
	return ch, func() {
		c.diagMu.Lock()
		delete(c.waiters, key)
		c.diagMu.Unlock()
	}
}
