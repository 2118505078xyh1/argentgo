package config

import (
	"bytes"
	"encoding/json"
	"sort"
	"testing"
	"unicode/utf8"
)

// schemaDocument 解析 configSchemaJSON 的输出，先验证其为合法 JSON。
func schemaDocument(t *testing.T) map[string]any {
	t.Helper()
	data := configSchemaJSON()
	if len(data) == 0 {
		t.Fatal("configSchemaJSON 返回了空字节")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("configSchemaJSON 不是合法的 JSON: %v", err)
	}
	return doc
}

// schemaProperties 返回文档中的 properties 映射。
func schemaProperties(t *testing.T) map[string]any {
	t.Helper()
	props, ok := schemaDocument(t)["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema 文档缺少 properties 对象")
	}
	return props
}

// schemaProperty 返回单个配置键的 schema 片段。
func schemaProperty(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, ok := schemaProperties(t)[name].(map[string]any)
	if !ok {
		t.Fatalf("schema 缺少配置键 %q", name)
	}
	return raw
}

// 输出必须是一份带基本信封字段的合法 JSON Schema 文档。
func TestConfigSchemaJSONIsValidDocument(t *testing.T) {
	doc := schemaDocument(t)
	if got, _ := doc["$schema"].(string); got == "" {
		t.Error("schema 缺少 $schema 声明")
	}
	if got, _ := doc["type"].(string); got != "object" {
		t.Errorf("type = %q，期望 object", got)
	}
	if got, _ := doc["title"].(string); !containsCJK(got) {
		t.Errorf("title = %q，期望包含中文", got)
	}
}

// 主要配置键都必须出现在 schema 中，且类型与实现一致。
func TestConfigSchemaJSONKeyFieldsPresent(t *testing.T) {
	props := schemaProperties(t)
	want := map[string]string{
		"model":                      "string",
		"baseURL":                    "string",
		"apiKey":                     "string",
		"protocol":                   "string",
		"locale":                     "string",
		"sandboxMode":                "string",
		"privacyMode":                "string",
		"maxTurns":                   "integer",
		"yolo":                       "boolean",
		"storageDir":                 "string",
		"provider":                   "string",
		"profile":                    "string",
		"verifyCommand":              "array",
		"verifyCommands":             "object",
		"verifyTimeoutMS":            "integer",
		"verifyMaxReflections":       "integer",
		"contextWindow":              "integer",
		"stopTokenIds":               "array",
		"autoCondenseContextPercent": "integer",
		"profiles":                   "object",
		"providers":                  "object",
	}
	for name, typ := range want {
		raw, ok := props[name].(map[string]any)
		if !ok {
			t.Errorf("schema 缺少配置键 %q", name)
			continue
		}
		if got, _ := raw["type"].(string); got != typ {
			t.Errorf("%s 的 type = %q，期望 %q", name, got, typ)
		}
	}
}

// 默认值、枚举与数值边界必须来自实现使用的常量。
func TestConfigSchemaJSONDefaultsAndBounds(t *testing.T) {
	if got := schemaProperty(t, "model")["default"]; got != defaultModel {
		t.Errorf("model 默认值 = %v，期望 %q", got, defaultModel)
	}
	if got := schemaProperty(t, "baseURL")["default"]; got != defaultBaseURL {
		t.Errorf("baseURL 默认值 = %v，期望 %q", got, defaultBaseURL)
	}
	if got := schemaProperty(t, "protocol")["default"]; got != defaultProtocol {
		t.Errorf("protocol 默认值 = %v，期望 %q", got, defaultProtocol)
	}
	if got := schemaProperty(t, "maxTurns")["default"]; got != float64(defaultMaxTurns) {
		t.Errorf("maxTurns 默认值 = %v，期望 %d", got, defaultMaxTurns)
	}
	if got := schemaProperty(t, "verifyTimeoutMS")["default"]; got != float64(defaultVerifyTimeoutMS) {
		t.Errorf("verifyTimeoutMS 默认值 = %v，期望 %d", got, defaultVerifyTimeoutMS)
	}
	if got := schemaProperty(t, "yolo")["default"]; got != false {
		t.Errorf("yolo 默认值 = %v，期望 false", got)
	}

	sandbox := schemaProperty(t, "sandboxMode")
	if got := sandbox["default"]; got != SandboxWorkspaceWrite {
		t.Errorf("sandboxMode 默认值 = %v，期望 %q", got, SandboxWorkspaceWrite)
	}
	assertEnum(t, "sandboxMode", sandbox, []string{SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess})
	assertEnum(t, "privacyMode", schemaProperty(t, "privacyMode"), []string{PrivacyModeAny, PrivacyModeLocalOnly})

	assertBound(t, "maxTurns", schemaProperty(t, "maxTurns"), "minimum", 0)
	assertBound(t, "verifyTimeoutMS", schemaProperty(t, "verifyTimeoutMS"), "maximum", 120000)
	assertBound(t, "verifyMaxReflections", schemaProperty(t, "verifyMaxReflections"), "minimum", 0)
	assertBound(t, "contextWindow", schemaProperty(t, "contextWindow"), "minimum", 0)
	assertBound(t, "autoCondenseContextPercent", schemaProperty(t, "autoCondenseContextPercent"), "minimum", -1)
	assertBound(t, "autoCondenseContextPercent", schemaProperty(t, "autoCondenseContextPercent"), "maximum", 100)
}

