package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DidOpen announces a document with its current text.
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

// DidChange replaces a document's content and bumps its version. An unknown
// document is opened first, because servers reject changes for closed files.
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

// DidSave reports that the document was written to disk.
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

// DidClose tells the server the document is no longer tracked.
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

// Version reports the version last sent for a document, or 0 if unknown.
func (c *Client) Version(uri string) int {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if state := c.docs[uri]; state != nil {
		return state.version
	}
	return 0
}

// LanguageID reports the language id a document was opened with.
func (c *Client) LanguageID(uri string) string {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if state := c.docs[uri]; state != nil {
		return state.languageID
	}
	return ""
}

// Open reads a file, sends didOpen (or didChange when already open) and
// returns its URI.
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

// Refresh re-reads a file from disk and sends didChange.
func (c *Client) Refresh(ctx context.Context, path string) (string, error) {
	return c.Open(ctx, path)
}

// publishParams is the textDocument/publishDiagnostics payload.
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

// storeDiagnostics replaces the diagnosis for one URI and wakes its waiters.
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
	c.seq++
	c.published[payload.URI] = publication{seq: c.seq, items: items}
	for key, waiter := range c.waiters {
		if waiter.uri == payload.URI {
			close(waiter.ch)
			delete(c.waiters, key)
		}
	}
	c.diagMu.Unlock()
}

// Diagnostics returns the diagnostics for uri.
//
// It returns immediately when the server has already published newer
// diagnostics than the caller's last edit. Otherwise it waits up to wait for
// the next publish, and honours ctx. A timeout is reported as ErrTimeout;
// context cancellation as the context error. In both cases any diagnostics
// that did arrive are returned alongside the error.
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
	// A publish may have landed between the first check and registration.
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

// DiagnosticsAll returns every diagnostic the server currently reports.
func (c *Client) DiagnosticsAll() []Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	var out []Diagnostic
	for _, entry := range c.published {
		out = append(out, entry.items...)
	}
	return out
}

// documentMark is the publish sequence at the caller's last edit of uri.
func (c *Client) documentMark(uri string) uint64 {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if state := c.docs[uri]; state != nil {
		return state.mark
	}
	return 0
}

// publishedAfter returns the diagnosis when it is newer than baseline.
func (c *Client) publishedAfter(uri string, baseline uint64) ([]Diagnostic, bool) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	entry, ok := c.published[uri]
	if !ok || entry.seq <= baseline {
		return nil, false
	}
	return append([]Diagnostic(nil), entry.items...), true
}

// current returns the cached diagnosis for uri, if one was published.
func (c *Client) current(uri string) ([]Diagnostic, bool) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	entry, ok := c.published[uri]
	if !ok {
		return nil, false
	}
	return append([]Diagnostic(nil), entry.items...), true
}

// addWaiter registers interest in the next publish for uri.
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
