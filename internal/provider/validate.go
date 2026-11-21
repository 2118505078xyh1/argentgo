// Package provider 模板验证。
//
// 本文件把以前分散在测试中的结构假设（非空字段、可解析的端点 URL、已知协议）变成
// 一个可复用、无副作用的诊断 API。CLI 的 `doctor` 路径或配置加载器可以调用 Validate/ValidateCatalog/ValidateAll 并显示
// 配置加载器可以调用 Validate/ValidateCatalog/ValidateAll 并显示
// 发现的问题，而无需 provider 包导入 config 或网络。
//
// 这里没有任何内容执行 I/O 或查询环境：验证只看 Template 的形状。发现的问题被返回，绝不记录日志或 panic。
// 只看 Template 的形状。发现的问题被返回，绝不记录日志或 panic。
package provider

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Severity 分类验证发现。
type Severity string

// 支持的严重性。错误使模板不可用；警告描述
// 可疑但可能是有意的配置。
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Diagnostic 是关于模板的一条发现。Template 是产生该发现的规范名称
// 支持的严重性。错误使模板不可用；警告描述可疑但可能是有意的配置。
// Diagnostic 是关于模板的一条发现。Template 是产生该发现的规范名称（名称本身损坏时为空），Field 是 Template 字段（对跨模板发现为 "catalog"），Message 是易读说明。
// Message 是易读说明。
type Diagnostic struct {
	Severity Severity
	Template string
	Field    string
	Message  string
}

// String 渲染诊断以供直接显示："[severity] template/field: message"。空的 Template/Field 部分被省略。
// message"。空的 Template/Field 部分被省略。
func (d Diagnostic) String() string {
	where := d.Field
	if d.Template != "" {
		where = d.Template
		if d.Field != "" {
			where += "/" + d.Field
		}
	}
	if where == "" {
		return fmt.Sprintf("[%s] %s", d.Severity, d.Message)
	}
	return fmt.Sprintf("[%s] %s: %s", d.Severity, where, d.Message)
}

// HasErrors 报告是否有任何发现是错误（而非警告）。
func HasErrors(diagnostics []Diagnostic) bool {
	for _, d := range diagnostics {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

var (
	canonicalNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	envNamePattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Validate 检查单个模板的结构问题。它按稳定顺序返回发现（
// Validate 检查单个模板的结构问题。它按稳定顺序返回发现（Name、Protocol、BaseURL、DefaultModel、凭据、协议一致性），从不修改 t，且对健全模板返回空切片。空切片始终非 nil，使调用方可自由追加。
// 协议一致性），从不修改 t，且对健全模板返回空切片。
// 空切片始终非 nil，使调用方可自由追加。
func Validate(t Template) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	add := func(severity Severity, field, format string, args ...any) {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: severity,
			Template: t.Name,
			Field:    field,
			Message:  fmt.Sprintf(format, args...),
		})
	}

	switch {
	case strings.TrimSpace(t.Name) == "":
		add(SeverityError, "Name", "missing template name")
	case t.Name != strings.TrimSpace(t.Name):
		add(SeverityWarning, "Name", "name %q has surrounding whitespace", t.Name)
	case !canonicalNamePattern.MatchString(t.Name):
		add(SeverityError, "Name", "name %q is not a valid canonical id", t.Name)
	case t.Name != strings.ToLower(t.Name):
		add(SeverityWarning, "Name", "name %q is not lowercase; Resolve is case-insensitive", t.Name)
	}

	switch t.Protocol {
	case ProtocolAnthropic, ProtocolOpenAI, ProtocolResponses:
	default:
		add(SeverityError, "Protocol", "unsupported protocol %q", t.Protocol)
	}

	if strings.TrimSpace(t.BaseURL) == "" {
		add(SeverityError, "BaseURL", "missing endpoint URL")
	} else {
		validateBaseURL(t.BaseURL, add)
	}

	if strings.TrimSpace(t.DefaultModel) == "" {
		add(SeverityError, "DefaultModel", "missing default model")
	}

	validateCredentials(t, add)
	validateProtocolURL(t, add)
	return diagnostics
}

// validateBaseURL 报告畸形的端点 URL。只接受 llm 客户端可用的绝对 http(s) URL；base URL 中的凭据、查询串和片段几乎总是复制/粘贴错误。
// llm 客户端可用的绝对 http(s) URL；base URL 中的凭据、查询串和
// 片段几乎总是复制/粘贴错误。
func validateBaseURL(raw string, add func(Severity, string, string, ...any)) {
	parsed, err := url.Parse(raw)
	if err != nil {
		add(SeverityError, "BaseURL", "cannot parse endpoint URL %q: %v", raw, err)
		return
	}
	lowerScheme := strings.ToLower(parsed.Scheme)
	if lowerScheme != "http" && lowerScheme != "https" {
		add(SeverityError, "BaseURL", "endpoint URL %q must use http or https, got %q", raw, parsed.Scheme)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		add(SeverityError, "BaseURL", "endpoint URL %q has no host", raw)
	}
	if parsed.User != nil {
		add(SeverityError, "BaseURL", "endpoint URL %q embeds credentials; use APIKeyEnv instead", raw)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		add(SeverityError, "BaseURL", "endpoint URL %q must not contain a query or fragment", raw)
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == ".." {
			add(SeverityError, "BaseURL", "endpoint URL %q contains a '..' path segment", raw)
			break
		}
	}
	if strings.HasSuffix(parsed.Path, "/") && parsed.Path != "/" {
		add(SeverityWarning, "BaseURL", "endpoint URL %q ends in '/'; llm clients append their own path", raw)
	}
}

