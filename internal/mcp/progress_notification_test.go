package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseProgressNotification(t *testing.T) {
	tests := []struct {
		name         string
		params       string
		wantProgress float64
		wantTotal    float64
		wantMessage  string
		wantOK       bool
	}{
		{
			name:         "完整通知",
			params:       `{"progress":50,"total":100,"message":"Halfway done"}`,
			wantProgress: 50,
			wantTotal:    100,
			wantMessage:  "Halfway done",
			wantOK:       true,
		},
		{
			name:         "仅必填进度",
			params:       `{"progress":1}`,
			wantProgress: 1,
			wantOK:       true,
		},
		{
			name:         "带总量无消息",
			params:       `{"progress":3.5,"total":7}`,
			wantProgress: 3.5,
			wantTotal:    7,
			wantOK:       true,
		},
		{
			name:         "带消息无总量",
			params:       `{"progress":2,"message":"working"}`,
			wantProgress: 2,
			wantMessage:  "working",
			wantOK:       true,
		},
		{
			name:         "科学计数法与零值",
			params:       `{"progress":1e2,"total":0,"message":""}`,
			wantProgress: 100,
			wantTotal:    0,
			wantMessage:  "",
			wantOK:       true,
		},
		{
			name:         "忽略未知字段",
			params:       `{"progress":9,"unknown":{"any":[1,2,3]}}`,
			wantProgress: 9,
			wantOK:       true,
		},
		{
			name:         "接受有限负数",
			params:       `{"progress":-1,"total":-2}`,
			wantProgress: -1,
			wantTotal:    -2,
			wantOK:       true,
		},
		{
			name:   "空载荷",
			params: ``,
		},
		{
			name:   "纯空白载荷",
			params: "  \n\t ",
		},
		{
			name:   "非法JSON",
			params: `{"progress":`,
		},
		{
			name:   "对象后有尾随数据",
			params: `{"progress":1} trailing`,
		},
		{
			name:   "null载荷",
			params: `null`,
		},
		{
			name:   "数组载荷",
			params: `[{"progress":1}]`,
		},
		{
			name:   "标量载荷字符串",
			params: `"progress"`,
		},
		{
			name:   "标量载荷数字",
			params: `42`,
		},
		{
			name:   "缺少必填进度",
			params: `{"total":100,"message":"done"}`,
		},
		{
			name:   "进度为null",
			params: `{"progress":null}`,
		},
		{
			name:   "进度为字符串",
			params: `{"progress":"50"}`,
		},
		{
			name:   "进度为布尔",
			params: `{"progress":true}`,
		},
		{
			name:   "进度为对象",
			params: `{"progress":{}}`,
		},
		{
			name:   "进度为数组",
			params: `{"progress":[]}`,
		},
		{
			name:   "进度超出浮点范围",
			params: `{"progress":1e400}`,
		},
		{
			name:   "进度为NaN字面量导致整个JSON非法",
			params: `{"progress":NaN}`,
		},
		{
			name:   "总量为null",
			params: `{"progress":1,"total":null}`,
		},
		{
			name:   "总量为字符串",
			params: `{"progress":1,"total":"100"}`,
		},
		{
			name:   "消息为null",
			params: `{"progress":1,"message":null}`,
		},
		{
			name:   "消息为数字",
			params: `{"progress":1,"message":123}`,
		},
		{
			name:   "消息为数组",
			params: `{"progress":1,"message":["a"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			progress, total, message, ok := parseProgressNotification(json.RawMessage(tt.params))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, 期望 %v", ok, tt.wantOK)
			}
			if progress != tt.wantProgress {
				t.Errorf("progress = %v, 期望 %v", progress, tt.wantProgress)
			}
			if total != tt.wantTotal {
				t.Errorf("total = %v, 期望 %v", total, tt.wantTotal)
			}
			if message != tt.wantMessage {
				t.Errorf("message = %q, 期望 %q", message, tt.wantMessage)
			}
			if !ok && (progress != 0 || total != 0 || message != "") {
				t.Errorf("失败路径必须返回全零值，实际得到 (%v, %v, %q)", progress, total, message)
			}
		})
	}
}

func TestParseProgressNotificationPreservesCJKMessage(t *testing.T) {
	// CJK 与 emoji 都是多字节字符，净化与截断不得改变其内容。
	const want = "正在处理中文任务：文件与图像 🎉 完成"
	params, err := json.Marshal(map[string]any{
		"progress": 12,
		"message":  want,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, _, got, ok := parseProgressNotification(params)
	if !ok {
		t.Fatal("合法通知应解析成功")
	}
	if got != want {
		t.Fatalf("message = %q, 期望 %q", got, want)
	}
}

func TestParseProgressNotificationMessageIsBoundedAndRuneSafe(t *testing.T) {
	long := strings.Repeat("汉", progressMessageMaxRunes+50)
	params, err := json.Marshal(map[string]any{
		"progress": 1,
		"message":  long,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, _, got, ok := parseProgressNotification(params)
	if !ok {
		t.Fatal("合法通知应解析成功")
	}
	if r := utf8.RuneCountInString(got); r != progressMessageMaxRunes {
		t.Fatalf("截断后 rune 数 = %d, 期望 %d", r, progressMessageMaxRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("截断后的消息必须是合法 UTF-8，不能切在多字节字符中间")
	}
	if strings.Count(got, "汉") != progressMessageMaxRunes {
		t.Fatal("CJK 字符应被完整保留")
	}
}

func TestParseProgressNotificationSanitizesControlCharacters(t *testing.T) {
	// 控制字符被替换为空格，首尾空白被去掉，避免污染显示区域。
	params := json.RawMessage("{\"progress\":1,\"message\":\"  line1\\nline2\\t\\u0000  \"}")
	_, _, got, ok := parseProgressNotification(params)
	if !ok {
		t.Fatal("合法通知应解析成功")
	}
	if want := "line1 line2"; got != want {
		t.Fatalf("message = %q, 期望 %q", got, want)
	}
}

func TestParseProgressNotificationIsPure(t *testing.T) {
	// 纯函数不修改输入，且重复调用结果一致。
	original := json.RawMessage(`{"progress":5,"total":10,"message":"半程"}`)
	snapshot := append(json.RawMessage(nil), original...)

	firstProgress, firstTotal, firstMessage, firstOK := parseProgressNotification(original)
	secondProgress, secondTotal, secondMessage, secondOK := parseProgressNotification(original)

	if !bytes.Equal(original, snapshot) {
		t.Fatal("parseProgressNotification 修改了输入")
	}
	if firstProgress != secondProgress || firstTotal != secondTotal ||
		firstMessage != secondMessage || firstOK != secondOK {
		t.Fatal("相同输入两次调用结果不一致")
	}
}
