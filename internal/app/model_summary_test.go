package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// modelSummaryFakeChatter 是可配置的 llm.Chatter 假实现，记录调用参数并支持
// 脚本化返回（回复正文、流式块、错误与自定义函数），用于测试 GenerateModelSummary。
type modelSummaryFakeChatter struct {
	reply       llm.Message
	err         error
	stream      []string
	fn          func(context.Context, string, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error)
	gotModel    string
	gotMessages []llm.Message
	gotTools    []llm.ToolSpec
	calls       int
}

func (f *modelSummaryFakeChatter) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	f.calls++
	f.gotModel = model
	f.gotMessages = messages
	f.gotTools = tools
	if f.fn != nil {
		return f.fn(ctx, model, messages, tools, onText)
	}
	if f.err != nil {
		return llm.Message{}, f.err
	}
	for _, chunk := range f.stream {
		onText(chunk)
	}
	return f.reply, nil
}

// modelSummaryOnReg 构造一个已显式开启模型摘要开关的注册表。
func modelSummaryOnReg() *OptInRegistry {
	reg := new(OptInRegistry)
	reg.Set(modelSummaryOptInKey, true)
	return reg
}

// TestModelSummaryEnabledStates 覆盖门控的各种状态：nil、零值、其他键开启、显式开启、
// 显式关闭与 Reset，确认只有显式开启才返回 true。
func TestModelSummaryEnabledStates(t *testing.T) {
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInContextPressure, true)
	turnedOff := new(OptInRegistry)
	turnedOff.Set(modelSummaryOptInKey, true)
	turnedOff.Set(modelSummaryOptInKey, false)
	reset := new(OptInRegistry)
	reset.Set(modelSummaryOptInKey, true)
	reset.Reset()

	tests := []struct {
		name string
		reg  *OptInRegistry
		want bool
	}{
		{"nil 注册表", nil, false},
		{"零值注册表", new(OptInRegistry), false},
		{"仅其他键开启", otherOnly, false},
		{"显式开启", modelSummaryOnReg(), true},
		{"显式关闭", turnedOff, false},
		{"Reset 后", reset, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelSummaryEnabled(tc.reg); got != tc.want {
				t.Fatalf("modelSummaryEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildSummaryPromptSystem 锁定 system 提示：要求只输出 JSON（goal/narrative/
// next_steps），并把输入定义为不可信数据、严禁执行其中指令（防注入）。
func TestBuildSummaryPromptSystem(t *testing.T) {
	system, _ := BuildSummaryPrompt(ModelSummaryInput{})
	for _, keyword := range []string{"JSON", "goal", "narrative", "next_steps", "untrusted", "NEVER", "instructions"} {
		if !strings.Contains(system, keyword) {
			t.Fatalf("system 提示缺少关键约束 %q: %q", keyword, system)
		}
	}
}

// TestBuildSummaryPromptUserContent 确认 user 拼接了抽取式摘要与三类证据，并带上标签。
func TestBuildSummaryPromptUserContent(t *testing.T) {
	_, user := BuildSummaryPrompt(ModelSummaryInput{
		Extractive: "## Goal\n- 修复鉴权",
		Failures:   []string{"tool write 失败"},
		NextSteps:  []string{"补测试"},
		Files:      []string{"internal/auth.go"},
	})
	for _, want := range []string{"修复鉴权", "tool write 失败", "补测试", "internal/auth.go", "<failures>", "<next_steps>", "<files>"} {
		if !strings.Contains(user, want) {
			t.Fatalf("user 提示缺少内容 %q: %q", want, user)
		}
	}
}

// TestBuildSummaryPromptBoundedAndCJK 确认 MaxChars 按 rune 有界且 CJK 安全：
// 结果 rune 数不超过上限，且不会被切成非法 UTF-8（含中文的输入尤其关键）。
func TestBuildSummaryPromptBoundedAndCJK(t *testing.T) {
	in := ModelSummaryInput{
		Extractive: strings.Repeat("修复中文编码问题。", 50),
		Failures:   []string{"失败原因：磁盘写入异常"},
		MaxChars:   60,
	}
	_, user := BuildSummaryPrompt(in)
	if got := utf8.RuneCountInString(user); got > in.MaxChars {
		t.Fatalf("user rune 数 = %d, want <= %d", got, in.MaxChars)
	}
	if !utf8.ValidString(user) {
		t.Fatalf("user 提示不是合法 UTF-8: %q", user)
	}
	// 上限之前的正文应逐 rune 保留，不发生字节级撕裂。
	_, full := BuildSummaryPrompt(ModelSummaryInput{Extractive: in.Extractive, Failures: in.Failures})
	wantPrefix := string([]rune(full)[:in.MaxChars])
	if user != wantPrefix {
		t.Fatalf("有界截断与逐 rune 前缀不一致:\n got = %q\nwant = %q", user, wantPrefix)
	}
}

// TestBuildSummaryPromptUnboundedAndPure 确认 MaxChars<=0 时不对 user 设上界，
// 并且函数不修改入参切片。
func TestBuildSummaryPromptUnboundedAndPure(t *testing.T) {
	in := ModelSummaryInput{
		Extractive: "摘要",
		Failures:   []string{"失败一", "失败二"},
		NextSteps:  []string{"步骤一"},
		Files:      []string{"a.go", "b.go"},
		MaxChars:   0,
	}
	failuresBefore := append([]string(nil), in.Failures...)
	stepsBefore := append([]string(nil), in.NextSteps...)
	filesBefore := append([]string(nil), in.Files...)

	system, user := BuildSummaryPrompt(in)
	if system == "" || user == "" {
		t.Fatal("system/user 提示不应为空")
	}
	largeInput := in
	largeInput.MaxChars = 100000
	if _, userPositive := BuildSummaryPrompt(largeInput); userPositive != user {
		t.Fatal("MaxChars<=0 时不应截断 user 提示")
	}
	for i := range in.Failures {
		if in.Failures[i] != failuresBefore[i] {
			t.Fatal("BuildSummaryPrompt 修改了 Failures 入参")
		}
	}
	for i := range in.NextSteps {
		if in.NextSteps[i] != stepsBefore[i] {
			t.Fatal("BuildSummaryPrompt 修改了 NextSteps 入参")
		}
	}
	for i := range in.Files {
		if in.Files[i] != filesBefore[i] {
			t.Fatal("BuildSummaryPrompt 修改了 Files 入参")
		}
	}
}

// TestParseModelSummaryForms 覆盖模型回复的多种形态：裸 JSON、```json 包裹、
// 带散文的包裹、散文中的对象，均应解析出叙事与步骤。
func TestParseModelSummaryForms(t *testing.T) {
	tests := []struct {
		name  string
		reply string
	}{
		{"裸 JSON", `{"goal":"g","narrative":"修复了鉴权逻辑","next_steps":["补测试"]}`},
		{"json 围栏", "```json\n{\"goal\":\"g\",\"narrative\":\"修复了鉴权逻辑\",\"next_steps\":[\"补测试\"]}\n```"},
		{"无语言围栏", "```\n{\"narrative\":\"修复了鉴权逻辑\",\"next_steps\":[\"补测试\"]}\n```"},
		{"散文加围栏", "好的，摘要如下：\n```json\n{\"narrative\":\"修复了鉴权逻辑\",\"next_steps\":[\"补测试\"]}\n```\n以上。"},
		{"散文中的对象", "结果：{\"narrative\":\"修复了鉴权逻辑\",\"next_steps\":[\"补测试\"]} 结束"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			narrative, steps, ok := ParseModelSummary(tc.reply)
			if !ok {
				t.Fatal("有效的 JSON 回复应解析成功")
			}
			if narrative != "修复了鉴权逻辑" {
				t.Fatalf("narrative = %q", narrative)
			}
			if len(steps) != 1 || steps[0] != "补测试" {
				t.Fatalf("nextSteps = %q", steps)
			}
		})
	}
}

// TestParseModelSummaryRejectsInvalid 覆盖失败形态：坏 JSON、非对象（数组/null）、
// 只有 goal 没有叙事内容，均应 ok=false 以便调用方回落。
func TestParseModelSummaryRejectsInvalid(t *testing.T) {
	tests := []struct {
		name  string
		reply string
	}{
		{"坏 JSON", `{"narrative":`},
		{"空串", ""},
		{"纯散文", "这是一段没有 JSON 的回复"},
		{"数组", `["narrative"]`},
		{"null", `null`},
		{"无叙事内容", `{"goal":"g","narrative":"","next_steps":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			narrative, steps, ok := ParseModelSummary(tc.reply)
			if ok || narrative != "" || steps != nil {
				t.Fatalf("无效回复应回落，得到 (%q, %q, %v)", narrative, steps, ok)
			}
		})
	}
}

// TestParseModelSummaryBoundsAndCJK 确认叙事按 rune 截断、步骤按条数与 rune 有界，
// 且 CJK 截断不产生非法 UTF-8；返回的步骤是新建切片，修改它不影响再次解析的结果。
func TestParseModelSummaryBoundsAndCJK(t *testing.T) {
	long := strings.Repeat("很长的一段中文叙事。", 800)
	steps := make([]string, modelSummaryMaxSteps+5)
	for i := range steps {
		// 每条加唯一前缀，避免去重后条数坍缩；正文仍远超单条 rune 上限。
		steps[i] = string(rune('A'+i)) + strings.Repeat("步骤", 200)
	}
	reply, err := jsonReply(long, steps)
	if err != nil {
		t.Fatal(err)
	}

	narrative, gotSteps, ok := ParseModelSummary(reply)
	if !ok {
		t.Fatal("应解析成功")
	}
	if got := utf8.RuneCountInString(narrative); got != modelSummaryMaxNarrativeRunes {
		t.Fatalf("narrative rune 数 = %d, want %d", got, modelSummaryMaxNarrativeRunes)
	}
	if !utf8.ValidString(narrative) {
		t.Fatal("narrative 截断产生了非法 UTF-8")
	}
	if len(gotSteps) != modelSummaryMaxSteps {
		t.Fatalf("步骤条数 = %d, want %d", len(gotSteps), modelSummaryMaxSteps)
	}
	for _, step := range gotSteps {
		if got := utf8.RuneCountInString(step); got != modelSummaryMaxStepRunes {
			t.Fatalf("步骤 rune 数 = %d, want %d", got, modelSummaryMaxStepRunes)
		}
	}

	// 返回值是新建切片：修改它不得影响再次解析。
	gotSteps[0] = "被修改"
	_, again, _ := ParseModelSummary(reply)
	if again[0] == "被修改" {
		t.Fatal("ParseModelSummary 返回了与内部共享的切片")
	}
}

// jsonReply 用标准库拼一个模型 JSON 回复，避免测试里手写转义。
func jsonReply(narrative string, steps []string) (string, error) {
	payload := modelSummaryJSON{Narrative: narrative, NextSteps: steps}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// TestApplyModelSummaryAppendsAndPreservesAuthoritative 确认叙事作为增量段追加，
// 绝不覆盖抽取式摘要里的 files/failures 等权威字段。
func TestApplyModelSummaryAppendsAndPreservesAuthoritative(t *testing.T) {
	extractive := "## Goal\n- 修复鉴权\n\n## Files changed\n- internal/auth.go\n\n## Failures\n- write 超时\n"
	got := ApplyModelSummary(extractive, "补充：定位到并发竞态", []string{"加互斥锁"})
	if !strings.HasPrefix(got, extractive) {
		t.Fatalf("增量段应追加在权威内容之后，得到: %q", got)
	}
	for _, want := range []string{modelSummaryMarker, "## Model narrative", "补充：定位到并发竞态", "## Model next steps", "- 加互斥锁"} {
		if !strings.Contains(got, want) {
			t.Fatalf("结果缺少 %q: %q", want, got)
		}
	}
	for _, authoritative := range []string{"## Files changed\n- internal/auth.go", "## Failures\n- write 超时"} {
		if !strings.Contains(got, authoritative) {
			t.Fatalf("权威字段被覆盖: %q", authoritative)
		}
	}
}

// TestApplyModelSummaryIdempotent 锁定幂等：对结果再次以相同入参调用不改变字节；
// 以新叙事调用只替换标记段，旧叙事消失而权威前缀不变。
func TestApplyModelSummaryIdempotent(t *testing.T) {
	extractive := "## Goal\n- 修复鉴权\n"
	first := ApplyModelSummary(extractive, "旧叙事", []string{"旧步骤"})
	second := ApplyModelSummary(first, "旧叙事", []string{"旧步骤"})
	if first != second {
		t.Fatalf("相同入参结果不稳定:\n first = %q\nsecond = %q", first, second)
	}
	if strings.Count(second, modelSummaryMarker) != 1 {
		t.Fatalf("标记应恰好出现一次，实际 %d 次", strings.Count(second, modelSummaryMarker))
	}

	third := ApplyModelSummary(first, "新叙事", []string{"新步骤"})
	if strings.Contains(third, "旧叙事") || strings.Contains(third, "旧步骤") {
		t.Fatalf("新叙事未替换旧段: %q", third)
	}
	if !strings.HasPrefix(third, extractive) {
		t.Fatalf("替换时覆盖了权威前缀: %q", third)
	}
	if strings.Count(third, modelSummaryMarker) != 1 {
		t.Fatalf("标记应恰好出现一次，实际 %d 次", strings.Count(third, modelSummaryMarker))
	}
}

// TestApplyModelSummaryAuthoritativeQuoteOfMarker 确认权威内容即便在独立一行引用了标记
// 文本，也不会被当作增量段截断（替换只针对最后一次出现的标记）。
func TestApplyModelSummaryAuthoritativeQuoteOfMarker(t *testing.T) {
	extractive := "## Failures\n- 日志包含 " + modelSummaryMarker + " 字面量\n"
	got := ApplyModelSummary(extractive, "新叙事", nil)
	if !strings.HasPrefix(got, extractive) {
		t.Fatalf("权威内容中的标记引用被截断: %q", got)
	}
	if !strings.Contains(got, "新叙事") {
		t.Fatalf("未追加新叙事: %q", got)
	}
}

// TestApplyModelSummaryEmptyIsNoop 确认叙事与步骤都为空时原样返回，便于失败回落。
func TestApplyModelSummaryEmptyIsNoop(t *testing.T) {
	extractive := "## Goal\n- 修复鉴权\n"
	for _, got := range []string{
		ApplyModelSummary(extractive, "", nil),
		ApplyModelSummary(extractive, "   ", []string{"  ", ""}),
	} {
		if got != extractive {
			t.Fatalf("空增量应原样返回: got %q, want %q", got, extractive)
		}
	}
}

// TestApplyModelSummaryCJKAndPure 确认 CJK 叙事逐 rune 保留、超长部分被安全截断，
// 并且函数不修改入参切片与字符串。
func TestApplyModelSummaryCJKAndPure(t *testing.T) {
	extractive := "## 目标\n- 修复中文编码\n"
	narrative := strings.Repeat("定位到中文编码问题。", 500)
	steps := []string{"补充中文步骤", strings.Repeat("很长", 300)}
	stepsBefore := append([]string(nil), steps...)

	got := ApplyModelSummary(extractive, narrative, steps)
	if !strings.HasPrefix(got, extractive) {
		t.Fatal("CJK 权威前缀未被保留")
	}
	if !utf8.ValidString(got) {
		t.Fatal("结果包含非法 UTF-8")
	}
	if got := utf8.RuneCountInString(extractNarrativeSection(got)); got != modelSummaryMaxNarrativeRunes {
		t.Fatalf("叙事 rune 数 = %d, want %d", got, modelSummaryMaxNarrativeRunes)
	}
	for i := range steps {
		if steps[i] != stepsBefore[i] {
			t.Fatal("ApplyModelSummary 修改了 nextSteps 入参")
		}
	}
	if extractive != "## 目标\n- 修复中文编码\n" {
		t.Fatal("ApplyModelSummary 修改了 extractive 入参")
	}
}

// extractNarrativeSection 取出模型叙事段的正文，供断言截断长度使用。
func extractNarrativeSection(text string) string {
	start := strings.Index(text, "## Model narrative\n")
	if start < 0 {
		return ""
	}
	body := text[start+len("## Model narrative\n"):]
	if end := strings.Index(body, "\n## "); end >= 0 {
		return strings.TrimSpace(body[:end])
	}
	return strings.TrimSpace(body)
}

// TestGenerateModelSummarySuccess 确认成功路径：单次无工具调用、传入 system/user 两条
// 消息、透传 model，并把回复正文解析为叙事与步骤。
func TestGenerateModelSummarySuccess(t *testing.T) {
	fake := &modelSummaryFakeChatter{
		reply: llm.Message{Role: "assistant", Content: `{"goal":"g","narrative":"模型叙事","next_steps":["模型步骤"]}`},
	}
	narrative, steps, err := GenerateModelSummary(context.Background(), fake, "test-model", ModelSummaryInput{
		Extractive: "权威摘要", Failures: []string{"失败"}, MaxChars: 5000,
	})
	if err != nil {
		t.Fatalf("GenerateModelSummary 返回错误: %v", err)
	}
	if narrative != "模型叙事" || len(steps) != 1 || steps[0] != "模型步骤" {
		t.Fatalf("结果不符: narrative=%q steps=%q", narrative, steps)
	}
	if fake.calls != 1 {
		t.Fatalf("chatter 调用次数 = %d, want 1", fake.calls)
	}
	if fake.gotModel != "test-model" {
		t.Fatalf("model 未透传: %q", fake.gotModel)
	}
	if len(fake.gotTools) != 0 {
		t.Fatalf("不应传工具规格，得到 %v", fake.gotTools)
	}
	if len(fake.gotMessages) != 2 || fake.gotMessages[0].Role != "system" || fake.gotMessages[1].Role != "user" {
		t.Fatalf("消息形态不符: %+v", fake.gotMessages)
	}
}

// TestGenerateModelSummaryUsesStream 确认当 reply.Content 为空时使用缓冲的流式输出。
func TestGenerateModelSummaryUsesStream(t *testing.T) {
	fake := &modelSummaryFakeChatter{
		stream: []string{"```json\n", `{"narrative":"流式叙事","next_steps":["流式步骤"]}`, "\n```"},
	}
	narrative, steps, err := GenerateModelSummary(context.Background(), fake, "m", ModelSummaryInput{})
	if err != nil {
		t.Fatalf("GenerateModelSummary 返回错误: %v", err)
	}
	if narrative != "流式叙事" || len(steps) != 1 || steps[0] != "流式步骤" {
		t.Fatalf("流式结果不符: narrative=%q steps=%q", narrative, steps)
	}
}

// TestGenerateModelSummaryFailures 覆盖失败与回落路径：nil chatter、chatter 报错、
// 空回复、非法 JSON、context 取消，均应返回 error 且不产出内容。
func TestGenerateModelSummaryFailures(t *testing.T) {
	sentinel := errors.New("提供方不可用")
	tests := []struct {
		name    string
		chatter llm.Chatter
		ctx     func() (context.Context, context.CancelFunc)
	}{
		{"nil chatter", nil, nil},
		{"chatter 报错", &modelSummaryFakeChatter{err: sentinel}, nil},
		{"空回复", &modelSummaryFakeChatter{reply: llm.Message{Content: "   "}}, nil},
		{"非法 JSON", &modelSummaryFakeChatter{reply: llm.Message{Content: "not json"}}, nil},
		{"context 已取消", &modelSummaryFakeChatter{fn: func(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			return llm.Message{}, ctx.Err()
		}}, func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctx != nil {
				var cancel context.CancelFunc
				ctx, cancel = tc.ctx()
				defer cancel()
			}
			narrative, steps, err := GenerateModelSummary(ctx, tc.chatter, "m", ModelSummaryInput{})
			if err == nil {
				t.Fatal("失败路径应返回 error")
			}
			if narrative != "" || steps != nil {
				t.Fatalf("失败路径不应产出内容: %q, %q", narrative, steps)
			}
			if tc.name == "chatter 报错" && !errors.Is(err, sentinel) {
				t.Fatalf("错误应被透传: %v", err)
			}
		})
	}
}
