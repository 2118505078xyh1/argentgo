package security

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRedactJSONExtendedSecretKeys 覆盖新补入的三组敏感键名（credential / pwd /
// passphrase）在 JSON 审计路径上的识别：值必须被对应占位符替换，原值不得残留。
// dbPassword 这类 password 复合写法已被既有的 "password" 子串覆盖，故不重复列举。
func TestRedactJSONExtendedSecretKeys(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		secret   string
		wantKind string
	}{
		{"credential", `{"credential":"opaque-42"}`, "opaque-42", "credential"},
		{"credentials 复数", `{"credentials":["opaque-42"]}`, "opaque-42", "credential"},
		{"dbCredential 复合", `{"dbCredential":{"value":"opaque-42"}}`, "opaque-42", "credential"},
		{"DB_CREDENTIAL 大写下划线", `{"DB_CREDENTIAL":"opaque-42"}`, "opaque-42", "credential"},
		{"pwd", `{"pwd":"opaque-42"}`, "opaque-42", "password"},
		{"db_pwd 复合", `{"db_pwd":"opaque-42"}`, "opaque-42", "password"},
		{"dbPwd 复合", `{"dbPwd":"opaque-42"}`, "opaque-42", "password"},
		{"PWD 大写", `{"PWD":"opaque-42"}`, "opaque-42", "password"},
		{"passphrase", `{"passphrase":"opaque-42"}`, "opaque-42", "passphrase"},
		{"dbPassphrase 复合", `{"dbPassphrase":"opaque-42"}`, "opaque-42", "passphrase"},
		{"PASSPHRASE 大写", `{"PASSPHRASE":"opaque-42"}`, "opaque-42", "passphrase"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactJSON([]byte(tc.raw))
			if strings.Contains(got, tc.secret) {
				t.Fatalf("RedactJSON(%s) 泄露了 %q: %s", tc.raw, tc.secret, got)
			}
			if want := placeholder(tc.wantKind); !strings.Contains(got, want) {
				t.Fatalf("RedactJSON(%s) 没有产生 %q: %s", tc.raw, want, got)
			}
		})
	}
}

// TestRedactJSONExtendedSecretKeysNestedAndScalar 验证新键名下的子树与标量同样被
// 脱敏：字符串、数字、布尔值在凭据上下文中都必须变成占位符，不能原样进入审计。
func TestRedactJSONExtendedSecretKeysNestedAndScalar(t *testing.T) {
	raw := `{
		"credential":{"note":"opaque-note","list":["opaque-item"]},
		"pwd":123456,
		"passphrase":true,
		"note":"keep-me",
		"count":7
	}`
	out := RedactJSON([]byte(raw))
	for _, secret := range []string{"opaque-note", "opaque-item", "123456", "true"} {
		if strings.Contains(out, secret) {
			t.Fatalf("RedactJSON 泄露了 %q: %s", secret, out)
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("脱敏结果不是合法 JSON: %v (%q)", err, out)
	}
	want := map[string]any{
		"credential": map[string]any{
			"note": placeholder("credential"),
			"list": []any{placeholder("credential")},
		},
		"pwd":        placeholder("password"),
		"passphrase": placeholder("passphrase"),
		"note":       "keep-me",
		"count":      float64(7),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("脱敏结果不符:\n got: %v\nwant: %v\n输出: %q", got, want, out)
	}
}

// TestRedactExtendedSecretKeysPlainText 通过 keyValueNames 路径验证 KEY=value 形式
// 的新键名在工具输出（Redact / RedactRow）里同样被脱敏。
func TestRedactExtendedSecretKeysPlainText(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		secret   string
		wantKind string
	}{
		{"credential 等号", "credential=opaque-42", "opaque-42", "credential"},
		{"credential 冒号大写", "CREDENTIAL: opaque-42", "opaque-42", "credential"},
		{"pwd 等号", "pwd=opaque-42", "opaque-42", "password"},
		{"db_pwd 等号", "db_pwd=opaque-42", "opaque-42", "password"},
		{"passphrase 等号", "passphrase=opaque-42", "opaque-42", "passphrase"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Redact(tc.in)
			if strings.Contains(out, tc.secret) {
				t.Fatalf("Redact(%q) 泄露了 %q: %s", tc.in, tc.secret, out)
			}
			if want := placeholder(tc.wantKind); !strings.Contains(out, want) {
				t.Fatalf("Redact(%q) 没有产生 %q: %s", tc.in, want, out)
			}
		})
	}

	// RedactRow 必须与 Redact 共享同一模式表，逐行路径也不得泄露新键名的凭据。
	line := []byte("\x1b[32mOK\x1b[0m credential=opaque-42 end")
	row := RedactRow(line)
	if strings.Contains(string(row), "opaque-42") {
		t.Fatalf("RedactRow 泄露了凭据: %s", row)
	}
	if !strings.Contains(string(row), placeholder("credential")) {
		t.Fatalf("RedactRow 没有产生占位符: %s", row)
	}
}

// TestRedactExtendedSecretKeysConservative 守护保守性：仅形似新键名但并非凭据字段的
// 普通键（如 pwdfile、passcode、cred）不得被误脱敏。pwd 使用尾部 \b 正是为此。
func TestRedactExtendedSecretKeysConservative(t *testing.T) {
	raw := `{"pwdfile":"ok","passcode":"ok","cred":"ok","bypass":"ok","pass_count":3}`
	out := RedactJSON([]byte(raw))
	for _, keep := range []string{"pwdfile", "passcode", "cred", "bypass", "pass_count", "ok", "3"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("无关键 %q 被错误脱敏: %s", keep, out)
		}
	}
	if strings.Contains(out, "[redacted:") {
		t.Fatalf("普通字段被错误标记脱敏: %s", out)
	}
}

// TestRedactExtendedSecretKeysCJK 验证 CJK 文本与新键名同时出现时：凭据被脱敏、
// 周围中文原样保留、输出仍是合法 UTF-8，且没有被从多字节字符中间截断。
func TestRedactExtendedSecretKeysCJK(t *testing.T) {
	raw := `{"备注":"请求","credential":"中文口令-42","pwd":"密钥-99","说明":"结束"}`
	out := RedactJSON([]byte(raw))
	for _, secret := range []string{"中文口令-42", "密钥-99"} {
		if strings.Contains(out, secret) {
			t.Fatalf("CJK 凭据 %q 泄露: %s", secret, out)
		}
	}
	if !utf8.ValidString(out) {
		t.Fatalf("脱敏输出不是合法 UTF-8: %q", out)
	}
	for _, keep := range []string{"请求", "结束", placeholder("credential"), placeholder("password")} {
		if !strings.Contains(out, keep) {
			t.Fatalf("文本 %q 丢失: %q", keep, out)
		}
	}
}

// TestSecretKeyKindExtendedKeys 直接验证键名分类：新键名映射到正确类别，
// 而形似键（pwdfile 等）必须得到空类别。
func TestSecretKeyKindExtendedKeys(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"credential", "credential"},
		{"credentials", "credential"},
		{"dbCredential", "credential"},
		{"pwd", "password"},
		{"db_pwd", "password"},
		{"dbPwd", "password"},
		{"passphrase", "passphrase"},
		{"dbPassphrase", "passphrase"},
		{"pwdfile", ""},
		{"passcode", ""},
		{"cred", ""},
	}
	for _, tc := range cases {
		if got := secretKeyKind(tc.key); got != tc.want {
			t.Errorf("secretKeyKind(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}
