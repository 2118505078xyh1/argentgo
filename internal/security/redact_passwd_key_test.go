package security

import (
	"strings"
	"testing"
)

// TestRedactJSONPasswdKey 验证凭据键名 passwd 下的值在写入审计日志前被脱敏。
// secretKeyKind 的分支里已经显式处理了 passwd，但入口的键名正则漏掉了它，
// 导致 {"passwd":"hunter2"} 这类参数会以明文进入审计记录。
func TestRedactJSONPasswdKey(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		secrets []string
	}{
		{"字符串值", `{"passwd":"hunter2"}`, []string{"hunter2"}},
		{"大写键名", `{"db":{"PASSWD":"hunter2"}}`, []string{"hunter2"}},
		{"数字值", `{"passwd":123456}`, []string{"123456"}},
		{"嵌套子树", `{"cfg":{"passwd":{"note":"hunter2"}}}`, []string{"hunter2"}},
		{"数组元素", `{"passwords":["hunter2"]}`, []string{"hunter2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactJSON([]byte(tc.raw))
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("RedactJSON(%s) 泄露了 %q: %s", tc.raw, secret, got)
				}
			}
			if !strings.Contains(got, placeholder("password")) && !strings.Contains(got, "[redacted:") {
				t.Fatalf("RedactJSON(%s) 没有产生脱敏占位符: %s", tc.raw, got)
			}
		})
	}
}

// TestRedactJSONPasswdKeyDoesNotSwallowUnrelatedKeys 防止把 passwd 加入键名识别后
// 误伤仅含 pass 字样的普通键（例如 bypass、pass_count）。
func TestRedactJSONPasswdKeyDoesNotSwallowUnrelatedKeys(t *testing.T) {
	got := RedactJSON([]byte(`{"bypass":"ok","pass_count":3}`))
	if !strings.Contains(got, "ok") || !strings.Contains(got, "3") {
		t.Fatalf("无关键被错误脱敏: %s", got)
	}
}

// TestPasswdKeyKind 直接验证键名分类：passwd 必须被识别为密码键。
func TestPasswdKeyKind(t *testing.T) {
	if got := secretKeyKind("passwd"); got != "password" {
		t.Fatalf("secretKeyKind(passwd) = %q, want %q", got, "password")
	}
}
