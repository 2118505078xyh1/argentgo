package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRunExecutesStepsInDependencyOrder 检查步骤输出引用即使在生产者声明于消费者之后时
// 也能在运行时被解析。
// ValidateDAG 接受前向引用并计算依赖在前的顺序；
// 依赖必须先行；executor 必须遵守该顺序，否则
// executor 必须遵守该顺序，否则消费者的提示中会残留字面量 {{steps...output}} 标记。
func TestRunExecutesStepsInDependencyOrder(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "PAYLOAD", nil
		}
		return "consumed:" + text, nil
	})}
	def := mustDefinition(t, `{"name":"forward","steps":[
		{"name":"consume","prompt":"consume {{steps.produce.output}}"},
		{"name":"produce","prompt":"produce data"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	want := []string{"produce data", "consume PAYLOAD"}
	if len(rec.prompts) != len(want) {
		t.Fatalf("prompts = %q, want %q", rec.prompts, want)
	}
	for i := range want {
		if rec.prompts[i] != want[i] {
			t.Errorf("prompt[%d] = %q, want %q", i, rec.prompts[i], want[i])
		}
	}
	if out, ok := run.Output("consume"); !ok || out != "consumed:consume PAYLOAD" {
		t.Errorf("consume output = %q, ok=%v", out, ok)
	}
	if len(run.Steps) != 2 || run.Steps[0].Name != "produce" || run.Steps[1].Name != "consume" {
		t.Errorf("step results = %+v, want producer before consumer", run.Steps)
	}
}

// TestRunSkipsDependentWhenProducerFails 检查当生产者失败时，消费者绝不会带着未解析的步骤输出占位符运行。
// 带着未解析的步骤输出占位符运行，即使其生产者已失败。
func TestRunSkipsDependentWhenProducerFails(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "", errors.New("producer boom")
		}
		return "consumed:" + text, nil
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"consume","prompt":"consume {{steps.produce.output}}"},
		{"name":"produce","prompt":"produce data"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q", run.Status)
	}
	if rec.promptCount() != 1 {
		t.Fatalf("the consumer must not run after its producer failed: %q", rec.prompts)
	}
	if len(run.Steps) != 2 || run.Steps[0].Name != "produce" || run.Steps[1].Name != "consume" || run.Steps[1].Status != StatusSkipped {
		t.Errorf("step results = %+v, want producer failed and consumer skipped", run.Steps)
	}
}

// TestRunSkipsDependentWhenProducerFailsButContinues 检查 on_failure:
// continue 不会让消费者带着字面量 {{steps...output}} 运行。
// "继续"不得破坏 DAG 验证所建立的依赖关系。
// established.
func TestRunSkipsDependentWhenProducerFailsButContinues(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if text == "produce" {
			return "", errors.New("producer boom")
		}
		return "consumed:" + text, nil
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"produce","prompt":"produce","on_failure":"continue"},
		{"name":"consume","prompt":"consume {{steps.produce.output}}"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q", run.Status)
	}
	if rec.promptCount() != 1 {
		t.Fatalf("the consumer must not run when its producer produced no output: %q", rec.prompts)
	}
	if len(run.Steps) != 2 || run.Steps[1].Status != StatusSkipped {
		t.Fatalf("step results = %+v, want consumer skipped", run.Steps)
	}
}

// TestRunSkipsDependentWhenProducerSkippedByCondition 检查读取另一步骤输出的步骤在该生产者
// 读取另一步骤输出的步骤在该生产者自身被跳过时也被跳过，
// 自身被 When 条件跳过时也被跳过，而不是把未解析标记泄漏到
// 消费者的提示中。
func TestRunSkipsDependentWhenProducerSkippedByCondition(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		return "out:" + text, nil
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"scan","prompt":"scan","when":{"previous_contains":"NEVER"}},
		{"name":"report","prompt":"report {{steps.scan.output}}"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(run.Steps) != 2 || run.Steps[0].Status != StatusSkipped || run.Steps[1].Status != StatusSkipped {
		t.Fatalf("step results = %+v, want both steps skipped", run.Steps)
	}
	if rec.promptCount() != 0 {
		t.Fatalf("no seam should run: %q", rec.prompts)
	}
	// 被跳过的生产者不是失败；运行本身保持成功。
	if run.Status != StatusOK {
		t.Fatalf("status = %q, want %q", run.Status, StatusOK)
	}
}
