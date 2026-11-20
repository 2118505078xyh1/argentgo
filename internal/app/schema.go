package app

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// jsonSchema 是 --output-schema 所强制执行的实用子集：type、required、properties、enum 和 items。
// 未知关键字会被忽略而非拒绝，使真实世界的 schema 仍然可用。
type jsonSchema struct {
	Type       string                     `json:"type"`
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
	Enum       []json.RawMessage          `json:"enum"`
	Items      json.RawMessage            `json:"items"`
}

func decodeSchema(text string) (jsonSchema, error) {
	var schema jsonSchema
	if err := json.Unmarshal([]byte(text), &schema); err != nil {
		return schema, fmt.Errorf("invalid output schema: %w", err)
	}
	if schema.Type != "" && !schemaTypeValid(schema.Type) {
		return schema, fmt.Errorf("invalid output schema: unsupported type %q", schema.Type)
	}
	return schema, nil
}

func schemaTypeValid(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	}
	return false
}

// validateOutputText 根据 schema 检查最终的助手文本。接受单个围栏 JSON 块，但文档周围的正文不允许。
func validateOutputText(schemaText, text string) error {
	schema, err := decodeSchema(schemaText)
	if err != nil {
		return err
	}
	value, err := extractJSONDocument(text)
	if err != nil {
		return err
	}
	return validateSchemaValue(value, schema, "$")
}

func extractJSONDocument(text string) (any, error) {
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```") {
		if line := strings.IndexByte(body, '\n'); line >= 0 {
			body = body[line+1:]
		}
		if fence := strings.LastIndex(body, "```"); fence >= 0 {
			body = body[:fence]
		}
		body = strings.TrimSpace(body)
	}
	if body == "" {
		return nil, fmt.Errorf("the reply is empty, expected a JSON document")
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("the reply is not a JSON document: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("the reply has trailing content after the JSON document")
	}
	return value, nil
}

func validateSchemaValue(value any, schema jsonSchema, path string) error {
	if len(schema.Enum) > 0 {
		matched := false
		for _, option := range schema.Enum {
			if reflect.DeepEqual(looseJSON(value), looseJSON(decodeJSONValue(option))) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value %s is not one of the schema's enum values", path, shortJSON(value))
		}
	}
	if schema.Type != "" && !valueHasType(value, strings.ToLower(strings.TrimSpace(schema.Type))) {
		return fmt.Errorf("%s: expected %s, got %s", path, schema.Type, jsonKind(value))
	}
	switch strings.ToLower(strings.TrimSpace(schema.Type)) {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return nil // 上面的类型检查已经报告了这一点
		}
		for _, key := range schema.Required {
			if _, present := object[key]; !present {
				return fmt.Errorf("%s: required property %q is missing", path, key)
			}
		}
		for key, raw := range schema.Properties {
			child, present := object[key]
			if !present {
				continue
			}
			var nested jsonSchema
			if err := json.Unmarshal(raw, &nested); err != nil {
				return fmt.Errorf("%s.%s: invalid property schema: %w", path, key, err)
			}
			if err := validateSchemaValue(child, nested, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok || len(schema.Items) == 0 || string(schema.Items) == "null" {
			return nil
		}
		var nested jsonSchema
		if err := json.Unmarshal(schema.Items, &nested); err != nil {
			return fmt.Errorf("%s[]: invalid items schema: %w", path, err)
		}
		for i, item := range items {
			if err := validateSchemaValue(item, nested, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func valueHasType(value any, kind string) bool {
	switch kind {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number", "integer":
		switch number := value.(type) {
		case json.Number:
			if kind == "number" {
				return true
			}
			text := number.String()
			return !strings.ContainsAny(text, ".eE")
		case float64, int, int64:
			return true
		}
	}
	return false
}

func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, int, int64:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// decodeJSONValue 解析一个原始 JSON 值，在 looseJSON 规范化之前保持数字的精确性。
func decodeJSONValue(raw []byte) any {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return value
}

// looseJSON 在比较之前规范化数字，使 1 和 1.0 能匹配同一个枚举值。
func looseJSON(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = looseJSON(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = looseJSON(child)
		}
		return out
	default:
		return value
	}
}

func shortJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	text := string(encoded)
	if len(text) > 80 {
		return text[:77] + "..."
	}
	return text
}

// correctiveInstruction 把验证失败转换为一条系统消息，下一轮次可以据此行动，
// 而无需改变工具集或会话历史。
func correctiveInstruction(schema string, err error) string {
	var b strings.Builder
	b.WriteString("\nThe previous reply did not satisfy the required output schema: ")
	b.WriteString(err.Error())
	b.WriteString("\nReply once more with a single JSON document that conforms exactly to this JSON Schema, ")
	b.WriteString("with no prose, no markdown fences and no extra commentary:\n")
	b.WriteString(strings.TrimSpace(schema))
	b.WriteString("\n")
	return b.String()
}
