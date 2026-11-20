package llm

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"
	"sync"
	"unicode/utf8"
)

// ModelCompat 选择 ProjectToolSchema 应用哪些模型兼容性投影。
// 每条规则都是独立的位标志，调用方可以组合它们；
// 零值不执行任何投影。
type ModelCompat uint8

const (
	compatObjectRoot ModelCompat = 1 << iota
	compatEnumType
)

const (
	// CompatNone 让 schema 逐字节保持不变。
	CompatNone ModelCompat = 0
	// CompatGemini 规范化顶层 schema，使其声明 object
	// 类型：Gemini 会拒绝根节点没有 object 类型的工具 schema（如
	// 空 schema、裸 anyOf/oneOf 或 not:null 守卫）。
	CompatGemini = compatObjectRoot
	// CompatMoonshot 为无类型的枚举推断原始类型：Kimi /
	// Moonshot 会拒绝省略 "type" 的枚举 schema。
	CompatMoonshot = compatEnumType
	// CompatAll 应用所有可用的投影。
	CompatAll = CompatGemini | CompatMoonshot
)

// ProjectToolSchema 返回针对目标模型系列调整后的工具参数 schema。
// 它是纯函数且确定性的：
//
//   - 无效 JSON、非 UTF-8 输入以及无需调整的 schema
//     原样返回（因此现有 schema 保持其字节不变）；
//   - 嵌套的 object/array schema 会遍历 properties、items、
//     additionalProperties、not 以及 anyOf/oneOf/allOf 组合子；
//   - 相等的输入总是产生相等的输出（encoding/json 对对象键排序，
//     且通过 json.Number 保持数字精确）。
//
// 由 agent 循环（internal/agent/loop.go）接入，它在把工具定义发送给提供方之前
// 投影每个工具的参数 schema。
func ProjectToolSchema(schemaJSON string, compat ModelCompat) string {
	if compat == CompatNone || !utf8.ValidString(schemaJSON) || !json.Valid([]byte(schemaJSON)) {
		return schemaJSON
	}

	decoder := json.NewDecoder(strings.NewReader(schemaJSON))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return schemaJSON
	}

	if !projectSchemaNode(root, compat, true) {
		return schemaJSON
	}

	result, err := encodeSchemaJSON(root)
	if err != nil {
		return schemaJSON
	}
	return result
}

// --- 缓存投影 --------------------------------------------------------------

// defaultSchemaCacheCapacity 限制 memo 表的大小。工具注册表每轮都会重建
// 其 spec，因此实践中只有几十种不同的 schema 会被
// 投影；256 留有充足余量，同时保持内存有界。
const defaultSchemaCacheCapacity = 256

// schemaCacheKey 标识一次投影请求。compat 标志刻意是
// 键的一部分：同一 schema 为 Gemini 和为 Moonshot 投影
// 会产生不同的字节，因此仅以 schema 为键是不正确的。
type schemaCacheKey struct {
	schema string
	compat ModelCompat
}

// schemaProjectionCache 缓存纯函数 ProjectToolSchema 的计算结果。它是
// opencode 的 codecFor WeakMap 在 Go 中的对应物（"每个 schema
// 对象只构建一次"）：昂贵的 JSON 解码/编码对每个不同输入只付出一次，
// 而不是每轮一次。它可安全并发使用。
//
// 增长是有界的：当表已满时，下一次未命中会先清空它
// 再插入。这是简单的"满则清空"策略；稳态工作集远低于上限，
// 因此清空很少发生，而且即使 schema 在一个会话中不断变化，内存
// 也绝不会超过容量条目数。
type schemaProjectionCache struct {
	mu       sync.Mutex
	entries  map[schemaCacheKey]string
	capacity int
	// projectFn 是底层纯投影函数。它放在结构体上，以便
	// 测试可以注入计数函数；生产环境使用 ProjectToolSchema。
	projectFn func(string, ModelCompat) string
}

func newSchemaProjectionCache(capacity int, projectFn func(string, ModelCompat) string) *schemaProjectionCache {
	if capacity <= 0 {
		capacity = defaultSchemaCacheCapacity
	}
	if projectFn == nil {
		projectFn = ProjectToolSchema
	}
	return &schemaProjectionCache{
		entries:   make(map[schemaCacheKey]string, capacity),
		capacity:  capacity,
		projectFn: projectFn,
	}
}

// project 返回 (schemaJSON, compat) 的缓存投影，未命中时
// 计算它。解码/编码在锁外运行，使不同键的并发未命中
// 不会在 CPU 工作后串行化。
func (c *schemaProjectionCache) project(schemaJSON string, compat ModelCompat) string {
	key := schemaCacheKey{schema: schemaJSON, compat: compat}

	c.mu.Lock()
	if got, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return got
	}
	c.mu.Unlock()

	result := c.projectFn(schemaJSON, compat)

	c.mu.Lock()
	if len(c.entries) >= c.capacity {
		// 有界增长：重新开始，而不是保留陈旧的 schema。
		c.entries = make(map[schemaCacheKey]string, c.capacity)
	}
	c.entries[key] = result
	c.mu.Unlock()
	return result
}

