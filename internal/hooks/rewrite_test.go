package hooks

import (
	"encoding/json"
	"testing"
)

func TestRewritesFromStdout(t *testing.T) {
	stdout := `log line
{"hookSpecificOutput":{"updatedInput":{"path":"safe.txt"},"updatedOutput":"redacted output"}}
`
	in, out := rewritesFromStdout(stdout)
	if len(in) == 0 || len(out) == 0 {
		t.Fatalf("rewrites not parsed: in=%s out=%s", in, out)
	}
	var input map[string]any
	if err := json.Unmarshal(in, &input); err != nil {
		t.Fatalf("updatedInput not JSON object: %v", err)
	}
	if input["path"] != "safe.txt" {
		t.Fatalf("updatedInput = %v", input)
	}
	var text string
	if err := json.Unmarshal(out, &text); err != nil || text != "redacted output" {
		t.Fatalf("updatedOutput = %s, err=%v", out, err)
	}
}

func TestRewritesFromStdoutIgnoresInvalid(t *testing.T) {
	// 非字符串/对象的改写值以及超长值应被忽略。
	in, out := rewritesFromStdout(`{"hookSpecificOutput":{"updatedInput":123,"updatedOutput":{"nested":true}}}`)
	if len(in) != 0 {
		t.Fatalf("numeric updatedInput should be ignored: %s", in)
	}
	// 对象形式的 updatedOutput 也被接受（调用方按需解释）。
	if len(out) == 0 {
		t.Fatal("object updatedOutput should be accepted")
	}
}

func TestRewritesFromStdoutNoSignal(t *testing.T) {
	in, out := rewritesFromStdout("no json here")
	if len(in) != 0 || len(out) != 0 {
		t.Fatalf("unexpected rewrites: %s %s", in, out)
	}
}
