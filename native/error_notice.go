package main

import (
	"regexp"
	"strings"
)

// Engine errors need their own presentation policy in the native window.
//
// The TUI bounds, collapses and redacts stream errors before putting them in
// the transcript (internal/tui.StreamErrorEntry). Native used to paste
// err.Error() straight into the Markdown view: a provider stack trace could be
// arbitrary long, multi-line and carry credentials, which both wrecks the
// single-bubble layout and pins an unbounded string in the transcript until
// the session ends. This file keeps the same policy locally so native stays
// dependency-free of the engine packages.

const (
	engineErrorPrefix    = "⚠ 引擎错误: "
	maxEngineErrorDetail = 200 // runes, including the truncation ellipsis
)

var (
	// Credential fields and well-known key prefixes in provider errors. The
	// trailing [:=] requirement on the field rule keeps ordinary prose such as
	// "token 已过期" from being rewritten.
	engineCredentialFieldRE = regexp.MustCompile(`(?i)(api[\s_\-]*key|access[\s_\-]*token|refresh[\s_\-]*token|authorization|bearer|password|passwd|secret|token)(["']?\s*[:=]\s*["']?)([^\s"',;}]+)`)
	engineKnownSecretRE     = regexp.MustCompile(`(?i)\b(?:sk|rk|pk|xoxb|ghp|gho|github_pat)[-_][A-Za-z0-9_\-]{12,}\b`)
)

// collapseEngineErrorSpace folds every whitespace run (including newlines and
// tabs from a panic dump) into one space, so one error always renders as one
// line and cannot introduce fake Markdown blocks.
func collapseEngineErrorSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// redactEngineError masks credential-shaped values in an error detail. It
// intentionally mirrors the TUI's policy without importing internal/llm.
func redactEngineError(s string) string {
	s = engineCredentialFieldRE.ReplaceAllString(s, "${1}${2}[redacted]")
	return engineKnownSecretRE.ReplaceAllString(s, "[redacted]")
}

// clipErrorRunes bounds s to at most max runes, marking a cut with "…". It
// works on runes, so a CJK character or emoji is never split in half.
func clipErrorRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// engineErrorNotice formats an engine error for the transcript: one bounded,
// single-line, redacted notice. A nil or empty error still yields a visible
// banner so a failed turn never looks like a silently empty reply.
func engineErrorNotice(err error) string {
	if err == nil {
		return strings.TrimSpace(engineErrorPrefix)
	}
	detail := clipErrorRunes(redactEngineError(collapseEngineErrorSpace(err.Error())), maxEngineErrorDetail)
	if detail == "" {
		return strings.TrimSpace(engineErrorPrefix)
	}
	return engineErrorPrefix + detail
}

// finishEngineError is the single integration point for a failed engine
// prompt. main.go's completeFromPrompt can replace its inline
//
//	app.finish("⚠ 引擎错误: " + err.Error())
//	app.status = "引擎错误"
//
// with app.finishEngineError(err). Cancellation is handled by the caller (see
// isCancelError) and must not reach here. finish itself is defined in main.go;
// keeping the call here means no engine or bridge code needs to change.
func (app *chat) finishEngineError(err error) {
	app.finish(engineErrorNotice(err))
	app.status = "引擎错误"
}
