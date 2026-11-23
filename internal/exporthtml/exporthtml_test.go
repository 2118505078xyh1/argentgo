package exporthtml

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRenderSelfContained(t *testing.T) {
	var out bytes.Buffer
	err := Render(&out, []Message{{ID: "1", Role: RoleUser, Content: "hello"}}, Options{Title: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, must := range []string{"<!doctype html>", "<style>", "Test", "hello"} {
		if !strings.Contains(html, must) {
			t.Errorf("HTML missing %q", must)
		}
	}
	// 自包含：无外链资源。
	for _, forbidden := range []string{"http://", "https://", "src=/", "href=/"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("HTML has external reference %q", forbidden)
		}
	}
}

func TestRenderEscapesXSS(t *testing.T) {
	var out bytes.Buffer
	malicious := `<script>alert(1)</script><img onerror="x">`
	Render(&out, []Message{{Role: RoleUser, Content: malicious}}, Options{})
	html := out.String()
	if strings.Contains(html, "<script>alert(1)") {
		t.Fatal("script tag not escaped")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("expected escaped script tag")
	}
	if strings.Contains(html, `onerror="x"`) {
		t.Fatal("event handler not escaped")
	}
}

func TestRenderToolFold(t *testing.T) {
	msg := Message{ID: "t1", Role: RoleTool, ToolName: "bash", Content: "output line 1\noutput line 2"}
	var folded bytes.Buffer
	Render(&folded, []Message{msg}, Options{FoldTools: true})
	if !strings.Contains(folded.String(), "<details>") || !strings.Contains(folded.String(), "展开工具输出") {
		t.Fatal("tool output should be folded")
	}

	var expanded bytes.Buffer
	Render(&expanded, []Message{msg}, Options{FoldTools: false})
	if strings.Contains(expanded.String(), "展开工具输出") {
		t.Fatal("tool output should not be folded when disabled")
	}
	if !strings.Contains(expanded.String(), "output line 1") {
		t.Fatal("expanded content missing")
	}
}

func TestRenderReasoningFold(t *testing.T) {
	var out bytes.Buffer
	Render(&out, []Message{{
		Role: RoleAssistant, Content: "answer", Reasoning: "let me think...",
	}}, Options{ShowReasoning: true})
	if !strings.Contains(out.String(), "思考过程") || !strings.Contains(out.String(), "let me think...") {
		t.Fatal("reasoning should be shown folded")
	}

	var out2 bytes.Buffer
	Render(&out2, []Message{{Role: RoleAssistant, Content: "answer", Reasoning: "secret"}}, Options{})
	if strings.Contains(out2.String(), "secret") {
		t.Fatal("reasoning should be hidden when disabled")
	}
}

func TestRenderMarkdownFence(t *testing.T) {
	var out bytes.Buffer
	Render(&out, []Message{{Role: RoleAssistant, Content: "before\n```go\nfunc main(){}\n```\nafter"}}, Options{})
	html := out.String()
	if !strings.Contains(html, "<pre>") || !strings.Contains(html, "func main(){}") {
		t.Fatal("fenced code should render as pre")
	}
}

func TestRenderTimestampsAndRoles(t *testing.T) {
	ts := time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC)
	var out bytes.Buffer
	Render(&out, []Message{
		{Role: RoleUser, TS: ts},
		{Role: RoleAssistant, TS: ts},
		{Role: RoleTool, ToolName: "grep", TS: ts},
		{Role: RoleSystem},
	}, Options{})
	html := out.String()
	wantTime := ts.Local().Format("01-02 15:04:05")
	for _, label := range []string{"你", "argentgo", "工具", "系统", wantTime, "grep"} {
		if !strings.Contains(html, label) {
			t.Errorf("HTML missing %q", label)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := Render(&out, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "<!doctype html>") {
		t.Fatal("empty render should still produce a document")
	}
}
