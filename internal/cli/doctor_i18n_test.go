package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/i18n"
)

// TestDoctorI18nIssuesClean 验证审计通过时不产生任何 issue：这是 doctor
// 在健康仓库上输出逐字节不变、golden 不破的前提。
func TestDoctorI18nIssuesClean(t *testing.T) {
	report := i18n.AuditReport{OK: true}
	if issues := doctorI18nIssues(report); len(issues) != 0 {
		t.Fatalf("OK 报告产生了 issue: %+v", issues)
	}
}

// TestDoctorI18nIssuesFailed 验证审计不通过时只追加一条 warn 级 issue，
// 名称稳定为 i18n，消息复用审计自带的中文摘要（两态均可表驱动覆盖）。
func TestDoctorI18nIssuesFailed(t *testing.T) {
	tests := []struct {
		name   string
		report i18n.AuditReport
	}{
		{"key issues only", i18n.AuditReport{KeyIssues: 2}},
		{"placeholder issues only", i18n.AuditReport{PlaceholderIssues: 3}},
		{"both issue kinds", i18n.AuditReport{KeyIssues: 1, PlaceholderIssues: 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := doctorI18nIssues(tt.report)
			if len(issues) != 1 {
				t.Fatalf("issues = %d, want 1: %+v", len(issues), issues)
			}
			issue := issues[0]
			if issue.Name != "i18n" || issue.Level != statusWarn {
				t.Fatalf("issue = %+v, want warn/i18n", issue)
			}
			if issue.Message != tt.report.Summary() {
				t.Fatalf("message = %q, want %q", issue.Message, tt.report.Summary())
			}
			if !strings.Contains(issue.Message, "i18n 审计未通过") {
				t.Fatalf("message 未使用审计中文摘要: %q", issue.Message)
			}
		})
	}
}

// TestDoctorI18nRealAuditWiring 验证真实路径接入的是纯函数 i18n.FullAudit()：
// 审计通过时无 issue，不通过时恰好一条，且重复调用结果一致。
func TestDoctorI18nRealAuditWiring(t *testing.T) {
	report := i18n.FullAudit()
	issues := doctorI18nIssues(report)
	want := 0
	if !report.OK {
		want = 1
	}
	if len(issues) != want {
		t.Fatalf("issues = %d, want %d (report OK=%v): %+v", len(issues), want, report.OK, issues)
	}
	if again := doctorI18nIssues(i18n.FullAudit()); len(again) != len(issues) {
		t.Fatalf("重复调用结果不一致: %d vs %d", len(again), len(issues))
	}
}

// TestDoctorI18nIssueDoesNotBreakGolden 端到端验证 i18n 审计告警走 issues
// 旁路：健康仓库上文本输出不出现 i18n 告警行，JSON 输出仍是恰好 9 个 check、
// 零 failures、无 i18n issue，从而不破坏既有 golden 与退出码语义。
func TestDoctorI18nIssueDoesNotBreakGolden(t *testing.T) {
	// 当前真实文案表审计应当通过；若未通过，本测试的"无 i18n 告警"断言
	// 会失败——那也是正确的信号（i18n 完整性回归应被 doctor 暴露）。
	if report := i18n.FullAudit(); !report.OK {
		t.Fatalf("真实 i18n 审计未通过，golden 前提不成立: %s", report.Summary())
	}

	// 文本分支：provider 指向固定失败的 ftp URL，退出码只由既有 check 决定。
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState}
	if code := RunWith(app, []string{"doctor", "--base-url", "ftp://doctor.invalid"}); code != 0 {
		t.Fatalf("text exit = %d, want 0; out = %s", code, out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "i18n") && strings.Contains(line, statusWarn) {
			t.Fatalf("健康仓库的文本输出出现 i18n 告警，golden 被破坏: %q", line)
		}
	}

	// JSON 分支：checks 数量仍为 9，failures 为 0，issues 无 i18n 条目。
	ioState2, out2, _ := newTestIO("", nil)
	app2 := App{IO: ioState2}
	if code := RunWith(app2, []string{"doctor", "--json", "--base-url", "ftp://doctor.invalid"}); code != 0 {
		t.Fatalf("json exit = %d, want 0; out = %s", code, out2.String())
	}
	var report struct {
		Checks   []map[string]any `json:"checks"`
		Failures int              `json:"failures"`
		Issues   []doctorIssue    `json:"issues"`
	}
	if err := json.Unmarshal(out2.Bytes(), &report); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, out2.String())
	}
	if len(report.Checks) != 9 || report.Failures != 0 {
		t.Fatalf("checks = %d, failures = %d, want 9/0", len(report.Checks), report.Failures)
	}
	for _, issue := range report.Issues {
		if issue.Name == "i18n" {
			t.Fatalf("健康仓库的 JSON issues 出现 i18n: %+v", issue)
		}
	}
}
