package app

import (
	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// This file holds additive wiring adapters for two pure helpers that already
// live in the dependency-free core packages. They are deliberately kept here,
// in the app package (the only package allowed to import everything), so the
// core packages do not gain any app-level dependency.
//
// Each adapter is a no-op adapter on the path where its feature is not used:
// a prompt without a declaration leaves the Budget untouched, and a clean
// transcript yields an empty notice and no confirmation requirement. That
// makes them safe to call unconditionally at their eventual Execute call
// sites without changing existing runs.

// applyDeclaredTurnBudget copies a per-turn token budget the user declared in
// prompt into b.DeclaredTurnTokens. Parsing is delegated to
// agent.ParseTurnBudget, which ignores code spans, injected XML blocks and
// @-references so only the user's own prose can set the cap.
//
// It is a no-op when b is nil or the prompt carries no declaration, so callers
// may invoke it on every run without changing the behaviour of runs that
// declare nothing. When a declaration exists it overwrites any prior value;
// the parsed cap is an additional cumulative token limit and never relaxes the
// other limits configured on b.
func applyDeclaredTurnBudget(prompt string, b *agent.Budget) {
	if b == nil {
		return
	}
	tokens, ok := agent.ParseTurnBudget(prompt)
	if !ok {
		return
	}
	b.DeclaredTurnTokens = tokens
}

// recoveryDecision is the app-layer view of session.ClassifyRecovery: the
// user-visible notice to announce and whether continuation needs an explicit
// operator decision. Computing both from one plan keeps a future wiring site
// from classifying the same transcript twice.
type recoveryDecision struct {
	Notice            string
	NeedsConfirmation bool
}

// recoveryDecisionFor classifies messages once and returns both the notice
// and the confirmation requirement. It mirrors session.ClassifyRecovery's
// conservative rule: NeedsConfirmation is true only for degraded history,
// while interrupted prompts/turns are auto-continuable yet still announced.
func recoveryDecisionFor(messages []session.Entry) recoveryDecision {
	plan := session.ClassifyRecovery(messages)
	return recoveryDecision{
		Notice:            plan.Announcement,
		NeedsConfirmation: !plan.CanAutoContinue || plan.RequiresUserConfirmation,
	}
}

// recoveryNotice returns the user-visible announcement for resuming messages.
// It is empty for a clean transcript. Kept as a string-only adapter so the app
// layer can surface a notice without depending on the shape of RecoveryPlan.
func recoveryNotice(messages []session.Entry) string {
	return recoveryDecisionFor(messages).Notice
}

// recoveryNeedsConfirmation reports whether resuming messages needs an
// explicit operator decision before the run continues.
func recoveryNeedsConfirmation(messages []session.Entry) bool {
	return recoveryDecisionFor(messages).NeedsConfirmation
}
