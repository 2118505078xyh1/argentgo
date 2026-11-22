package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/mcp"
)

// TestDoctorMCPRuntimeHealthIssuesFromReport 用 fake reporter（1 健康 + 1 失败，含 CJK
// 服务名）验证纯映射：健康服务被跳过，失败服务的名称与 LastError 出现在 issue 中。
func TestDoctorMCPRuntimeHealthIssuesFromReport(t *testing.T) {
	fake := mcp.MCPHealthReport{
		OverallHealthy: false,
		Servers: []mcp.ServerHealth{
			{Name: "健康服务", Healthy: true, ToolCount: 2, State: string(mcp.ConnReady)},
			{Name: "失败服务", Healthy: false, State: string(mcp.ConnFailed), LastError: "握手超时：connection reset"},
		},
	}
	issues := doctorMCPRuntimeHealthIssuesFromReport(fake)
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1: %+v", len(issues), issues)
	}
	issue := issues[0]
	if issue.Name != doctorMCPRuntimeHealthIssueName || issue.Level != statusWarn {
		t.Fatalf("issue = %+v, want %s/%s", issue, doctorMCPRuntimeHealthIssueName, statusWarn)
	}
	for _, want := range []string{"失败服务", "握手超时：connection reset", "state=failed"} {
		if !strings.Contains(issue.Message, want) {
			t.Fatalf("message %q missing %q", issue.Message, want)
		}
	}
	for _, leak := range []string{"健康服务"} {
		if strings.Contains(issue.Message, leak) {
			t.Fatalf("message %q leaked healthy server %q", issue.Message, leak)
		}
	}
}

// TestDoctorMCPRuntimeHealthIssuesDefaultOff 验证默认关闭：opt-in 未设置时即使
// 存储目录存在快照也不读取，零输出逐字节不变；开启但文件缺失同样静默。
func TestDoctorMCPRuntimeHealthIssuesDefaultOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mcp.MCPHealthReportFile)
	fake := mcp.MCPHealthReport{Servers: []mcp.ServerHealth{
		{Name: "失败服务", Healthy: false, LastError: "boom"},
	}}
	data, err := json.Marshal(fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// opt-in 关闭：env 明确为空。
	off := App{StorageDir: dir, IO: IO{Env: func(string) string { return "" }}}
	if issues := doctorMCPRuntimeHealthIssues(off); len(issues) != 0 {
		t.Fatalf("默认关闭却产生 issues: %+v", issues)
	}
	// opt-in 开启但文件不存在：同样静默。
	missing := App{StorageDir: t.TempDir(), IO: IO{Env: func(key string) string {
		if key == mcp.MCPHealthEnv {
			return "1"
		}
		return ""
	}}}
	if issues := doctorMCPRuntimeHealthIssues(missing); len(issues) != 0 {
		t.Fatalf("文件缺失却产生 issues: %+v", issues)
	}
}

// TestDoctorMCPRuntimeHealthEndToEnd 端到端：存储目录有 fake 快照且 opt-in 开启时，
// 文本分支在 warnings 段输出服务名与 LastError，但不新增 check、不改退出码；
// JSON 分支保持 9 check / 0 failures，issues 仍是 name/level/message 三字段。
func TestDoctorMCPRuntimeHealthEndToEnd(t *testing.T) {
	dir := t.TempDir()
	fake := mcp.MCPHealthReport{Servers: []mcp.ServerHealth{
		{Name: "健康服务", Healthy: true},
		{Name: "失败服务", Healthy: false, State: string(mcp.ConnFailed), LastError: "秘钥 sk-secret-出错了"},
	}}
	data, err := json.Marshal(fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mcp.MCPHealthReportFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{mcp.MCPHealthEnv: "1"}

	// 文本分支。
	ioState, out, _ := newTestIO("", env)
	app := App{IO: ioState, StorageDir: dir}
	if code := RunWith(app, []string{"doctor", "--base-url", doctorVerifyRefusedURL}); code != 0 {
		t.Fatalf("文本退出码 = %d, want 0; out = %s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{"warnings:", "mcpHealth", "失败服务", "lastError=", "state=failed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("文本输出缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "check(s) failed") {
		t.Fatalf("MCP 健康 issue 不应计入失败:\n%s", text)
	}
	// 健康服务名不应出现在告警里。
	if strings.Contains(text, "健康服务") {
		t.Fatalf("健康服务不应出现在告警: %s", text)
	}

	// JSON 分支。
	ioState2, out2, _ := newTestIO("", env)
	app2 := App{IO: ioState2, StorageDir: dir}
	if code := RunWith(app2, []string{"doctor", "--json", "--base-url", doctorVerifyRefusedURL}); code != 0 {
		t.Fatalf("JSON 退出码 = %d, want 0; out = %s", code, out2.String())
	}
	var report struct {
		Checks   []map[string]any `json:"checks"`
		Failures int              `json:"failures"`
		Issues   []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(out2.Bytes(), &report); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, out2.String())
	}
	if len(report.Checks) != 9 || report.Failures != 0 {
		t.Fatalf("checks=%d failures=%d, want 9/0: %s", len(report.Checks), report.Failures, out2.String())
	}
	if len(report.Issues) != 1 {
		t.Fatalf("issues=%d, want 1: %s", len(report.Issues), out2.String())
	}
	issue := report.Issues[0]
	if len(issue) != 3 || issue["name"] != doctorMCPRuntimeHealthIssueName || issue["level"] != statusWarn {
		t.Fatalf("issue = %v, want 三字段 %s/warn", issue, doctorMCPRuntimeHealthIssueName)
	}
	// LastError 形似密钥的片段必须已被脱敏，不能原样进入报告。
	message := issue["message"].(string)
	if strings.Contains(message, "sk-secret-出错了") {
		t.Fatalf("issue message 泄漏了原始错误文本: %s", message)
	}
}
