package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEveryEventNameRoundTrips(t *testing.T) {
	if len(AllEvents) != 15 {
		t.Fatalf("expected 15 events, got %d", len(AllEvents))
	}
	for _, event := range AllEvents {
		parsed, ok := ParseEvent(event.String())
		if !ok || parsed != event {
			t.Fatalf("event %q did not round trip", event)
		}
	}
	if _, ok := ParseEvent("SesionStart"); ok {
		t.Fatal("a typo must not parse as an event")
	}
}

func TestParseConfigShapes(t *testing.T) {
	document := []byte(`{"hooks":{
		"PreToolUse":[
			{"matcher":"bash","hooks":[
				{"type":"command","command":"echo bash","timeoutMs":5000,"onError":"allow"},
				{"type":"prompt","command":"ignored"},
				{"command":"   "}
			]},
			{"type":"command","command":"echo bare","timeoutMs":5}
		],
		"Stop":[{"command":"echo stop","async":true}],
		"SesionStart":[{"command":"echo typo"}]
	}}`)

	config, err := ParseConfig(document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(config.Commands) != 3 {
		t.Fatalf("unexpected command count: %+v", config.Commands)
	}

	pre := config.ForEvent(PreToolUse, "bash")
	if len(pre) != 2 {
		t.Fatalf("unexpected PreToolUse commands: %+v", pre)
	}
	if pre[0].Command != "echo bash" || pre[0].Matcher != "bash" {
		t.Fatalf("group command lost its matcher: %+v", pre[0])
	}
	if pre[0].Timeout != 5*time.Second || pre[0].OnError != "allow" || pre[0].BlocksOnFailure() {
		t.Fatalf("command options lost: %+v", pre[0])
	}
	if pre[1].Command != "echo bare" || pre[1].Matcher != "" {
		t.Fatalf("bare command shape unsupported: %+v", pre[1])
	}
	if pre[1].Timeout != minTimeout {
		t.Fatalf("timeoutMs below the floor was not clamped: %s", pre[1].Timeout)
	}

	stop := config.ForEvent(Stop, "")
	if len(stop) != 1 || !stop[0].Async {
		t.Fatalf("async flag lost: %+v", stop)
	}
	if len(config.ForEvent(SessionStart, "")) != 0 {
		t.Fatal("unknown events must be skipped")
	}
}

func TestParseConfigDefaults(t *testing.T) {
	config, err := ParseConfig([]byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"echo hi"}]}]}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	command := config.Commands[0]
	if command.Timeout != defaultTimeout {
		t.Fatalf("default timeout: %s", command.Timeout)
	}
	if command.OnError != "block" || !command.BlocksOnFailure() {
		t.Fatalf("default onError: %+v", command)
	}
	if command.Async {
		t.Fatalf("hooks are synchronous by default: %+v", command)
	}
}

func TestParseConfigRejectsMissingHooks(t *testing.T) {
	if _, err := ParseConfig([]byte(`{}`)); err == nil {
		t.Fatal("expected a missing hooks object to fail")
	}
	if _, err := ParseConfig([]byte(`not json`)); err == nil {
		t.Fatal("expected invalid JSON to fail")
	}
}

