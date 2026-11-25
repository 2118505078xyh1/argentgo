package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestCoerceToolArgumentsNumericStrings 覆盖数字/布尔字符串的保守强转及失败保留。
func TestCoerceToolArgumentsNumericStrings(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"number"},"c":{"type":"boolean"}}}`)
	tests := []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{"整数字符串", map[string]any{"a": "4"}, map[string]any{"a": float64(4)}},
		{"负数与零", map[string]any{"a": "-3", "b": "0"}, map[string]any{"a": float64(-3), "b": float64(0)}},
		{"浮点字符串", map[string]any{"b": "3.14"}, map[string]any{"b": float64(3.14)}},
		{"指数字符串", map[string]any{"b": "1e3"}, map[string]any{"b": float64(1000)}},
		{"布尔真", map[string]any{"c": "true"}, map[string]any{"c": true}},
		{"布尔假", map[string]any{"c": "false"}, map[string]any{"c": false}},
		{"原本就是数字与布尔", map[string]any{"a": float64(4), "c": true}, map[string]any{"a": float64(4), "c": true}},
		{"整数非数字串保留", map[string]any{"a": "abc"}, map[string]any{"a": "abc"}},
		{"整数拒绝小数串", map[string]any{"a": "4.5"}, map[string]any{"a": "4.5"}},
		{"布尔拒绝数字串", map[string]any{"c": "1"}, map[string]any{"c": "1"}},
		{"布尔拒绝大小写变体", map[string]any{"c": "True"}, map[string]any{"c": "True"}},
		{"超大整数保留以免精度丢失", map[string]any{"a": "9007199254740993"}, map[string]any{"a": "9007199254740993"}},
		{"二的五十三次方仍精确", map[string]any{"a": "9007199254740992"}, map[string]any{"a": float64(1 << 53)}},
		{"非法数字写法保留", map[string]any{"b": "007"}, map[string]any{"b": "007"}},
		{"带加号数字保留", map[string]any{"b": "+4"}, map[string]any{"b": "+4"}},
		{"NaN 保留", map[string]any{"b": "NaN"}, map[string]any{"b": "NaN"}},
		{"无穷保留", map[string]any{"b": "Inf"}, map[string]any{"b": "Inf"}},
		{"超出浮点范围的指数保留", map[string]any{"b": "1e400"}, map[string]any{"b": "1e400"}},
		{"CJK 数字保留", map[string]any{"a": "四", "b": "３.１４"}, map[string]any{"a": "四", "b": "３.１４"}},
		{"空字符串保留", map[string]any{"a": ""}, map[string]any{"a": ""}},
		{"schema 未声明的字段保留", map[string]any{"x": "4"}, map[string]any{"x": "4"}},
		{"多个字段混合", map[string]any{"a": "4", "c": "true", "x": "保持"}, map[string]any{"a": float64(4), "c": true, "x": "保持"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceToolArguments(tt.args, schema)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("coerceToolArguments() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestCoerceToolArgumentsJSONStrings 覆盖 pi 兼容：模型把声明为 array/object 的
// 参数发成 JSON 字符串时（如 edits 参数），在 schema 明确且无损的前提下解析。
func TestCoerceToolArgumentsJSONStrings(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
		"edits":{"type":"array"},
		"config":{"type":"object"},
		"text":{"type":"string"}
	}}`)
	tests := []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{"JSON 数组字符串解析", map[string]any{"edits": `[{"oldText":"a","newText":"b"}]`},
			map[string]any{"edits": []any{map[string]any{"oldText": "a", "newText": "b"}}}},
		{"JSON 对象字符串解析", map[string]any{"config": `{"key":"value"}`},
			map[string]any{"config": map[string]any{"key": "value"}}},
		{"空数组字符串解析", map[string]any{"edits": `[]`}, map[string]any{"edits": []any{}}},
		{"类型不符保留：数组字段收到对象串", map[string]any{"edits": `{"a":1}`}, map[string]any{"edits": `{"a":1}`}},
		{"类型不符保留：对象字段收到数组串", map[string]any{"config": `[1,2]`}, map[string]any{"config": `[1,2]`}},
		{"非法 JSON 保留", map[string]any{"edits": `{oops`}, map[string]any{"edits": `{oops`}},
		{"声明 string 的字段不解析", map[string]any{"text": `{"a":1}`}, map[string]any{"text": `{"a":1}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceToolArguments(tt.args, schema)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("coerceToolArguments() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestCoerceToolArgumentsUnionTypes 覆盖 type 为联合数组时的歧义处理。
func TestCoerceToolArgumentsUnionTypes(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
		"intOrNull":{"type":["integer","null"]},
		"stringOrInt":{"type":["string","integer"]},
		"stringOrBool":{"type":["boolean","string"]},
		"numOrBool":{"type":["number","boolean"]},
		"boolOrNum":{"type":["boolean","number"]},
		"unknown":{"type":["frobnicate"]}
	}}`)
	tests := []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{"联合含整数则转换", map[string]any{"intOrNull": "4"}, map[string]any{"intOrNull": float64(4)}},
		{"联合含字符串则保留", map[string]any{"stringOrInt": "4"}, map[string]any{"stringOrInt": "4"}},
		{"字符串与布尔联合保留", map[string]any{"stringOrBool": "true"}, map[string]any{"stringOrBool": "true"}},
		{"数字与布尔联合按值选择布尔", map[string]any{"numOrBool": "true"}, map[string]any{"numOrBool": true}},
		{"数字与布尔联合按值选择数字", map[string]any{"boolOrNum": "4"}, map[string]any{"boolOrNum": float64(4)}},
		{"无法转换则保留", map[string]any{"intOrNull": "abc"}, map[string]any{"intOrNull": "abc"}},
		{"未知类型保留", map[string]any{"unknown": "4"}, map[string]any{"unknown": "4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceToolArguments(tt.args, schema)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("coerceToolArguments() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestCoerceToolArgumentsNested 验证对象与数组的递归强转，且不修改入参中的嵌套结构。
func TestCoerceToolArgumentsNested(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
		"obj":{"type":"object","properties":{"n":{"type":"integer"},"flag":{"type":"boolean"}}},
		"list":{"type":"array","items":{"type":"integer"}},
		"objs":{"type":"array","items":{"type":"object","properties":{"k":{"type":"number"}}}},
		"plain":{"type":"array","items":{"type":"string"}}
	}}`)
	inner := map[string]any{"n": "4", "flag": "true", "keep": "原样"}
	list := []any{"1", "2", "x"}
	objs := []any{map[string]any{"k": "3.5"}}
	plain := []any{"4", "5"}
	args := map[string]any{"obj": inner, "list": list, "objs": objs, "plain": plain}

	got := coerceToolArguments(args, schema)
	want := map[string]any{
		"obj":   map[string]any{"n": float64(4), "flag": true, "keep": "原样"},
		"list":  []any{float64(1), float64(2), "x"},
		"objs":  []any{map[string]any{"k": float64(3.5)}},
		"plain": []any{"4", "5"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coerceToolArguments() = %#v, want %#v", got, want)
	}

	// 入参及其嵌套值必须保持原样。
	if !reflect.DeepEqual(inner, map[string]any{"n": "4", "flag": "true", "keep": "原样"}) {
		t.Errorf("嵌套对象被修改：%#v", inner)
	}
	if !reflect.DeepEqual(list, []any{"1", "2", "x"}) {
		t.Errorf("数组被修改：%#v", list)
	}
	if !reflect.DeepEqual(objs, []any{map[string]any{"k": "3.5"}}) {
		t.Errorf("对象数组被修改：%#v", objs)
	}
}

// TestCoerceToolArgumentsDoesNotMutateInput 验证返回的是新 map，改动返回值不会影响入参。
func TestCoerceToolArgumentsDoesNotMutateInput(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"}}}`)
	args := map[string]any{"a": "4"}
	got := coerceToolArguments(args, schema)
	if args["a"] != "4" {
		t.Fatalf("入参被修改：a = %#v", args["a"])
	}
	got["a"] = "改写返回值"
	if args["a"] != "4" {
		t.Fatalf("改动返回值影响了入参：a = %#v", args["a"])
	}
}

