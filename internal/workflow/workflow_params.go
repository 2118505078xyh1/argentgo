package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 限制镜像 goose 的配方参数上限。它们使工作流保持足够小以便共享，并阻止畸形声明在验证时耗尽内存。
// 以便共享，并阻止畸形声明在验证时耗尽
// 内存。
const (
	maxWorkflowParams        = 32
	maxTotalSelectOptions    = 200
	maxSerializedParamsBytes = 128 << 10 // 128 KiB
	maxParamValueBytes       = 128 << 10 // 128 KiB
)

// nextPlaceholder 返回 text 中第一个 {{...}} 标记。start 是
// "{{" 的索引，tokenEnd 是刚好越过 "}}" 的索引，name 是裁剪后的内部
// nextPlaceholder 返回 text 中第一个 {{...}} 标记。start 是 "{{" 的索引，tokenEnd 是刚好越过 "}}" 的索引，name 是裁剪后的内部文本。当 text 没有完整标记时 ok 为 false。这是校验用的 ExtractPlaceholders 和渲染用的 Substitute 共享的占位符语法的唯一真相来源。
// 占位符语法的唯一真相来源，由 ExtractPlaceholders（验证）
// 和 Substitute（渲染）共享。
func nextPlaceholder(text string) (start, tokenEnd int, name string, ok bool) {
	start = strings.Index(text, "{{")
	if start < 0 {
		return 0, 0, "", false
	}
	rel := strings.Index(text[start:], "}}")
	if rel < 0 {
		return 0, 0, "", false
	}
	closeAt := start + rel
	return start, closeAt + 2, strings.TrimSpace(text[start+2 : closeAt]), true
}

// ExtractPlaceholders 返回每个 {{...}} 标记内去重并排序后的
// ExtractPlaceholders 返回 s 中每个 {{...}} 标记内去重并排序后的名称。未终止和空的标记被忽略。
func ExtractPlaceholders(s string) []string {
	seen := make(map[string]bool)
	var names []string
	rest := s
	for {
		_, tokenEnd, name, ok := nextPlaceholder(rest)
		if !ok {
			break
		}
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		rest = rest[tokenEnd:]
	}
	sort.Strings(names)
	return names
}

// isBuiltinPlaceholder 报告 name 是否为 runner 的保留占位符而非声明的参数。
// 占位符而非声明的参数。
func isBuiltinPlaceholder(name string) bool {
	return name == "input" || strings.HasPrefix(name, "steps.")
}

// ValidateParameters 检查参数声明，并在工作流声明参数时强制声明与提示和工具参数中使用的占位符双向一致。
// 声明参数时强制声明与占位符双向一致
// 提示与工具参数中使用的占位符之间的一致性。
//
// 每个错误都点名一个字段/键和规则；声明的值（默认值和
// 选项）从不被包含，使工作流可被共享且验证错误可粘贴到 issue
// 每个错误都点名一个字段/键和规则；声明的值（默认值和选项）从不被包含，使工作流可被共享且验证错误可粘贴到 issue 而不泄漏秘密。
func ValidateParameters(def Definition) error {
	if len(def.Parameters) == 0 {
		// 旧工作流没有参数：其模板只使用
		// 旧工作流没有参数：其模板只使用内置 {{input}} / {{steps...}} 占位符（且可能有意把其他标记留给模型），因此不适用新检查。
		// 把其他标记留给模型），因此不适用新检查。
		return nil
	}
	if len(def.Parameters) > maxWorkflowParams {
		return fmt.Errorf("workflow: too many parameters (%d, max %d)", len(def.Parameters), maxWorkflowParams)
	}
	// 只有序列化大小与上限有关；绝不回显内容。
	encoded, err := json.Marshal(def.Parameters)
	if err != nil {
		return fmt.Errorf("workflow: parameters[%d].default cannot be serialized", len(def.Parameters)-1)
	}
	if len(encoded) > maxSerializedParamsBytes {
		return fmt.Errorf("workflow: parameters exceed %d bytes when serialized", maxSerializedParamsBytes)
	}

	declared := make(map[string]bool, len(def.Parameters))
	optionCount := 0
	for i, p := range def.Parameters {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			return fmt.Errorf("workflow: parameters[%d].key is required", i)
		}
		if declared[key] {
			return fmt.Errorf("workflow: parameters[%d].key %q is duplicated", i, key)
		}
		declared[key] = true
		if isBuiltinPlaceholder(key) {
			return fmt.Errorf("workflow: parameters[%d].key %q is reserved", i, key)
		}
		switch p.InputType {
		case ParamTypeString, ParamTypeNumber, ParamTypeBoolean, ParamTypeDate, ParamTypeFile, ParamTypeSelect:
		default:
			return fmt.Errorf("workflow: parameters[%d].input_type is invalid (use %s, %s, %s, %s, %s or %s)",
				i, ParamTypeString, ParamTypeNumber, ParamTypeBoolean, ParamTypeDate, ParamTypeFile, ParamTypeSelect)
		}
		switch p.Requirement {
		case ParamRequired, ParamOptional, ParamUserPrompt:
		default:
			return fmt.Errorf("workflow: parameters[%d].requirement is invalid (use %s, %s or %s)",
				i, ParamRequired, ParamOptional, ParamUserPrompt)
		}
		hasDefault := p.Default != nil
		switch {
		case p.InputType == ParamTypeSelect && len(p.Options) == 0:
			return fmt.Errorf("workflow: parameters[%d] (%s) needs options for a select input", i, key)
		case p.InputType == ParamTypeFile && hasDefault:
			return fmt.Errorf("workflow: parameters[%d] (%s) file input cannot have a default", i, key)
		case p.Requirement == ParamRequired && hasDefault:
			return fmt.Errorf("workflow: parameters[%d] (%s) required input cannot have a default", i, key)
		}
		if p.InputType == ParamTypeSelect {
			optionCount += len(p.Options)
		}
	}
	if optionCount > maxTotalSelectOptions {
		return fmt.Errorf("workflow: select options exceed %d in total", maxTotalSelectOptions)
	}

	// 提示或参数中的每个非内置占位符都必须被声明。
	used := make(map[string]bool, len(def.Parameters))
	for _, step := range def.Steps {
		for _, field := range []string{step.Prompt, step.Args} {
			for _, name := range ExtractPlaceholders(field) {
				if isBuiltinPlaceholder(name) {
					continue
				}
				used[name] = true
				if !declared[name] {
					return fmt.Errorf("workflow: placeholder %q has no parameter declaration", name)
				}
			}
		}
	}
	// 而每个声明都必须被实际引用。
	for _, p := range def.Parameters {
		key := strings.TrimSpace(p.Key)
		if !used[key] {
			return fmt.Errorf("workflow: parameter %q is declared but never used in a prompt or args", key)
		}
	}
	return nil
}

