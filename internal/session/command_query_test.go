package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// cqAssistant 构造一条 assistant 记录；snake 为 true 时使用 "tool_calls"，否则使用 "toolCalls"。
func cqAssistant(id, parent string, snake bool, calls ...map[string]any) Record {
	key := "tool_calls"
	if !snake {
		key = "toolCalls"
	}
	data, err := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": "执行命令",
		key:       calls,
	})
	if err != nil {
		panic(err)
	}
	return Record{ID: id, Parent: parent, Kind: KindAssistant, Data: data}
}

// cqToolResult 构造一条 KindTool 的命令结果记录；nameKey 决定使用 "name" 还是回退键 "tool"。
func cqToolResult(id, parent, name, output, nameKey string) Record {
	data, err := json.Marshal(map[string]any{
		nameKey:      name,
		"output":     output,
		"toolCallId": "call-" + id,
	})
	if err != nil {
		panic(err)
	}
	return Record{ID: id, Parent: parent, Kind: KindTool, Data: data}
}

// cqPlain 构造一条非工具记录，data 为原始 JSON 文本。
func cqPlain(id, parent, kind, data string) Record {
	return Record{ID: id, Parent: parent, Kind: kind, Data: json.RawMessage(data)}
}

// cqIDs 提取记录的 id 序列，便于与期望顺序逐项比较。
func cqIDs(records []Record) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// cqSample 建一组交错的命令记录与非命令记录，用于多个测试共享。
func cqSample() []Record {
	bashCall := map[string]any{
		"id":       "c1",
		"function": map[string]any{"name": "bash", "arguments": `{"cmd":"echo 你好"}`},
	}
	psCall := map[string]any{
		"id":       "c2",
		"function": map[string]any{"name": "powershell", "arguments": `{"cmd":"Get-ChildItem"}`},
	}
	return []Record{
		cqPlain("h", "", KindHeader, `{"title":"记录"}`),
		cqPlain("u", "h", KindUser, `"请运行命令"`),
		cqAssistant("a1", "u", true, bashCall),                                                            // 命令发起（snake）
		cqToolResult("t1", "a1", "bash", "你好，输出完成", "name"),                                               // 命令结果
		cqAssistant("a2", "t1", false, psCall),                                                            // 命令发起（camel）
		cqToolResult("t2", "a2", "powershell", "目录列表", "tool"),                                            // 命令结果（回退键）
		cqPlain("m", "t2", KindMessage, `{"role":"assistant","content":"完成"}`),                            // 无 tool_calls
		cqPlain("r", "m", KindTool, `{"name":"read","output":"文本"}`),                                      // 非 shell 工具
		cqAssistant("a3", "r", true),                                                                      // 空 tool_calls
		cqPlain("bad", "a3", KindAssistant, `{not json`),                                                  // 载荷损坏
		cqPlain("s", "bad", KindSummary, `{"summary":"摘要"}`),                                              // 摘要
		cqPlain("m2", "s", KindMessage, `{"role":"assistant","content":"再说","tool_calls":[{"id":"c3"}]}`), // KindMessage 形式的 assistant
	}
}

func TestCommandRecords(t *testing.T) {
	sample := cqSample()
	cjk := []Record{
		cqAssistant("命", "", true, map[string]any{"id": "c甲", "function": map[string]any{"name": "bash", "arguments": `{"cmd":"ls 中文目录"}`}}),
		cqToolResult("果", "命", "bash", "中文目录内容", "name"),
		cqAssistant("令", "果", false, map[string]any{"id": "c乙", "function": map[string]any{"name": "powershell", "arguments": `{"cmd":"写文件 测试"}`}}),
		cqToolResult("应", "令", "powershell", "完成：成功", "name"),
	}
	tests := []struct {
		name   string
		input  []Record
		wantID []string
	}{
		{
			name:   "识别各类命令记录并保持稳定顺序",
			input:  sample,
			wantID: []string{"a1", "t1", "a2", "t2", "m2"},
		},
		{
			name:   "CJK 命令与输出",
			input:  cjk,
			wantID: []string{"命", "果", "令", "应"},
		},
		{
			name: "非命令记录被过滤",
			input: []Record{
				cqPlain("h", "", KindHeader, `{"title":"根"}`),
				cqPlain("u", "h", KindUser, `"文本"`),
				cqPlain("m", "u", KindMessage, `{"role":"assistant","content":"纯文本"}`),
				cqPlain("r", "m", KindTool, `{"name":"fs","output":"文本"}`),
				cqAssistant("a", "r", true),
				cqPlain("s", "a", KindSummary, `{"summary":"摘要"}`),
			},
			wantID: []string{},
		},
		{
			name:   "空切片",
			input:  []Record{},
			wantID: []string{},
		},
		{
			name:   "nil 切片",
			input:  nil,
			wantID: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneRecords(tt.input)
			got := CommandRecords(tt.input)
			if got == nil {
				t.Fatal("返回切片为 nil，期望独立的空切片")
			}
			if ids := cqIDs(got); !reflect.DeepEqual(ids, tt.wantID) {
				t.Fatalf("id = %v，期望 %v", ids, tt.wantID)
			}
			if !reflect.DeepEqual(tt.input, snapshot) {
				t.Fatalf("入参被修改: got %v，期望 %v", tt.input, snapshot)
			}
		})
	}
}