// defaultSchemaCache 为 ProjectToolSchemaCached 提供后端。
var defaultSchemaCache = newSchemaProjectionCache(defaultSchemaCacheCapacity, ProjectToolSchema)

// ProjectToolSchemaCached 是带有限 memo 层的 ProjectToolSchema：
// 重复的 (schema, compat) 对复用第一次投影的字节，因此
// JSON 解码/编码遍历对每个不同 schema 只运行一次，而不是每轮一次。
// 它在语义上与 ProjectToolSchema 完全相同——输入相同字节则输出相同字节，
// 包括无效 JSON、无效 UTF-8 和无操作投影——且可安全
// 并发使用。ProjectToolSchema 本身未改变；调用方通过
// 调用此变体来选择加入。
func ProjectToolSchemaCached(schemaJSON string, compat ModelCompat) string {
	return defaultSchemaCache.project(schemaJSON, compat)
}

// encodeSchemaJSON 确定性地序列化解码后的 schema：对象键
// 会排序，非 ASCII UTF-8 以及 <、>、& 保持原样。两种投影
// 共享它，使无操作投影能保留调用方的原始字节。
func encodeSchemaJSON(root any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(root); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// projectSchemaNode 原地遍历解码后的 JSON 值，并报告是否
// 做了改动。map 和 slice 是引用类型，因此原地修改
// 就足够；标量原样返回。
func projectSchemaNode(node any, compat ModelCompat, isRoot bool) bool {
	switch value := node.(type) {
	case map[string]any:
		return projectSchemaObject(value, compat, isRoot)
	case []any:
		changed := false
		for _, element := range value {
			if projectSchemaNode(element, compat, false) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func projectSchemaObject(obj map[string]any, compat ModelCompat, isRoot bool) bool {
	changed := false

	// Gemini：工具 schema 必须在根节点声明 object 类型。空
	// schema、裸 anyOf/oneOf 分组和 not:null 守卫都没有。
	if isRoot && compat&compatObjectRoot != 0 {
		if _, hasType := obj["type"]; !hasType {
			obj["type"] = "object"
			changed = true
		}
	}

	// Kimi / Moonshot：为省略 "type" 的枚举推断原始类型。
	if compat&compatEnumType != 0 {
		if _, hasType := obj["type"]; !hasType {
			if enum, hasEnum := obj["enum"]; hasEnum {
				if inferred, ok := inferEnumType(enum); ok {
					obj["type"] = inferred
					changed = true
				}
			}
		}
	}

	if properties, ok := obj["properties"].(map[string]any); ok {
		for _, property := range properties {
			if projectSchemaNode(property, compat, false) {
				changed = true
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not"} {
		if child, ok := obj[key]; ok {
			if projectSchemaNode(child, compat, false) {
				changed = true
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if children, ok := obj[key].([]any); ok {
			if projectSchemaNode(children, compat, false) {
				changed = true
			}
		}
	}

	return changed
}

// inferEnumType 返回所有枚举值共享的单一原始类型。
// 不相关类型的混合（或 null/复合值）无法确定，
// 留给调用方跳过。整数与数字视为兼容，
// 并放宽为 number。
func inferEnumType(enum any) (string, bool) {
	values, ok := enum.([]any)
	if !ok || len(values) == 0 {
		return "", false
	}

	inferred := ""
	for _, value := range values {
		var kind string
		switch typed := value.(type) {
		case string:
			kind = "string"
		case json.Number:
			kind = "number"
			if _, err := typed.Int64(); err == nil {
				kind = "integer"
			}
		case bool:
			kind = "boolean"
		default:
			return "", false
		}

		switch {
		case inferred == "":
			inferred = kind
		case inferred == kind:
			// 值一致
		case (inferred == "integer" && kind == "number") || (inferred == "number" && kind == "integer"):
			inferred = "number"
		default:
			return "", false
		}
	}
	if inferred == "" {
		return "", false
	}
	return inferred, true
}

// safeIntMinBounds / safeIntMaxBounds 是 effect（以及
// 其他 schema 生成器）附加到每个整数 schema 上的哨兵边界。它们对
// 工具调用没有任何校验意义，但它们的存在会改变 schema
// 字节并破坏提示缓存。第一对是 JS
// Number.MIN/MAX_SAFE_INTEGER（opencode 的 flattenAllOf 使用）；第二对是
// MinInt64/MaxInt64 除以三的下取整/上取整，由其他
// "安全整数"生成器发出。
var (
	safeIntMinBounds = [...]int64{-9007199254740991, -3074457345618258603}
	safeIntMaxBounds = [...]int64{9007199254740991, 3074457345618258602}
)

// FlattenToolSchema 是面向那些处理 allOf 不佳的提供方的纯的、选择加入的投影。
// 它模仿 opencode 的 flattenAllOf：
//
//   - 整数 schema 去掉其安全整数哨兵 minimum/maximum；
//   - 只有当没有任何键被多个成员声明时，才把 allOf 组
//     合并到其父节点（也包括父节点除 "allOf" 外的键），因此
//     required/type/properties 语义永不会被覆盖；
//   - 遍历是递归的（对象、数组、组合子），且合并结果
//     会被再次遍历，以防它产生了新的可扁平化 allOf。
//
// 无效 JSON、非 UTF-8 输入以及无需改动的 schema 会被
// 逐字节原样返回。该函数从不修改其输入，也不
// 改变 ProjectToolSchema 的默认行为；调用方必须显式选择加入。
func FlattenToolSchema(schemaJSON string) string {
	if !utf8.ValidString(schemaJSON) || !json.Valid([]byte(schemaJSON)) {
		return schemaJSON
	}

	decoder := json.NewDecoder(strings.NewReader(schemaJSON))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return schemaJSON
	}

	if !flattenAllOfNode(root) {
		return schemaJSON
	}

	result, err := encodeSchemaJSON(root)
	if err != nil {
		return schemaJSON
	}
	return result
}

// flattenAllOfNode 原地遍历解码后的 JSON 值，并报告是否
// 做了改动。map 和 slice 是引用类型，因此原地修改
// 就足够；标量原样返回。
func flattenAllOfNode(node any) bool {
	switch value := node.(type) {
	case map[string]any:
		return flattenAllOfObject(value)
	case []any:
		changed := false
		for _, element := range value {
			if flattenAllOfNode(element) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func flattenAllOfObject(obj map[string]any) bool {
	changed := false

	// 在查看 allOf 之前，先从整数 schema 中去掉安全整数哨兵范围，
	// 因为哨兵本身可能位于某个 allOf 子项中。
	if kind, _ := obj["type"].(string); kind == "integer" {
		for _, bound := range safeIntMinBounds {
			if numberEqualsInt64(obj["minimum"], bound) {
				delete(obj, "minimum")
				changed = true
				break
			}
		}
		for _, bound := range safeIntMaxBounds {
			if numberEqualsInt64(obj["maximum"], bound) {
				delete(obj, "maximum")
				changed = true
				break
			}
		}
	}

	// 先递归，模仿 opencode：子项（包括 allOf 成员）在
	// 父节点决定该组能否合并之前先被扁平化。
	for _, value := range obj {
		if flattenAllOfNode(value) {
			changed = true
		}
	}

	allOf, ok := obj["allOf"].([]any)
	if !ok || !allOfHasObjects(allOf) || !canFlattenAllOf(allOf, obj) {
		return changed
	}

	// 按顺序合并每个成员，然后是父节点自己的键。canFlattenAllOf
	// 保证没有键出现两次，因此成员顺序不会覆盖任何内容，
	// 也不会丢失任何校验关键字（required、type 等）。
	merged := make(map[string]any, len(obj)+len(allOf))
	for _, item := range allOf {
		for key, value := range item.(map[string]any) {
			merged[key] = value
		}
	}
	for key, value := range obj {
		if key != "allOf" {
			merged[key] = value
		}
	}
	for key := range obj {
		delete(obj, key)
	}
	for key, value := range merged {
		obj[key] = value
	}

	// 合并可能暴露出另一个可扁平化的 allOf（例如原本位于
	// 某个成员内部的 allOf 键）。JSON 树是有限的，每轮消耗一个
	// allOf 层，因此这总会终止。
	flattenAllOfNode(obj)
	return true
}

func allOfHasObjects(allOf []any) bool {
	for _, item := range allOf {
		if _, ok := item.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// canFlattenAllOf 报告是否每个键最多由 allOf 的一个成员声明，
// 且不与父节点的键冲突（"allOf" 除外）。
func canFlattenAllOf(allOf []any, parent map[string]any) bool {
	seen := make(map[string]bool, len(parent))
	for key := range parent {
		if key != "allOf" {
			seen[key] = true
		}
	}
	for _, item := range allOf {
		for key := range item.(map[string]any) {
			if seen[key] {
				return false
			}
			seen[key] = true
		}
	}
	return true
}

// numberEqualsInt64 把解码后的 json.Number 与精确整数比较，
// 既接受普通十进制字面量（"9007199254740991"），也接受
// Int64 无法解析的形式，如 "9.007199254740991e15" 或 "...991.0"。
func numberEqualsInt64(value any, want int64) bool {
	num, ok := value.(json.Number)
	if !ok {
		return false
	}
	if n, err := num.Int64(); err == nil {
		return n == want
	}
	got, ok := new(big.Rat).SetString(num.String())
	if !ok {
		return false
	}
	return got.Cmp(new(big.Rat).SetInt64(want)) == 0
}