// 中文说明必须以原始 UTF-8 写出，不能转义成 \uXXXX。
func TestConfigSchemaJSONChineseNotEscaped(t *testing.T) {
	data := configSchemaJSON()
	if !utf8.Valid(data) {
		t.Fatal("schema 输出不是合法的 UTF-8")
	}
	if bytes.Contains(data, []byte{'\\', 'u'}) {
		t.Fatal("schema 把中文转义成了 \\uXXXX")
	}
	for _, name := range []string{"model", "baseURL", "sandboxMode", "privacyMode", "autoCondenseContextPercent"} {
		desc, _ := schemaProperty(t, name)["description"].(string)
		if !containsCJK(desc) {
			t.Errorf("%s 的 description = %q，期望包含中文", name, desc)
		}
	}
}

// 纯函数必须稳定：多次调用字节完全一致，且 map 键按名称有序。
func TestConfigSchemaJSONStable(t *testing.T) {
	first := configSchemaJSON()
	for i := 0; i < 5; i++ {
		if !bytes.Equal(first, configSchemaJSON()) {
			t.Fatal("多次调用 configSchemaJSON 的输出不一致")
		}
	}
	// 属性是唯一的 map，encoding/json 会按名称排序其键。
	// 这里确认输出中属性名确实按升序出现，证明顺序稳定有序。
	var doc map[string]any
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatalf("解析 schema 失败: %v", err)
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema 缺少 properties 对象")
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	previous := -1
	for _, name := range names {
		index := bytes.Index(first, []byte(`"`+name+`":`))
		if index <= previous {
			t.Errorf("属性 %q 未按名称升序排列", name)
		}
		previous = index
	}
}

// 空值与边界情况：每个属性都有非空类型、无别名时省略 x-aliases、
// 未知键被允许、数组与对象属性带有正确的内层约束。
func TestConfigSchemaJSONEmptyAndBoundary(t *testing.T) {
	props := schemaProperties(t)
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("属性 %q 不是对象", name)
			continue
		}
		if typ, _ := prop["type"].(string); typ == "" {
			t.Errorf("属性 %q 缺少 type", name)
		}
	}
	if _, ok := schemaProperty(t, "locale")["x-aliases"]; ok {
		t.Error("没有别名的 locale 不应出现 x-aliases")
	}
	if _, ok := schemaProperty(t, "provider")["x-aliases"]; ok {
		t.Error("没有别名的 provider 不应出现 x-aliases")
	}
	if got := schemaDocument(t)["additionalProperties"]; got != true {
		t.Errorf("additionalProperties = %v，期望 true（未知键应被保留）", got)
	}

	verifyCommands := schemaProperty(t, "verifyCommands")
	values, ok := verifyCommands["additionalProperties"].(map[string]any)
	if !ok || values["type"] != "string" {
		t.Error("verifyCommands 的 values 应为字符串")
	}
	verifyCommand := schemaProperty(t, "verifyCommand")
	items, ok := verifyCommand["items"].(map[string]any)
	if !ok || items["type"] != "string" {
		t.Error("verifyCommand 的 items 应为字符串")
	}
	stopIDs := schemaProperty(t, "stopTokenIds")
	if items, ok := stopIDs["items"].(map[string]any); !ok || items["type"] != "integer" {
		t.Error("stopTokenIds 的 items 应为整数")
	}
}

// assertEnum 校验某属性的枚举值完整且有序。
func assertEnum(t *testing.T, name string, prop map[string]any, want []string) {
	t.Helper()
	raw, ok := prop["enum"].([]any)
	if !ok || len(raw) != len(want) {
		t.Fatalf("%s 的 enum = %v，期望 %v", name, prop["enum"], want)
	}
	for i, value := range want {
		if raw[i] != value {
			t.Errorf("%s 的 enum[%d] = %v，期望 %q", name, i, raw[i], value)
		}
	}
}

// assertBound 校验某属性的数值边界（JSON 数字解码为 float64）。
func assertBound(t *testing.T, name string, prop map[string]any, key string, want int) {
	t.Helper()
	if got, ok := prop[key].(float64); !ok || got != float64(want) {
		t.Errorf("%s 的 %s = %v，期望 %d", name, key, prop[key], want)
	}
}

// containsCJK 报告字符串是否含有 CJK 统一表意文字。
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