// TestCoerceToolArgumentsInvalidSchema 验证 schema 无效时返回入参的拷贝而不是 panic 或原引用。
func TestCoerceToolArgumentsInvalidSchema(t *testing.T) {
	args := map[string]any{"a": "4"}
	schemas := []json.RawMessage{nil, json.RawMessage(`not json`), json.RawMessage(`[]`), json.RawMessage(`{"type":"object"}`)}
	for _, schema := range schemas {
		got := coerceToolArguments(args, schema)
		if !reflect.DeepEqual(got, args) {
			t.Errorf("schema=%s: got %#v, want %#v", schema, got, args)
		}
		got["a"] = "改写"
		if args["a"] != "4" {
			t.Fatalf("schema=%s: 返回值与入参共享引用", schema)
		}
	}
}

// TestCoerceToolArgumentsJSON 验证原始 JSON 字符串的转换：无变化字节不变，
// CJK 与 HTML 特殊字符不被转义。
func TestCoerceToolArgumentsJSON(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"c":{"type":"boolean"}}}`)
	t.Run("发生转换时输出合法 JSON", func(t *testing.T) {
		raw := `{"a":"4","c":"true","text":"中文 <tag>","keep":"4"}`
		got := coerceToolArgumentsJSON(raw, schema)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(got), &decoded); err != nil {
			t.Fatalf("输出不是合法 JSON：%s (%v)", got, err)
		}
		want := map[string]any{"a": float64(4), "c": true, "text": "中文 <tag>", "keep": "4"}
		if !reflect.DeepEqual(decoded, want) {
			t.Errorf("解码后 = %#v, want %#v", decoded, want)
		}
		if strings.Contains(got, `\u003c`) || !strings.Contains(got, "中文") {
			t.Errorf("输出错误地转义了非 ASCII 或 HTML 字符：%s", got)
		}
	})
	t.Run("无变化时字节不变", func(t *testing.T) {
		raw := `{"a":4,"c":true}`
		if got := coerceToolArgumentsJSON(raw, schema); got != raw {
			t.Errorf("got %q, want %q", got, raw)
		}
	})
	t.Run("无效 JSON 原样返回", func(t *testing.T) {
		raw := `{"a":`
		if got := coerceToolArgumentsJSON(raw, schema); got != raw {
			t.Errorf("got %q, want %q", got, raw)
		}
	})
}

// TestToolArgumentCoercionOptInWiring 验证接线默认是关闭的，且打开后只改变工具收到的参数。
func TestToolArgumentCoercionOptInWiring(t *testing.T) {
	var seen []string
	ft := &fakeTools{run: func(_ context.Context, _ string, args string) (string, error) {
		seen = append(seen, args)
		return "ok", nil
	}}
	s := newRunState(Options{Tools: ft}, "test", nil)
	var spec llm.ToolSpec
	spec.Function.Name = "read"
	spec.Function.Parameters = json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"},"outline":{"type":"boolean"}}}`)
	s.specs = []llm.ToolSpec{spec}
	raw := `{"limit":"4","outline":"true"}`
	calls := []llm.ToolCall{toolCall("c1", "read", raw)}

	SetToolArgumentCoercion(false)
	s.runTools(context.Background(), 0, calls)
	if len(seen) != 1 || seen[0] != raw {
		t.Fatalf("默认关闭时工具收到的参数 = %#v, want [%q]", seen, raw)
	}

	SetToolArgumentCoercion(true)
	defer SetToolArgumentCoercion(false)
	seen = nil
	s.runTools(context.Background(), 0, calls)
	if len(seen) != 1 {
		t.Fatalf("工具调用次数 = %d, want 1", len(seen))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(seen[0]), &decoded); err != nil {
		t.Fatalf("工具收到的参数不是合法 JSON：%s", seen[0])
	}
	want := map[string]any{"limit": float64(4), "outline": true}
	if !reflect.DeepEqual(decoded, want) {
		t.Errorf("工具收到的参数 = %#v, want %#v", decoded, want)
	}
}