// validateCredentials 检查凭据
// validateCredentials 检查凭据来源的形态和相互一致性。loopback 端点缺少 key 没问题；远端端点同样的缺失则足以警告。
// 远端端点同样的缺失则足以警告。
func validateCredentials(t Template, add func(Severity, string, string, ...any)) {
	if t.NoAuth && (t.APIKeyEnv != "" || len(t.APIKeyEnvs) > 0) {
		add(SeverityWarning, "NoAuth", "NoAuth overrides declared credential sources; remove them")
	}

	names := make([]string, 0, len(t.APIKeyEnvs)+1)
	if t.APIKeyEnv != "" {
		names = append(names, t.APIKeyEnv)
	}
	names = append(names, t.APIKeyEnvs...)

	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !envNamePattern.MatchString(name) {
			add(SeverityError, "APIKeyEnv", "%q is not a valid environment variable name", name)
		}
		if _, duplicate := seen[name]; duplicate {
			add(SeverityWarning, "APIKeyEnvs", "credential source %q is declared more than once", name)
		}
		seen[name] = struct{}{}
	}

	if t.NoAuth || len(names) != 0 || strings.TrimSpace(t.BaseURL) == "" {
		return
	}
	parsed, err := url.Parse(t.BaseURL)
	if err != nil {
		return // already reported by validateBaseURL
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return // local servers are normally unauthenticated
	default:
		add(SeverityWarning, "APIKeyEnv", "remote endpoint %q declares no credential source", t.BaseURL)
	}
}

// validateProtocolURL 应用一条刻意保守的一致性规则：
// validateProtocolURL 应用一条刻意保守的一致性规则：base URL 的最后一段路径应匹配该协议通常的 API 根。它警告而非报错，因为供应商有时暴露非标准路径；不匹配通常意味着为目标选错了协议。
// 它警告而非报错，因为供应商有时暴露非标准
// 路径；不匹配通常意味着为目标选错了协议。
func validateProtocolURL(t Template, add func(Severity, string, string, ...any)) {
	var expected []string
	switch t.Protocol {
	case ProtocolOpenAI, ProtocolResponses:
		expected = []string{"v1"}
	case ProtocolAnthropic:
		expected = []string{"v1", "anthropic"}
	default:
		return // unknown protocol already reported by Validate
	}
	parsed, err := url.Parse(t.BaseURL)
	if err != nil {
		return // already reported by validateBaseURL
	}
	last := lastPathSegment(parsed.Path)
	for _, hint := range expected {
		if strings.EqualFold(last, hint) {
			return
		}
	}
	add(SeverityWarning, "BaseURL",
		"base URL path ending in %q is inconsistent with protocol %q (expected %s)",
		last, t.Protocol, strings.Join(expected, " or "))
}

// lastPathSegment 返回 URL 路径中最后一个非空段，忽略首尾斜杠。空路径产生空字符串。
// 忽略首尾斜杠。空路径产生空字符串。
func lastPathSegment(path string) string {
	trimmed := strings.Trim(path, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// ValidateCatalog 检查一组模板并添加跨模板发现，
// ValidateCatalog 检查一组模板并添加 Validate 单独看不到的跨模板发现，目前是重复的规范名称。输入切片不被修改且发现遵循输入顺序。
// 输入切片不被修改且发现遵循输入顺序。
func ValidateCatalog(templates []Template) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	seen := make(map[string]struct{}, len(templates))
	for _, template := range templates {
		diagnostics = append(diagnostics, Validate(template)...)
		if template.Name == "" {
			continue
		}
		if _, duplicate := seen[template.Name]; duplicate {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: SeverityError,
				Template: template.Name,
				Field:    "Name",
				Message:  fmt.Sprintf("duplicate template name %q", template.Name),
			})
		}
		seen[template.Name] = struct{}{}
	}
	return diagnostics
}

// ValidateAll 连同别名映射一起验证内置模板表：
// 映射：每个模板、唯一名称，以及解析到真实
// ValidateAll 连同别名映射一起验证内置模板表：每个模板、唯一名称，以及解析到真实规范名称而不遮蔽它的别名。别名发现使用 Field "aliases" 和稳定的（排序）顺序，使输出可复现。
// 稳定的（排序）顺序，使输出可复现。
func ValidateAll() []Diagnostic {
	all := List()
	diagnostics := ValidateCatalog(all)

	names := make(map[string]struct{}, len(all))
	for _, template := range all {
		names[template.Name] = struct{}{}
	}

	keys := make([]string, 0, len(aliases))
	for key := range aliases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := aliases[key]
		if key != strings.ToLower(strings.TrimSpace(key)) {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: SeverityError,
				Field:    "aliases",
				Message:  fmt.Sprintf("alias %q will never match Resolve's normalized lookup", key),
			})
		}
		if _, shadows := names[key]; shadows {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: SeverityError,
				Field:    "aliases",
				Message:  fmt.Sprintf("alias %q shadows a canonical template name", key),
			})
		}
		if _, ok := names[target]; !ok {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: SeverityError,
				Field:    "aliases",
				Message:  fmt.Sprintf("alias %q points to unknown template %q", key, target),
			})
		}
	}
	return diagnostics
}