// dateLayout 是日期输入默认值唯一被接受的形式。保持单一布局使声明检查确定，并给作者一个具体格式目标。
// 单一布局使声明检查确定，并给作者一个
// 具体格式目标。
const dateLayout = "2006-01-02"

// ValidateParamDefaults 检查每个声明的默认值是否与
// ValidateParamDefaults 检查每个声明的默认值与其 input_type 一致（对 select 还要与其选项一致）。它是在验证时运行的声明检查，使畸形默认值无法到达 ResolveParameters，在那里它会被原样字符串化进提示或工具参数。
// 验证时运行，使畸形默认值无法到达
// ResolveParameters，在那里它会被原样字符串化进提示或
// 工具参数。
//
// 这补充了 ValidateParameters：后者检查声明的
// 形状（键、类型、必需性、上限、使用），而本函数检查
// 默认值是否符合声明类型。错误点名参数
// 和期望类型；值从不回显，使工作流可被共享
// 这补充了 ValidateParameters：后者检查声明的形状（键、类型、必需性、上限、使用），而本函数按声明类型检查默认值。错误点名参数和期望类型；值从不回显，使工作流可被共享且验证错误可粘贴到 issue 而不泄漏秘密。
func ValidateParamDefaults(def Definition) error {
	for i, p := range def.Parameters {
		if p.Default == nil {
			// 没有默认值意味着值在运行时来自调用方；
			// 这里没有可检查的类型。
			continue
		}
		if !defaultMatchesType(p) {
			return fmt.Errorf("workflow: parameters[%d] (%s): default does not match %s input",
				i, strings.TrimSpace(p.Key), p.InputType)
		}
	}
	return nil
}

// 没有默认值意味着值在运行时来自调用方；这里没有可检查的类型。
// defaultMatchesType 报告声明的默认值是否为其 input_type 的良类型值。接受的 Go 类型镜像 stringifyDefault，使通过验证的值总能被解析器渲染。
// 通过验证的值总能被解析器渲染。
func defaultMatchesType(p WorkflowParam) bool {
	switch p.InputType {
	case ParamTypeString:
		_, ok := p.Default.(string)
		return ok
	case ParamTypeNumber:
		switch p.Default.(type) {
		case float64, float32, int, int32, int64, json.Number:
			return true
		default:
			return false
		}
	case ParamTypeBoolean:
		_, ok := p.Default.(bool)
		return ok
	case ParamTypeDate:
		s, ok := p.Default.(string)
		return ok && isDateValue(s)
	case ParamTypeSelect:
		s, ok := p.Default.(string)
		if !ok {
			return false
		}
		for _, opt := range p.Options {
			if s == opt {
				return true
			}
		}
		return false
	case ParamTypeFile:
		// 文件输入没有默认值；ValidateParameters 会拒绝该声明本身，因此这里的默认值永远不是良类型。
		// 声明本身，因此这里的默认值永远不是良类型。
		return false
	default:
		// 未知的 input_type 会先被 ValidateParameters 拒绝。
		return false
	}
}

// isDateValue 报告 s 是否能按声明的日期布局解析。
func isDateValue(s string) bool {
	_, err := time.Parse(dateLayout, s)
	return err == nil
}

// ResolveParameters 从提供的输入或其声明默认值中为每个参数填充值。值被强制转为字符串并限制大小。
// 默认值。值被强制转为字符串并限制大小。
// 错误是秘密安全的：它们点名参数和规则，绝不点名值。
func ResolveParameters(def Definition, provided map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(def.Parameters))
	for _, p := range def.Parameters {
		key := strings.TrimSpace(p.Key)
		if value, ok := provided[key]; ok && value != "" {
			if len(value) > maxParamValueBytes {
				return nil, fmt.Errorf("workflow: parameter %q value exceeds %d bytes", key, maxParamValueBytes)
			}
			resolved[key] = value
			continue
		}
		if value, ok := stringifyDefault(p.Default); ok {
			if len(value) > maxParamValueBytes {
				return nil, fmt.Errorf("workflow: parameter %q value exceeds %d bytes", key, maxParamValueBytes)
			}
			resolved[key] = value
			continue
		}
		if p.Requirement == ParamRequired {
			return nil, fmt.Errorf("workflow: parameter %q is required but no value was provided", key)
		}
		// 可选和 user_prompt 参数没有值时直接缺席。
	}
	return resolved, nil
}

// stringifyDefault 将声明的默认值强制转为模板字符串。一个
// stringifyDefault 将声明的默认值强制转为模板字符串。nil 默认值表示"无值"。
func stringifyDefault(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case json.Number:
		return x.String(), true
	default:
		return fmt.Sprint(x), true
	}
}
