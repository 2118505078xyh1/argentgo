package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// provider_catalog 是纯本地检查：它渲染 provider.ValidateAll 的诊断，
// 状态只能是 ok/warn，绝不参与失败计数与退出码。
func TestCheckProviderCatalog(t *testing.T) {
	check := checkProviderCatalog()
	if check.name != "provider_catalog" {
		t.Fatalf("name = %q, want provider_catalog", check.name)
	}
	if check.status != statusOK && check.status != statusWarn {
		t.Fatalf("status = %q, want ok or warn", check.status)
	}
	if strings.TrimSpace(check.detail) == "" {
		t.Fatal("detail is empty")
	}
}

func TestDoctorRendersProviderCatalog(t *testing.T) {
	// ftp scheme 在 http.Client 层就报错，避免测试依赖真实网络或随机端口。
	const baseURL = "ftp://doctor.invalid"

	t.Run("text", func(t *testing.T) {
		ioState, out, _ := newTestIO("", nil)
		if code := RunWith(App{IO: ioState}, []string{"doctor", "--base-url", baseURL}); code != 0 {
			t.Fatalf("exit = %d, want 0; out = %s", code, out.String())
		}
		if !strings.Contains(out.String(), "provider_catalog") {
			t.Fatalf("text output missing provider_catalog:\n%s", out.String())
		}
	})

	t.Run("json", func(t *testing.T) {
		ioState, out, _ := newTestIO("", nil)
		if code := RunWith(App{IO: ioState}, []string{"doctor", "--json", "--base-url", baseURL}); code != 0 {
			t.Fatalf("exit = %d, want 0; out = %s", code, out.String())
		}
		var report struct {
			Checks   []doctorJSONCheck `json:"checks"`
			Failures int               `json:"failures"`
		}
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("不是合法 JSON: %v\n%s", err, out.String())
		}
		var catalog *doctorJSONCheck
		for i := range report.Checks {
			if report.Checks[i].Name == "provider_catalog" {
				catalog = &report.Checks[i]
				break
			}
		}
		if catalog == nil {
			t.Fatalf("JSON 缺少 provider_catalog: %s", out.String())
		}
		if catalog.Status != statusOK && catalog.Status != statusWarn {
			t.Fatalf("status = %q, want ok or warn", catalog.Status)
		}
		if report.Failures != 0 {
			t.Fatalf("failures = %d, want 0", report.Failures)
		}
	})
}
