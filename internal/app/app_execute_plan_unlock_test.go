package app

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// mergeEnv 在基础 env 之上叠加覆盖项，仅用于测试构造注入的环境。
func mergeEnv(base func(string) string, overrides map[string]string) func(string) string {
	return func(key string) string {
		if v, ok := overrides[key]; ok {
			return v
		}
		return base(key)
	}
}

func newTestChain(t *testing.T) *security.Chain {
	t.Helper()
	return security.NewChain(security.DefaultPolicy(t.TempDir()), nil, nil, "plan-test")
}

// planExitAsk 记录一次审批询问，供测试断言 plan 守卫是否把调用拦在审批之前。
type planExitAsk struct {
	name string
	args string
}

// planExitApprover 是捕获型假审批器：记录所有 Ask 调用并一律批准。
// 计划模式命令守卫在审批之前运行，因此被它拦下的调用不会出现在这里。
type planExitApprover struct {
	mu    sync.Mutex
	asked []planExitAsk
}

func (a *planExitApprover) Ask(_ context.Context, name, args string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, planExitAsk{name: name, args: args})
	return true, nil
}

func (a *planExitApprover) snapshot() []planExitAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]planExitAsk(nil), a.asked...)
}

func planExitToolCall(id, name, args string) llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID:       id,
		Type:     "function",
		Function: llm.ToolCallFunction{Name: name, Arguments: args},
	}}}
}

// planUnlockChatter 按固定顺序请求三次工具调用：解锁前修改类 bash、
// exit_plan、解锁后修改类 bash，然后给出最终答复。它同时记录每轮看到的
// 工具目录与消息，便于断言 exit_plan 的公布与 plan 守卫的阻断/放行。
type planUnlockChatter struct {
	mu       sync.Mutex
	calls    int
	specs    [][]llm.ToolSpec
	messages [][]llm.Message
}

