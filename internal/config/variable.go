package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// WarnFunc 报告非致命问题（缺失的环境变量或
// 不可读的文件引用）。nil WarnFunc 会丢弃警告。
type WarnFunc func(format string, args ...any)

var (
	envRefRe  = regexp.MustCompile(`\{env:([^}]+)\}`)
	fileRefRe = regexp.MustCompile(`\{file:[^}]+\}`)
)

// Substitute 把 {env:VAR} 和 {file:path} 引用应用到原始配置文本。
// 相对的 {file:} 路径基于 configDir（配置文件所在目录）
// 解析。缺失的引用替换为空字符串并调用 warn。
//
// 写在 JSONC 注释中的引用（包括 // 和 /* */）保持
// 不变：它们是文档，而非设置。除了避免虚假的
// "变量未设置"警告，这对正确性也很重要——替换进来的
// env/file 值若含 "*/"，否则会提前闭合块注释，
// 破坏 JSONC 文档的其余部分。
//
// 环境变量值默认原样替换，
// 使当前能解析的配置保持原义。当原样替换的结果不是合法 JSON
// （含引号或控制字节），或 env 值含有能在*语法上*保持合法、
// 却悄悄改变取值的字符时，会用 JSON 字符串转义重试：
// 例如反斜杠（C:\temp 中的 "\t" 会解码为制表符），或插入 JSON 字符串
// 内部的双引号。引号可能提前闭合字符串并注入额外的 JSON 结构——
// 例如 apiKey 为 `a", "role": "admin` 时，会在文档仍是合法 JSON
// 的情况下被解码为 "a" 加上恶意的 "role" 键。
// 字符串之外的引用有意保持原样：
// 配置正是通过它从环境注入原始 JSON
// （数字、数组或对象）。
func Substitute(text, configDir string, env func(string) string, warn WarnFunc) string {
	if env == nil {
		env = func(string) string { return "" }
	}
	if warn == nil {
		warn = func(string, ...any) {}
	}

	rawEnvText, needsEscape := replaceEnv(text, env, false, warn)
	raw := replaceFiles(rawEnvText, configDir, env, warn)
	if !strings.Contains(text, "{env:") || (isJSONConfig(raw) && !needsEscape) {
		return raw
	}
	// 上面的原样替换已经对每个缺失的引用发出过警告。
	// 重试会访问同样的引用，若再次传入 warn 就会把
	// 每一个都报告两次（反斜杠或字符串内的引号会触发重试）。
	// 只做转义、不重复警告的替换，既保持"每事件一次警告"的
	// 约定，又能产出保留原值的文档。
	quiet := func(string, ...any) {}
	escapedEnvText, _ := replaceEnv(text, env, true, quiet)
	return replaceFiles(escapedEnvText, configDir, env, quiet)
}

// replaceEnv 替换 {env:VAR} 引用。第二个返回值报告
// 是否有被替换的（非注释）值需要 JSON 字符串转义以
// 保留其含义（任意位置的反斜杠，或字符串内的引号），
// 调用方据此决定是否需要带转义重试。
func replaceEnv(text string, env func(string) string, escape bool, warn WarnFunc) (string, bool) {
	var out strings.Builder
	out.Grow(len(text))
	cursor := 0
	needsEscape := false
	for _, loc := range envRefRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		out.WriteString(text[cursor:start])
		cursor = end
		token := text[start:end]
		if inJSONCComment(text, start) {
			out.WriteString(token) // a comment is documentation, not a setting
			continue
		}
		name := strings.TrimPrefix(strings.TrimSuffix(token, "}"), "{env:")
		value := env(name)
		if value == "" {
			warn("config: environment variable %q referenced in config is not set", name)
		}
		// 反斜杠能把合法的 JSON 转义变成不同的值
		// （C:\temp -> C:<TAB>），JSON 字符串内的引号能提前闭合该
		// 字符串并注入结构。纯"结果是否是合法 JSON？"的检查对此
		// 视而不见，因此它们会触发转义重试。
		// 字符串之外的引号保持不动：在那里注入原始 JSON 是
		// 有意为之。
		if strings.ContainsRune(value, '\\') ||
			(strings.ContainsRune(value, '"') && inJSONCString(text, start)) {
			needsEscape = true
		}
		if escape && inJSONCString(text, start) {
			// 只有落在 JSON 字符串内的引用才被转义。作为原始 JSON
			// 使用的引用（数字、数组或对象）保持
			// 原样，否则由无关字符串值触发的转义重试会破坏它。
			// （承接上文）否则由无关字符串值触发的转义重试会破坏它。
			out.WriteString(jsonStringBody(value))
		} else {
			out.WriteString(value)
		}
	}
	out.WriteString(text[cursor:])
	return out.String(), needsEscape
}

