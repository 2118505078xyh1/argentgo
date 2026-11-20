package security

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestRedactJSONMasksNonStringSecrets 验证凭据名下的标量值不会以明文写进审计日志。
// 数字 PIN、数字令牌和布尔凭据不是 credential-shaped 字符串，Redact 识别不到；
// 若 redactValue 的默认分支原样返回它们，RedactJSON 就会把这些秘密原封不动地
// 交给审计记录，违背“脱敏发生在任何内容写入审计日志之前”的约束。
func TestRedactJSONMasksNonStringSecrets(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    map[string]any
		secrets []string // 不得出现在输出中的原始标量字面量
	}{
		{
			name:  "number under secret key",
			input: `{"password":12345678,"token":9876543210}`,
			want: map[string]any{
				"password": placeholder("password"),
				"token":    placeholder("token"),
			},
			secrets: []string{"12345678", "9876543210"},
		},
		{
			name:  "boolean and null under secret key",
			input: `{"secret":true,"api_key":null}`,
			want: map[string]any{
				"secret":  placeholder("secret"),
				"api_key": placeholder("api key"),
			},
		},
		{
			name:  "nested scalar under inherited secret context",
			input: `{"config":{"tokens":[111,222],"note":{"value":333}},"port":5432}`,
			want: map[string]any{
				"config": map[string]any{
					"tokens": []any{placeholder("token"), placeholder("token")},
					// note 是 tokens 的兄弟节点，不在秘密子树内，数字应保持原值。
					"note": map[string]any{"value": float64(333)},
				},
				"port": float64(5432), // 非凭据名下的数字必须保持原值
			},
			secrets: []string{"111", "222"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactJSON([]byte(tc.input))
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("脱敏结果不是合法 JSON: %v (%q)", err, out)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("脱敏结果不符:\n got: %v\nwant: %v\n输出: %q", got, tc.want, out)
			}
			for _, literal := range tc.secrets {
				if strings.Contains(out, literal) {
					t.Errorf("标量秘密 %q 泄露到输出: %q", literal, out)
				}
			}
		})
	}
}