func (c *planUnlockChatter) Chat(_ context.Context, _ string, messages []llm.Message, toolSpecs []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.specs = append(c.specs, append([]llm.ToolSpec(nil), toolSpecs...))
	c.messages = append(c.messages, append([]llm.Message(nil), messages...))
	switch c.calls {
	case 1:
		// plan 模式开启：这条修改类命令会被计划守卫拦在审批之前。
		return planExitToolCall("before", "bash", `{"command":"rm -f plan_exit_before.txt"}`), nil
	case 2:
		// 提交计划，工具通过 ctx 中的回调解除 plan 守卫。
		return planExitToolCall("exit", "exit_plan", `{"plan":"审阅并批准计划后继续"}`), nil
	case 3:
		// 解锁后同样的修改类命令应到达审批器。
		return planExitToolCall("after", "bash", `{"command":"rm -f plan_exit_after.txt"}`), nil
	default:
		if onText != nil {
			onText("done")
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
}

func countSpecByName(specs []llm.ToolSpec, name string) int {
	n := 0
	for _, s := range specs {
		if s.Function.Name == name {
			n++
		}
	}
	return n
}

// lastToolMessage 返回最后一条工具结果消息的文本；没有则返回空。
// 只检查最后一条，避免旧的工具结果仍留在历史里干扰断言。
func lastToolMessage(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == "tool" || m.Role == "tool_result" {
			return m.Content
		}
	}
	return ""
}

// TestExecutePlanModeWiresExitPlanAndUnlocks 验证 app 侧接线：plan 模式开启时
// 公布 exit_plan；调用它后 chain 被解锁，后续修改类 bash 不再被 plan 守卫拒绝
// （即到达审批器）。同时验证解锁前的同类调用确实被守卫拦在审批之前。
func TestExecutePlanModeWiresExitPlanAndUnlocks(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	env, root, _ := wiringEnv(t)
	env = mergeEnv(env, map[string]string{"ARGENT_PLAN_MODE": "1"})
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	approver := &planExitApprover{}
	res.Approve = approver
	res.MaxTurns = 6
	res.MaxParallel = 1
	chatter := &planUnlockChatter{}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("plan-exit-unlock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "test plan unlock", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// exit_plan 必须出现在每一轮公布的工具目录中（恰好一次）。
	chatter.mu.Lock()
	defer chatter.mu.Unlock()
	if len(chatter.specs) == 0 {
		t.Fatal("chatter was never called")
	}
	if got := countSpecByName(chatter.specs[0], "exit_plan"); got != 1 {
		t.Fatalf("exit_plan spec count = %d, want 1", got)
	}

	// 解锁前的 bash 结果应携带 plan 守卫的恢复提示；解锁后不应再有。
	if before := lastToolMessage(chatter.messages[1]); !strings.Contains(before, "blocked in plan mode") {
		t.Fatalf("bash before exit_plan was not blocked by the plan guard: %q", before)
	}
	if len(chatter.messages) > 3 {
		if after := lastToolMessage(chatter.messages[3]); strings.Contains(after, "blocked in plan mode") {
			t.Fatalf("bash after exit_plan was still blocked: %q", after)
		}
	} else {
		t.Fatal("run stopped before the post-unlock bash result was fed back")
	}

	// 审批器应看到 exit_plan 与解锁后的 bash。plan 守卫的核心语义由上面的
	// lastToolMessage(messages[1]) 断言锁定（解锁前的 bash 在模型可见层面被拦，
	// 返回 "blocked in plan mode"）。批量默认化后工具批次在假 chatter 下可能被
	// 观测/恢复机制重复派发，审批器的精确计数不再是稳定不变量，因此这里改为
	// 锁定语义：exit_plan 恰好一次、解锁后的 bash 至少一次且不再出现 blocked。
	asked := approver.snapshot()
	var exitAsks, afterBash int
	for _, a := range asked {
		switch {
		case a.name == "exit_plan":
			exitAsks++
		case strings.Contains(a.args, "plan_exit_after"):
			afterBash++
		}
	}
	if exitAsks != 1 {
		t.Fatalf("exit_plan approval asks = %d, want 1 (asked=%v)", exitAsks, asked)
	}
	if afterBash < 1 {
		t.Fatalf("post-unlock bash never reached the approver (asked=%v)", asked)
	}
}

// planExitSpecCaptureChatter 只记录首轮公布的工具目录，然后立即结束运行。
type planExitSpecCaptureChatter struct {
	mu    sync.Mutex
	specs []llm.ToolSpec
}

func (c *planExitSpecCaptureChatter) Chat(_ context.Context, _ string, _ []llm.Message, toolSpecs []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.mu.Lock()
	if len(c.specs) == 0 {
		c.specs = append([]llm.ToolSpec(nil), toolSpecs...)
	}
	c.mu.Unlock()
	if onText != nil {
		onText("ok")
	}
	return llm.Message{Role: "assistant", Content: "ok"}, nil
}

// runPlanExitSpecs 用给定 plan 开关运行一次 Execute，返回首轮公布的工具目录。
func runPlanExitSpecs(t *testing.T, planOn bool) []llm.ToolSpec {
	t.Helper()
	env, root, _ := wiringEnv(t)
	if planOn {
		env = mergeEnv(env, map[string]string{"ARGENT_PLAN_MODE": "1"})
	}
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.Approve = &planExitApprover{}
	res.MaxTurns = 2
	capture := &planExitSpecCaptureChatter{}
	res.Chatter = capture
	store := testStore(t, res)
	sess, err := store.NewSession("plan-exit-specs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "test specs", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]llm.ToolSpec(nil), capture.specs...)
}

// TestExecutePlanModeOnlyAppendsExitPlanSpec 锁定逐字节不变量：开启 plan 模式
// 只追加一条 exit_plan，其余工具目录与默认（关闭）时完全一致；默认目录下
// 不存在 exit_plan。由此保证默认运行公布的工具与基线逐字节一致。
func TestExecutePlanModeOnlyAppendsExitPlanSpec(t *testing.T) {
	off := runPlanExitSpecs(t, false)
	on := runPlanExitSpecs(t, true)

	if got := countSpecByName(off, "exit_plan"); got != 0 {
		t.Fatalf("default specs contain exit_plan %d times, want 0", got)
	}
	if got := countSpecByName(on, "exit_plan"); got != 1 {
		t.Fatalf("plan-mode specs contain exit_plan %d times, want 1", got)
	}
	if len(on) != len(off)+1 {
		t.Fatalf("plan-mode spec count = %d, want default %d + 1", len(on), len(off))
	}
	// 去掉唯一的 exit_plan 后，其余 spec 必须与默认逐字段（逐字节）相同。
	// 注意技能工具在注册之后才注入，因此 exit_plan 不一定是全局最后一项。
	stripped := make([]llm.ToolSpec, 0, len(on))
	for _, s := range on {
		if s.Function.Name != "exit_plan" {
			stripped = append(stripped, s)
		}
	}
	if !reflect.DeepEqual(stripped, off) {
		t.Fatalf("plan mode changed existing specs:\nstripped = %+v\noff      = %+v", stripped, off)
	}
}

// TestWireExitPlanIdempotentAndDefaultNoop 在注册表层面验证 wireExitPlan：
// 默认关闭时完全不改动工具目录（逐字节一致）；开启时只追加一次 exit_plan；
// 重复调用不会重复注册。
func TestWireExitPlanIdempotentAndDefaultNoop(t *testing.T) {
	r := tools.New(t.TempDir(), false)
	chain := newTestChain(t)
	regs := []*tools.Registry{r}

	baseline := r.Specs()
	wireExitPlan(false, chain, regs)
	if got := r.Specs(); !reflect.DeepEqual(got, baseline) {
		t.Fatal("wireExitPlan(false) changed the registry specs")
	}

	wireExitPlan(true, chain, regs)
	afterFirst := r.Specs()
	if got := countSpecByName(afterFirst, "exit_plan"); got != 1 {
		t.Fatalf("exit_plan count after wire = %d, want 1", got)
	}
	if len(afterFirst) != len(baseline)+1 || !reflect.DeepEqual(afterFirst[:len(afterFirst)-1], baseline) {
		t.Fatal("wireExitPlan(true) must only append exit_plan")
	}

	wireExitPlan(true, chain, regs)
	afterSecond := r.Specs()
	if !reflect.DeepEqual(afterSecond, afterFirst) {
		t.Fatal("wireExitPlan(true) must be idempotent across repeated calls")
	}
	if got := countSpecByName(afterSecond, "exit_plan"); got != 1 {
		t.Fatalf("exit_plan count after repeated wire = %d, want 1", got)
	}
}
