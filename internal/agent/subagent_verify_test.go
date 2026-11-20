package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// verifyTaskArgs 构建设置了验证字段的委派参数。
func verifyTaskArgs(description, prompt, verify string, retries int) string {
	b, _ := json.Marshal(taskArgs{
		Description:      description,
		Prompt:           prompt,
		Verify:           verify,
		VerifyMaxRetries: retries,
	})
	return string(b)
}

// TestTaskWithoutVerifyRunsChildOnce 锁定向后兼容路径：没有 verify 字段意味着恰好运行一次子级，
// 并返回子级的文本。
func TestTaskWithoutVerifyRunsChildOnce(t *testing.T) {
	childRequests := 0
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			childRequests++
			return textReply("answer 42"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root got: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("look", "find the answer"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Emit:     (&eventSink{}).emit,
		Subagent: &SubagentConfig{},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if childRequests != 1 {
		t.Fatalf("child requests = %d, want exactly 1 (no verify means one run)", childRequests)
	}
	if !strings.Contains(res.Text, "answer 42") {
		t.Fatalf("text = %q, want the child's answer", res.Text)
	}
}

// TestTaskVerifyAppliesInstructionAndAcceptsPass 检查启用验证的调用会发出一个后续子级，其提示携带验证指令，
// 且 PASS 会返回原始结果。
// （接上行）
func TestTaskVerifyAppliesInstructionAndAcceptsPass(t *testing.T) {
	const marker = "VERIFY_CLAUSE_XYZ"
	originalRuns, verifyRuns := 0, 0
	var verifyUserContent string
	events := &eventSink{}
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			user := lastUserContent(messages)
			if strings.Contains(user, marker) {
				verifyRuns++
				verifyUserContent = user
				return textReply("PASS - looks correct"), nil
			}
			originalRuns++
			return textReply("draft answer"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root got: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, verifyTaskArgs("do", "do work", marker, 0))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Emit:     events.emit,
		Subagent: &SubagentConfig{},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if originalRuns != 1 || verifyRuns != 1 {
		t.Fatalf("original runs = %d, verify runs = %d; want 1 each", originalRuns, verifyRuns)
	}
	if !strings.Contains(verifyUserContent, marker) {
		t.Fatalf("verification follow-up prompt = %q, want it to contain %q", verifyUserContent, marker)
	}
	if !strings.Contains(res.Text, "draft answer") {
		t.Fatalf("text = %q, want the original (verified) result", res.Text)
	}
	passNotice := false
	for _, event := range events.events {
		if event.Kind == EventNotice && strings.Contains(event.Text, "PASS") {
			passNotice = true
		}
	}
	if !passNotice {
		t.Fatalf("no PASS verification notice emitted; events = %+v", events.kinds())
	}
}

// TestTaskVerifyRetriesAreBounded 检查重复的 FAIL 结论最多把原任务重试 verify_max_retries 次，然后停止并返回最后的结果。
// 每次尝试运行一个验证子级，绝不递归。
// （接上行）
func TestTaskVerifyRetriesAreBounded(t *testing.T) {
	const retries = 2
	originalRuns, verifyRuns := 0, 0
	events := &eventSink{}
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			if strings.Contains(lastUserContent(messages), "Verify the following result") {
				verifyRuns++
				return textReply("FAIL - insufficient detail"), nil
			}
			originalRuns++
			return textReply(fmt.Sprintf("draft %d", originalRuns)), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root got: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, verifyTaskArgs("do", "do work", "be perfect", retries))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Emit:     events.emit,
		Subagent: &SubagentConfig{},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := retries + 1 // 初次尝试加上 `retries` 次重试
	if originalRuns != want || verifyRuns != want {
		t.Fatalf("original runs = %d, verify runs = %d; want %d each", originalRuns, verifyRuns, want)
	}
	if !strings.Contains(res.Text, fmt.Sprintf("draft %d", want)) {
		t.Fatalf("text = %q, want the last attempt's result %q", res.Text, fmt.Sprintf("draft %d", want))
	}
	failNotices := 0
	for _, event := range events.events {
		if event.Kind == EventNotice && strings.Contains(event.Text, "FAIL") {
			failNotices++
		}
	}
	if failNotices != want {
		t.Fatalf("FAIL notices = %d, want %d", failNotices, want)
	}
}

// TestTaskVerifyRetriesClampedToMax 锁定硬上限：过大的 verify_max_retries 不能产生超过 MaxVerifyRetries 的重试。
// （接上行）
func TestTaskVerifyRetriesClampedToMax(t *testing.T) {
	originalRuns := 0
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			if strings.Contains(lastUserContent(messages), "Verify the following result") {
				return textReply("FAIL"), nil
			}
			originalRuns++
			return textReply("work"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root got: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, verifyTaskArgs("do", "do work", "check", 99))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{},
		Subagent: &SubagentConfig{},
	}}

	if _, err := runner.Run(context.Background(), "delegate"); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := MaxVerifyRetries + 1
	if originalRuns != want {
		t.Fatalf("original runs = %d, want at most %d (verify_max_retries clamped)", originalRuns, want)
	}
}

