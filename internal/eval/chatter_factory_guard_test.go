package eval

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件守护 runOne 的 ChatterFactory 注入接缝：
//  1. 默认（factory 为 nil）时仍走 Scripted，脚本化断言照常生效，golden 行为不变；
//  2. 注入假 provider 时确实被使用，请求到达、计数包装与 Expect.Turns 评分一并生效；
//  3. runOne 不改写传入的 Scenario / Options 及其字段。
//
// 所有断言都是内存中的，不联网、不写 golden/testdata。

// chatterFactoryChatter 是注入用假 provider：记录每次 Chat 的模型与消息，返回固定答复。
type chatterFactoryChatter struct {
	calls    int
	models   []string
	messages [][]llm.Message
	reply    string
}

var _ llm.Chatter = (*chatterFactoryChatter)(nil)

func (f *chatterFactoryChatter) Chat(_ context.Context, model string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	f.calls++
	f.models = append(f.models, model)
	f.messages = append(f.messages, append([]llm.Message(nil), messages...))
	return llm.Message{Role: "assistant", Content: f.reply}, nil
}

// chatterFactoryRun 是确定性的 RunFunc：调用注入的 Chatter 固定次数并回传最终文本。
// 它把工作区复制到 probe 供测试断言，但从不修改任何入参。
func chatterFactoryRun(probe *Workspace, turns int) RunFunc {
	return func(ctx context.Context, _ Scenario, workspace Workspace) (Trace, error) {
		*probe = workspace
		var trace Trace
		messages := []llm.Message{
			{Role: "system", Content: workspace.System},
			{Role: "user", Content: workspace.Prompt},
		}
		for i := 0; i < turns; i++ {
			reply, err := workspace.Chatter.Chat(ctx, workspace.Model, messages, nil, nil)
			if err != nil {
				return trace, err
			}
			trace.Text = reply.Content
			messages = append(messages, reply)
		}
		return trace, nil
	}
}

// TestRunOneDefaultsToScriptedWhenNoChatterFactory 验证默认路径逐字节不变：
// 未注入 factory 时工作区拿到 *Scripted，脚本的 expect_request_contains 真的被检查，
// 回复文本与轮数来自脚本，且场景字段未被改写。
func TestRunOneDefaultsToScriptedWhenNoChatterFactory(t *testing.T) {
	scenario := Scenario{
		Name:   "default provider",
		Model:  "m-default",
		System: "system hint",
		Prompt: "the magic needle",
		Expect: Expect{TextContains: []string{"scripted-answer"}, Turns: intPtr(1)},
		Provider: []ScriptTurn{{
			ExpectRequestContains: []string{"the magic needle"},
			Respond:               Respond{Text: "scripted-answer"},
		}},
	}
	original := scenario

	var seen Workspace
	got := runOne(context.Background(), 1, scenario, chatterFactoryRun(&seen, 1), Options{})

	if got.Reason != "" || !got.Passed {
		t.Fatalf("默认路径不应失败：%+v", got)
	}
	if got.Text != "scripted-answer" || got.Turns != 1 {
		t.Fatalf("默认路径结果漂移：%+v", got)
	}
	scripted, ok := seen.Chatter.(*Scripted)
	if !ok {
		t.Fatalf("默认 Chatter 应为 *Scripted，实际 %T", seen.Chatter)
	}
	if scripted.Calls() != 1 {
		t.Fatalf("Scripted 调用次数 = %d，want 1", scripted.Calls())
	}
	if violations := scripted.Violations(); len(violations) != 0 {
		t.Fatalf("默认脚本化 provider 出现违例：%v", violations)
	}
	if seen.Prompt != scenario.Prompt || seen.System != scenario.System || seen.Model != scenario.Model {
		t.Fatalf("工作区入参被改写：%+v", seen)
	}
	if !reflect.DeepEqual(scenario, original) {
		t.Fatalf("runOne 改写了入参场景：%+v", scenario)
	}

	// 反向对照：脚本期望的 needle 不在请求中时必须失败，证明默认路径确实执行了脚本化断言。
	missing := scenario
	missing.Prompt = "no needle here"
	missing.Expect = Expect{}
	var seenMissing Workspace
	gotMissing := runOne(context.Background(), 2, missing, chatterFactoryRun(&seenMissing, 1), Options{})
	if gotMissing.Passed {
		t.Fatalf("缺少 needle 的场景不应通过：%+v", gotMissing)
	}
	if !strings.Contains(gotMissing.Reason, "does not contain") {
		t.Fatalf("失败原因应来自脚本化断言，got %q", gotMissing.Reason)
	}
}

