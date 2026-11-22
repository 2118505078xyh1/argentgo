package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestSubagentSharedPromptPrefix(t *testing.T) {
	t.Cleanup(func() { SetSubagentSharedPrompt(false) })
	parentSystem := "You are argentgo, a concise coding assistant.\nWork tree rules here."

	// 默认关闭：子代理用原有模板，不含父级 system。
	SetSubagentSharedPrompt(false)
	defaultPrompt := buildSubagentSystem(SubagentConfig{}, "review", parentSystem)
	if strings.Contains(defaultPrompt, "Work tree rules here") {
		t.Fatal("default-off prompt must not contain parent system")
	}
	if defaultPrompt != fmt.Sprintf(subagentTemplate, "review") {
		t.Fatal("default-off prompt changed")
	}

	// 开启：父级 system 必须是前缀，约束/任务标签只在末尾（缓存安全）。
	SetSubagentSharedPrompt(true)
	shared := buildSubagentSystem(SubagentConfig{}, "review", parentSystem)
	if !strings.HasPrefix(shared, parentSystem) {
		t.Fatalf("shared prompt must start with parent system, got %q", shared)
	}
	if !strings.Contains(shared, "You are a subagent working for another agent") {
		t.Fatal("shared prompt dropped the subagent role constraint")
	}
	if !strings.Contains(shared, "Task: review") {
		t.Fatal("shared prompt dropped the task label")
	}
	// 父级 system 与约束之间只追加，不改写前缀。
	if strings.Index(shared, "You are a subagent") < len(parentSystem) {
		t.Fatal("role constraint must appear after the parent prefix")
	}
}

func TestSubagentSharedPromptRespectsExplicitSystem(t *testing.T) {
	t.Cleanup(func() { SetSubagentSharedPrompt(false) })
	SetSubagentSharedPrompt(true)
	// 显式 cfg.System 始终优先：不注入父级前缀。
	got := buildSubagentSystem(SubagentConfig{System: "custom persona"}, "review", "parent system")
	if !strings.HasPrefix(got, "custom persona") || strings.Contains(got, "parent system") {
		t.Fatalf("explicit System must win over shared prefix, got %q", got)
	}
}

func TestSubagentSharedPromptEmptyParentFallsBack(t *testing.T) {
	t.Cleanup(func() { SetSubagentSharedPrompt(false) })
	SetSubagentSharedPrompt(true)
	got := buildSubagentSystem(SubagentConfig{}, "review", "   ")
	if got != fmt.Sprintf(subagentTemplate, "review") {
		t.Fatalf("empty parent system must fall back to template, got %q", got)
	}
}