func replaceFiles(text, configDir string, env func(string) string, warn WarnFunc) string {
	var out strings.Builder
	cursor := 0
	for _, loc := range fileRefRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		out.WriteString(text[cursor:start])
		cursor = end
		token := text[start:end]

		// 写在 // 或 /* */ 注释中的引用不是真正的
		// 引用；保持其原样也能避免当被引用文件含 "*/" 时，其值
		// 从块注释中逃逸出来。
		if inJSONCComment(text, start) {
			out.WriteString(token)
			continue
		}

		resolved := resolveFileRef(strings.TrimPrefix(strings.TrimSuffix(token, "}"), "{file:"), configDir, env)
		data, err := os.ReadFile(resolved)
		if err != nil {
			warn("config: file reference %q (%s) could not be read: %v", token, resolved, err)
			continue
		}
		// 文件内容总是被转义：密钥可能含引号、花括号
		// 或换行，这些都不能破坏外围的 JSON 字符串。
		out.WriteString(jsonStringBody(strings.TrimSpace(string(data))))
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// inJSONCComment 报告 offset 处的字节是否位于 // 行注释
// 或 /* */ 块注释内。它模仿 StripJSONC 的扫描器：
// 带引号的字符串（包括转义的引号与反斜杠）是透明的，
// 因此写在 JSON 字符串值里的 "{file:...}"
// 永远不会被误判为被注释掉的引用。
func inJSONCComment(text string, offset int) bool {
	if offset > len(text) {
		offset = len(text)
	}
	inString, escaped, line, block := false, false, false, false
	for i := 0; i < offset; i++ {
		char := text[i]
		if line {
			if char == '\n' {
				line = false
			}
			continue
		}
		if block {
			if char == '*' && i+1 < len(text) && text[i+1] == '/' {
				block = false
				i++
			}
			continue
		}
		if inString {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch {
		case char == '"':
			inString = true
		case char == '/' && i+1 < len(text) && text[i+1] == '/':
			line = true
			i++
		case char == '/' && i+1 < len(text) && text[i+1] == '*':
			block = true
			i++
		}
	}
	return line || block
}

// inJSONCString 报告 offset 处的字节是否位于带引号的 JSON
// 字符串内。它模仿 inJSONCComment 的扫描器，使 // 或 /* */ 注释里的引号
// 不会开启字符串，字符串内转义的引号也不会
// 闭合它。Substitute 用它判断 env 值中的双引号
// 是需要 JSON 字符串转义，还是有意的原始 JSON 注入。
func inJSONCString(text string, offset int) bool {
	if offset > len(text) {
		offset = len(text)
	}
	inString, escaped, line, block := false, false, false, false
	for i := 0; i < offset; i++ {
		char := text[i]
		if line {
			if char == '\n' {
				line = false
			}
			continue
		}
		if block {
			if char == '*' && i+1 < len(text) && text[i+1] == '/' {
				block = false
				i++
			}
			continue
		}
		if inString {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch {
		case char == '"':
			inString = true
		case char == '/' && i+1 < len(text) && text[i+1] == '/':
			line = true
			i++
		case char == '/' && i+1 < len(text) && text[i+1] == '*':
			block = true
			i++
		}
	}
	return inString
}

func resolveFileRef(ref, configDir string, env func(string) string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "~/") || strings.HasPrefix(ref, `~\`) {
		ref = filepath.Join(homeDir(env), ref[2:])
	}
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref)
	}
	if strings.TrimSpace(configDir) == "" {
		configDir = "."
	}
	return filepath.Join(configDir, ref)
}

// jsonStringBody 返回转义后的 value，用于放入 JSON 字符串内部，
// 不含外围双引号（相当于参考实现中的
// JSON.stringify(value).slice(1, -1)）。
func jsonStringBody(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	text := string(encoded)
	if len(text) < 2 {
		return ""
	}
	return text[1 : len(text)-1]
}

func isJSONConfig(text string) bool {
	return json.Valid([]byte(StripJSONC(text)))
}
