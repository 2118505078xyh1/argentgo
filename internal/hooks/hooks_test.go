package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEveryEventNameRoundTrips(t *testing.T) {
	if len(AllEvents) != 12 {
		t.Fatalf("expected 12 events, got %d", len(AllEvents))
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
	// The document format is unchanged: "priority" and "stopPropagation" are
	// not document keys, so even a document that happens to carry them must
	// parse into plain, priority-0, non-stopping commands.
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
	// A '*' in the pattern is always a wildcard. The old case order let a
	// literal '*' in the value consume the pattern's star as a literal match,
	// so matching failed even though the wildcard should absorb it. Paths and
	// tool names can legitimately contain '*' (e.g. on Unix file systems).
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
	// End-to-end through the document parser and ForEvent: a FileChanged hook
	// with a "*.go" matcher must still match a changed path that itself has a
	// literal '*' in a directory or file name.
	config, err := ParseConfig([]byte(`{"hooks":{"FileChanged":[
		{"matcher":"*.go","hooks":[{"command":"go-hook"}]},
		{"matcher":"*.md","hooks":[{"command":"md-hook"}]}
	]}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The leading '*' in the path collides with the pattern's wildcard on the
	// first comparison, which is exactly where the old case order failed.
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

	// Forgiving parsing still yields the runnable command.
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
	// A known event must not be reported as a silent typo.
	if warnings := Validate(document); len(warnings) != 0 {
		t.Fatalf("Validate warned on a supported event: %v", warnings)
	}
	// Unknown events are still skipped, as before.
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
	// Other events omit the field entirely.
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
