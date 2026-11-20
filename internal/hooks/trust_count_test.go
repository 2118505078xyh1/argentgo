package hooks

import (
	"reflect"
	"testing"
)

// 以下形状模拟 Config / Command 后续可能携带的信任标注字段。
// 它们只用于测试 countByTrust 的信任判定逻辑，不改变真实 Config 的结构。
type trustStringConfig struct {
	Trust string
}

type trustBoolConfig struct {
	Trusted bool
}

type trustBoolCommand struct {
	Trusted bool
}

type trustStringCommand struct {
	TrustState string
}

type trustPointerCommand struct {
	Trust *string
}

// plainCommand 不带任何信任标注字段，用于验证配置级默认值。
type plainCommand struct {
	Command string
}

// assertTrustCounts 比较统计结果与期望的三个键，便于各用例共享断言。
func assertTrustCounts(t *testing.T, got, want map[string]int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("统计结果 = %v, want %v", got, want)
	}
}

func trustCounts(trusted, untrusted, unknown int) map[string]int {
	return map[string]int{
		hookTrustTrusted:   trusted,
		hookTrustUntrusted: untrusted,
		hookTrustUnknown:   unknown,
	}
}

// TestCountHooksByTrustNil 验证 nil 配置返回三个零值键，绝不 panic。
func TestCountHooksByTrustNil(t *testing.T) {
	assertTrustCounts(t, countHooksByTrust(nil), trustCounts(0, 0, 0))
}

// TestCountHooksByTrustEmpty 验证空配置返回三个零值键。
func TestCountHooksByTrustEmpty(t *testing.T) {
	assertTrustCounts(t, countHooksByTrust(&Config{}), trustCounts(0, 0, 0))
}

// TestCountHooksByTrustWithoutTrustInfo 验证当前 Config 不带信任信息时，
// 所有命令（含 CJK 文本、异步/内部标志、跨事件）都归入 unknown。
func TestCountHooksByTrustWithoutTrustInfo(t *testing.T) {
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "写入守卫"},
		{Event: PreToolUse, Command: "echo hello", Matcher: "bash"},
		{Event: Stop, Command: "通知用户", Async: true, StopPropagation: true},
		{Event: FileChanged, Command: "记录变更", Priority: 5},
	}}
	assertTrustCounts(t, countHooksByTrust(config), trustCounts(0, 0, 4))
}

// TestCountHooksByTrustParsedConfig 验证文档解析出的配置同样全部归入 unknown，
// 且解析出的命令条数与统计总数一致。
func TestCountHooksByTrustParsedConfig(t *testing.T) {
	config := mustParseConfig(t, `{"hooks":{"PreToolUse":[
		{"matcher":"写入","hooks":[{"command":"echo 开始"},{"command":"echo 结束"}]},
		{"command":"echo bare"}
	]}}`)
	got := countHooksByTrust(config)
	assertTrustCounts(t, got, trustCounts(0, 0, len(config.Commands)))
}

// TestCountHooksByTrustCJK 验证 CJK 命令名不会被按字节截断：每条命令恰好计一次。
func TestCountHooksByTrustCJK(t *testing.T) {
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "备份文件"},
		{Event: PreToolUse, Command: "检查权限"},
		{Event: Stop, Command: "通知用户"},
	}}
	got := countHooksByTrust(config)
	assertTrustCounts(t, got, trustCounts(0, 0, 3))
}

