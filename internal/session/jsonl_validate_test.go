package session

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// issueSummary 把问题压成 "行号:种类" 的紧凑序列，便于整体验断顺序与分类。
func issueSummary(issues []JSONLIssue) []string {
	out := make([]string, len(issues))
	for i, issue := range issues {
		out[i] = fmt.Sprintf("%d:%s", issue.Line, issue.Kind)
	}
	return out
}

func assertIssues(t *testing.T, data string, want []string) []JSONLIssue {
	t.Helper()
	issues := ValidateJSONL([]byte(data))
	if issues == nil {
		t.Fatal("ValidateJSONL must return a non-nil slice")
	}
	if got := issueSummary(issues); !reflect.DeepEqual(got, want) {
		t.Fatalf("issues = %v, want %v\nfull: %v", got, want, issues)
	}
	return issues
}

func TestValidateJSONLValidSession(t *testing.T) {
	const data = `{"id":"n000001","parent":"","kind":"header","data":{}}
{"id":"n000002","parent":"n000001","kind":"message","data":{"role":"user"}}
{"id":"n000003","parent":"n000002","kind":"message","data":{"role":"assistant"}}`
	assertIssues(t, data, []string{})
}

func TestValidateJSONLEmptyInput(t *testing.T) {
	for name, data := range map[string]string{
		"nil-like empty": "",
		"one newline":    "\n",
		"only blanks":    "  \t\n\r\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			assertIssues(t, data, []string{})
		})
	}
}

func TestValidateJSONLInvalidJSON(t *testing.T) {
	const data = `{"id":"a","parent":""}
{"id": broken
{"id":"c","parent":"a"}`
	issues := assertIssues(t, data, []string{"2:invalid_json"})
	if !strings.Contains(issues[0].Detail, "invalid JSON") {
		t.Fatalf("Detail = %q, want it to mention invalid JSON", issues[0].Detail)
	}
}

func TestValidateJSONLInvalidJSONNonObjectAndWrongTypes(t *testing.T) {
	// 数组、纯数字、以及 id 非字符串的行都记为 invalid_json。
	const data = `[1,2,3]
42
{"id":123,"parent":""}`
	assertIssues(t, data, []string{
		"1:invalid_json",
		"2:invalid_json",
		"3:invalid_json",
	})
}

func TestValidateJSONLMissingID(t *testing.T) {
	const data = `{"id":"a","parent":""}
{"parent":"a"}
{"id":"","parent":"a"}`
	issues := assertIssues(t, data, []string{"2:missing_id", "3:missing_id"})
	for _, issue := range issues {
		if !strings.Contains(issue.Detail, "no id") {
			t.Fatalf("Detail = %q, want it to mention no id", issue.Detail)
		}
	}
}

func TestValidateJSONLDuplicateID(t *testing.T) {
	const data = `{"id":"a","parent":""}
{"id":"b","parent":"a"}
{"id":"a","parent":"b"}`
	issues := assertIssues(t, data, []string{"3:duplicate_id"})
	if !strings.Contains(issues[0].Detail, "first seen on line 1") {
		t.Fatalf("Detail = %q, want first-seen line reference", issues[0].Detail)
	}
}

func TestValidateJSONLDanglingParent(t *testing.T) {
	const data = `{"id":"a","parent":""}
{"id":"b","parent":"missing"}`
	issues := assertIssues(t, data, []string{"2:dangling_parent"})
	if !strings.Contains(issues[0].Detail, `"missing"`) {
		t.Fatalf("Detail = %q, want it to name the missing parent", issues[0].Detail)
	}
}

func TestValidateJSONLParentMayAppearLater(t *testing.T) {
	// 两遍实现：parent 指向更靠后的记录不应误报为悬空。
	const data = `{"id":"a","parent":"b"}
{"id":"b","parent":""}`
	assertIssues(t, data, []string{})
}

func TestValidateJSONLSelfParent(t *testing.T) {
	issues := assertIssues(t, `{"id":"a","parent":"a"}`, []string{"1:self_parent"})
	if !strings.Contains(issues[0].Detail, "itself") {
		t.Fatalf("Detail = %q, want it to mention self-reference", issues[0].Detail)
	}
}

func TestValidateJSONLCJKIDs(t *testing.T) {
	const data = `{"id":"记录一","parent":""}
{"id":"记录二","parent":"记录一"}
{"id":"中文三号","parent":"记录二"}`
	assertIssues(t, data, []string{})
}

func TestValidateJSONLCJKIDIssues(t *testing.T) {
	// CJK id 同样参与重复与悬空检测；同一行可以产生多个问题，顺序固定。
	const data = `{"id":"甲","parent":""}
{"id":"甲","parent":"不存在"}
{"id":"乙","parent":"甲"}`
	assertIssues(t, data, []string{
		"2:duplicate_id",
		"2:dangling_parent",
	})
}

func TestValidateJSONLBlankLinesKeepLineNumbers(t *testing.T) {
	data := "\n" + // line 1
		"  \t \n" + // line 2
		`{"id":"a","parent":""}` + "\n" + // line 3
		"\r\n" + // line 4 (CRLF blank)
		`{"id":"b","parent":"gone"}` + "\n" // line 5
	assertIssues(t, data, []string{"5:dangling_parent"})
}

func TestValidateJSONLIssuesSortedByLineAndStable(t *testing.T) {
	const data = `{"id":"a","parent":""}
{"id":"b","parent":"bad"}
not-json
{"id":"c","parent":"c"}
{"id":"a","parent":"a"}`
	issues := assertIssues(t, data, []string{
		"2:dangling_parent",
		"3:invalid_json",
		"4:self_parent",
		"5:duplicate_id",
		"5:self_parent",
	})
	for i := 1; i < len(issues); i++ {
		if issues[i].Line < issues[i-1].Line {
			t.Fatalf("issues are not sorted by line: %v", issueSummary(issues))
		}
	}
}

func TestValidateJSONLDoesNotModifyInput(t *testing.T) {
	data := []byte("{\"id\":\"甲\",\"parent\":\"\"}\n\n坏行\n")
	before := bytes.Clone(data)
	_ = ValidateJSONL(data)
	if !bytes.Equal(data, before) {
		t.Fatalf("ValidateJSONL modified its input: got %q, want %q", data, before)
	}
}

func TestValidateJSONLDeterministic(t *testing.T) {
	data := []byte(`{"id":"甲","parent":""}
{"id":"甲","parent":"不存在"}
坏行
{"id":"乙","parent":"乙"}`)
	first := ValidateJSONL(data)
	for i := 0; i < 10; i++ {
		got := ValidateJSONL(data)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d = %v, want %v", i, got, first)
		}
	}
}

func TestJSONLIssueString(t *testing.T) {
	issue := JSONLIssue{Line: 7, Kind: JSONLKindMissingID, Detail: "record has no id"}
	got := issue.String()
	for _, want := range []string{"line 7", JSONLKindMissingID, "record has no id"} {
		if !strings.Contains(got, want) {
			t.Fatalf("String() = %q, want it to contain %q", got, want)
		}
	}
}
