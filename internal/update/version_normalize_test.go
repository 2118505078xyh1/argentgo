package update

import (
	"strings"
	"testing"
)

func TestNormalizeVersionString(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		// 完整版本保持原样。
		{"full version", "1.2.3", "1.2.3"},
		{"full version with v prefix", "v1.2.3", "1.2.3"},
		{"full version with V prefix", "V1.2.3", "1.2.3"},
		// 简化写法补零到三段。
		{"major only", "8", "8.0.0"},
		{"major and minor", "0.8", "0.8.0"},
		{"v prefix major only", "v8", "8.0.0"},
		{"v prefix major and minor", "v0.8", "0.8.0"},
		{"V prefix major and minor", "V0.8", "0.8.0"},
		// 预发布标识原样保留。
		{"prerelease", "1.0.0-beta.2", "1.0.0-beta.2"},
		{"simplified with prerelease", "v2-rc.1", "2.0.0-rc.1"},
		{"single segment with prerelease", "8-beta", "8.0.0-beta"},
		// 构建元数据被剥离。
		{"build metadata", "1.2.3+build.5", "1.2.3"},
		{"simplified with build metadata", "v1.2+meta", "1.2.0"},
		{"prerelease with build metadata", "1.2.3-rc.1+exp.sha.5114", "1.2.3-rc.1"},
		// CJK 输入原样返回，不会在 UTF-8 字符中间切分。
		{"cjk with digits", "版本8", "版本8"},
		{"cjk suffix", "8版本", "8版本"},
		{"fullwidth digits", "１.２.３", "１.２.３"},
		{"cjk prerelease", "1.2.3-预览", "1.2.3-预览"},
		// 非法输入原样返回。
		{"empty", "", ""},
		{"only prefix", "v", "v"},
		{"letters in core", "1.x.3", "1.x.3"},
		{"too many segments", "1.2.3.4", "1.2.3.4"},
		{"empty segment", "1..3", "1..3"},
		{"empty prerelease", "1.2.3-", "1.2.3-"},
		{"empty prerelease identifier", "1.2.3-rc..1", "1.2.3-rc..1"},
		{"negative", "-1.2.3", "-1.2.3"},
		{"spaces around", " v0.8 ", "0.8.0"},
		{"over length bound", "1.2.3+" + strings.Repeat("a", 256), "1.2.3+" + strings.Repeat("a", 256)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeVersionString(test.input); got != test.want {
				t.Fatalf("normalizeVersionString(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeVersionStringIsIdempotent(t *testing.T) {
	for _, input := range []string{"v0.8", "8", "1.2.3-rc.1+build", "版本8", "1.x.3", ""} {
		once := normalizeVersionString(input)
		if twice := normalizeVersionString(once); twice != once {
			t.Fatalf("normalizeVersionString 非幂等: %q -> %q -> %q", input, once, twice)
		}
	}
}