// TestTaskVerifyHonorsDepthLimit 检查深度防护在启用验证的调用上先于任何子级（或验证子级）运行生效，
// 使验证无法在深度边界处失控。
// （接上行）
func TestTaskVerifyHonorsDepthLimit(t *testing.T) {
	sink := &eventSink{}
	tt := &taskTools{
		depth:  2,
		cfg:    &SubagentConfig{MaxDepth: 2},
		parent: Options{Emit: sink.emit},
	}
	_, err := tt.runTask(context.Background(), verifyTaskArgs("do", "do work", "check", 3))
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("err = %v, want a depth-limit error", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("events = %+v, want none when the depth guard rejects the call", sink.kinds())
	}
}

// TestTaskToolSpecAdvertisesVerifyFields 检查工具 schema 公布了两个新的可选字段，而不改变既有的必需集合。
// （接上行）
func TestTaskToolSpecAdvertisesVerifyFields(t *testing.T) {
	spec := TaskToolSpec()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(spec.Function.Parameters), &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	for _, key := range []string{"description", "prompt", "task", "persona", "role", "verify", "verify_max_retries"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("schema property %q missing", key)
		}
	}
	requiredPrompt := false
	for _, key := range schema.Required {
		if key == "prompt" {
			requiredPrompt = true
		}
	}
	if !requiredPrompt {
		t.Error("schema must still require prompt")
	}

	var verify struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(schema.Properties["verify"], &verify); err != nil {
		t.Fatalf("unmarshal verify property: %v", err)
	}
	if verify.Type != "string" {
		t.Errorf("verify type = %q, want string", verify.Type)
	}
	var retries struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(schema.Properties["verify_max_retries"], &retries); err != nil {
		t.Fatalf("unmarshal verify_max_retries property: %v", err)
	}
	if retries.Type != "integer" {
		t.Errorf("verify_max_retries type = %q, want integer", retries.Type)
	}
}

// TestVerificationFailedParsesVerdict 锁定父级复核使用的 PASS/FAIL 解析，包括冗长回复的回退。
// （接上行）
func TestVerificationFailedParsesVerdict(t *testing.T) {
	cases := []struct {
		verdict string
		fail    bool
	}{
		{"PASS", false},
		{"PASS - all checks green", false},
		{"FAIL", true},
		{"FAIL: missing tests", true},
		{"Result: FAIL - missing detail", true},
		{"", true},
	}
	for _, tc := range cases {
		if got := verificationFailed(tc.verdict); got != tc.fail {
			t.Errorf("verificationFailed(%q) = %v, want %v", tc.verdict, got, tc.fail)
		}
	}
}
