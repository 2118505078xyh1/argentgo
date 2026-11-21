package agent

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// wireCJKMessages 返回含 CJK 与 emoji 的消息，供多个测试复用。
func wireCJKMessages() []SignalInput {
	return []SignalInput{
		{Role: "tool", ToolName: "编辑", Content: strings.Repeat("失败", 200)},
		{Role: "tool", ToolName: "emoji🔥", Content: "崩溃 🚀🚀 " + strings.Repeat("错误", 200)},
	}
}

// TestFailureSignalsForCompactionDisabledReturnsZero 验证默认关闭：
// 即便输入含明确失败信号，也返回零值证据（逐字节等于未接线时的行为）。
func TestFailureSignalsForCompactionDisabledReturnsZero(t *testing.T) {
	messages := []SignalInput{
		{Role: "tool", ToolName: "shell", Content: "[exit 2]", IsError: true},
		{Role: "user", Content: "下一步：修复构建"},
	}
	got := FailureSignalsForCompaction(messages, false)
	if want := (CompactionEvidence{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled=false 证据 = %+v, want 零值 %+v", got, want)
	}
	if got.Failures != nil || got.NextSteps != nil {
		t.Fatalf("enabled=false 不应分配切片：Failures=%v NextSteps=%v", got.Failures, got.NextSteps)
	}
}

// TestSignalsToEvidenceFieldMapping 验证三条字段映射与 Error 保持 false。
func TestSignalsToEvidenceFieldMapping(t *testing.T) {
	sigs := []FailureSignal{
		{Text: "compiling [exit 2]", Turn: 3, Tool: "shell", Kind: SignalKindExit},
		{Text: "读取失败", Turn: 7, Tool: "fs", Kind: SignalKindWord},
		{Text: "is_error", Turn: 9, Tool: "", Kind: SignalKindIsError},
	}
	got := SignalsToEvidence(sigs)
	want := CompactionEvidence{Failures: []Failure{
		{Turn: 3, Tool: "shell", Reason: "compiling [exit 2]", Error: false},
		{Turn: 7, Tool: "fs", Reason: "读取失败", Error: false},
		{Turn: 9, Tool: "", Reason: "is_error", Error: false},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SignalsToEvidence = %+v, want %+v", got, want)
	}
	if got.NextSteps != nil {
		t.Fatalf("SignalsToEvidence 不应产生 NextSteps: %v", got.NextSteps)
	}
}

// TestFailureSignalsForCompactionEnabledConverts 验证开启后的端到端提取与转换。
func TestFailureSignalsForCompactionEnabledConverts(t *testing.T) {
	messages := []SignalInput{
		{Role: "user", Content: "正常运行"},
		{Role: "tool", ToolName: "shell", Content: "build\n[exit 2]"},
		{Role: "tool", ToolName: "fs", Content: "读取失败"},
	}
	got := FailureSignalsForCompaction(messages, true)
	if len(got.Failures) != 2 {
		t.Fatalf("Failures 条数 = %d, want 2: %+v", len(got.Failures), got.Failures)
	}
	if f := got.Failures[0]; f.Turn != 1 || f.Tool != "shell" || f.Reason != "[exit 2]" {
		t.Errorf("Failures[0] = %+v, want turn=1 tool=shell reason=%q", f, "[exit 2]")
	}
	if f := got.Failures[1]; f.Turn != 2 || f.Tool != "fs" || f.Reason != "读取失败" {
		t.Errorf("Failures[1] = %+v, want turn=2 tool=fs reason=%q", f, "读取失败")
	}
}

// TestFailureSignalsForCompactionDedup 验证开启后按 Text 去重且保序。
// 两条相同失败文本（来自不同轮次）只保留首次出现的一条。
func TestFailureSignalsForCompactionDedup(t *testing.T) {
	messages := []SignalInput{
		{Role: "tool", ToolName: "shell", Content: "[exit 1]"},
		{Role: "tool", ToolName: "shell", Content: "[exit 1]"}, // 与第一条同 Text
		{Role: "tool", ToolName: "fs", Content: "读取失败"},
		{Role: "tool", ToolName: "shell", Content: "[exit 1]"}, // 再次重复
	}
	got := FailureSignalsForCompaction(messages, true)
	want := CompactionEvidence{Failures: []Failure{
		{Turn: 0, Tool: "shell", Reason: "[exit 1]"},
		{Turn: 2, Tool: "fs", Reason: "读取失败"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("去重结果 = %+v, want %+v", got, want)
	}
}

// TestFailureSignalsForCompactionCJK 验证 CJK / emoji 安全：
// 超长文本按 rune 截断且仍是合法 UTF-8，工具名原样保留。
func TestFailureSignalsForCompactionCJK(t *testing.T) {
	messages := wireCJKMessages()
	before := append([]SignalInput(nil), messages...)

	got := FailureSignalsForCompaction(messages, true)
	if len(got.Failures) != 2 {
		t.Fatalf("Failures 条数 = %d, want 2: %+v", len(got.Failures), got.Failures)
	}
	for i, f := range got.Failures {
		if !utf8.ValidString(f.Reason) {
			t.Errorf("Failures[%d].Reason 不是合法 UTF-8: %q", i, f.Reason)
		}
		if !strings.HasSuffix(f.Reason, "...") {
			t.Errorf("Failures[%d].Reason 应以 ... 结尾: %q", i, f.Reason)
		}
		if r := utf8.RuneCountInString(f.Reason); r != signalTextRunes+3 {
			t.Errorf("Failures[%d].Reason 符文数 = %d, want %d", i, r, signalTextRunes+3)
		}
		core := strings.TrimSuffix(f.Reason, "...")
		if !utf8.ValidString(core) {
			t.Errorf("Failures[%d].Reason 截断正文不是合法 UTF-8: %q", i, core)
		}
	}
	if got.Failures[0].Tool != "编辑" || got.Failures[1].Tool != "emoji🔥" {
		t.Errorf("工具名未原样保留: %q, %q", got.Failures[0].Tool, got.Failures[1].Tool)
	}
	if !reflect.DeepEqual(messages, before) {
		t.Fatalf("开启了 CJK 提取却修改了入参:\n got %+v\nwant %+v", messages, before)
	}
}

// TestSignalsToEvidenceNonMutating 验证纯类型转换不改入参、重复调用稳定。
func TestSignalsToEvidenceNonMutating(t *testing.T) {
	sigs := []FailureSignal{
		{Text: "A", Turn: 1, Tool: "shell", Kind: SignalKindExit},
		{Text: "B", Turn: 2, Tool: "fs", Kind: SignalKindWord},
	}
	before := append([]FailureSignal(nil), sigs...)

	first := SignalsToEvidence(sigs)
	second := SignalsToEvidence(sigs)

	if !reflect.DeepEqual(sigs, before) {
		t.Fatalf("SignalsToEvidence 修改了入参:\n got %+v\nwant %+v", sigs, before)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重复调用结果不稳定:\nfirst  %+v\nsecond %+v", first, second)
	}
}

// TestFailureSignalsForCompactionStable 验证门控入口在相同输入下结果稳定，
// 且 Failures 顺序与提取顺序一致（按轮次递增）。
func TestFailureSignalsForCompactionStable(t *testing.T) {
	messages := []SignalInput{
		{Role: "tool", ToolName: "shell", Content: "[exit 3]", IsError: true},
		{Role: "user", Content: "ok"},
		{Role: "tool", ToolName: "net", Content: "timed out"},
		{Role: "tool", ToolName: "patch", Content: "ERROR: anchor not found"},
	}
	first := FailureSignalsForCompaction(messages, true)
	second := FailureSignalsForCompaction(messages, true)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重复调用结果不稳定:\nfirst  %+v\nsecond %+v", first, second)
	}
	for i := 1; i < len(first.Failures); i++ {
		if first.Failures[i-1].Turn >= first.Failures[i].Turn {
			t.Fatalf("Failures 顺序不稳定: %+v", first.Failures)
		}
	}
	// 开启/关闭交替调用不应互相污染。
	if got := FailureSignalsForCompaction(messages, false); !reflect.DeepEqual(got, CompactionEvidence{}) {
		t.Fatalf("关闭调用被前一次开启调用污染: %+v", got)
	}
}

// TestFailureSignalsForCompactionEmpty 验证空输入：两种门控状态都返回零值证据。
func TestFailureSignalsForCompactionEmpty(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, messages := range [][]SignalInput{nil, {}} {
			got := FailureSignalsForCompaction(messages, enabled)
			if want := (CompactionEvidence{}); !reflect.DeepEqual(got, want) {
				t.Fatalf("enabled=%v messages=%v 证据 = %+v, want 零值", enabled, messages, got)
			}
		}
	}
	if got := SignalsToEvidence(nil); !reflect.DeepEqual(got, CompactionEvidence{}) {
		t.Fatalf("SignalsToEvidence(nil) = %+v, want 零值", got)
	}
	if got := SignalsToEvidence([]FailureSignal{}); !reflect.DeepEqual(got, CompactionEvidence{}) {
		t.Fatalf("SignalsToEvidence(空切片) = %+v, want 零值", got)
	}
}
