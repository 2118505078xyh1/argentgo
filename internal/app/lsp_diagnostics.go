package app

// Diagnostics bridge. internal/lsp already exposes a version-aware, wait-capable
// fetch (Client.Diagnostics) that drops stale publishes. This file surfaces it
// as a neutral app-side DTO plus a pure formatter so callers can attach a
// compact "errors only" block to a tool result without importing internal/lsp
// or changing any tools signature.
//
// GRACEFUL DEGRADATION: a missing/unsupported language server, a slow server or
// any transport error yields an empty result and no error. Diagnostics are
// advisory; they must never fail an otherwise successful operation or change
// default behaviour.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
)

const (
	// diagnosticsWait bounds how long one Diagnostics call waits for a fresh
	// publish after refreshing the document. It is advisory: a slow server
	// must not stall the run, so a timeout degrades to an empty result.
	diagnosticsWait = 2 * time.Second

	// maxReportDiagnostics is the per-file cap applied by FormatDiagnostics.
	maxReportDiagnostics = 20

	// diagnosticsTruncFmt is appended when the per-file cap dropped entries.
	diagnosticsTruncFmt = "... (%d more error(s) truncated)"
)

// NeutralDiagnostic is a provider-neutral problem report for app-side
// consumers. Unlike the LSP wire format, Line and Column are 1-based so they
// can be pasted straight into a file:line:column reference.
type NeutralDiagnostic struct {
	Line     int
	Column   int
	Severity string // "error", "warning", "information" or "hint"
	Message  string
	Source   string
}

// fetchDiagnostics is the seam tests replace to avoid a real language server.
// Production refreshes the document (didOpen/didChange) and then waits for a
// fresh publish; the lsp client's version-freshness filtering ensures older
// publishes are never returned as current.
var fetchDiagnostics = func(ctx context.Context, client *lsp.Client, absPath string) ([]lsp.Diagnostic, error) {
	uri, err := client.Open(ctx, absPath)
	if err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, lspCallTimeout)
	defer cancel()
	return client.Diagnostics(waitCtx, uri, diagnosticsWait)
}

// Diagnostics refreshes filePath with its language server and returns the
// diagnostics it publishes for it, with 1-based line/column numbers.
//
// It degrades gracefully to an empty slice with no error when no language
// server is configured or installed, the extension is unsupported, the
// document cannot be opened, or the wait times out. Diagnostics are advisory
// and must never change the outcome of the operation that triggered them.
func (a *lspIdeAdapter) Diagnostics(ctx context.Context, filePath string) ([]NeutralDiagnostic, error) {
	client, err := a.clientForPath(filePath)
	if err != nil {
		return []NeutralDiagnostic{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	items, err := fetchDiagnostics(ctx, client, a.resolvePath(filePath))
	if err != nil {
		// Timeout, cancellation and transport errors all degrade to empty. In
		// particular a timeout may only have stale cached diagnostics, which
		// are worse than none for a model deciding what to fix.
		return []NeutralDiagnostic{}, nil
	}
	return mapNeutralDiagnostics(items), nil
}

// mapNeutralDiagnostics converts lsp diagnostics (0-based positions) into
// neutral DTOs with 1-based positions. It always returns a non-nil slice.
func mapNeutralDiagnostics(items []lsp.Diagnostic) []NeutralDiagnostic {
	out := make([]NeutralDiagnostic, 0, len(items))
	for _, d := range items {
		out = append(out, NeutralDiagnostic{
			Line:     d.Range.Start.Line + 1,
			Column:   d.Range.Start.Character + 1,
			Severity: d.Severity,
			Message:  d.Message,
			Source:   d.Source,
		})
	}
	return out
}

// FormatDiagnostics renders only error-severity diagnostics as compact
// "path:line:column [source]: message" lines, sorted by position, capped at
// max per file with a truncation marker when entries were dropped. Line and
// column are already 1-based in NeutralDiagnostic. A non-positive max uses
// maxReportDiagnostics; an empty string is returned when there are no errors.
//
// This is a pure function: the input slice is never mutated and no I/O is
// performed, so it is safe to call on any tool result.
func FormatDiagnostics(path string, diags []NeutralDiagnostic, max int) string {
	if max <= 0 {
		max = maxReportDiagnostics
	}
	errs := make([]NeutralDiagnostic, 0, len(diags))
	for _, d := range diags {
		if strings.EqualFold(strings.TrimSpace(d.Severity), "error") {
			errs = append(errs, d)
		}
	}
	if len(errs) == 0 {
		return ""
	}
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Line != errs[j].Line {
			return errs[i].Line < errs[j].Line
		}
		return errs[i].Column < errs[j].Column
	})

	shown := len(errs)
	if shown > max {
		shown = max
	}
	var b strings.Builder
	for _, d := range errs[:shown] {
		b.WriteString(path)
		if path != "" {
			b.WriteByte(':')
		}
		b.WriteString(strconv.Itoa(d.Line))
		if d.Column > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(d.Column))
		}
		if d.Source != "" {
			b.WriteString(" [")
			b.WriteString(d.Source)
			b.WriteByte(']')
		}
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(d.Message))
		b.WriteByte('\n')
	}
	if dropped := len(errs) - shown; dropped > 0 {
		fmt.Fprintf(&b, diagnosticsTruncFmt, dropped)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}
