package commands

// Built-in /context: a read-only explainability view of the model context.
//
// Commands in this package are data (Command descriptors plus hook structs),
// not a Go interface, so the core logic lives in the pure builder
// BuildContextReport. A built-in hook (ContextHook) and a markdown command can
// both call it without this package importing internal/app or internal/agent.
// All inputs are injected as plain slices/strings, keeping the function
// deterministic, stdlib-only and easy to test.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Bounds keep a pathological input from producing an unbounded report (the
// report itself can end up in the context it describes).
const (
	maxContextItems     = 50
	maxContextNameBytes = 120
	maxContextPathBytes = 240
)

// MemoryDoc is one instruction file currently injected into the system prompt.
// Bytes is the length of the injected text; Reason explains a truncation.
type MemoryDoc struct {
	Path      string
	Bytes     int
	Truncated bool
	Reason    string
}

// IgnoreStatus is the outcome of hierarchical .argentignore discovery.
type IgnoreStatus struct {
	Files []string
	Rules int
}

// ContextInput is everything needed to render a /context report. It contains
// only plain data so callers own the app/agent state and this package stays
// layering-safe.
type ContextInput struct {
	MemoryDocs   []MemoryDoc
	SkillCount   int
	CommandCount int
	Tools        []string
	SandboxMode  string
	ApprovalMode string
	Ignore       IgnoreStatus
}

// contextReport is the normalized, bounded form shared by both renderers.
// Field order is fixed, which also makes the JSON output deterministic.
type contextReport struct {
	MemoryDocs     []MemoryDoc `json:"memory_docs"`
	SkillCount     int         `json:"skill_count"`
	CommandCount   int         `json:"command_count"`
	Tools          []string    `json:"tools"`
	SandboxMode    string      `json:"sandbox_mode"`
	ApprovalMode   string      `json:"approval_mode"`
	IgnoreFiles    []string    `json:"ignore_files"`
	IgnoreRules    int         `json:"ignore_rules"`
	EstimatedBytes int         `json:"estimated_bytes"`
}

// normalizeContext sorts, bounds and clips the input. Slicing copies keep the
// caller's slices untouched, so repeated calls with the same input are stable.
func normalizeContext(in ContextInput) contextReport {
	out := contextReport{
		SkillCount:   in.SkillCount,
		CommandCount: in.CommandCount,
		SandboxMode:  clipContextText(in.SandboxMode, maxContextNameBytes),
		ApprovalMode: clipContextText(in.ApprovalMode, maxContextNameBytes),
		IgnoreRules:  in.Ignore.Rules,
	}

	docs := append([]MemoryDoc(nil), in.MemoryDocs...)
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	for i, doc := range docs {
		if i >= maxContextItems {
			break
		}
		doc.Path = clipContextText(doc.Path, maxContextPathBytes)
		doc.Reason = clipContextText(doc.Reason, maxContextNameBytes)
		if doc.Bytes < 0 {
			doc.Bytes = 0
		}
		out.MemoryDocs = append(out.MemoryDocs, doc)
		if doc.Bytes > 0 {
			if out.EstimatedBytes > math.MaxInt-doc.Bytes {
				out.EstimatedBytes = math.MaxInt
				break
			}
			out.EstimatedBytes += doc.Bytes
		}
	}

	tools := append([]string(nil), in.Tools...)
	sort.Strings(tools)
	for i, name := range tools {
		if i >= maxContextItems {
			break
		}
		out.Tools = append(out.Tools, clipContextText(name, maxContextNameBytes))
	}

	files := append([]string(nil), in.Ignore.Files...)
	sort.Strings(files)
	for i, file := range files {
		if i >= maxContextItems {
			break
		}
		out.IgnoreFiles = append(out.IgnoreFiles, clipContextText(file, maxContextPathBytes))
	}
	return out
}

// BuildContextReport renders a compact, human-readable "what is in context"
// view. Every section is present even for empty input. The output is bounded
// and deterministic: the same input always renders the same text.
func BuildContextReport(in ContextInput) string {
	r := normalizeContext(in)
	var b strings.Builder
	b.WriteString("context: read-only view of injected inputs\n")

	if len(r.MemoryDocs) == 0 {
		b.WriteString("memory docs: none\n")
	} else {
		fmt.Fprintf(&b, "memory docs (%d):\n", len(r.MemoryDocs))
		for _, doc := range r.MemoryDocs {
			status := ""
			if doc.Truncated {
				reason := doc.Reason
				if reason == "" {
					reason = "truncated"
				}
				status = fmt.Sprintf(" [truncated: %s]", reason)
			}
			fmt.Fprintf(&b, "  - %s%s (%d B)\n", doc.Path, status, doc.Bytes)
		}
	}

	fmt.Fprintf(&b, "skills: %d\n", r.SkillCount)
	fmt.Fprintf(&b, "commands: %d\n", r.CommandCount)

	if len(r.Tools) == 0 {
		b.WriteString("tools: none\n")
	} else {
		fmt.Fprintf(&b, "tools (%d): %s\n", len(r.Tools), strings.Join(r.Tools, ", "))
	}

	sandbox := r.SandboxMode
	if sandbox == "" {
		sandbox = "unspecified"
	}
	approval := r.ApprovalMode
	if approval == "" {
		approval = "unspecified"
	}
	fmt.Fprintf(&b, "sandbox: %s\n", sandbox)
	fmt.Fprintf(&b, "approval: %s\n", approval)

	if len(r.IgnoreFiles) == 0 {
		b.WriteString("ignore files: none")
	} else {
		fmt.Fprintf(&b, "ignore files (%d): %s", len(r.IgnoreFiles), strings.Join(r.IgnoreFiles, ", "))
	}
	fmt.Fprintf(&b, "; rules: %d\n", r.IgnoreRules)

	fmt.Fprintf(&b, "estimated bytes: %d\n", r.EstimatedBytes)
	return b.String()
}

// BuildContextReportJSON is the machine-readable twin of BuildContextReport.
// It uses the same normalized input, so both views always agree.
func BuildContextReportJSON(in ContextInput) ([]byte, error) {
	return json.MarshalIndent(normalizeContext(in), "", "  ")
}

// clipContextText bounds s to max bytes without splitting a UTF-8 rune.
func clipContextText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max - 1 // leave room for the ellipsis
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ContextCommand is the built-in slash-command descriptor. Registration is
// the caller's job (internal/app): set.Add(ContextCommand()) after Discover
// and route `/context` to ContextHook.Run, mirroring /review. This package
// never builds an app or reads the filesystem for the report.
func ContextCommand() Command {
	return Command{
		Name:        "context",
		Description: "Show a read-only view of what is in the model context without modifying anything",
		Body:        "Report the active memory documents, skills, commands, tools, sandbox/approval mode, .argentignore status and an estimated context size. Read-only: do not modify files.",
		Path:        "<builtin:context>",
	}
}

// ContextHook is the registration seam for the /context slash command. The
// caller supplies the ContextInput it owns; Run stays pure and never errors.
type ContextHook struct {
	Input ContextInput
}

// Run implements the hook signature used by the built-in command hooks.
func (h ContextHook) Run(_ string) (string, error) {
	return BuildContextReport(h.Input), nil
}
