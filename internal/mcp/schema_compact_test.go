package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// deepSchema 生成 levels 层嵌套的对象，最深处带一个可识别的键，
// 用于验证“折叠深层”级别确实把深层内容替换掉了。
func deepSchema(levels int) json.RawMessage {
	s := `{"type":"string","uniqueDeepKey":1}`
	for i := 0; i < levels; i++ {
		s = `{"type":"object","properties":{"n` + strconv.Itoa(i) + `":` + s + `}}`
	}
	return json.RawMessage(`{"type":"object","properties":{"top":` + s + `}}`)
}

// TestCompactSchemaUnderLimitKeepsBytes 固定关键保证：未超限的 schema
// 逐字节原样返回，连空白和 CJK 都保留。
func TestCompactSchemaUnderLimitKeepsBytes(t *testing.T) {
	const small = `{ "type" : "object" , "description" : "保留原样" }`
	for _, max := range []int{len(small), len(small) + 1000} {
		if got := compactSchema(json.RawMessage(small), max); string(got) != small {
			t.Errorf("max=%d: 未超限 schema 被改动：%s", max, got)
		}
	}
}

// TestCompactSchemaInvalidJSONKeptAsIs 确保非法 JSON（且超限）不会被
// 猜测性“修复”，原样返回交给调用方处理。
func TestCompactSchemaInvalidJSONKeptAsIs(t *testing.T) {
	bad := json.RawMessage(strings.Repeat("{oops", 100))
	if got := compactSchema(bad, 10); string(got) != string(bad) {
		t.Errorf("非法 JSON 被改动：%s", got)
	}
}

// TestCompactSchemaLevelDescriptions 强制只用到第 1 级：超大
// description 被去掉，其余字段必须保留。
func TestCompactSchemaLevelDescriptions(t *testing.T) {
	input := json.RawMessage(`{"type":"object","title":"T","description":"` +
		strings.Repeat("d", 200) + `","properties":{"x":{"type":"string","description":"nested"}}}`)
	out := compactSchema(input, len(input)-1)
	if !json.Valid(out) {
		t.Fatalf("输出不是合法 JSON：%s", out)
	}
	text := string(out)
	if strings.Contains(text, "description") {
		t.Errorf("第 1 级未删除 description：%s", text)
	}
	if !strings.Contains(text, `"title":"T"`) || !strings.Contains(text, `"x"`) {
		t.Errorf("第 1 级误删了其他字段：%s", text)
	}
}

// TestCompactSchemaLevelDefs 强制只用到第 2 级：去掉 $defs 后达标，
// 提前返回意味着更激进的级别不会被误用。
func TestCompactSchemaLevelDefs(t *testing.T) {
	input := json.RawMessage(`{"type":"object","title":"T","$defs":{"Big":"` +
		strings.Repeat("A", 300) + `"}}`)
	out := compactSchema(input, len(input)-1)
	if !json.Valid(out) {
		t.Fatalf("输出不是合法 JSON：%s", out)
	}
	text := string(out)
	if strings.Contains(text, "$defs") {
		t.Errorf("第 2 级未删除 $defs：%s", text)
	}
	if !strings.Contains(text, `"title":"T"`) {
		t.Errorf("第 2 级误删了其他字段：%s", text)
	}
}

// TestCompactSchemaLevelCollapseDeep 强制只用到第 3 级：深层对象被
// 折叠成占位符，深层独有的键消失，输出仍合法且更小。
func TestCompactSchemaLevelCollapseDeep(t *testing.T) {
	input := deepSchema(6)
	out := compactSchema(input, len(input)-1)
	if !json.Valid(out) {
		t.Fatalf("输出不是合法 JSON：%s", out)
	}
	if strings.Contains(string(out), "uniqueDeepKey") {
		t.Errorf("第 3 级未折叠深层对象：%s", out)
	}
	if len(out) >= len(input) {
		t.Errorf("压缩后反而更大：%d -> %d", len(input), len(out))
	}
}

