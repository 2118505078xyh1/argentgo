package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Sentinel errors let a caller tell a hard deny from an operator refusal with
// errors.Is.
var (
	// ErrDenied means the policy or the sandbox stopped the call.
	ErrDenied = errors.New("security: tool call denied")
	// ErrRefused means the operator answered no.
	ErrRefused = errors.New("security: tool call refused by the operator")
	// ErrRecoverablePlanBlock means the optional plan-mode command guard stopped
	// a mutating shell command before policy/approval. Unlike StopError this is
	// recoverable: the agent loop turns it into a tool result the model reads.
	ErrRecoverablePlanBlock = errors.New("security: mutating shell command blocked in plan mode")
	// RestoreToolName is the audit name for a checkpoint restore (undo) action.
	RestoreToolName = "undo"
)

// StopError tells the caller the run must stop instead of retrying: the tool
// call was denied by policy or refused by the operator.
type StopError struct {
	Tool    string
	Reason  string
	Refused bool
}

// finalitySentence is appended to every policy-block error so the model stops
// retrying a denial it cannot overturn (idea borrowed from gptme's policy-block
// hook). Keeping it one shared string makes agent-visible wording consistent
// across policy denies and operator refusals.
const finalitySentence = ". This block is intentional and final, not a transient error. " +
	"Do not retry it or reach the same outcome another way."

// Error implements error. The leading message is preserved exactly; only the
// finality sentence is appended so callers matching "denied"/"refused" still work.
func (e *StopError) Error() string {
	verb := "denied"
	if e.Refused {
		verb = "refused by the operator"
	}
	var message string
	if e.Reason == "" {
		message = fmt.Sprintf("security: the %s tool call was %s", e.Tool, verb)
	} else {
		message = fmt.Sprintf("security: the %s tool call was %s: %s", e.Tool, verb, e.Reason)
	}
	return message + finalitySentence
}

// Unwrap maps the error onto ErrDenied or ErrRefused.
func (e *StopError) Unwrap() error {
	if e.Refused {
		return ErrRefused
	}
	return ErrDenied
}

// FeedbackRefusal is the error an Approver returns alongside a false decision
// when the operator rejected a call and left a correction ("do it like this
// instead"). Approver.Ask returns only a bool, so a typed error is the
// additive channel for that text: no existing Approver signature changes and
// approvers that never produce it keep the historical bool-only behaviour.
//
// The chain turns it into a RefusalError the model can read and act on instead
// of a final StopError. A plain (false, nil) answer still means a hard refusal.
type FeedbackRefusal struct {
	Feedback string
}

func (e *FeedbackRefusal) Error() string {
	return "security: the operator rejected the tool call with feedback: " + e.Feedback
}

// maxApprovalFeedbackRunes bounds the operator text copied into a tool error.
// Feedback is model-facing; without a cap one reply could fill the next turn's
// context with an arbitrary amount of operator text.
const maxApprovalFeedbackRunes = 2000

// RefusalError is a recoverable tool error produced when the operator rejects
// a call and explains how to change it. It deliberately is *not* a StopError:
// the finality sentence on a StopError tells the model to abandon that outcome,
// which is the opposite of an actionable correction. The feedback reaches the
// model as ordinary tool output, so the loop continues and the model can retry
// with a changed request. errors.Is still matches ErrRefused.
type RefusalError struct {
	Tool     string
	Feedback string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf(
		"security: the %s tool call was rejected by the operator: %s. Adjust the request and try again.",
		e.Tool, e.Feedback,
	)
}

// Unwrap keeps the refusal sentinel intact for callers using errors.Is.
func (e *RefusalError) Unwrap() error { return ErrRefused }

// clipApprovalFeedback trims rune-safely and marks a truncation so the model
// knows the operator's instruction was shortened rather than ending there.
func clipApprovalFeedback(text string) string {
	if utf8.RuneCountInString(text) <= maxApprovalFeedbackRunes {
		return text
	}
	return runeHead(text, maxApprovalFeedbackRunes) + "…[feedback clipped]"
}

// Chain combines the policy guard, the approval prompt and the audit log into
// the single hook the tool registry calls before running anything.
//
// All fields are optional except Policy, which defaults to a no-rule policy.
type Chain struct {
	Policy   *Policy
	Approver Approver
	Audit    *AuditLog
	// Session tags audit entries; typically the session file or id.
	Session string
	// PlanCommandGuard, when true, blocks mutating shell commands before the
	// policy → approval → audit chain. The zero value false preserves behaviour.
	PlanCommandGuard bool
}

// NewChain builds a chain. A nil approver denies; a nil audit log records
// nothing.
func NewChain(policy *Policy, approver Approver, audit *AuditLog, session string) *Chain {
	return &Chain{Policy: policy, Approver: approver, Audit: audit, Session: session}
}

// Check evaluates one tool call in order: policy, then approval, then audit.
// A non-nil error means the call must not run. A policy deny or a plain operator
// refusal returns a StopError; a refusal carrying feedback returns a
// RefusalError so the model can read the correction and retry differently.
func (c *Chain) Check(ctx context.Context, toolName string, args json.RawMessage) error {
	return c.check(ctx, toolName, args, args)
}

