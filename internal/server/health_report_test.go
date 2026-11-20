package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthReportJSONStructure 验证报告含四个稳定字段且类型正确，
// checks 的布尔值被如实保留。
func TestHealthReportJSONStructure(t *testing.T) {
	got := healthReportJSON("ok", "v1.2.3", "windows/amd64", map[string]bool{
		"config":  true,
		"storage": false,
	})

	var report struct {
		Status   string          `json:"status"`
		Version  string          `json:"version"`
		Platform string          `json:"platform"`
		Checks   map[string]bool `json:"checks"`
	}
	if err := json.Unmarshal(got, &report); err != nil {
		t.Fatalf("unmarshal %q: %v", got, err)
	}
	if report.Status != "ok" || report.Version != "v1.2.3" || report.Platform != "windows/amd64" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.Checks) != 2 || !report.Checks["config"] || report.Checks["storage"] {
		t.Fatalf("checks = %v", report.Checks)
	}
}

// TestHealthReportJSONFieldStability 验证字段始终存在（即使为空）、
// 输出确定（相同输入逐字节一致），且 checks 键按字典序排列。
func TestHealthReportJSONFieldStability(t *testing.T) {
	checks := map[string]bool{"zeta": true, "alpha": false, "mid": true}
	first := healthReportJSON("", "", "", checks)
	second := healthReportJSON("", "", "", checks)
	if !bytes.Equal(first, second) {
		t.Fatalf("non-deterministic output:\n%s\n%s", first, second)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(first, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"status", "version", "platform", "checks"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing field %q in %s", key, first)
		}
	}
	// 字典序：alpha < mid < zeta。
	const wantOrder = `"alpha":false,"mid":true,"zeta":true`
	if !strings.Contains(string(first), wantOrder) {
		t.Fatalf("checks not sorted: %s", first)
	}
}

// TestHealthReportJSONRedactionCollisionIsDeterministic 守护脱敏键碰撞时的
// 确定性：两个不同的键脱敏后相同，胜出者由字典序固定，重复调用结果一致。
func TestHealthReportJSONRedactionCollisionIsDeterministic(t *testing.T) {
	checks := map[string]bool{"sk-aaa": true, "sk-bbb": false}
	first := healthReportJSON("ok", "v", "p", checks)
	for i := 0; i < 20; i++ {
		if got := healthReportJSON("ok", "v", "p", checks); !bytes.Equal(got, first) {
			t.Fatalf("collision output non-deterministic:\n%s\n%s", first, got)
		}
	}
	if strings.Contains(string(first), "sk-aaa") || strings.Contains(string(first), "sk-bbb") {
		t.Fatalf("collision leaked original key: %s", first)
	}
}

// TestHealthReportJSONCJK 验证 CJK 与 HTML 特殊字符不被转义为 \uXXXX。
func TestHealthReportJSONCJK(t *testing.T) {
	const cjk = "用户健康：系统正常（日本語もOK）<ok>&"
	got := healthReportJSON(cjk, cjk, "linux/中文", map[string]bool{"中文检查": true})
	s := string(got)
	for _, want := range []string{cjk, "linux/中文", "中文检查"} {
		if !strings.Contains(s, want) {
			t.Fatalf("CJK text mangled in %s", s)
		}
	}
	if strings.Contains(s, `\u`) {
		t.Fatalf("CJK/HTML was escaped as \\uXXXX: %s", s)
	}
}

// TestHealthReportJSONEmptyChecks 验证 nil 与空 checks 都输出 {}，
// 而不是 null，且所有字段仍存在。
func TestHealthReportJSONEmptyChecks(t *testing.T) {
	cases := map[string]map[string]bool{
		"nil":   nil,
		"empty": {},
	}
	for name, checks := range cases {
		t.Run(name, func(t *testing.T) {
			got := healthReportJSON("ok", "v1", "linux/amd64", checks)
			if !strings.Contains(string(got), `"checks":{}`) {
				t.Fatalf("empty checks not rendered as {}: %s", got)
			}
			if strings.Contains(string(got), "null") {
				t.Fatalf("unexpected null in %s", got)
			}
		})
	}
}

// TestHealthReportJSONInvalidInput 验证异常输入不会 panic：空字符串原样
// 保留为空字段，凭据形状的子串被脱敏且不泄露到字节中。
func TestHealthReportJSONInvalidInput(t *testing.T) {
	const secret = "sk-abcdef0123456789"
	got := healthReportJSON("", secret, "", map[string]bool{"provider-" + secret: true})
	s := string(got)
	if strings.Contains(s, secret) {
		t.Fatalf("secret leaked: %s", s)
	}
	if !strings.Contains(s, "[redacted:") {
		t.Fatalf("redaction marker missing: %s", s)
	}

	var report healthReport
	if err := json.Unmarshal(got, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if report.Status != "" || report.Platform != "" {
		t.Fatalf("empty inputs not preserved: %+v", report)
	}
	if report.Version == secret {
		t.Fatal("version was not redacted")
	}
}

// TestHealthDefaultResponseUnchanged 守护 opt-in 的向后兼容：未开启时
// /v1/health 仍返回旧的扁平对象，没有新增字段。
func TestHealthDefaultResponseUnchanged(t *testing.T) {
	h := NewHandler(Config{Token: "t", Runner: &fakeRunner{}, Version: "1.2.3"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" || body["version"] != "1.2.3" || len(body) != 2 {
		t.Fatalf("default health body = %v, want only status+version", body)
	}
	if _, ok := body["platform"]; ok {
		t.Fatalf("default health gained a platform field: %v", body)
	}
	if _, ok := body["checks"]; ok {
		t.Fatalf("default health gained a checks field: %v", body)
	}
}

// TestHealthStructuredResponseOptIn 验证显式开启后 /v1/health 返回结构化
// 报告：四个稳定字段都存在，且取值来自配置与运行时。
func TestHealthStructuredResponseOptIn(t *testing.T) {
	h := NewHandler(Config{Token: "t", Runner: &fakeRunner{}, Version: "1.2.3", StructuredHealth: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var report healthReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if report.Status != HealthOK || report.Version != "1.2.3" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Platform == "" {
		t.Fatal("platform is empty")
	}
	if report.Checks == nil || len(report.Checks) != 0 {
		t.Fatalf("checks = %v, want empty map", report.Checks)
	}
}
