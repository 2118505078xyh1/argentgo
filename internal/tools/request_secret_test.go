package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type fakeSecretRequester struct {
	calls  int
	name   string
	reason string
	value  string
	err    error
}

func (f *fakeSecretRequester) RequestSecret(_ context.Context, name, reason string) (string, error) {
	f.calls++
	f.name, f.reason = name, reason
	return f.value, f.err
}

type fakeSecretSink struct {
	saved map[string]string
	env   map[string]string
}

func newFakeSecretSink() *fakeSecretSink {
	return &fakeSecretSink{saved: map[string]string{}, env: map[string]string{}}
}

func (s *fakeSecretSink) SaveSecret(name, value string) error {
	s.saved[name] = value
	// app 的真实实现会调用 os.Setenv；测试只在这里记录「值只经 $NAME 暴露」的事实。
	s.env[name] = value
	return nil
}

func (s *fakeSecretSink) HasSecret(name string) bool {
	_, ok := s.saved[name]
	return ok
}

// TestRequestSecretNeverLeaksValue 是 #18 的核心安全断言：密钥字节绝不进入
// 工具结果、工具 schema/描述、模拟事件或模拟 JSONL 转录；它只落到凭据存储并以
// $NAME 暴露给子进程。
func TestRequestSecretNeverLeaksValue(t *testing.T) {
	const secret = "sk-超敏感值- NEVER-LEAK-9f3a"
	requester := &fakeSecretRequester{value: secret}
	sink := newFakeSecretSink()
	tool := &requestSecretTool{requester: requester, sink: sink}

	out, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{"name":"github_token","reason":"调用 GitHub API"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := `[secret-request] {"status":"saved","name":"GITHUB_TOKEN"}`
	if out != want {
		t.Fatalf("result = %q, want %q", out, want)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("工具结果泄漏了密钥: %q", out)
	}
	// 描述与 schema 也必须不含任何值（值甚至不是参数）。
	for _, surface := range []string{tool.Description(), tool.Schema()} {
		if strings.Contains(surface, secret) {
			t.Fatalf("工具描述/schema 泄漏了密钥: %q", surface)
		}
	}
	// 模拟事件与 JSONL 转录：只有状态名前缀，没有密钥字节。
	event, _ := json.Marshal(map[string]any{"kind": "tool_end", "name": "request_secret", "output": out})
	jsonl := string(event) + "\n" + `{"kind":"message","role":"tool","content":` + requestSecretJSON(t, out) + "}\n"
	for _, surface := range []string{string(event), jsonl} {
		if strings.Contains(surface, secret) {
			t.Fatalf("事件/JSONL 泄漏了密钥: %q", surface)
		}
	}
	// 值确实到了凭据存储，且只以 $NAME 暴露。
	if got := sink.saved["GITHUB_TOKEN"]; got != secret {
		t.Fatalf("saved[%s] = %q, want secret", "GITHUB_TOKEN", got)
	}
	if got := sink.env["GITHUB_TOKEN"]; got != secret {
		t.Fatalf("env[%s] = %q, want secret", "GITHUB_TOKEN", got)
	}
	if requester.name != "GITHUB_TOKEN" || requester.reason != "调用 GitHub API" {
		t.Fatalf("requester got name=%q reason=%q", requester.name, requester.reason)
	}
}

func requestSecretJSON(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRequestSecretAsksOncePerName(t *testing.T) {
	requester := &fakeSecretRequester{value: "v"}
	sink := newFakeSecretSink()
	tool := &requestSecretTool{requester: requester, sink: sink}
	args := json.RawMessage(`{"name":"MY_KEY","reason":"r"}`)
	for i := 0; i < 3; i++ {
		if _, err := tool.Run(context.Background(), t.TempDir(), args); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if requester.calls != 1 {
		t.Fatalf("requester.calls = %d, want 1", requester.calls)
	}

	// 跨运行/恢复：sink 已有该名字时同样不再询问。
	other := &requestSecretTool{requester: &fakeSecretRequester{value: "ignored"}, sink: sink}
	if _, err := other.Run(context.Background(), t.TempDir(), args); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if len(sink.saved) != 1 {
		t.Fatalf("sink should keep one saved secret, got %v", sink.saved)
	}
}

func TestRequestSecretRejectsMalformedRequests(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"bad json", `{`},
		{"missing reason", `{"name":"A"}`},
		{"empty name", `{"name":"","reason":"r"}`},
		{"name with equals", `{"name":"A=B","reason":"r"}`},
		{"name with space", `{"name":"A B","reason":"r"}`},
		{"leading digit", `{"name":"1KEY","reason":"r"}`},
		{"reserved name", `{"name":"PATH","reason":"r"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := &requestSecretTool{requester: &fakeSecretRequester{value: "x"}, sink: newFakeSecretSink()}
			if _, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(tc.args)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestRegisterRequestSecretKeepsDefaultGolden(t *testing.T) {
	baseline := New(t.TempDir(), true).Names()
	r := New(t.TempDir(), true)
	want := append(append([]string(nil), baseline...), requestSecretName)
	RegisterRequestSecret(r, &fakeSecretRequester{}, newFakeSecretSink())
	if got := r.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	RegisterRequestSecret(r, &fakeSecretRequester{}, newFakeSecretSink())
	if len(r.Names()) != len(want) {
		t.Fatalf("重复注册改变了清单: %v", r.Names())
	}
	if got := New(t.TempDir(), true).Names(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("默认注册表被污染: %v", got)
	}
	RegisterRequestSecret(nil, nil, nil) // nil 安全
}
