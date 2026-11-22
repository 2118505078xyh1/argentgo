package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 本文件守护 codegraph 能力提示的文案与 opt-in 判定：默认关闭时零追加，
// 系统提示逐字节不变；开启时追加的提示是完整 UTF-8 的中文单行文本。

func TestCodeGraphCapabilityHintText(t *testing.T) {
	const want = "可用 codegraph_query(name) 查询符号定义与引用位置（本地静态分析、只读，结果附源码位置与提取规则）。"
	if got := codeGraphCapabilityHint(); got != want {
		t.Fatalf("codeGraphCapabilityHint() = %q, want %q", got, want)
	}
	if !utf8.ValidString(want) {
		t.Fatal("提示不是完整 UTF-8")
	}
	if strings.ContainsAny(codeGraphCapabilityHint(), "\r\n") {
		t.Fatal("提示必须是单行文本")
	}
}

func TestCodeGraphHintEnabled(t *testing.T) {
	tests := []struct {
		name string
		reg  *OptInRegistry
		want bool
	}{
		{"nil reg", nil, false},
		{"empty reg", &OptInRegistry{}, false},
		{"other key off", codeGraphReg(false), false},
		{"codegraph on", codeGraphReg(true), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeGraphHintEnabled(tc.reg); got != tc.want {
				t.Fatalf("codeGraphHintEnabled(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestCodeGraphHintEnvAndConfigPaths 守护阻断修复：codegraph 提示由统一注册表驱动，
// 因此 env 与 config 两条激活路径都必须生效，fail-closed 路径保持关闭。
func TestCodeGraphHintEnvAndConfigPaths(t *testing.T) {
	paths := []struct {
		name   string
		env    func(string) string
		cfgOpt map[OptInKey]bool
		want   bool
	}{
		{"both off", func(string) string { return "" }, nil, false},
		{"env only", func(string) string { return "codegraph" }, nil, true},
		{"config only", func(string) string { return "" }, map[OptInKey]bool{OptInCodeGraph: true}, true},
		{"other config key", func(string) string { return "" }, map[OptInKey]bool{OptInApplyPatch: true}, false},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			optIns := mergeOptIns(ParseOptInEnv(tc.env), tc.cfgOpt)
			reg := &OptInRegistry{}
			for k, on := range optIns {
				reg.Set(k, on)
			}
			if got := codeGraphHintEnabled(reg); got != tc.want {
				t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestCodeGraphHintByteExact 复刻 app_execute.go 的提示追加逻辑：关闭时系统提示
// 逐字节不变；开启时恰好在末尾追加 "\n" + 提示，不多一个空行也不少一个字符。
func TestCodeGraphHintByteExact(t *testing.T) {
	base := "# 角色\n请修复 bug。\n\n编辑前先读文件。"
	appendHint := func(system string, on bool) string {
		if codeGraphHintEnabled(codeGraphReg(on)) {
			system += "\n" + codeGraphCapabilityHint()
		}
		return system
	}

	off := appendHint(base, false)
	if off != base {
		t.Fatalf("默认关闭时系统提示变化:\n got %q\nwant %q", off, base)
	}
	on := appendHint(base, true)
	if want := base + "\n" + codeGraphCapabilityHint(); on != want {
		t.Fatalf("开启时提示字节不符:\n got %q\nwant %q", on, want)
	}
	// CJK 提示没有被切坏：去回车换行后整串仍是完整 UTF-8。
	if !utf8.ValidString(on) {
		t.Fatal("追加后系统提示不是完整 UTF-8")
	}
}
