package agent

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// 本文件守护 R51 研究 #17 的接线：appendTool 在 opt-in 打开时把工具输出中的
// 凭据清洗掉再回灌模型。它复用 tool_result_wrapping_wire_test.go 里的
// wireEvent / wireRecorder（只保留与"逐字节不变"相关的字段），因此事件与记录
// 快照可以直接与基线做 reflect.DeepEqual 比较。
//
// 刻意不调用 t.Parallel()：这些是进程级全局状态，必须与同包其它测试串行。

const (
	redactAPIKey = "sk-abcdef1234567890"
	redactDBPass = "myP@ss!"
	// 同时含通用凭据形态（sk-*）与任意口令（DB_PASSWORD=...）的原始输出。
	redactRawOutput = "OPENAI_API_KEY=" + redactAPIKey + "\nDB_PASSWORD=" + redactDBPass + "\nPLAIN=visible-value\n"
)

// redactRecorder 实现 agent.Recorder，真正捕获 s.record 写入的 tool 记录。
// 注意：tool_result_wrapping_wire_test.go 里的 wireRecorder 用的是小写 append，
// 并不满足 Recorder 接口（其记录断言实际是空对空比较），这里不复用它。
type redactRecorder struct {
	mu   sync.Mutex
	rows []wireRecord
}

func (r *redactRecorder) Append(kind string, payload any) error {
	out := ""
	if m, ok := payload.(map[string]string); ok {
		out = m["output"]
	}
	r.mu.Lock()
	r.rows = append(r.rows, wireRecord{kind: kind, output: out})
	r.mu.Unlock()
	return nil
}

func (r *redactRecorder) snapshot() []wireRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wireRecord(nil), r.rows...)
}

// runOutputRedactionScenario 按给定的脱敏/封装开关跑一次单工具批次，
// 返回工具消息、事件快照与记录快照。output 是假工具返回的原始输出。
func runOutputRedactionScenario(redact, wrap bool, output string) ([]llm.Message, []wireEvent, []wireRecord) {
	SetOutputRedaction(redact)
	SetToolResultWrapping(wrap)

	tools := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return output, nil
	}}
	var events []wireEvent
	emit := func(e Event) { events = append(events, wireEvent{kind: e.Kind, output: e.Output}) }
	rec := &redactRecorder{}
	s := newRunState(Options{Tools: tools, Record: rec}, "prompt", emit)
	s.runTools(context.Background(), 1, []llm.ToolCall{toolCall("c1", "read", `{}`)})

	var msgs []llm.Message
	for _, m := range s.messages {
		if m.Role == "tool" {
			msgs = append(msgs, m)
		}
	}
	return msgs, events, rec.snapshot()
}

// TestOutputRedactionRedactsSecretsAndKeepsUnwrap 是验收主用例：开启脱敏后
// 回灌消息不含两个明文值、仍能被 UnwrapToolResult 解封；事件与会话记录保持
// 原始字节（含明文），证明清洗只作用于模型可见的消息内容。
func TestOutputRedactionRedactsSecretsAndKeepsUnwrap(t *testing.T) {
	t.Cleanup(func() {
		SetOutputRedaction(false)
		SetToolResultWrapping(false)
	})

	baseMsgs, baseEvents, baseRecords := runOutputRedactionScenario(false, false, redactRawOutput)
	gotMsgs, gotEvents, gotRecords := runOutputRedactionScenario(true, true, redactRawOutput)

	if len(baseMsgs) != 1 || len(gotMsgs) != 1 {
		t.Fatalf("工具消息数异常: 基线=%d, 开启=%d", len(baseMsgs), len(gotMsgs))
	}
	// 基线必须真的带明文，否则"开启后不含明文"是假绿。
	if !strings.Contains(baseMsgs[0].Content, redactAPIKey) || !strings.Contains(baseMsgs[0].Content, redactDBPass) {
		t.Fatalf("基线消息未含明文，测试无意义: %q", baseMsgs[0].Content)
	}
	if gotMsgs[0].Role != baseMsgs[0].Role || gotMsgs[0].ToolCallID != baseMsgs[0].ToolCallID {
		t.Fatalf("Role/ToolCallID 被改动: %+v -> %+v", baseMsgs[0], gotMsgs[0])
	}

	content := gotMsgs[0].Content
	if strings.Contains(content, redactAPIKey) || strings.Contains(content, redactDBPass) {
		t.Fatalf("开启脱敏后消息仍含明文: %q", content)
	}
	if !IsWrapped(content) {
		t.Fatalf("开启后消息未被封装: %q", content)
	}
	unwrapped, ok := UnwrapToolResult(content)
	if !ok {
		t.Fatalf("开启后消息无法解封: %q", content)
	}
	if strings.Contains(unwrapped.Content, redactAPIKey) || strings.Contains(unwrapped.Content, redactDBPass) {
		t.Fatalf("解封正文仍含明文: %q", unwrapped.Content)
	}
	if unwrapped.Tool != "read" {
		t.Fatalf("解封来源工具 = %q, want read", unwrapped.Tool)
	}
	if !strings.Contains(unwrapped.Content, "PLAIN=visible-value") {
		t.Fatalf("非机密行被误删: %q", unwrapped.Content)
	}

	// EventToolEnd / tool 记录是给前端与 session 的持久化字节，必须与基线完全相同。
	if !reflect.DeepEqual(gotEvents, baseEvents) {
		t.Fatalf("事件字节改变:\ngot : %+v\nwant: %+v", gotEvents, baseEvents)
	}
	if !reflect.DeepEqual(gotRecords, baseRecords) {
		t.Fatalf("记录字节改变:\ngot : %+v\nwant: %+v", gotRecords, baseRecords)
	}
	var rawInRecords bool
	for _, r := range baseRecords {
		if strings.Contains(r.output, redactAPIKey) && strings.Contains(r.output, redactDBPass) {
			rawInRecords = true
		}
	}
	if !rawInRecords {
		t.Fatalf("基线记录未保存原始明文，测试无意义: %+v", baseRecords)
	}
}

