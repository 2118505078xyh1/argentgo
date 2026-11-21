package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 doctor 覆盖面的"验证层"测试：R35 的 baseURL scheme 告警与 R36 的
// provider.ValidateAll 诊断已在 doctor.go 中实现，这里不重复实现，而是把这些
// 不变量钉死，防止后续接线回归。所有用例都用注入的 env 与临时目录，不联网、
// 不读进程环境；provider 探测指向 ftp scheme（在 http.Client 层立即失败），
// 只会产生 warn，不改变退出码。

const doctorVerifyRefusedURL = "ftp://doctor.invalid"

// TestDoctorLegalAndIllegalBaseURLWarnings 端到端锁定 baseURL 告警：
// 合法配置（https 与回环 http）不得出现 "warnings:" 段，非法配置（明文 http）
// 必须出现 warn 级 baseURL 行；两种情况的退出码都为 0。
func TestDoctorLegalAndIllegalBaseURLWarnings(t *testing.T) {
	tests := []struct {
		name       string
		configJSON string
		wantWarn   bool
	}{
		{"https baseURL is clean", `{"baseURL":"https://api.example.test/v1"}`, false},
		{"loopback http baseURL is clean", `{"baseURL":"http://127.0.0.1:8080/v1"}`, false},
		{"all legal aliases are clean", `{
			"baseURL":"https://a.example.test/v1",
			"baseUrl":"https://b.example.test/v1",
			"base_url":"https://c.example.test/v1",
			"providers":{
				"zeta":{"baseURL":"https://z.example.test/v1"},
				"alpha":{"base_url":"http://localhost:11434/v1"}
			}
		}`, false},
		{"http non-loopback baseURL warns", `{"baseURL":"http://evil.example.test/v1"}`, true},
		{"provider baseURL warns with path", `{
			"baseURL":"https://safe.example.test/v1",
			"providers":{"evil":{"base_url":"http://evil.example.test/v1"}}
		}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tt.configJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			ioState, out, _ := newTestIO("", nil)
			app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
			if code := RunWith(app, []string{"doctor", "--base-url", doctorVerifyRefusedURL}); code != 0 {
				t.Fatalf("exit = %d, want 0; out:\n%s", code, out.String())
			}

			hasWarnings := strings.Contains(out.String(), "\nwarnings:")
			hasBaseURLWarn := strings.Contains(out.String(), doctorBaseURLIssueMessage)
			if hasWarnings != tt.wantWarn || hasBaseURLWarn != tt.wantWarn {
				t.Fatalf("warnings=%v baseURLWarn=%v, want both %v; out:\n%s",
					hasWarnings, hasBaseURLWarn, tt.wantWarn, out.String())
			}
			if tt.wantWarn {
				// 必须是 warn 级、且给出配置路径的独立 issue 行。
				found := false
				for _, line := range strings.Split(out.String(), "\n") {
					if strings.Contains(line, statusWarn) && strings.Contains(line, "baseURL") &&
						strings.Contains(line, "$.") {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("缺少 warn 级 baseURL issue 行:\n%s", out.String())
				}
			}
		})
	}
}

// TestDoctorJSONIssuesFieldStable 锁定 --json 输出的 issues 字段结构：
// 合法配置时它是空数组（而非 null 或缺失），非法配置时恰好一条 warn issue，
// 且 failures 始终为 0（告警不改变退出码语义）。
func TestDoctorJSONIssuesFieldStable(t *testing.T) {
	tests := []struct {
		name       string
		configJSON string
		wantIssues int
	}{
		{"legal baseURL yields empty issues array", `{"baseURL":"https://api.example.test/v1"}`, 0},
		{"illegal baseURL yields one warn issue", `{"baseURL":"http://evil.example.test/v1"}`, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tt.configJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			ioState, out, _ := newTestIO("", nil)
			app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
			if code := RunWith(app, []string{"doctor", "--json", "--base-url", doctorVerifyRefusedURL}); code != 0 {
				t.Fatalf("exit = %d, want 0; out:\n%s", code, out.String())
			}

			// 用 RawMessage 确认 issues 键存在且为数组，避免 json.Unmarshal
			// 把缺失字段与 null 都映射到 nil 切片而漏检。
			var raw struct {
				Failures int             `json:"failures"`
				Issues   json.RawMessage `json:"issues"`
			}
			if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
				t.Fatalf("不是合法 JSON: %v\n%s", err, out.String())
			}
			if len(raw.Issues) == 0 || string(raw.Issues) == "null" {
				t.Fatalf("issues 字段缺失或为 null: %s", out.String())
			}
			var issues []doctorIssue
			if err := json.Unmarshal(raw.Issues, &issues); err != nil {
				t.Fatalf("issues 不是数组: %v (%s)", err, raw.Issues)
			}
			if len(issues) != tt.wantIssues {
				t.Fatalf("issues = %d, want %d: %s", len(issues), tt.wantIssues, out.String())
			}
			if raw.Failures != 0 {
				t.Fatalf("failures = %d, want 0", raw.Failures)
			}
			for _, issue := range issues {
				if issue.Level != statusWarn {
					t.Fatalf("issue level = %q, want warn: %+v", issue.Level, issue)
				}
			}
		})
	}
}

// TestDoctorProviderCatalogUsesValidateAll 验证 R36 的 provider_catalog 检查由
// provider.ValidateAll 驱动： doctor 报告必须含一条结构完整的 provider_catalog
// 检查，其状态与 detail 直接来自 checkProviderCatalog（纯本地、无 I/O）。
func TestDoctorProviderCatalogUsesValidateAll(t *testing.T) {
	catalog := checkProviderCatalog()
	if catalog.name != "provider_catalog" {
		t.Fatalf("name = %q, want provider_catalog", catalog.name)
	}
	if catalog.status != statusOK && catalog.status != statusWarn {
		t.Fatalf("status = %q, want ok/warn", catalog.status)
	}
	// 异常模板走 warn，正常走 ok；两种状态都不得计入 failures（无 fail）。
	if catalog.status == statusFail {
		t.Fatalf("provider_catalog 不应为 fail: %+v", catalog)
	}
	if strings.TrimSpace(catalog.detail) == "" {
		t.Fatal("provider_catalog detail 为空")
	}

	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState}
	if code := RunWith(app, []string{"doctor", "--json", "--base-url", doctorVerifyRefusedURL}); code != 0 {
		t.Fatalf("exit = %d, want 0; out:\n%s", code, out.String())
	}
	var report doctorJSONRaw
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, out.String())
	}
	found := false
	for _, check := range report.Checks {
		if check["name"] == "provider_catalog" {
			found = true
			if check["status"] != catalog.status || check["detail"] != redactDoctorDetail(catalog.detail) {
				t.Fatalf("JSON provider_catalog = %v, want %+v", check, catalog)
			}
		}
	}
	if !found {
		t.Fatalf("JSON 缺少 provider_catalog 检查: %s", out.String())
	}
}
