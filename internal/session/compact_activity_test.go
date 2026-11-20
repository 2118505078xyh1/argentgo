package session

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// activityToolEntry 按 agent 写入的形态构建一条工具结果记录：
// 顶层 name 加上序列化为 JSON 字符串的 arguments。
func activityToolEntry(name, arguments string) Entry {
	data, _ := json.Marshal(map[string]any{
		"id": "call-1", "name": name, "arguments": arguments,
		"output": "ok", "is_error": "false",
	})
	return Entry{Role: "tool", Name: name, Data: data}
}

// activityAssistantEntry 构建携带 tool_calls 的 assistant 记录，这是
// 转录中可能出现的另一种布局。
func activityAssistantEntry(calls ...map[string]any) Entry {
	data, _ := json.Marshal(map[string]any{
		"role": "assistant", "content": "", "tool_calls": calls,
	})
	return Entry{Role: "assistant", Data: data}
}

func activityFunctionCall(name, arguments string) map[string]any {
	return map[string]any{
		"id": "call-1", "type": "function",
		"function": map[string]any{"name": name, "arguments": arguments},
	}
}

func activityBulletLines(section string) []string {
	var out []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "- ") {
			out = append(out, strings.TrimPrefix(line, "- "))
		}
	}
	return out
}

func TestSummarizeToolActivityReadsEditsCommands(t *testing.T) {
	dropped := []Entry{
		activityToolEntry("read", `{"path":"internal/foo.go","offset":10,"limit":5}`),
		activityToolEntry("read", `{"path":"a.txt","line":20,"before":2,"after":3}`),
		activityToolEntry("read", `{"path":"b.go:30:40"}`),
		activityToolEntry("edit", `{"path":"c.go","old":"x","new":"y"}`),
		activityToolEntry("bash", `{"command":"go test ./internal/session -count=1"}`),
	}
	section := summarizeToolActivity(dropped)

	if !strings.Contains(section, "## Tool activity") {
		t.Fatalf("missing section header: %q", section)
	}
	for _, want := range []string{
		"read internal/foo.go:10-14", // offset/limit
		"read a.txt:18-23",           // line/before/after
		"read b.go:30-40",            // 内联路径选择器
		"edit c.go",                  // 编辑只记录路径
		"bash go test ./internal/session -count=1",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("activity section missing %q:\n%s", want, section)
		}
	}
	// 记录的数据中编辑没有 diff，因此不得凭空捏造行范围。
	if strings.Contains(section, "edit c.go:") {
		t.Errorf("edit should not carry a fabricated line range:\n%s", section)
	}
}

func TestSummarizeToolActivityInlineSelectors(t *testing.T) {
	dropped := []Entry{
		activityToolEntry("read", mustActivityArgs(map[string]any{"path": "x.go#L10-L20"})),
		activityToolEntry("read", mustActivityArgs(map[string]any{"path": `C:\src\a.go`})),
	}
	section := summarizeToolActivity(dropped)
	if !strings.Contains(section, "read x.go:10-20") {
		t.Errorf("#L selector not normalised:\n%s", section)
	}
	if !strings.Contains(section, `read C:\src\a.go`) {
		t.Errorf("Windows drive colon mistaken for a line range:\n%s", section)
	}
	if strings.Contains(section, `read C:\src\a.go:`) {
		t.Errorf("Windows path should carry no fabricated range:\n%s", section)
	}
}

func TestSummarizeToolActivityFromAssistantToolCalls(t *testing.T) {
	dropped := []Entry{
		activityAssistantEntry(
			activityFunctionCall("read", `{"path":"loop.go","line":7,"after":2}`),
			activityFunctionCall("bash", `{"command":"echo hi"}`),
		),
	}
	section := summarizeToolActivity(dropped)
	for _, want := range []string{"read loop.go:7-9", "bash echo hi"} {
		if !strings.Contains(section, want) {
			t.Errorf("assistant tool_calls lost %q:\n%s", want, section)
		}
	}
}

func TestSummarizeToolActivityDeduplicates(t *testing.T) {
	dropped := []Entry{
		activityToolEntry("read", `{"path":"a.go","offset":1,"limit":3}`),
		activityToolEntry("read", `{"path":"a.go","offset":1,"limit":3}`), // 重复
		activityToolEntry("bash", `{"command":"go build ./..."}`),
		activityToolEntry("bash", `{"command":"go build ./..."}`), // 重复
		activityToolEntry("bash", `{"cmd":"go build ./..."}`),     // 同一命令，别名键
	}
	bullets := activityBulletLines(summarizeToolActivity(dropped))
	if len(bullets) != 2 {
		t.Fatalf("bullets = %q, want 2 deduplicated entries", bullets)
	}
}

