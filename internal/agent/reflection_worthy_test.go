package agent

import (
	"strings"
	"testing"
)

// TestReflectionWorthyFixable 检查代码错误、格式错误与工具参数错误都被判定为值得反思。
func TestReflectionWorthyFixable(t *testing.T) {
	cases := []struct {
		name string
		err  string
	}{
		{"英文语法错误", "syntax error: unexpected token at line 7"},
		{"英文编译错误", "compile error: undefined variable foo"},
		{"英文类型错误", "type error: cannot use string as int"},
		{"英文编辑格式错误", "edit format error: old text not found"},
		{"英文格式错误", "invalid format: expected JSON object"},
		{"英文畸形参数", "bad arguments: cannot parse JSON"},
		{"英文无效参数", "invalid arguments: missing field path"},
		{"英文缺少参数", "missing required argument: path"},
		{"中文参数无效", "参数无效：缺少 path 字段"},
		{"中文格式错误", "格式错误：锚点不存在"},
		{"中文编译失败", "编译失败：未定义的标识符"},
		{"中文代码错误", "代码错误 at f.go:12"},
		{"中文工具参数错误", "工具参数错误：expected string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflectionWorthy(tc.err) {
				t.Fatalf("reflectionWorthy(%q) = false, want true", tc.err)
			}
		})
	}
}

// TestReflectionWorthyUnfixable 检查认证失败、网络不可达与配置错误都被判定为不值得反思。
func TestReflectionWorthyUnfixable(t *testing.T) {
	cases := []struct {
		name string
		err  string
	}{
		{"英文未授权", "401 unauthorized: invalid key"},
		{"英文认证失败", "authentication failed for user"},
		{"英文凭据无效", "invalid credentials supplied"},
		{"英文权限拒绝", "permission denied: read config"},
		{"英文禁止访问", "403 forbidden"},
		{"英文网络不可达", "network unreachable: host down"},
		{"英文连接拒绝", "dial tcp: connection refused"},
		{"英文无此主机", "lookup api.example.com: no such host"},
		{"英文超时", "request timed out after 30s"},
		{"英文网络错误", "network error while fetching token"},
		{"英文配置错误", "config error: missing region"},
		{"英文配置无效", "invalid configuration value for timeout"},
		{"中文认证失败", "认证失败：令牌已过期"},
		{"中文网络不可达", "网络不可达：无法连接远端"},
		{"中文连接超时", "连接超时"},
		{"中文配置错误", "配置错误：缺少 api_key"},
		{"中文权限不足", "权限不足，拒绝访问"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if reflectionWorthy(tc.err) {
				t.Fatalf("reflectionWorthy(%q) = true, want false", tc.err)
			}
		})
	}
}

// TestReflectionWorthyCaseInsensitive 检查英文大小写折叠后判定结果一致。
func TestReflectionWorthyCaseInsensitive(t *testing.T) {
	pairs := []struct {
		lower string
		upper string
		want  bool
	}{
		{"syntax error: bad token", "SYNTAX ERROR: BAD TOKEN", true},
		{"bad arguments: missing path", "BAD ARGUMENTS: MISSING PATH", true},
		{"authentication failed", "AUTHENTICATION FAILED", false},
		{"network unreachable", "NETWORK UNREACHABLE", false},
		{"config error: bad key", "CONFIG ERROR: BAD KEY", false},
	}
	for _, tc := range pairs {
		for _, input := range []string{tc.lower, tc.upper, strings.ToUpper(tc.lower)} {
			if got := reflectionWorthy(input); got != tc.want {
				t.Fatalf("reflectionWorthy(%q) = %v, want %v", input, got, tc.want)
			}
		}
	}
}