// TestRunOneInjectedFactoryDrivesFakeProvider 验证注入路径：factory 每场景只调用一次且拿到原场景，
// 假 provider 的请求确实到达，计数包装器让 Turns 与 Expect.Turns 评分对真实 provider 生效。
func TestRunOneInjectedFactoryDrivesFakeProvider(t *testing.T) {
	fake := &chatterFactoryChatter{reply: "factory-answer"}
	scenario := Scenario{
		Name:     "live provider",
		Model:    "m-live",
		System:   "system hint",
		Prompt:   "question from prompt",
		Provider: []ScriptTurn{{Respond: Respond{Text: "scripted-answer"}}},
		Expect:   Expect{TextContains: []string{"factory-answer"}, Turns: intPtr(2)},
	}
	original := scenario

	factoryCalls := 0
	var gotScenario Scenario
	factory := func(s Scenario) llm.Chatter {
		factoryCalls++
		gotScenario = s
		return fake
	}
	opts := Options{ChatterFactory: factory}

	var seen Workspace
	got := runOne(context.Background(), 7, scenario, chatterFactoryRun(&seen, 2), opts)

	if factoryCalls != 1 {
		t.Fatalf("ChatterFactory 调用次数 = %d，want 1", factoryCalls)
	}
	if !reflect.DeepEqual(gotScenario, original) {
		t.Fatalf("工厂收到的场景与入参不一致：\n got: %+v\nwant: %+v", gotScenario, original)
	}
	if !reflect.DeepEqual(scenario, original) {
		t.Fatalf("runOne 改写了入参场景：%+v", scenario)
	}
	if opts.ChatterFactory == nil {
		t.Fatal("runOne 改写了入参 Options")
	}
	if got.Reason != "" || !got.Passed {
		t.Fatalf("注入路径不应失败：%+v", got)
	}
	if got.Text != "factory-answer" || got.Turns != 2 {
		t.Fatalf("注入路径结果不符：%+v", got)
	}

	// 注入的 provider 真正被调用，而不是回退到脚本。
	if fake.calls != 2 {
		t.Fatalf("假 provider 的 Chat 调用次数 = %d，want 2", fake.calls)
	}
	if !reflect.DeepEqual(fake.models, []string{"m-live", "m-live"}) {
		t.Fatalf("请求未携带正确模型：%v", fake.models)
	}
	for i, messages := range fake.messages {
		joined := strings.Join(flatten(messages), "\n")
		if !strings.Contains(joined, scenario.System) || !strings.Contains(joined, scenario.Prompt) {
			t.Errorf("第 %d 轮请求缺少 system/prompt：%q", i+1, joined)
		}
	}

	// 计数包装必须生效：Workspace 里是 *countingChatter，Calls 与结果 Turns 一致。
	counting, ok := seen.Chatter.(*countingChatter)
	if !ok {
		t.Fatalf("注入的 Chatter 应被计数包装，实际 %T", seen.Chatter)
	}
	if counting.Calls() != got.Turns {
		t.Fatalf("计数包装 Calls=%d，结果 Turns=%d", counting.Calls(), got.Turns)
	}
	if len(counting.Violations()) != 0 {
		t.Fatalf("真实 provider 的违规列表应恒为空：%v", counting.Violations())
	}
}

// TestLiveChatterAdapterReusesInstance 验证便捷适配器：同一实例被复用于每个场景。
func TestLiveChatterAdapterReusesInstance(t *testing.T) {
	fake := &chatterFactoryChatter{reply: "ok"}
	factory := LiveChatterAdapter(fake)

	if got := factory(Scenario{Name: "a"}); got != fake {
		t.Fatalf("适配器未复用同一实例：%T", got)
	}
	if got := factory(Scenario{Name: "b"}); got != fake {
		t.Fatalf("适配器未复用同一实例：%T", got)
	}
}