func TestSummarizeToolActivityCJK(t *testing.T) {
	dropped := []Entry{
		activityToolEntry("bash", `{"command":"echo 中文测试补注释"}`),
		activityToolEntry("read", `{"path":"中文/文件.go","offset":3,"limit":2}`),
	}
	section := summarizeToolActivity(dropped)
	if !utf8.ValidString(section) {
		t.Fatalf("activity section is not valid UTF-8: %q", section)
	}
	for _, want := range []string{"bash echo 中文测试补注释", "read 中文/文件.go:3-4"} {
		if !strings.Contains(section, want) {
			t.Errorf("CJK activity lost %q:\n%s", want, section)
		}
	}
}

func TestSummarizeToolActivityTruncatesLongValues(t *testing.T) {
	longCommand := "echo " + strings.Repeat("a", 300)
	dropped := []Entry{activityToolEntry("bash", mustActivityArgs(map[string]any{"command": longCommand}))}
	bullets := activityBulletLines(summarizeToolActivity(dropped))
	if len(bullets) != 1 {
		t.Fatalf("bullets = %q, want 1", bullets)
	}
	// "bash " 前缀加上裁剪到 activityCommandLimit 个符文的命令。
	if got := utf8.RuneCountInString(bullets[0]); got != len("bash ")+activityCommandLimit {
		t.Fatalf("command bullet has %d runes, want %d", got, len("bash ")+activityCommandLimit)
	}

	longPath := strings.Repeat("路径", 200)
	dropped = []Entry{activityToolEntry("read", mustActivityArgs(map[string]any{"path": longPath, "offset": 1, "limit": 1}))}
	bullets = activityBulletLines(summarizeToolActivity(dropped))
	if len(bullets) != 1 {
		t.Fatalf("bullets = %q, want 1", bullets)
	}
	if got := utf8.RuneCountInString(bullets[0]); got > activityBulletLimit {
		t.Fatalf("path bullet has %d runes, want <= %d", got, activityBulletLimit)
	}
}

func TestSummarizeToolActivityBounded(t *testing.T) {
	var dropped []Entry
	for i := 0; i < activityMaxBullets+20; i++ {
		dropped = append(dropped, activityToolEntry("bash", mustActivityArgs(map[string]any{
			"command": "cmd-" + string(rune('A'+i%26)) + "-" + itoaActivity(i),
		})))
	}
	bullets := activityBulletLines(summarizeToolActivity(dropped))
	if len(bullets) != activityMaxBullets {
		t.Fatalf("bullets = %d, want the cap of %d", len(bullets), activityMaxBullets)
	}
	if !strings.Contains(bullets[len(bullets)-1], itoaActivity(activityMaxBullets+19)) {
		t.Errorf("most recent activity was not kept: %q", bullets[len(bullets)-1])
	}
}

func TestSummarizeToolActivityNoActivity(t *testing.T) {
	tests := []struct {
		name    string
		dropped []Entry
	}{
		{
			name: "plain messages only",
			dropped: []Entry{
				{Role: "user", Content: "目标：实现词法分析器"},
				{Role: "assistant", Content: "已完成。"},
			},
		},
		{
			name:    "tool outside the activity categories",
			dropped: []Entry{activityToolEntry("todo", `{"op":"read"}`)},
		},
		{
			name:    "read without a path",
			dropped: []Entry{activityToolEntry("read", `{"offset":1}`)},
		},
		{
			name:    "bash without a command",
			dropped: []Entry{activityToolEntry("bash", `{"timeout_seconds":5}`)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeToolActivity(tc.dropped); got != "" {
				t.Fatalf("summarizeToolActivity = %q, want empty", got)
			}
		})
	}
}

func TestSummarizeIncludesToolActivitySection(t *testing.T) {
	withActivity := []Entry{activityToolEntry("bash", `{"command":"go test ./..."}`)}
	if got := Summarize(withActivity, 0).Summary; !strings.Contains(got, "## Tool activity") {
		t.Fatalf("Summarize did not include the activity section:\n%s", got)
	}
	withoutActivity := []Entry{{Role: "user", Content: "hi"}}
	if got := Summarize(withoutActivity, 0).Summary; strings.Contains(got, "## Tool activity") {
		t.Fatalf("Summarize added an empty activity section:\n%s", got)
	}
}

func mustActivityArgs(value map[string]any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func itoaActivity(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
