package tools_test

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/tools"
)

func TestToolSearchMatchesNamesAndDescriptions(t *testing.T) {
	registry := tools.New(t.TempDir(), true)

	out := mustRun(t, registry, "tool_search", `{"query":"edit"}`)
	for _, want := range []string{"edit:", "multi_edit:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool_search(edit) = %q, want %q", out, want)
		}
	}
	upper := mustRun(t, registry, "tool_search", `{"query":"WEB_FETCH"}`)
	if !strings.Contains(upper, "web_fetch:") {
		t.Fatalf("tool_search is not case-insensitive: %q", upper)
	}
	byDescription := mustRun(t, registry, "tool_search", `{"query":"IANA"}`)
	if !strings.Contains(byDescription, "current_time:") {
		t.Fatalf("tool_search did not match a description: %q", byDescription)
	}
}

func TestToolSearchFindsEveryNewToolAndCapsResults(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	for _, name := range []string{"current_time", "web_search", "repo_map", "submit_plan", "tool_search"} {
		out := mustRun(t, registry, "tool_search", `{"query":"`+name+`"}`)
		if !strings.Contains(out, name+":") {
			t.Fatalf("tool_search(%s) = %q", name, out)
		}
	}
	if !contains(registry.Names(), "tool_search") {
		t.Fatalf("tool_search is not registered: %v", registry.Names())
	}

	one := mustRun(t, registry, "tool_search", `{"query":"e","limit":1}`)
	if strings.Contains(one, "\n") {
		t.Fatalf("tool_search ignored the limit: %q", one)
	}
	if out := mustRun(t, registry, "tool_search", `{"query":"zzz-no-such-tool"}`); !strings.Contains(out, "no tools match") {
		t.Fatalf("tool_search no-match = %q", out)
	}
}

func TestToolSearchRejectsInvalidArgs(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	if _, err := registry.Run(context.Background(), "tool_search", `{"query":""}`); err == nil || !strings.Contains(err.Error(), "query must not be empty") {
		t.Fatalf("err = %v, want an empty-query error", err)
	}
	if _, err := registry.Run(context.Background(), "tool_search", `{`); err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Fatalf("err = %v, want a bad-arguments error", err)
	}
}
