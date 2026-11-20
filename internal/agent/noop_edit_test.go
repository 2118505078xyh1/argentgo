package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunToolAddsNoopEditHint checks that an edit tool reporting a no-op gets
// the aider-style anti-resend line in the result the model reads next.
func TestRunToolAddsNoopEditHint(t *testing.T) {
	const toolOutput = "File already up to date."
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return toolOutput, nil
	}}
	s := &runState{tools: ft}

	res := s.runTool(context.Background(), toolCall("c1", "edit", "{}"), time.Now())
	if res.isError {
		t.Fatalf("isError = true, output = %q", res.output)
	}
	if !strings.HasPrefix(res.output, toolOutput) {
		t.Fatalf("output = %q, want the tool's own report kept", res.output)
	}
	if !strings.Contains(res.output, noopEditHint) {
		t.Fatalf("output = %q, want the no-op hint", res.output)
	}
}

// TestRunToolMultiEditNoChangesGetHint checks that multi_edit no-op wording is
// recognised too.
func TestRunToolMultiEditNoChangesGetHint(t *testing.T) {
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "no changes made", nil
	}}
	s := &runState{tools: ft}

	res := s.runTool(context.Background(), toolCall("c2", "multi_edit", "{}"), time.Now())
	if !strings.Contains(res.output, noopEditHint) {
		t.Fatalf("output = %q, want the no-op hint", res.output)
	}
}

// TestRunToolKeepsNormalEditOutputByteIdentical checks that a successful edit
// with no no-op marker is passed through untouched.
func TestRunToolKeepsNormalEditOutputByteIdentical(t *testing.T) {
	const toolOutput = "Edited file successfully.\n"
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return toolOutput, nil
	}}
	s := &runState{tools: ft}

	res := s.runTool(context.Background(), toolCall("c3", "edit", "{}"), time.Now())
	if res.output != toolOutput {
		t.Fatalf("output = %q, want byte-identical %q", res.output, toolOutput)
	}
}

// TestRunToolKeepsNonEditToolOutputByteIdentical checks that the hint is scoped
// to edit tools: other tools keep their output even with the same wording.
func TestRunToolKeepsNonEditToolOutputByteIdentical(t *testing.T) {
	const toolOutput = "file already exists"
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return toolOutput, nil
	}}
	s := &runState{tools: ft}

	res := s.runTool(context.Background(), toolCall("c4", "bash", "{}"), time.Now())
	if res.output != toolOutput {
		t.Fatalf("output = %q, want byte-identical %q", res.output, toolOutput)
	}
}

// TestRunToolNoopEditHintIsIdempotent checks that a tool which already echoes
// the hint does not get a second copy appended.
func TestRunToolNoopEditHintIsIdempotent(t *testing.T) {
	toolOutput := "Already applied.\n" + noopEditHint
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return toolOutput, nil
	}}
	s := &runState{tools: ft}

	res := s.runTool(context.Background(), toolCall("c5", "edit", "{}"), time.Now())
	if res.output != toolOutput {
		t.Fatalf("output = %q, want the hint appended exactly once", res.output)
	}
}