// TestReflectionWorthyCJK 检查中文错误被正确分类，且输入保留原始字节不被破坏。
func TestReflectionWorthyCJK(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"编辑失败：未找到匹配的锚点文本", true},
		{"参数错误：path 字段必须是字符串", true},
		{"编译错误：语法错误 at main.go:3", true},
		{"认证失败，请重新登录", false},
		{"网络不可达，稍后重试", false},
		{"配置错误：环境变量缺失", false},
	}
	for _, tc := range cases {
		got := reflectionWorthy(tc.err)
		if got != tc.want {
			t.Fatalf("reflectionWorthy(%q) = %v, want %v", tc.err, got, tc.want)
		}
		if !strings.Contains(tc.err, "：") && !strings.Contains(tc.err, "，") {
			t.Fatalf("测试输入被意外修改：%q", tc.err)
		}
	}
}

// TestReflectionWorthyEmptyIsFalse 检查空串与仅含空白的错误没有可操作信息，返回 false。
func TestReflectionWorthyEmptyIsFalse(t *testing.T) {
	for _, err := range []string{"", "   ", "\n\t ", "  \r\n"} {
		if reflectionWorthy(err) {
			t.Fatalf("reflectionWorthy(%q) = true, want false", err)
		}
	}
}

// TestReflectionWorthyUnfixableWins 检查同时命中两类标记时，不可修复信号优先，
// 从而不会因为一句无关的"参数错误"就为认证/网络失败消耗反思预算。
func TestReflectionWorthyUnfixableWins(t *testing.T) {
	cases := []string{
		"invalid arguments: authentication failed for token",
		"edit format error; network unreachable",
		"参数错误，但网络不可达",
		"配置错误：参数无效",
	}
	for _, err := range cases {
		if reflectionWorthy(err) {
			t.Fatalf("reflectionWorthy(%q) = true, want false", err)
		}
	}
}

// TestReflectionWorthyUnknownIsFalse 检查没有可修复证据的未知错误保守地返回 false，
// 避免占用反思预算。该默认值被明确锁定，便于日后有意调整时发现。
func TestReflectionWorthyUnknownIsFalse(t *testing.T) {
	for _, err := range []string{
		"something unexpected happened",
		"operation failed",
		"boom",
		"the frobnicator returned a non-zero value",
	} {
		if reflectionWorthy(err) {
			t.Fatalf("reflectionWorthy(%q) = true, want false", err)
		}
	}
}

// TestReflectionWorthyStable 检查重复调用产生逐字节一致的结果，
// 且不同大小写写法的同一错误判定相同；同时对非法 UTF-8 字节不panic。
func TestReflectionWorthyStable(t *testing.T) {
	const err = "  BaD ARGUMENTS: syntax ERROR at 文件.go:9  "
	want := reflectionWorthy(err)
	for i := 0; i < 32; i++ {
		if got := reflectionWorthy(err); got != want {
			t.Fatalf("call %d = %v, want %v", i, got, want)
		}
	}
	// 非法 UTF-8 字节不应导致越界或 panic；结果仍须稳定。
	invalid := string([]byte{0xff, 0xfe, 'b', 'a', 'd', 0x80, ' ', 'a', 'r', 'g', 'u', 'm', 'e', 'n', 't', 's'})
	got := reflectionWorthy(invalid)
	for i := 0; i < 8; i++ {
		if reflectionWorthy(invalid) != got {
			t.Fatalf("invalid UTF-8 input gave unstable result at call %d", i)
		}
	}
}

// TestAugmentToolErrorReflectionSkipsUnfixable 检查接线门控把不可修复错误原样透传，
// 只有可修复错误才会被包装成反思反馈，从而不占用反思预算。
func TestAugmentToolErrorReflectionSkipsUnfixable(t *testing.T) {
	t.Cleanup(func() { SetToolErrorReflection(false) })
	SetToolErrorReflection(true)

	for _, err := range []string{
		"error: 401 unauthorized: invalid key",
		"error: network unreachable",
		"error: config error: missing region",
		"error: 认证失败",
	} {
		if got := AugmentToolErrorReflection("edit", err, true); got != err {
			t.Fatalf("unfixable error = %q, want raw %q", got, err)
		}
	}

	const fixable = "error: bad arguments: cannot parse JSON"
	if got := AugmentToolErrorReflection("edit", fixable, true); got != toolErrorReflection("edit", fixable) {
		t.Fatalf("fixable error = %q, want wrapped feedback", got)
	}
}
