package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
// A non-nil error means the call must not run; StopError is returned for both
// a policy deny and an operator refusal.
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
	ok, err := approver.Ask(ctx, name, string(args))
	if err != nil {
		return fmt.Errorf("security: approval for the %s tool call failed: %w", name, err)
	}
	if !ok {
		c.record(name, "refused", "the operator refused the tool call", auditArgs)
		return &StopError{Tool: name, Reason: "the operator refused the tool call", Refused: true}
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
