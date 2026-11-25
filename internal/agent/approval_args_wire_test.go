package agent

import (
	"context"
	"encoding/json"
	"testing"

	"example.com/argentgo/internal/llm"
)

// recordingApprover 捕获 Approver.Ask 实际收到的参数字节。
type recordingApprover struct {
	args string
}

func (a *recordingApprover) Ask(_ context.Context, _ string, args string) (bool, error) {
	a.args = args
	return true, nil
}

// TestApprovalArgsMatchExecutedArgs 守护"批准 A、执行 B"修复：审批器看到的参数
// 必须与工具实际收到的参数逐字节相同。开启强转且 schema 声明数字时，模型写的
// 字符串 "5" 会被强转为 5；审批与执行必须都看到强转后的字节。
func TestApprovalArgsMatchExecutedArgs(t *testing.T) {
	SetToolArgumentCoercion(true)
	defer SetToolArgumentCoercion(false)

	approver := &recordingApprover{}
	var executed string
	var schema llm.ToolSpec
	schema.Type = "function"
	schema.Function.Name = "write"
	schema.Function.Parameters = json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}}}`)
	tools := &fakeTools{
		specs: []llm.ToolSpec{schema},
		run: func(_ context.Context, _ string, args string) (string, error) {
			executed = args
			return "ok", nil
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "write", `{"limit":"5","i":"set limit"}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools,
		Approve:       approver,
		MutatingTools: []string{"write"},
	}}
	if _, err := runner.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if approver.args == "" {
		t.Fatal("审批器未收到参数")
	}
	if approver.args != executed {
		t.Fatalf("审批/执行参数不一致:\n approval=%q\n executed=%q", approver.args, executed)
	}
	// 强转与意图剥离都应生效：字符串 "5" 变成数字 5，简洁意图 i 被剥离。
	if want, ok := mustNumberArg(t, executed, "limit"); !ok || want != 5 {
		t.Fatalf("limit 强转失败: %q", executed)
	}
}

// mustNumberArg 从 JSON 参数中取出数字字段，失败时报告。
func mustNumberArg(t *testing.T, args, key string) (float64, bool) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		t.Fatalf("解析参数 %q: %v", args, err)
	}
	v, ok := m[key].(float64)
	return v, ok
}