// TestCompactSchemaLevelAnyOf 强制只用到第 4 级：anyOf 被截断到
// compactCompositionBranches 个分支。
func TestCompactSchemaLevelAnyOf(t *testing.T) {
	input := json.RawMessage(`{"type":"object","anyOf":[{"type":"string"},` +
		`{"type":"number"},{"type":"boolean"},{"type":"null"},{"type":"integer"}]}`)
	out := compactSchema(input, len(input)-1)
	if !json.Valid(out) {
		t.Fatalf("输出不是合法 JSON：%s", out)
	}
	var parsed struct {
		AnyOf []json.RawMessage `json:"anyOf"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("解析输出失败：%v", err)
	}
	if len(parsed.AnyOf) != compactCompositionBranches {
		t.Errorf("anyOf 分支数 = %d，期望 %d：%s", len(parsed.AnyOf), compactCompositionBranches, out)
	}
}

// TestCompactSchemaAllLevelsApplied 用一个极小上限迫使四级全部应用，
// 验证它们叠加后仍产出合法 JSON，且每一级的效果都在。
func TestCompactSchemaAllLevelsApplied(t *testing.T) {
	inputBytes, err := json.Marshal(map[string]any{
		"description": strings.Repeat("说明", 100),
		"$defs":       map[string]any{"Def": map[string]any{"type": "string", "description": "def"}},
		"type":        "object",
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
			map[string]any{"type": "boolean"},
		},
		"properties": map[string]any{"deep": deepSchema(6)},
	})
	if err != nil {
		t.Fatalf("构造输入失败：%v", err)
	}
	out := compactSchema(inputBytes, 8)
	if !json.Valid(out) {
		t.Fatalf("输出不是合法 JSON：%s", out)
	}
	text := string(out)
	if strings.Contains(text, "description") {
		t.Errorf("description 未去除：%s", text)
	}
	if strings.Contains(text, "$defs") {
		t.Errorf("$defs 未去除：%s", text)
	}
	if strings.Contains(text, "uniqueDeepKey") {
		t.Errorf("深层对象未折叠：%s", text)
	}
	var parsed struct {
		AnyOf []json.RawMessage `json:"anyOf"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("解析输出失败：%v", err)
	}
	if len(parsed.AnyOf) != compactCompositionBranches {
		t.Errorf("anyOf 分支数 = %d，期望 %d：%s", len(parsed.AnyOf), compactCompositionBranches, out)
	}
}

// TestCompactSchemaCJK 验证多字节 UTF-8 安全：CJK 描述被压缩后，
// 输出仍是合法 JSON，且不出现半个 UTF-8 字符；小 CJK schema 原样保留。
func TestCompactSchemaCJK(t *testing.T) {
	const smallCJK = `{"description":"中文字符"}`
	if got := compactSchema(json.RawMessage(smallCJK), 1024); string(got) != smallCJK {
		t.Errorf("未超限 CJK schema 被改动：%s", got)
	}

	desc := strings.Repeat("搜索文件并读取内容", 200)
	big := json.RawMessage(`{"type":"object","description":"` + desc +
		`","properties":{"q":{"type":"string","description":"` + desc + `"}}}`)
	out := compactSchema(big, 200)
	if !json.Valid(out) {
		t.Fatalf("CJK 输出不是合法 JSON：%s", out)
	}
	if strings.Contains(string(out), "description") {
		t.Errorf("CJK description 未去除：%s", out)
	}
	if len(out) >= len(big) {
		t.Errorf("CJK schema 压缩后反而更大：%d -> %d", len(big), len(out))
	}
	if !utf8.Valid(out) {
		t.Errorf("输出含非法 UTF-8：%q", out)
	}
}

// TestCompactSchemaDeterministic 固定确定性：同一输入调用两次结果一致。
func TestCompactSchemaDeterministic(t *testing.T) {
	inputBytes, err := json.Marshal(map[string]any{
		"z":           1,
		"a":           2,
		"description": strings.Repeat("x", 500),
		"anyOf":       []any{"a", "b", "c", "d"},
	})
	if err != nil {
		t.Fatalf("构造输入失败：%v", err)
	}
	first := compactSchema(inputBytes, 50)
	second := compactSchema(inputBytes, 50)
	if string(first) != string(second) {
		t.Errorf("压缩不确定：\n第一次：%s\n第二次：%s", first, second)
	}
}

// TestListToolsCompactSchemaOptIn 验证接线是 opt-in 且默认关闭：
// 默认不改变工具定义；设置上限后只压缩面向模型的 Schema，
// RawInputSchema 始终保留服务器原文。
func TestListToolsCompactSchemaOptIn(t *testing.T) {
	bigDesc := strings.Repeat("长", 300)
	schema := json.RawMessage(`{"type":"object","description":"` + bigDesc + `"}`)
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			return map[string]any{"tools": []any{
				map[string]any{"name": "big", "inputSchema": schema},
			}}, nil
		},
	}}

	// 默认关闭：Schema 仍带完整描述。
	def := &client{spec: Spec{Name: "x"}, tr: ft, timeout: time.Second}
	defTools, err := def.ListTools(context.Background())
	if err != nil {
		t.Fatalf("默认 ListTools：%v", err)
	}
	if len(defTools) != 1 || !strings.Contains(string(defTools[0].Schema), "description") {
		t.Fatalf("默认应保留原始 schema：%+v", defTools)
	}

	// 显式开启：Schema 被压缩，原文仍在 RawInputSchema。
	opt := &client{spec: Spec{Name: "x", CompactSchemaMaxBytes: 60}, tr: ft, timeout: time.Second}
	optTools, err := opt.ListTools(context.Background())
	if err != nil {
		t.Fatalf("opt-in ListTools：%v", err)
	}
	if len(optTools) != 1 {
		t.Fatalf("工具数 = %d，期望 1", len(optTools))
	}
	if strings.Contains(string(optTools[0].Schema), "description") {
		t.Errorf("opt-in 后 Schema 未被压缩：%s", optTools[0].Schema)
	}
	if string(optTools[0].RawInputSchema) != string(schema) {
		t.Errorf("RawInputSchema 被改动：%s", optTools[0].RawInputSchema)
	}
}
