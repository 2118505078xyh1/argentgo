package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func hasItemContaining(items []string, sub string) bool {
	for _, item := range items {
		if strings.Contains(item, sub) {
			return true
		}
	}
	return false
}

func summaryIsEmpty(s StructuredSummary) bool {
	return len(s.Progress) == 0 && len(s.Uncertainties) == 0 && len(s.UserActions) == 0
}

func TestParseStructuredSummaryCanonicalSections(t *testing.T) {
	text := "## Progress\n" +
		"- built lexer\n" +
		"- added tests\n\n" +
		"## Uncertainties\n" +
		"- windows build fails\n\n" +
		"## User actions\n" +
		"- review patch\n"

	got, ok := ParseStructuredSummary(text)
	if !ok {
		t.Fatalf("ParseStructuredSummary returned false for canonical sections")
	}
	want := StructuredSummary{
		Progress:      []string{"built lexer", "added tests"},
		Uncertainties: []string{"windows build fails"},
		UserActions:   []string{"review patch"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed summary mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestParseStructuredSummaryFromSummarizeText(t *testing.T) {
	// Existing free text from Summarize: legacy headings must be recognized
	// without changing what Summarize writes.
	data, _ := json.Marshal(map[string]any{"path": "lexer.go"})
	dropped := []Entry{
		{Role: "user", Content: "build a lexer"},
		{Role: "tool", Name: "edit", Data: data},
		{Role: "tool", Name: "shell", Content: "compiling...\nerror: undefined: token"},
		{Role: "assistant", Content: "已改完 X。\nNext steps: add parser tests"},
	}
	text := Summarize(dropped, 1).Summary

	got, ok := ParseStructuredSummary(text)
	if !ok {
		t.Fatalf("could not parse existing Summarize text:\n%s", text)
	}
	if !hasItemContaining(got.Progress, "build a lexer") {
		t.Errorf("goal missing from Progress: %q", got.Progress)
	}
	if !hasItemContaining(got.Progress, "lexer.go") {
		t.Errorf("changed file missing from Progress: %q", got.Progress)
	}
	if !hasItemContaining(got.Uncertainties, "error: undefined: token") {
		t.Errorf("failure missing from Uncertainties: %q", got.Uncertainties)
	}
	if !hasItemContaining(got.UserActions, "add parser tests") {
		t.Errorf("next step missing from UserActions: %q", got.UserActions)
	}
}

func TestParseStructuredSummaryEmptyAndMissing(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantOK    bool
		wantEmpty bool
	}{
		{"empty", "", false, true},
		{"whitespace only", " \n\t\n", false, true},
		{"only unknown heading", "## Compaction\n- dropped 1 message(s), kept the last 0\n", false, true},
		{"recognized heading with placeholder", "## Progress\n- (none)\n", true, true},
		{"recognized heading with content", "## Progress\n- built lexer\n", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseStructuredSummary(tc.text)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (text %q)", ok, tc.wantOK, tc.text)
			}
			if empty := summaryIsEmpty(got); empty != tc.wantEmpty {
				t.Fatalf("empty = %v, want %v; got %#v", empty, tc.wantEmpty, got)
			}
			if !ok && !summaryIsEmpty(got) {
				t.Fatalf("failed parse must return a zero DTO, got %#v", got)
			}
		})
	}
}

func TestParseStructuredSummaryCJK(t *testing.T) {
	text := "## 进展\n" +
		"- 已完成词法分析器\n\n" +
		"## 不确定事项\n" +
		"- 编译失败：未定义符号\n\n" +
		"## 用户操作\n" +
		"- 下一步：补测试\n"

	got, ok := ParseStructuredSummary(text)
	if !ok {
		t.Fatalf("CJK headings were not recognized")
	}
	want := StructuredSummary{
		Progress:      []string{"已完成词法分析器"},
		Uncertainties: []string{"编译失败：未定义符号"},
		UserActions:   []string{"下一步：补测试"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CJK parse mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestParseStructuredSummaryFormatDriftFallsBack(t *testing.T) {
	// None of these carry a recognized markdown heading, so the caller must
	// keep treating the summary as free text instead of a trusted DTO.
	cases := []string{
		"Progress:\n- built lexer\nUncertainties:\n- windows build fails\nUser actions:\n- review patch\n",
		"## Progress update\n- built lexer\n## Unknowns today\n- x\n## Actions for later\n- y\n",
		"**Progress**\n- built lexer\n",
		"just prose about progress\n- built lexer\n",
	}
	for _, text := range cases {
		got, ok := ParseStructuredSummary(text)
		if ok {
			t.Errorf("drifted format was accepted as structured: %q -> %#v", text, got)
		}
		if !summaryIsEmpty(got) {
			t.Errorf("drifted format returned a non-zero DTO: %#v", got)
		}
	}
}

func TestParseStructuredSummarySkipsPlaceholdersAndDuplicates(t *testing.T) {
	text := "## Progress\n" +
		"- built lexer\n" +
		"- (none)\n" +
		"- built lexer\n" +
		"- 无\n"

	got, ok := ParseStructuredSummary(text)
	if !ok {
		t.Fatalf("recognized heading was not parsed")
	}
	if want := []string{"built lexer"}; !reflect.DeepEqual(got.Progress, want) {
		t.Fatalf("Progress = %q, want %q", got.Progress, want)
	}
}

func TestRenderStructuredSummaryRoundTrip(t *testing.T) {
	cases := []StructuredSummary{
		{
			Progress:      []string{"built lexer", "added tests"},
			Uncertainties: []string{"windows build fails"},
			UserActions:   []string{"review patch"},
		},
		{}, // empty fields round-trip through "- (none)" placeholders
	}
	for _, want := range cases {
		text := RenderStructuredSummary(want)
		for _, heading := range []string{"## Progress", "## Uncertainties", "## User actions"} {
			if !strings.Contains(text, heading) {
				t.Errorf("rendered text missing %q:\n%s", heading, text)
			}
		}
		got, ok := ParseStructuredSummary(text)
		if !ok {
			t.Fatalf("rendered text could not be parsed:\n%s", text)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip mismatch:\n got %#v\nwant %#v\ntext:\n%s", got, want, text)
		}
	}
}

func TestCompactionStructuredIsOptInProjection(t *testing.T) {
	c := Compaction{Summary: "## Progress\n- did a thing\n"}
	got, ok := c.Structured()
	if !ok {
		t.Fatalf("Compaction.Structured returned false")
	}
	if !reflect.DeepEqual(got.Progress, []string{"did a thing"}) {
		t.Fatalf("Progress = %q, want [did a thing]", got.Progress)
	}
	// The projection is read-only: the underlying free text is untouched.
	if c.Summary != "## Progress\n- did a thing\n" {
		t.Fatalf("Structured mutated Compaction.Summary: %q", c.Summary)
	}
}
