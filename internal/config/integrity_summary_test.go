package config

import (
	"reflect"
	"testing"
)

// 以下用例直接覆盖 formatIntegritySummary 的输出契约。测试与源码同包，
// 因此可以传入手工构造的缺失项，精确校验排序、Fatal 标记、换行压缩与 CJK。
func TestFormatIntegritySummary(t *testing.T) {
	tests := []struct {
		name    string
		missing []MissingEntry
		want    string
	}{
		{
			name:    "nil missing returns fixed literal",
			missing: nil,
			want:    "integrity: ok",
		},
		{
			name:    "empty missing returns fixed literal",
			missing: []MissingEntry{},
			want:    "integrity: ok",
		},
		{
			name: "single fatal entry",
			missing: []MissingEntry{
				{Entry: "$.model", Reason: "model is required", Fatal: true},
			},
			want: "integrity: 1 missing\n" +
				"  [fatal] $.model: model is required",
		},
		{
			name: "single non-fatal entry",
			missing: []MissingEntry{
				{Entry: "$.providers.openai.apiKey", Reason: `provider "openai" has no credential`, Fatal: false},
			},
			want: "integrity: 1 missing\n" +
				`  [non-fatal] $.providers.openai.apiKey: provider "openai" has no credential`,
		},
		{
			name: "entries sorted by Entry regardless of input order",
			missing: []MissingEntry{
				{Entry: "$.storageDir", Reason: "storage directory is required", Fatal: true},
				{Entry: "$.model", Reason: "model is required", Fatal: true},
				{Entry: "$.baseURL", Reason: "baseURL is required", Fatal: true},
			},
			want: "integrity: 3 missing\n" +
				"  [fatal] $.baseURL: baseURL is required\n" +
				"  [fatal] $.model: model is required\n" +
				"  [fatal] $.storageDir: storage directory is required",
		},
		{
			name: "stable order preserved for equal Entry",
			missing: []MissingEntry{
				{Entry: "$.same", Reason: "first", Fatal: true},
				{Entry: "$.same", Reason: "second", Fatal: false},
				{Entry: "$.aaa", Reason: "zzz", Fatal: true},
			},
			want: "integrity: 3 missing\n" +
				"  [fatal] $.aaa: zzz\n" +
				"  [fatal] $.same: first\n" +
				"  [non-fatal] $.same: second",
		},
		{
			name: "CJK preserved and newlines compressed",
			missing: []MissingEntry{
				{Entry: "$.providers.中文.apiKey", Reason: "缺少凭据：请设置\n密钥\r\n或继续", Fatal: true},
				{Entry: "$.模型", Reason: "模型 名称 必填", Fatal: true},
			},
			want: "integrity: 2 missing\n" +
				"  [fatal] $.providers.中文.apiKey: 缺少凭据：请设置 密钥 或继续\n" +
				"  [fatal] $.模型: 模型 名称 必填",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 快照入参，确认格式化（含排序）不修改调用方持有的切片。
			// 保留 nil 与空切片的区分，避免把二者误判为"被修改"。
			var before []MissingEntry
			if tc.missing != nil {
				before = append([]MissingEntry{}, tc.missing...)
			}

			got := formatIntegritySummary(tc.missing)
			if got != tc.want {
				t.Errorf("formatIntegritySummary() = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(tc.missing, before) {
				t.Errorf("formatIntegritySummary() mutated input: got %+v, want %+v", tc.missing, before)
			}
		})
	}
}

// TestMissingEntryLess 覆盖稳定比较器的字典序与相等语义，包含 CJK 条目的
// UTF-8 字节序：汉字首字节大于 ASCII 字母，因此 "$.abc" < "$.中文"。
func TestMissingEntryLess(t *testing.T) {
	tests := []struct {
		name string
		a    MissingEntry
		b    MissingEntry
		want bool
	}{
		{name: "less", a: MissingEntry{Entry: "$.aaa"}, b: MissingEntry{Entry: "$.bbb"}, want: true},
		{name: "greater", a: MissingEntry{Entry: "$.bbb"}, b: MissingEntry{Entry: "$.aaa"}, want: false},
		{name: "equal entries compare false", a: MissingEntry{Entry: "$.same"}, b: MissingEntry{Entry: "$.same"}, want: false},
		{name: "ASCII sorts before CJK", a: MissingEntry{Entry: "$.abc"}, b: MissingEntry{Entry: "$.中文"}, want: true},
		{name: "CJK after ASCII compares false", a: MissingEntry{Entry: "$.中文"}, b: MissingEntry{Entry: "$.abc"}, want: false},
		{name: "CJK prefix sorts before longer CJK", a: MissingEntry{Entry: "$.模型"}, b: MissingEntry{Entry: "$.模型2"}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MissingEntryLess(tc.a, tc.b); got != tc.want {
				t.Errorf("MissingEntryLess(%q, %q) = %v, want %v", tc.a.Entry, tc.b.Entry, got, tc.want)
			}
		})
	}
}

// TestIntegritySummary 覆盖公开方法：它使用 os.Getenv 读取环境，因此在父测试
// 中把相关密钥环境变量置空，保证结果不依赖运行机器的真实环境。
func TestIntegritySummary(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "complete config is ok",
			cfg:  integrityCompleteConfig(),
			want: "integrity: ok",
		},
		{
			name: "local provider needs no credential",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.ProviderName = "ollama"
				c.APIKey = ""
				return c
			}(),
			want: "integrity: ok",
		},
		{
			name: "missing required fields sorted by Entry",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.Model = ""
				c.BaseURL = ""
				c.Protocol = ""
				return c
			}(),
			want: "integrity: 3 missing\n" +
				"  [fatal] $.baseURL: baseURL is required\n" +
				"  [fatal] $.model: model is required\n" +
				"  [fatal] $.protocol: protocol is required",
		},
		{
			name: "mixed fatal and non-fatal entries",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.Model = ""
				c.CWD = ""
				c.APIKey = ""
				return c
			}(),
			want: "integrity: 3 missing\n" +
				"  [fatal] $.cwd: working directory is required\n" +
				"  [fatal] $.model: model is required\n" +
				`  [non-fatal] $.providers.openai.apiKey: provider "openai" has no credential: set apiKey or provide OPENAI_API_KEY at runtime`,
		},
		{
			name: "unresolvable provider is fatal",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.ProviderName = "does-not-exist"
				return c
			}(),
			want: "integrity: 1 missing\n" +
				"  [fatal] $.provider: no provider is configured or resolvable",
		},
		{
			name: "CJK provider name appears intact in summary",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.APIKey = ""
				c.Providers = map[string]Provider{
					"openai": {Name: "中文提供方", APIKeyEnv: "OPENAI_API_KEY"},
				}
				return c
			}(),
			want: "integrity: 1 missing\n" +
				`  [non-fatal] $.providers.中文提供方.apiKey: provider "中文提供方" has no credential: set apiKey or provide OPENAI_API_KEY at runtime`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.IntegritySummary()
			if got != tc.want {
				t.Errorf("IntegritySummary() = %q, want %q", got, tc.want)
			}
		})
	}
}
