package update

import "testing"

func TestCompareVersionsOrdering(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
		want        int
	}{
		// 大小比较与相等。
		{"相等", "1.2.3", "1.2.3", 0},
		{"补丁更大", "1.2.3", "1.2.2", 1},
		{"补丁更小", "1.2.2", "1.2.3", -1},
		{"次版本按数值比较", "1.10.0", "1.9.9", 1},
		{"主版本优先", "2.0.0", "1.99.99", 1},
		{"小版本数值比较", "0.2.0", "0.1.9", 1},

		// v/V 前缀。
		{"小写v前缀相等", "v1.2.3", "1.2.3", 0},
		{"大小写v前缀相等", "V1.2.3", "v1.2.3", 0},
		{"v前缀大小比较", "v2.0.0", "v1.9.9", 1},
		{"V前缀大小比较", "V1.2.4", "v1.2.3", 1},

		// prerelease 低于正式版，且标识符间按规则排序。
		{"预发布低于正式版", "1.0.0-beta", "1.0.0", -1},
		{"正式版高于预发布", "1.0.0", "1.0.0-rc.1", 1},
		{"字母预发布序", "1.0.0-alpha", "1.0.0-beta", -1},
		{"数字预发布按值比较", "1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"数字低于字母标识符", "1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"字母预发布序二", "1.0.0-rc.1", "1.0.0-beta.11", 1},

		// 补丁版本、构建元数据与短版本。
		{"补丁递增", "1.2.3", "1.2.4", -1},
		{"次版本递增", "1.2.3", "1.3.0", -1},
		{"构建元数据忽略", "1.2.3+build.5", "1.2.3", 0},
		{"构建元数据互不影响", "1.2.3+alpha", "1.2.3+beta", 0},
		{"两段版本补零", "1.2", "1.2.0", 0},
		{"一段版本补零", "1", "1.0.0", 0},

		// 非法版本回退为字符串比较。
		{"两个非法字符串升序", "abc", "abd", -1},
		{"两个非法字符串相等", "abc", "abc", 0},
		{"两个非法字符串降序", "abd", "abc", 1},
		{"非法对合法退化为字符串比较", "garbage", "1.0.0", 1},
		{"合法对非法退化为字符串比较", "v1.0.0", "zzz", -1},
		{"前导零预发布视为非法", "1.0.0-01", "1.0.0-1", -1},

		// CJK 输入：既覆盖回退分支，也覆盖预发布标识符分支。
		{"CJK非法回退有序", "版本一", "版本二", -1},
		{"CJK非法回退相等", "版本", "版本", 0},
		{"CJK非法对合法", "版本一", "1.0.0", 1},
		{"CJK预发布标识符", "1.0.0-版本一", "1.0.0-版本二", -1},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := compareVersions(test.left, test.right)
			if got != test.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d",
					test.left, test.right, got, test.want)
			}
			if got < -1 || got > 1 {
				t.Fatalf("compareVersions(%q, %q) = %d, want -1/0/1",
					test.left, test.right, got)
			}
			if reversed := compareVersions(test.right, test.left); reversed != -test.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d (反对称)",
					test.right, test.left, reversed, -test.want)
			}
		})
	}
}

// TestCompareVersionsFallsBackUnlikeCompareVersions 记录本函数与导出版本的
// 差异：前者对非法输入退化为字符串比较，后者把非法输入当作 0.0.0。
func TestCompareVersionsFallsBackUnlikeCompareVersions(t *testing.T) {
	if got := compareVersions("zzz", "1.0.0"); got != 1 {
		t.Fatalf("compareVersions(\"zzz\", \"1.0.0\") = %d, want 1", got)
	}
	if got := CompareVersions("zzz", "1.0.0"); got != -1 {
		t.Fatalf("CompareVersions(\"zzz\", \"1.0.0\") = %d, want -1", got)
	}
}