// TestNormalizeTrustState 覆盖信任标注字段的各种取值：布尔、字符串、
// 指针与无法识别的值都安全归一到三个固定状态之一。
func TestNormalizeTrustState(t *testing.T) {
	trusted := "TrUsTeD"
	untrusted := "  untrusted  "
	cjk := "受信任"
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "bool true", value: true, want: hookTrustTrusted},
		{name: "bool false", value: false, want: hookTrustUntrusted},
		{name: "string trusted", value: trusted, want: hookTrustTrusted},
		{name: "string untrusted with spaces", value: untrusted, want: hookTrustUntrusted},
		{name: "string lowercase trusted", value: "trusted", want: hookTrustTrusted},
		{name: "CJK string falls back to unknown", value: cjk, want: hookTrustUnknown},
		{name: "empty string falls back to unknown", value: "", want: hookTrustUnknown},
		{name: "unrecognized string falls back to unknown", value: "maybe", want: hookTrustUnknown},
		{name: "int falls back to unknown", value: 7, want: hookTrustUnknown},
		{name: "nil pointer falls back to unknown", value: (*string)(nil), want: hookTrustUnknown},
		{name: "non-nil string pointer trusted", value: &trusted, want: hookTrustTrusted},
		{name: "non-nil string pointer untrusted", value: &untrusted, want: hookTrustUntrusted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeTrustState(reflect.ValueOf(tc.value))
			if got != tc.want {
				t.Fatalf("normalizeTrustState(%v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestTrustAnnotation 验证信任标注字段按名字识别，缺失或非结构体时返回 false。
func TestTrustAnnotation(t *testing.T) {
	if _, ok := trustAnnotation(reflect.ValueOf(trustStringConfig{Trust: "trusted"})); !ok {
		t.Fatal("Trust field should be detected")
	}
	if field, ok := trustAnnotation(reflect.ValueOf(trustBoolConfig{Trusted: true})); !ok || field.Kind() != reflect.Bool {
		t.Fatalf("Trusted bool field should be detected, got field=%v ok=%v", field, ok)
	}
	if _, ok := trustAnnotation(reflect.ValueOf(struct{ Other string }{})); ok {
		t.Fatal("struct without a trust field should not be annotated")
	}
	if _, ok := trustAnnotation(reflect.ValueOf("trusted")); ok {
		t.Fatal("non-struct value should not be annotated")
	}
}

// TestCountByTrustConfigLevelAnnotation 验证配置级字符串标注作为默认值：
// 带标注时所有命令归入该状态，标注为空或无法识别时回退 unknown。
func TestCountByTrustConfigLevelAnnotation(t *testing.T) {
	commands := []plainCommand{{}, {}, {}}
	assertTrustCounts(t, countByTrust(trustStringConfig{Trust: "trusted"}, commands), trustCounts(3, 0, 0))
	assertTrustCounts(t, countByTrust(trustStringConfig{Trust: "untrusted"}, commands), trustCounts(0, 3, 0))
	assertTrustCounts(t, countByTrust(trustStringConfig{Trust: "受信任"}, commands), trustCounts(0, 0, 3))
	assertTrustCounts(t, countByTrust(trustStringConfig{}, commands), trustCounts(0, 0, 3))
}

// TestCountByTrustCommandLevelOverride 验证命令级布尔标注覆盖配置级默认值，
// 并与其他命令级字符串标注一起形成 trusted/untrusted/unknown 混合统计。
func TestCountByTrustCommandLevelOverride(t *testing.T) {
	config := trustStringConfig{Trust: "untrusted"}
	commands := []any{
		trustBoolCommand{Trusted: true},           // 覆盖为 trusted
		trustBoolCommand{Trusted: false},          // 覆盖为 untrusted
		trustStringCommand{TrustState: "trusted"}, // 覆盖为 trusted
		trustStringCommand{TrustState: "受信任"},     // 无法识别，安全回退 unknown
	}
	got := countByTrust(config, commands)
	assertTrustCounts(t, got, trustCounts(2, 1, 1))
}

// TestCountByTrustMixedWithCJK 验证混合场景下 CJK 命令文本原样保留且计数准确。
func TestCountByTrustMixedWithCJK(t *testing.T) {
	config := trustStringConfig{Trust: "trusted"}
	commands := []trustStringCommand{
		{TrustState: "trusted"},
		{TrustState: "untrusted"},
		{TrustState: "中文标注"},
		{TrustState: ""},
	}
	got := countByTrust(config, commands)
	// "中文标注" 与空串都是无法识别的命令级标注，安全回退 unknown。
	assertTrustCounts(t, got, trustCounts(1, 1, 2))
}

// TestCountByTrustPointerConfig 验证指针配置被解引用后读取信任标注，
// nil 指针则安全回退到 unknown。
func TestCountByTrustPointerConfig(t *testing.T) {
	commands := []plainCommand{{}, {}}
	assertTrustCounts(t, countByTrust(&trustStringConfig{Trust: "trusted"}, commands), trustCounts(2, 0, 0))
	var nilConfig *trustStringConfig
	assertTrustCounts(t, countByTrust(nilConfig, commands), trustCounts(0, 0, 2))
}

// TestCountByTrustNilableCommandTrust 验证指针型命令标注：nil 指针回退配置级
// 默认值，非 nil 指针按其值归类。
func TestCountByTrustNilableCommandTrust(t *testing.T) {
	trusted := "trusted"
	commands := []trustPointerCommand{
		{Trust: &trusted},
		{Trust: nil},
	}
	got := countByTrust(trustStringConfig{Trust: "untrusted"}, commands)
	// nil 指针是“存在但无法识别”的命令级标注，回退 unknown 而非配置级默认值。
	assertTrustCounts(t, got, trustCounts(1, 0, 1))
}
