package security

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// 夹具凭据由多个部分拼装，以免秘密扫描器把这些合成测试值误认为真实密钥。
// 拼装出的字符串刻意做得逼真，使脱敏器能在真实形态上得到检验。
var (
	stripeLiveFixture       = "sk_" + "live_51H8xQ2eZvKYlo2Ca" + "bcdefghIJ"
	stripeRestrictedFixture = "rk_" + "test_abc123" + "def456"
	stripeCJKFixture        = "sk_" + "live_51H8xQ2eZvKYlo2C" + "xyz"
	oauthTokenFixture       = "ya29." + "a0AfH6SMB.abc.def"
	oauthTokenAltFixture    = "ya29." + "a0AfH6SMBx-example.token.part"
)

// TestRedactCredentialShapeCoverage 审计脱敏器应识别的凭据形态。前四种在此次审计
// 之前已被覆盖，作为回归守护；后五种是新增的形态。
func TestRedactCredentialShapeCoverage(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "jwt already covered",
			in:      "jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.abc123",
			absent:  []string{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.abc123"},
			present: []string{"jwt=", placeholder("jwt")},
		},
		{
			name:    "aws access key already covered",
			in:      "aws AKIAIOSFODNN7EXAMPLE is the id",
			absent:  []string{"AKIAIOSFODNN7EXAMPLE"},
			present: []string{"aws", "is the id", placeholder("aws key")},
		},
		{
			name:    "github fine grained token already covered",
			in:      "github_pat_11ABCDEFG0abcDEF123456_abcdefghijklmnop push",
			absent:  []string{"github_pat_11ABCDEFG0abcDEF123456_abcdefghijklmnop"},
			present: []string{"push", placeholder("token")},
		},
		{
			name:    "database url password already covered",
			in:      "mysql://app:p%40ss@db.internal:3306/prod",
			absent:  []string{"app:p%40ss"},
			present: []string{"mysql://", "db.internal:3306/prod", placeholder("url credentials")},
		},
		{
			name:    "stripe live secret key",
			in:      "stripe key " + stripeLiveFixture + " ok",
			absent:  []string{stripeLiveFixture},
			present: []string{"stripe key", "ok", placeholder("api key")},
		},
		{
			name:    "stripe restricted test key",
			in:      stripeRestrictedFixture + " rotated",
			absent:  []string{stripeRestrictedFixture},
			present: []string{"rotated", placeholder("api key")},
		},
		{
			name:    "google oauth access token",
			in:      oauthTokenAltFixture + " granted",
			absent:  []string{oauthTokenAltFixture},
			present: []string{"granted", placeholder("oauth token")},
		},
		{
			name: "pem pkcs8 private key block",
			in: "before\n" +
				"-----BEGIN PRIVATE KEY-----\n" +
				"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n" +
				"-----END PRIVATE KEY-----\n" +
				"after",
			absent:  []string{"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", "BEGIN PRIVATE KEY", "END PRIVATE KEY"},
			present: []string{"before", "after", placeholder("private key")},
		},
		{
			name:    "pem openssh private key header without end marker",
			in:      "-----BEGIN OPENSSH PRIVATE KEY----- truncated",
			absent:  []string{"OPENSSH PRIVATE KEY"},
			present: []string{"truncated", placeholder("private key")},
		},
	}
	for _, tc := range cases {
		out := Redact(tc.in)
		for _, secret := range tc.absent {
			if strings.Contains(out, secret) {
				t.Errorf("%s: %q leaked %q", tc.name, out, secret)
			}
		}
		for _, marker := range tc.present {
			if !strings.Contains(out, marker) {
				t.Errorf("%s: %q lost %q", tc.name, out, marker)
			}
		}
	}
}

// TestRedactNewShapesNoFalsePositives 守护保守性契约：仅仅形似凭据前缀的相邻文本
// 必须保留。
func TestRedactNewShapesNoFalsePositives(t *testing.T) {
	cases := []string{
		"the PRIVATE KEY text in the docs",
		"-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A\n-----END PUBLIC KEY-----",
		"-----BEGIN CERTIFICATE-----\nMIIBkTCB+wIJAKzz\n-----END CERTIFICATE-----",
		"the sk_liv variable is not a key",
		"ya29 is just a number in the log",
		"https://example.com/docs/key-management",
		"rotate your api key soon",
	}
	for _, in := range cases {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactNewShapesWithCJK(t *testing.T) {
	in := "请求 " + stripeCJKFixture + " 完成 " + oauthTokenFixture + " 结束 " +
		"-----BEGIN OPENSSH PRIVATE KEY----- 尾"
	out := Redact(in)
	for _, secret := range []string{
		stripeCJKFixture,
		oauthTokenFixture,
		"OPENSSH PRIVATE KEY",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("credential %q leaked: %q", secret, out)
		}
	}
	if !utf8.ValidString(out) {
		t.Fatalf("redacted output is not valid UTF-8: %q", out)
	}
	for _, marker := range []string{"请求", "完成", "结束", "尾", placeholder("api key"), placeholder("oauth token"), placeholder("private key")} {
		if !strings.Contains(out, marker) {
			t.Fatalf("text %q lost: %q", marker, out)
		}
	}
}

func TestRedactRowPEMAndStripePreservesANSI(t *testing.T) {
	line := []byte("\x1b[32mOK\x1b[0m " + stripeCJKFixture + " -----BEGIN OPENSSH PRIVATE KEY----- \x1b[1mend\x1b[0m")
	before := ansiSequences(line)
	out := RedactRow(line)
	for _, secret := range []string{stripeCJKFixture, "OPENSSH PRIVATE KEY"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Fatalf("credential %q leaked into row: %q", secret, out)
		}
	}
	if got := ansiSequences(out); !reflectDeepEqualStrings(got, before) {
		t.Fatalf("ANSI sequences changed:\n got %q\nwant %q", got, before)
	}
}
