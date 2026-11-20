package workflow

import (
	"context"
	"strings"
	"testing"
)

// TestStepNameWhitespaceIsNormalisedForOutputReferences 检查带首尾空白的步骤
// 名 —— Validate 接受它，因为唯一性检查已先做了裁剪 —— 能从
// 唯一性检查已做裁剪 —— 能从引用中解析。
// {{steps.NAME.output}} 引用中解析。在规范化之前，executor 用原始名称作为输出键，而引用携带裁剪后的名称，导致
// 用原始名称作为输出键，而引用携带裁剪后的名称，因此
// 每个消费者都被跳过且标记保持未替换。
func TestStepNameWhitespaceIsNormalisedForOutputReferences(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "PAYLOAD", nil
		}
		return "consumed:" + text, nil
	})}
	def := mustDefinition(t, "{\"steps\":[\n\t\t{\"name\":\"  scan \\t\",\"prompt\":\"produce\"},\n\t\t{\"name\":\"report\",\"prompt\":\"report {{steps.scan.output}}\"}\n\t]}")

	if got := def.Steps[0].Name; got != "scan" {
		t.Fatalf("parsed step name = %q, want %q", got, "scan")
	}
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	want := []string{"produce", "report PAYLOAD"}
	if len(rec.prompts) != len(want) {
		t.Fatalf("prompts = %q, want %q", rec.prompts, want)
	}
	for i := range want {
		if rec.prompts[i] != want[i] {
			t.Errorf("prompt[%d] = %q, want %q", i, rec.prompts[i], want[i])
		}
	}
	if out, ok := run.Output("scan"); !ok || out != "PAYLOAD" {
		t.Errorf("Output(scan) = %q, ok=%v", out, ok)
	}
}

// 每个消费者都被跳过且标记保持未替换。
// TestRunNormalisesStepNamesOnACopy 守护直接 Definition 路径：在代码中构造 Definition（而非解析 JSON）的调用方
// 仍能使用输出引用，且其原始定义不被修改。
func TestRunNormalisesStepNamesOnACopy(t *testing.T) {
	def := &Definition{Steps: []Step{
		{Name: " produce ", Prompt: "produce"},
		{Name: "report ", Prompt: "report {{steps.produce.output}}"},
	}}
	exec := &Executor{Prompter: prompterFunc(func(ctx context.Context, text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "DATA", nil
		}
		return "ok:" + text, nil
	})}

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	if run.Steps[0].Name != "produce" || run.Steps[1].Name != "report" {
		t.Errorf("result names = %q, %q", run.Steps[0].Name, run.Steps[1].Name)
	}
	if out, ok := run.Output("produce"); !ok || out != "DATA" {
		t.Errorf("Output(produce) = %q, ok=%v", out, ok)
	}
	// 仍能使用输出引用，且其原始定义不被修改。
	if def.Steps[0].Name != " produce " || def.Steps[1].Name != "report " {
		t.Errorf("Run mutated the caller's definition: %+v", def.Steps)
	}
}
