package i18n

import "testing"

// envFromMap 把 map 包装成检测函数供各用例使用。
func envFromMap(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestDetectLocaleSourcePriority(t *testing.T) {
	// 四个来源同时存在时 LANGUAGE 优先。
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "LANGUAGE beats LC_ALL LANG and LC_MESSAGES",
			env: map[string]string{
				"LANGUAGE":    "en_US",
				"LC_ALL":      "zh_CN",
				"LANG":        "zh_CN",
				"LC_MESSAGES": "zh_CN",
			},
			want: EnUS,
		},
		{
			name: "LANGUAGE wins in the other direction",
			env: map[string]string{
				"LANGUAGE": "zh_CN",
				"LC_ALL":   "en_US",
			},
			want: ZhCN,
		},
		{
			name: "LC_ALL beats LANG and LC_MESSAGES",
			env: map[string]string{
				"LC_ALL":      "en_US",
				"LANG":        "zh_CN",
				"LC_MESSAGES": "zh_CN",
			},
			want: EnUS,
		},
		{
			name: "LC_ALL wins in the other direction",
			env: map[string]string{
				"LC_ALL": "zh_CN",
				"LANG":   "en_US",
			},
			want: ZhCN,
		},
		{
			name: "LANG beats LC_MESSAGES",
			env: map[string]string{
				"LANG":        "en_US",
				"LC_MESSAGES": "zh_CN",
			},
			want: EnUS,
		},
		{
			name: "LANG wins in the other direction",
			env: map[string]string{
				"LANG":        "zh_CN",
				"LC_MESSAGES": "en_US",
			},
			want: ZhCN,
		},
		{
			name: "LC_MESSAGES is the last source",
			env:  map[string]string{"LC_MESSAGES": "zh_CN"},
			want: ZhCN,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectLocale(envFromMap(tc.env)); got != tc.want {
				t.Errorf("detectLocale = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectLocaleFallsThroughInvalidSources(t *testing.T) {
	// 高优先级来源中的非法值不得掩盖低优先级来源中的有效值。
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "invalid LANGUAGE falls through to LC_ALL",
			env:  map[string]string{"LANGUAGE": "klingon", "LC_ALL": "zh_CN"},
			want: ZhCN,
		},
		{
			name: "invalid LC_ALL falls through to LANG",
			env:  map[string]string{"LC_ALL": "???", "LANG": "en_US"},
			want: EnUS,
		},
		{
			name: "invalid LANG falls through to LC_MESSAGES",
			env:  map[string]string{"LANG": "xx_YY", "LC_MESSAGES": "zh_CN"},
			want: ZhCN,
		},
		{
			name: "empty LANGUAGE falls through to LC_ALL",
			env:  map[string]string{"LANGUAGE": "   ", "LC_ALL": "en_US"},
			want: EnUS,
		},
		{
			name: "unsupported language falls through",
			env:  map[string]string{"LANGUAGE": "fr_FR", "LANG": "zh_CN"},
			want: ZhCN,
		},
		{
			name: "every source invalid falls back to default",
			env:  map[string]string{"LANGUAGE": "klingon", "LC_ALL": "xx_YY", "LANG": "???", "LC_MESSAGES": "123"},
			want: DefaultLocale,
		},
		{
			name: "nothing set falls back to default",
			env:  map[string]string{},
			want: DefaultLocale,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectLocale(envFromMap(tc.env)); got != tc.want {
				t.Errorf("detectLocale = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectLocaleParsesEncodingAndRegion(t *testing.T) {
	// 编码后缀、修饰符后缀、区域码以及大小写都要正确归一。
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"zh with UTF-8 suffix", "zh_CN.UTF-8", ZhCN},
		{"en with UTF-8 suffix", "en_US.UTF-8", EnUS},
		{"zh with GBK suffix", "zh_CN.GBK", ZhCN},
		{"en with UTF8 suffix", "en_US.UTF8", EnUS},
		{"zh with modifier suffix", "zh_CN@euro", ZhCN},
		{"en with region hyphen", "en-GB", EnUS},
		{"zh with region hyphen", "zh-TW", ZhCN},
		{"en with numeric region", "en-001", EnUS},
		{"zh with numeric region", "zh_001", ZhCN},
		{"uppercase region normalized", "ZH_cn", ZhCN},
		{"lowercase region normalized", "EN_us", EnUS},
		{"mixed case tag normalized", "Zh-CN", ZhCN},
		{"English alias", "English", EnUS},
		{"Chinese alias", "chinese", ZhCN},
		{"POSIX C is explicit English", "C", EnUS},
		{"POSIX locale with modifier", "POSIX.UTF-8", EnUS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envFromMap(map[string]string{"LANG": tc.value})
			if got := detectLocale(env); got != tc.want {
				t.Errorf("detectLocale(LANG=%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestDetectLocaleInvalidAndEmptyValues(t *testing.T) {
	// 无法解析或为空的值一律回退到默认语言环境。
	cases := []struct {
		name  string
		value string
	}{
		{"unsupported single language", "klingon"},
		{"unknown region", "xx_YY"},
		{"too many subtags", "en_US_extra"},
		{"numeric only", "123"},
		{"empty", ""},
		{"whitespace only", "   "},
		{"just a separator", "_"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envFromMap(map[string]string{"LANG": tc.value})
			if got := detectLocale(env); got != DefaultLocale {
				t.Errorf("detectLocale(LANG=%q) = %q, want %q", tc.value, got, DefaultLocale)
			}
		})
	}
}

func TestDetectLocaleCJKIsSafe(t *testing.T) {
	// 多字节 CJK 文本不会被误判或截断，只识别显式的"CJK 别名"。
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"unsupported CJK text falls back", "日本語", DefaultLocale},
		{"long CJK instruction falls back", "这是一段没有空格的中文指令文本", DefaultLocale},
		{"CJK with encoding suffix falls back", "日本語.UTF-8", DefaultLocale},
		{"explicit Chinese alias is recognized", "中文", ZhCN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envFromMap(map[string]string{"LANG": tc.value})
			if got := detectLocale(env); got != tc.want {
				t.Errorf("detectLocale(LANG=%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestDetectLocaleLanguageList(t *testing.T) {
	// LANGUAGE 是冒号分隔的优先级列表，逐项尝试直到命中受支持代码。
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"first entry wins", "zh_CN:en_US", ZhCN},
		{"invalid prefix is skipped", "xx:en_US", EnUS},
		{"multiple invalid prefixes are skipped", "xx:fr:zh_CN", ZhCN},
		{"all invalid falls back to lower source", "xx:fr", DefaultLocale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envFromMap(map[string]string{"LANGUAGE": tc.value})
			if got := detectLocale(env); got != tc.want {
				t.Errorf("detectLocale(LANGUAGE=%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestDetectLocaleStopsAtFirstMatch(t *testing.T) {
	// 命中后不再查询更低优先级的来源。
	var queried []string
	env := func(name string) string {
		queried = append(queried, name)
		if name == "LANGUAGE" {
			return "zh_CN"
		}
		return "en_US"
	}
	if got := detectLocale(env); got != ZhCN {
		t.Errorf("detectLocale = %q, want %q", got, ZhCN)
	}
	if len(queried) != 1 || queried[0] != "LANGUAGE" {
		t.Errorf("queried = %v, want [LANGUAGE]", queried)
	}
}

func TestDetectLocaleIsPure(t *testing.T) {
	// 纯函数：nil 安全，且对同一输入始终返回同一结果。
	if got := detectLocale(nil); got != DefaultLocale {
		t.Errorf("nil env = %q, want %q", got, DefaultLocale)
	}
	env := envFromMap(map[string]string{"LANG": "zh_CN.UTF-8"})
	first := detectLocale(env)
	second := detectLocale(env)
	if first != second || first != ZhCN {
		t.Errorf("not deterministic: %q then %q", first, second)
	}
}