func TestForEventFiltersByMatcher(t *testing.T) {
	config, err := ParseConfig([]byte(`{"hooks":{"PreToolUse":[
		{"matcher":"bash","hooks":[{"command":"echo bash"}]},
		{"matcher":"read*","hooks":[{"command":"echo read"}]},
		{"matcher":"*","hooks":[{"command":"echo all"}]}
	]}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	bash := config.ForEvent(PreToolUse, "bash")
	if len(bash) != 2 || bash[0].Command != "echo bash" || bash[1].Command != "echo all" {
		t.Fatalf("bash matcher: %+v", bash)
	}
	read := config.ForEvent(PreToolUse, "read_file")
	if len(read) != 2 || read[0].Command != "echo read" {
		t.Fatalf("wildcard matcher: %+v", read)
	}
	write := config.ForEvent(PreToolUse, "write")
	if len(write) != 1 || write[0].Command != "echo all" {
		t.Fatalf("non-matching tool still ran: %+v", write)
	}
	if got := config.ForEvent(PostToolUse, "bash"); len(got) != 0 {
		t.Fatalf("event filter leaked: %+v", got)
	}
}

func TestDocumentHooksIgnoreInternalPriorityFields(t *testing.T) {
	// 文档格式不变："priority" 和 "stopPropagation"
	// 不是文档键，因此即使文档碰巧携带它们，也必须
	// 解析为普通的优先级 0、不停止的命令。
	config, err := ParseConfig([]byte(`{"hooks":{"PreToolUse":[{"hooks":[
		{"command":"echo a","priority":100,"stopPropagation":true},
		{"command":"echo b"}
	]}]}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	commands := config.ForEvent(PreToolUse, "")
	if len(commands) != 2 {
		t.Fatalf("unexpected commands: %+v", commands)
	}
	for _, command := range commands {
		if command.Priority != 0 || command.StopPropagation {
			t.Fatalf("document must not set internal fields: %+v", command)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"*", "anything", true},
		{"read*", "read_file", true},
		{"*.rs", "main.rs", true},
		{"read*", "write", false},
		{"bash", "bash", true},
		{"", "bash", false},
	}
	for _, test := range cases {
		if got := globMatch(test.pattern, test.value); got != test.want {
			t.Fatalf("globMatch(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
		}
	}
}

func TestGlobMatchStarIsAWildcardEvenWhenValueContainsStar(t *testing.T) {
	// 模式中的 '*' 始终是通配符。旧的 case 顺序让值中的字面 '*'
	// 值中的字面 '*' 消耗模式星号作为字面匹配，
	// 把模式的星号当作字面匹配消耗掉，于是尽管通配符本应吸收它，匹配仍失败。路径和
	// 工具名合法地可以包含 '*'（例如在 Unix 文件系统上）。
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"a*", "a*b", true},
		{"a*", "a*", true},
		{"read*", "read*file", true},
		{"*.go", "a*.go", true},
		{"*a*", "a*b*a", true},
		{"a*b", "a*b", true}, // wildcard may match the single literal '*'
		{"a*b", "ax*b", true},
		{"a*c", "a*b", false}, // wildcard cannot invent a trailing 'c'
	}
	for _, test := range cases {
		if got := globMatch(test.pattern, test.value); got != test.want {
			t.Fatalf("globMatch(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
		}
	}
}

func TestForEventMatcherWorksWhenValueContainsStar(t *testing.T) {
	// 端到端经过文档解析器和 ForEvent：带 "*.go" 匹配器的 FileChanged 钩子
	// 带 "*.go" 匹配器的 FileChanged 钩子必须仍能匹配变更路径，即使
	// 其目录或文件名中含字面 '*'。
	config, err := ParseConfig([]byte(`{"hooks":{"FileChanged":[
		{"matcher":"*.go","hooks":[{"command":"go-hook"}]},
		{"matcher":"*.md","hooks":[{"command":"md-hook"}]}
	]}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 必须仍能匹配自身在目录或文件名中含字面 '*' 的变更路径。
	// 路径中前导的 '*' 在第一次比较时就与模式的通配符冲突，这正是旧 case 顺序失败的地方。
	got := config.ForEvent(FileChanged, "*scratch/main.go")
	if len(got) != 1 || got[0].Command != "go-hook" {
		t.Fatalf("matcher with '*' in value selected %+v", got)
	}
}

func TestValidateWarnsAboutSilentTypos(t *testing.T) {
	document := []byte(`{"hooks":{
		"SesionStart":[{"hooks":[{"command":"echo typo"}]}],
		"PreToolUse":[{"matcher":"ba sh","hooks":[
			{"command":"echo ok","onError":"sometimes","timeoutMs":5},
			{"type":"prompt","command":"ignored"},
			{"command":"  "}
		]}]
	}}`)
	warnings := strings.Join(Validate(document), "\n")
	for _, want := range []string{"unknown event", "onError", "timeoutMs", "not supported", "empty command", "contains a space"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("missing %q in warnings:\n%s", want, warnings)
		}
	}

	// 宽松解析仍产生可运行的命令。
	config, err := ParseConfig(document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(config.Commands) != 1 {
		t.Fatalf("unexpected commands: %+v", config.Commands)
	}
}

func TestMergeAppendsDocuments(t *testing.T) {
	first, err := ParseConfig([]byte(`{"hooks":{"Stop":[{"command":"echo one"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseConfig([]byte(`{"hooks":{"Stop":[{"command":"echo two"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	first.Merge(second)
	first.Merge(nil)

	var commands []string
	for _, command := range first.ForEvent(Stop, "") {
		commands = append(commands, command.Command)
	}
	if len(commands) != 2 || commands[0] != "echo one" || commands[1] != "echo two" {
		t.Fatalf("merge order lost: %v", commands)
	}
}

func TestPayloadMarshalsTheDocumentedShape(t *testing.T) {
	payload := Payload{
		Event:     "PreToolUse",
		Tool:      "bash",
		Args:      json.RawMessage(`{"command":"ls"}`),
		SessionID: "sess-1",
		CWD:       "C:/work",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"event", "tool", "args", "sessionId", "cwd"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("payload lost %q: %s", key, encoded)
		}
	}
}

func TestFileChangedIsAFullLifecycleEvent(t *testing.T) {
	parsed, ok := ParseEvent("FileChanged")
	if !ok || parsed != FileChanged {
		t.Fatalf("FileChanged did not parse: %q %v", parsed, ok)
	}
	found := false
	for _, event := range AllEvents {
		if event == FileChanged {
			found = true
		}
	}
	if !found {
		t.Fatal("FileChanged missing from AllEvents")
	}

	document := []byte(`{"hooks":{"FileChanged":[{"matcher":"*.go","hooks":[{"command":"echo changed"}]}]}}`)
	config, err := ParseConfig(document)
	if err != nil {
		t.Fatalf("parse FileChanged: %v", err)
	}
	commands := config.ForEvent(FileChanged, "main.go")
	if len(commands) != 1 || commands[0].Command != "echo changed" {
		t.Fatalf("FileChanged command lost: %+v", commands)
	}
	// 已知事件不得被报告为静默拼写错误。
	if warnings := Validate(document); len(warnings) != 0 {
		t.Fatalf("Validate warned on a supported event: %v", warnings)
	}
	// 未知事件仍像以前一样被跳过。
	if _, ok := ParseEvent("FileChange"); ok {
		t.Fatal("a near-miss must not parse as FileChanged")
	}
}

func TestPayloadMarshalsFileChangedPath(t *testing.T) {
	payload := Payload{Event: "FileChanged", Path: "internal/hooks/hooks.go"}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(fields["path"]) != `"internal/hooks/hooks.go"` {
		t.Fatalf("payload lost path: %s", encoded)
	}
	// 其他事件完全省略该字段。
	other, err := json.Marshal(Payload{Event: "Stop"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(other), "path") {
		t.Fatalf("path should be omitted when empty: %s", other)
	}
}

func TestReportTriggersCoversEveryEvent(t *testing.T) {
	rows := ReportTriggers()
	if len(rows) != len(AllEvents) {
		t.Fatalf("report has %d rows for %d events", len(rows), len(AllEvents))
	}
	for index, row := range rows {
		if row.Event != AllEvents[index] {
			t.Fatalf("report row %d is %q, want %q", index, row.Event, AllEvents[index])
		}
		if strings.TrimSpace(row.Trigger) == "" {
			t.Fatalf("event %q has no trigger description", row.Event)
		}
	}
	for _, row := range rows {
		if row.Event == FileChanged {
			if !strings.Contains(row.Payload, "Path") {
				t.Fatalf("FileChanged report must mention Path: %+v", row)
			}
			if !strings.Contains(strings.ToLower(row.Trigger), "file") {
				t.Fatalf("FileChanged report must mention files: %+v", row)
			}
			return
		}
	}
	t.Fatal("FileChanged missing from trigger report")
}