// CheckRestore gates a checkpoint restore through the same policy → approval →
// audit chain as a mutating tool call. Policy sees the real paths so a deny or
// a write-root escape still stops the action; the audit record sees redacted
// manifest paths only.
func (c *Chain) CheckRestore(ctx context.Context, paths []string) error {
	raw, err := json.Marshal(map[string]any{"paths": paths})
	if err != nil {
		return fmt.Errorf("security: encoding restore paths: %w", err)
	}
	return c.check(ctx, RestoreToolName, raw, RedactManifestPaths(raw))
}

func (c *Chain) check(ctx context.Context, toolName string, args, auditArgs json.RawMessage) error {
	// Optional plan-mode command guard. When enabled it runs first: a mutating
	// shell command is rejected as a recoverable error, before any policy deny,
	// approval prompt or audit entry. Disabled by default.
	if c != nil && c.PlanCommandGuard {
		if token := guardShellCommand(toolName, args); token != "" {
			return fmt.Errorf("%w: %q", ErrRecoverablePlanBlock, token)
		}
	}
	name := strings.TrimSpace(toolName)
	if name == "" {
		c.record("", "deny", "the tool name is empty", auditArgs)
		return &StopError{Tool: "", Reason: "the tool name is empty"}
	}
	result := c.policy().Evaluate(name, args)
	switch result.Decision {
	case Deny:
		c.record(name, "deny", result.Reason, auditArgs)
		return &StopError{Tool: name, Reason: result.Reason}
	case Allow:
		c.record(name, "allow", result.Reason, auditArgs)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	approver := c.Approver
	if approver == nil {
		approver = NeverApprover{}
	}
	// Show the real target in the prompt: a junction inside the workspace must
	// not let a prompt say "home/note.txt" while the tool writes elsewhere.
	// The tool and the audit log still use the original arguments.
	ok, err := approver.Ask(ctx, name, string(approvalArgs(args, c.policy().Root())))
	if err != nil {
		// A rejection carrying feedback is not an approval-infrastructure
		// failure: it is the operator's correction and must reach the model as
		// a readable, recoverable tool error. Keep the raw text out of the
		// audit log; only the model-facing error carries it.
		var corrected *FeedbackRefusal
		if !ok && errors.As(err, &corrected) {
			if feedback := strings.TrimSpace(corrected.Feedback); feedback != "" {
				c.record(name, "refused", "the operator rejected the tool call with feedback", auditArgs)
				return &RefusalError{Tool: name, Feedback: clipApprovalFeedback(feedback)}
			}
			// An empty correction is just a plain "no", not a failed prompt.
			err = nil
		} else {
			return fmt.Errorf("security: approval for the %s tool call failed: %w", name, err)
		}
	}
	if !ok {
		c.record(name, "refused", "the operator refused the tool call", auditArgs)
		return &StopError{Tool: name, Reason: "the operator refused the tool call", Refused: true}
	}
	// Fail-closed normalization of the raw Approver.Ask answer. Every branch
	// above already turns an error (including a cancelled or unavailable
	// approval channel) into a returned failure, so this guard is unreachable
	// with the current approvers and changes no existing deny/ask/allow event.
	// It is a defensive invariant: a future or machine approval path can never
	// slip an unexpected (granted, err) pair through as consent. Only a genuine
	// ApprovalAllowed may reach the allow record below (dsh/goose audit).
	if ClassifyApproval(ok, err) != ApprovalAllowed {
		c.record(name, "deny", "approval was not granted", auditArgs)
		return &StopError{Tool: name, Reason: "approval was not granted"}
	}
	c.record(name, "allow", "approved by the operator", auditArgs)
	return nil
}

func (c *Chain) policy() *Policy {
	if c == nil || c.Policy == nil {
		return defaultPolicy()
	}
	return c.Policy
}

var fallbackPolicy = DefaultPolicy(".")

func defaultPolicy() *Policy { return fallbackPolicy }

func (c *Chain) record(tool, decision, reason string, args json.RawMessage) {
	if c == nil || c.Audit == nil {
		return
	}
	entry := AuditEntry{
		Tool:     tool,
		Decision: decision,
		Reason:   reason,
		Session:  c.Session,
	}
	if len(args) > 0 {
		entry.Args = RedactJSON(args)
	}
	_ = c.Audit.Record(entry)
}

// approvalArgs replaces path arguments with their real on-disk locations for
// display in the approval prompt. It is display-only: the tool still receives
// the original arguments, and the audit record keeps them too. When a path
// cannot be resolved, or already names its real location, the original JSON is
// returned unchanged so prompts stay byte-identical for ordinary calls.
func approvalArgs(args json.RawMessage, root string) json.RawMessage {
	decoder := json.NewDecoder(strings.NewReader(string(args)))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return args
	}
	changed := false
	rewrite := func(value string) string {
		resolved := value
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, resolved)
		}
		real, err := ResolveRealPath(resolved)
		if err != nil || filepath.Clean(real) == filepath.Clean(resolved) {
			return value
		}
		changed = true
		return real
	}
	for _, key := range pathArgKeys {
		if value, ok := fields[key].(string); ok {
			fields[key] = rewrite(value)
		}
	}
	if list, ok := fields["paths"].([]any); ok {
		for i, item := range list {
			if value, ok := item.(string); ok {
				list[i] = rewrite(value)
			}
		}
	}
	if !changed {
		return args
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return args
	}
	return encoded
}
