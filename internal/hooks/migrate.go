package hooks

// 配置迁移：把旧版 hook 文档的字段/结构升级到当前 schema。
//
// 本文件提供一个纯函数式的迁移框架。它不读文件、不访问环境、不修改传入的
// 字节切片；只把 raw 解码成 JSON 树，对每一个对象节点依次套用迁移规则，
// 再编码回字节。没有任何规则命中时原样返回 raw（同一底层数组）和 false，
// 因此"无变化"是零拷贝且字节完全一致的；命中时返回新字节和 true。
//
// 解码使用 UseNumber 保留数字字面量，编码关闭 HTML 转义以保留 CJK 与原始
// 标点；encoding/json 对 map 键排序，因此输出稳定、可重复，便于验证幂等。

import (
	"bytes"
	"encoding/json"
	"io"
)

// migrationRule 是一条配置迁移规则。apply 就地修改传入的对象节点，返回该节点
// 是否发生了改动。采用函数式规则而非固定改名表，使框架既能做字段改名、也能做
// 结构升级；拆出 apply 也让测试可以注入任意规则来验证框架本身。
type migrationRule struct {
	name  string // 规则标识，仅供诊断/测试识别，不影响迁移行为
	apply func(node map[string]any) bool
}

// renameRule 构造一条"旧字段改名为新字段"的迁移规则。
// old 不存在时不改动；old 存在时删除它，仅当 new 尚不存在时才写入其值，
// 从而不会覆盖文档里已经存在的新字段。返回值表示是否删除了旧字段。
func renameRule(old, new string) migrationRule {
	return migrationRule{
		name: old + "->" + new,
		apply: func(node map[string]any) bool {
			value, ok := node[old]
			if !ok {
				return false
			}
			delete(node, old)
			if _, exists := node[new]; !exists {
				node[new] = value
			}
			return true
		},
	}
}

// defaultHookMigrationRules 是内置的迁移规则表。当前放入一条示例规则，演示
// 旧字段名到当前 schema 的改名；后续若确认历史版本使用过其它字段，只需在此
// 追加规则，遍历与编码逻辑无需改动。
var defaultHookMigrationRules = []migrationRule{
	// 旧版用 timeout（毫秒），当前 schema 统一为 timeoutMs。
	renameRule("timeout", "timeoutMs"),
}

// migrateHookConfig 把旧版 hook 配置升级到当前 schema。
// raw 无效、为空或没有任何规则命中时返回 (raw, false)；迁移成功时返回
// (新字节, true)。它不修改传入的切片，也不触碰任何外部状态。
func migrateHookConfig(raw json.RawMessage) (json.RawMessage, bool) {
	return applyHookMigrations(raw, defaultHookMigrationRules)
}

// ParseConfigMigrated 是迁移能力的显式 opt-in 入口：migrate 为 true 时先调用
// migrateHookConfig 再交给 ParseConfig；为 false 时行为与 ParseConfig 完全一致。
// 既有调用方（ParseConfig/LoadConfig/LoadDocument）未被改动，因此默认不生效。
//
// 注意：信任摘要作用于文件的原始字节。若要对落盘文档做迁移，应先迁移并写入、
// 再让用户重新批准新摘要，而不是在加载时透明改写已被批准内容的行为。
func ParseConfigMigrated(data []byte, migrate bool) (*Config, error) {
	if migrate {
		data, _ = migrateHookConfig(data)
	}
	return ParseConfig(data)
}

// applyHookMigrations 是 migrateHookConfig 的核心：按给定规则遍历并升级 raw。
// 拆出规则参数是为了让测试注入自定义规则，验证框架而不只验证内置示例规则。
func applyHookMigrations(raw json.RawMessage, rules []migrationRule) (json.RawMessage, bool) {
	if len(rules) == 0 || len(raw) == 0 {
		return raw, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return raw, false
	}
	// 多读一个值以拒绝尾随垃圾，等价于 json.Unmarshal 的严格性。
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return raw, false
	}
	changed := applyRulesToValue(root, rules)
	if !changed {
		return raw, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return raw, false
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), true
}

// applyRulesToValue 递归遍历 JSON 树，对每一个对象节点套用规则。
// 先对当前节点套用规则、再递归子节点，使规则新生成的对象也能被后续遍历覆盖。
func applyRulesToValue(value any, rules []migrationRule) bool {
	changed := false
	switch node := value.(type) {
	case map[string]any:
		for _, rule := range rules {
			if rule.apply != nil && rule.apply(node) {
				changed = true
			}
		}
		for _, child := range node {
			if applyRulesToValue(child, rules) {
				changed = true
			}
		}
	case []any:
		for _, child := range node {
			if applyRulesToValue(child, rules) {
				changed = true
			}
		}
	}
	return changed
}
