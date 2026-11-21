package tools

import (
	"strings"
	"testing"
)

// hasIssueKind 报告 issues 中是否包含指定类别。
func hasIssueKind(issues []Issue, kind IssueKind) bool {
	for _, issue := range issues {
		if issue.Kind == kind {
			return true
		}
	}
	return false
}

func TestValidateTodoStateAcceptsFourStatuses(t *testing.T) {
	// 四种枚举逐一通过校验；completed 单项需带验证记录以避开验证不变量，
	// 从而单独证明状态枚举本身是合法的。
	for _, status := range []string{
		todoStatusPending,
		todoStatusInProgress,
		todoStatusCompleted,
		todoStatusCancelled,
	} {
		t.Run(status, func(t *testing.T) {
			item := todoItem{ID: "1", Text: "x", Status: status}
			if status == todoStatusCompleted {
				item.Verification = "go test ./..."
			}
			if got := ValidateTodoState([]todoItem{item}); len(got) != 0 {
				t.Fatalf("ValidateTodoState(%q) = %+v, want no issues", status, got)
			}
		})
	}
}

func TestValidateTodoStateRejectsInvalidStatus(t *testing.T) {
	for _, status := range []string{"", "doing", "done", "IN_PROGRESS", "pending "} {
		t.Run(status, func(t *testing.T) {
			got := ValidateTodoState([]todoItem{{Status: status}})
			if !hasIssueKind(got, IssueInvalidStatus) {
				t.Fatalf("ValidateTodoState(status=%q) = %+v, want IssueInvalidStatus", status, got)
			}
			if hasIssueKind(got, IssueMissingVerification) {
				t.Fatalf("ValidateTodoState(status=%q) = %+v, unexpected verification issue", status, got)
			}
		})
	}
}

func TestValidateTodoStateRejectsMultipleInProgress(t *testing.T) {
	items := []todoItem{
		{ID: "1", Text: "a", Status: todoStatusPending},
		{ID: "2", Text: "b", Status: todoStatusInProgress},
		{ID: "3", Text: "c", Status: todoStatusInProgress},
	}
	got := ValidateTodoState(items)
	if !hasIssueKind(got, IssueMultipleInProgress) {
		t.Fatalf("ValidateTodoState = %+v, want IssueMultipleInProgress", got)
	}
	for _, issue := range got {
		if issue.Kind == IssueMultipleInProgress && issue.Value != "2" {
			t.Fatalf("multiple-in-progress Value = %q, want 2", issue.Value)
		}
	}
}

func TestValidateTodoStateAllowsSingleInProgress(t *testing.T) {
	// 合法混合状态：一条完成（带验证）、一条进行中、一条待办。
	items := []todoItem{
		{ID: "1", Text: "a", Status: todoStatusCompleted, Verification: "go test ./..."},
		{ID: "2", Text: "b", Status: todoStatusInProgress},
		{ID: "3", Text: "c", Status: todoStatusPending},
	}
	if got := ValidateTodoState(items); len(got) != 0 {
		t.Fatalf("ValidateTodoState = %+v, want no issues", got)
	}
}

func TestValidateTodoStateRequiresVerificationWhenAllCompleted(t *testing.T) {
	items := []todoItem{
		{ID: "1", Text: "a", Status: todoStatusCompleted},
		{ID: "2", Text: "b", Status: todoStatusCompleted},
	}
	if got := ValidateTodoState(items); !hasIssueKind(got, IssueMissingVerification) {
		t.Fatalf("ValidateTodoState = %+v, want IssueMissingVerification", got)
	}

	// 任意一条带上验证记录即满足要求。
	items[1].Verification = "make test"
	if got := ValidateTodoState(items); len(got) != 0 {
		t.Fatalf("ValidateTodoState with verification = %+v, want no issues", got)
	}
}

func TestValidateTodoStateWhitespaceVerificationDoesNotCount(t *testing.T) {
	items := []todoItem{{ID: "1", Text: "a", Status: todoStatusCompleted, Verification: "  \t\n"}}
	if got := ValidateTodoState(items); !hasIssueKind(got, IssueMissingVerification) {
		t.Fatalf("ValidateTodoState = %+v, want IssueMissingVerification for blank verification", got)
	}
}

func TestValidateTodoStateCollectsMultipleIssues(t *testing.T) {
	items := []todoItem{
		{ID: "1", Text: "a", Status: "doing"},
		{ID: "2", Text: "b", Status: todoStatusInProgress},
		{ID: "3", Text: "c", Status: todoStatusInProgress},
	}
	got := ValidateTodoState(items)
	if !hasIssueKind(got, IssueInvalidStatus) ||
		!hasIssueKind(got, IssueMultipleInProgress) {
		t.Fatalf("ValidateTodoState = %+v, want both invalid-status and multiple-in-progress", got)
	}
}

func TestValidateTodoStateEmptyListIsValid(t *testing.T) {
	if got := ValidateTodoState(nil); len(got) != 0 {
		t.Fatalf("ValidateTodoState(nil) = %+v, want no issues", got)
	}
}

func TestTodoVerificationFieldKeepsDefaultBytes(t *testing.T) {
	// 不带 Verification 的任务序列化后必须与旧版逐字节一致，证明新增字段
	// 是纯附加的、不改变默认持久化格式。
	const want = "{\n  \"todos\": [\n    {\n      \"id\": \"1\",\n      \"text\": \"x\",\n      \"status\": \"pending\"\n    }\n  ]\n}"
	data, err := marshalTodos([]todoItem{{ID: "1", Text: "x", Status: "pending"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("marshalTodos without verification = %q, want %q", data, want)
	}
	if strings.Contains(string(data), "verification") {
		t.Fatalf("default bytes unexpectedly contain verification field: %q", data)
	}
}

func TestTodoVerificationFieldSerializesWhenSet(t *testing.T) {
	data, err := marshalTodos([]todoItem{{ID: "1", Text: "x", Status: "completed", Verification: "go test ./..."}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"verification": "go test ./..."`) {
		t.Fatalf("marshalTodos with verification = %q, want field present", data)
	}
}