// TestOutputRedactionOffByteIdentical 锁定关闭态：封装开/关两种配置下，
// 两次运行都在消息、事件与记录上逐字节一致，且消息保留两个明文值。
func TestOutputRedactionOffByteIdentical(t *testing.T) {
	t.Cleanup(func() {
		SetOutputRedaction(false)
		SetToolResultWrapping(false)
	})
	for _, wrap := range []bool{false, true} {
		firstMsgs, firstEvents, firstRecords := runOutputRedactionScenario(false, wrap, redactRawOutput)
		secondMsgs, secondEvents, secondRecords := runOutputRedactionScenario(false, wrap, redactRawOutput)
		if !reflect.DeepEqual(firstMsgs, secondMsgs) ||
			!reflect.DeepEqual(firstEvents, secondEvents) ||
			!reflect.DeepEqual(firstRecords, secondRecords) {
			t.Fatalf("封装=%v 时关闭态两次运行不一致", wrap)
		}
		for _, m := range firstMsgs {
			if !strings.Contains(m.Content, redactAPIKey) || !strings.Contains(m.Content, redactDBPass) {
				t.Fatalf("封装=%v 时关闭态消息丢了明文: %q", wrap, m.Content)
			}
		}
	}
}

// TestOutputRedactionToggleRestoresBaseline 验证开→关切换后回到与初始基线
// 逐字节相同的状态；开启态只应改变消息内容，事件/记录始终不变。
func TestOutputRedactionToggleRestoresBaseline(t *testing.T) {
	t.Cleanup(func() {
		SetOutputRedaction(false)
		SetToolResultWrapping(false)
	})

	baseMsgs, baseEvents, baseRecords := runOutputRedactionScenario(false, false, redactRawOutput)
	onMsgs, onEvents, onRecords := runOutputRedactionScenario(true, false, redactRawOutput)
	if reflect.DeepEqual(baseMsgs, onMsgs) {
		t.Fatal("开启脱敏后消息内容未变化")
	}
	if !reflect.DeepEqual(onEvents, baseEvents) || !reflect.DeepEqual(onRecords, baseRecords) {
		t.Fatal("开启脱敏不应改变事件/记录")
	}

	backMsgs, backEvents, backRecords := runOutputRedactionScenario(false, false, redactRawOutput)
	if !reflect.DeepEqual(baseMsgs, backMsgs) ||
		!reflect.DeepEqual(baseEvents, backEvents) ||
		!reflect.DeepEqual(baseRecords, backRecords) {
		t.Fatal("关闭后未恢复到与初始基线逐字节相同的状态")
	}
}

// TestOutputRedactionCJKPreserved 校验脱敏只替换机密值：CJK 与 emoji 行
// 逐字节保留，输出仍是合法 UTF-8，不会被从多字节字符中间截断。
func TestOutputRedactionCJKPreserved(t *testing.T) {
	t.Cleanup(func() {
		SetOutputRedaction(false)
		SetToolResultWrapping(false)
	})

	const cjkOutput = "路径=保留值\nDB_PASSWORD=myP@ss!\n说明=你好，世界 🚀\n"
	baseMsgs, _, _ := runOutputRedactionScenario(false, false, cjkOutput)
	gotMsgs, _, _ := runOutputRedactionScenario(true, false, cjkOutput)
	if len(baseMsgs) != 1 || len(gotMsgs) != 1 {
		t.Fatalf("工具消息数异常: 基线=%d, 开启=%d", len(baseMsgs), len(gotMsgs))
	}
	if !strings.Contains(baseMsgs[0].Content, "说明=你好，世界 🚀") {
		t.Fatalf("基线未含 CJK 行，测试无意义: %q", baseMsgs[0].Content)
	}

	content := gotMsgs[0].Content
	if strings.Contains(content, redactDBPass) {
		t.Fatalf("CJK 场景下机密值未被脱敏: %q", content)
	}
	if !utf8.ValidString(content) {
		t.Fatalf("脱敏后输出不是合法 UTF-8: %q", content)
	}
	gotLines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for _, want := range []string{"路径=保留值", "说明=你好，世界 🚀"} {
		found := false
		for _, got := range gotLines {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CJK 行未逐字节保留: want %q, content %q", want, content)
		}
	}
}

// TestOutputRedactionConcurrentToggleGuard 并发反复切换开关，同时驱动消费端
// appendTool 读取该全局开关；所有写读都经 atomic.Bool，-race 下不应出现数据竞争。
func TestOutputRedactionConcurrentToggleGuard(t *testing.T) {
	t.Cleanup(func() {
		SetOutputRedaction(false)
		SetToolResultWrapping(false)
	})

	const (
		writers = 4
		readers = 4
		rounds  = 200
	)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				SetOutputRedaction((i+w)%2 == 0)
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				_ = outputRedactionEnabled()
				s := &runState{}
				s.appendTool(toolCall("c", "read", `{}`), redactRawOutput)
			}
		}()
	}
	close(start)
	wg.Wait()
}
