package hooks

import (
	"reflect"
	"strings"
)

// 信任状态的字符串键，作为统计结果的固定键。
// 它们与 CLI `hooks` 子命令输出的状态拼写保持一致（见 internal/cli/hooks.go）。
const (
	hookTrustTrusted   = "trusted"
	hookTrustUntrusted = "untrusted"
	hookTrustUnknown   = "unknown"
)

// trustFieldNames 是信任标注字段的候选名，按优先级排列。
//
// 当前信任以文档 SHA-256 的形式保存在 TrustStore 中，Config 与 Command 本身
// 不携带信任信息。为让本统计能力可向前兼容：若 Config 或单条 Command 后续
// 带有这些名字之一的导出字段，countHooksByTrust 会读取并采用它；字段缺失时
// 一律归入 unknown。只匹配导出字段，绝不触碰未导出状态。
var trustFieldNames = [...]string{"Trust", "TrustState", "TrustStatus", "Trusted"}

// countHooksByTrust 按信任状态统计配置中的 hook 命令数，供 doctor 展示
// “受信任/不受信任的 hook 数”。它是一个纯函数：不读取外部状态、不派发
// 事件、不修改配置，同一输入始终返回相同结果。
//
// 返回的 map 始终含有 "trusted"、"untrusted"、"unknown" 三个键，值为
// 对应命令数（即便为零也保留），调用方无需判空即可直接读取。nil 或空
// 配置下三个键均为 0。
//
// 信任信息当前不在 Config 中（信任按文档摘要保存在 TrustStore），因此默认
// 每条命令都是 unknown。若 Config 带有信任标注字段，它作为所有命令的默认值；
// 若单条 Command 带有标注字段，它覆盖配置级默认值。布尔真值为 trusted、
// 假值为 untrusted；字符串按大小写不敏感匹配 trusted/untrusted，空值、
// 无法识别的值（含 CJK 文本）均安全回退到 unknown。命令按值遍历，
// CJK 命令文本原样保留、每条恰好计一次，不会因字节切片而被截断。
func countHooksByTrust(c *Config) map[string]int {
	if c == nil {
		return emptyTrustCounts()
	}
	return countByTrust(c, c.Commands)
}

// emptyTrustCounts 返回三个状态均为零的统计结果。
func emptyTrustCounts() map[string]int {
	return map[string]int{
		hookTrustTrusted:   0,
		hookTrustUntrusted: 0,
		hookTrustUnknown:   0,
	}
}

// countByTrust 是 countHooksByTrust 的实现主体。config 接收配置对象（当前为
// *Config；带信任标注的测试形状同样适用），commands 是待统计的命令列表。
// 配置级标注作为默认值，命令级标注覆盖它。
func countByTrust[T any](config any, commands []T) map[string]int {
	counts := emptyTrustCounts()

	configDefault := hookTrustUnknown
	value := reflect.ValueOf(config)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if field, ok := trustAnnotation(value); ok {
		configDefault = normalizeTrustState(field)
	}

	for _, command := range commands {
		state := configDefault
		if field, ok := trustAnnotation(reflect.ValueOf(command)); ok {
			state = normalizeTrustState(field)
		}
		counts[state]++
	}
	return counts
}

// trustAnnotation 在结构体 v 中按 trustFieldNames 的顺序查找第一个可见的
// 信任标注字段并返回。v 不是结构体或没有匹配字段时返回零值与 false。
func trustAnnotation(v reflect.Value) (reflect.Value, bool) {
	if v.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	for _, name := range trustFieldNames {
		field := v.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			return field, true
		}
	}
	return reflect.Value{}, false
}

// normalizeTrustState 把一个信任标注字段归一到三个固定状态之一。
// 支持 bool、*bool 与 string（及其指针）；nil 指针、其他类型与无法识别的
// 字符串值一律返回 unknown。字符串处理按 rune 进行，CJK 值不会被截断。
func normalizeTrustState(field reflect.Value) string {
	value := field
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return hookTrustUnknown
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Bool:
		if value.Bool() {
			return hookTrustTrusted
		}
		return hookTrustUntrusted
	case reflect.String:
		switch strings.ToLower(strings.TrimSpace(value.String())) {
		case hookTrustTrusted:
			return hookTrustTrusted
		case hookTrustUntrusted:
			return hookTrustUntrusted
		}
	}
	return hookTrustUnknown
}