func TestCommandRecordsReturnsIndependentSlice(t *testing.T) {
	input := []Record{
		cqToolResult("t", "", "bash", "输出", "name"),
		cqPlain("u", "t", KindUser, `"文本"`),
	}
	snapshot := cloneRecords(input)
	got := CommandRecords(input)
	got[0].ID = "已改"
	got = append(got, cqToolResult("x", "", "bash", "更多输出", "name"))
	if input[0].ID != "t" || len(input) != 2 {
		t.Fatalf("返回切片与入参共享底层数组: input = %v", input)
	}
	if !reflect.DeepEqual(input, snapshot) {
		t.Fatalf("入参被修改: got %v，期望 %v", input, snapshot)
	}
}

func TestLastCommand(t *testing.T) {
	sample := cqSample()
	tests := []struct {
		name   string
		input  []Record
		wantID string
		wantOK bool
	}{
		{
			name:   "最后一条命令当为命令",
			input:  sample,
			wantID: "m2",
			wantOK: true,
		},
		{
			name: "命令后跟非命令记录",
			input: []Record{
				cqToolResult("t", "", "bash", "输出", "name"),
				cqPlain("u", "t", KindUser, `"文本"`),
			},
			wantID: "t",
			wantOK: true,
		},
		{
			name: "无命令记录",
			input: []Record{
				cqPlain("u", "", KindUser, `"文本"`),
				cqAssistant("a", "u", true),
			},
			wantID: "",
			wantOK: false,
		},
		{
			name:   "空切片",
			input:  []Record{},
			wantID: "",
			wantOK: false,
		},
		{
			name:   "nil 切片",
			input:  nil,
			wantID: "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneRecords(tt.input)
			got, ok := LastCommand(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v，期望 %v", ok, tt.wantOK)
			}
			if ok && got.ID != tt.wantID {
				t.Fatalf("id = %q，期望 %q", got.ID, tt.wantID)
			}
			if !ok && !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("无命令时应返回零值 Record，got %v", got)
			}
			if !reflect.DeepEqual(tt.input, snapshot) {
				t.Fatalf("入参被修改: got %v，期望 %v", tt.input, snapshot)
			}
		})
	}
}

func TestCommandCountByKind(t *testing.T) {
	sample := cqSample()
	tests := []struct {
		name  string
		input []Record
		want  map[string]int
	}{
		{
			name:  "按 Kind 精确计数",
			input: sample,
			want: map[string]int{
				KindAssistant: 2,
				KindTool:      2,
				KindMessage:   1,
			},
		},
		{
			name: "仅统计出现的类别",
			input: []Record{
				cqToolResult("t", "", "powershell", "输出", "name"),
			},
			want: map[string]int{KindTool: 1},
		},
		{
			name:  "无命令返回空 map",
			input: []Record{cqPlain("u", "", KindUser, `"文本"`)},
			want:  map[string]int{},
		},
		{
			name:  "空切片",
			input: []Record{},
			want:  map[string]int{},
		},
		{
			name:  "nil 切片",
			input: nil,
			want:  map[string]int{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneRecords(tt.input)
			got := CommandCountByKind(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("计数 = %v，期望 %v", got, tt.want)
			}
			if again := CommandCountByKind(tt.input); !reflect.DeepEqual(got, again) {
				t.Fatalf("重复调用结果不一致: %v 与 %v", got, again)
			}
			if !reflect.DeepEqual(tt.input, snapshot) {
				t.Fatalf("入参被修改: got %v，期望 %v", tt.input, snapshot)
			}
		})
	}
}
