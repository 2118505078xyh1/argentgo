package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunToolAddsNoopEditHint 检查报告无操作的编辑工具，会在模型下一步读取的结果中获得 aider 风格的防重发行。
// （接上行）
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

// TestRunToolMultiEditNoChangesGetHint 检查 multi_edit 的无操作措辞也会被识别。
// （接上行）
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

// TestRunToolKeepsNormalEditOutputByteIdentical 检查没有无操作标记的成功编辑会被原样透传。
// （接上行）
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

// TestRunToolKeepsNonEditToolOutputByteIdentical 检查提示仅作用于编辑工具：其他工具即使措辞相同也保留其输出。
// （接上行）
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

// TestRunToolNoopEditHintIsIdempotent 检查已经回显该提示的工具，不会被追加第二份副本。
// （接上行）
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
