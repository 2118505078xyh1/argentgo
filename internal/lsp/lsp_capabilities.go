package lsp

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Capability 是服务器声明支持的一项 LSP 能力。
// 使用具名字符串类型而非裸 string，便于在编译期约束可用的能力集合。
type Capability string

// 能力常量与本包实际提供的 IDE 功能一一对应。
// 字符串值采用稳定的小写标识符，既用于 Supports 的精确匹配，也用于 MergeCapabilities 的排序。
const (
	CapabilityHover          Capability = "hover"
	CapabilityDefinition     Capability = "definition"
	CapabilityDiagnostics    Capability = "diagnostics"
	CapabilityFormat         Capability = "format"
	CapabilityRename         Capability = "rename"
	CapabilityReferences     Capability = "references"
	CapabilitySymbols        Capability = "symbols"
	CapabilityCodeActions    Capability = "code_actions"
	CapabilitySemanticTokens Capability = "semantic_tokens"
	CapabilityInlayHints     Capability = "inlay_hints"
	CapabilityCodeLens       Capability = "code_lens"
)

// ServerCapabilities 是一台服务器能力的声明式描述。
// Name 是服务器名称，Caps 是支持的能力集合，Order 与注册顺序语义对齐。
type ServerCapabilities struct {
	Name  string
	Caps  []Capability
	Order int
}

// 能力校验相关的哨兵错误，调用方可用 errors.Is 判断。
var (
	// ErrEmptyCapabilitiesName 表示声明的名称为空（仅含空白亦视为空）。
	ErrEmptyCapabilitiesName = errors.New("lsp: empty capabilities name")
	// ErrDuplicateCapability 表示同一声明中出现重复的能力。
	ErrDuplicateCapability = errors.New("lsp: duplicate capability")
)

// Supports 报告声明中是否包含指定能力。
// 精确匹配，空能力列表返回 false。
func Supports(c ServerCapabilities, cap Capability) bool {
	for _, item := range c.Caps {
		if item == cap {
			return true
		}
	}
	return false
}

// MergeCapabilities 返回 a 与 b 的能力并集，去重后按能力字符串值升序排列。
// 结果是一份全新的切片，不会修改任一入参。
// Name 取 a 的非空名称，否则退化为 b 的名称；Order 随所用名称一并返回。
func MergeCapabilities(a, b ServerCapabilities) ServerCapabilities {
	merged := ServerCapabilities{Name: a.Name, Order: a.Order}
	if strings.TrimSpace(a.Name) == "" {
		merged.Name = b.Name
		merged.Order = b.Order
	}

	seen := make(map[Capability]struct{}, len(a.Caps)+len(b.Caps))
	var caps []Capability
	if total := len(a.Caps) + len(b.Caps); total > 0 {
		caps = make([]Capability, 0, total)
	}
	for _, source := range [][]Capability{a.Caps, b.Caps} {
		for _, cap := range source {
			if _, exists := seen[cap]; exists {
				continue
			}
			seen[cap] = struct{}{}
			caps = append(caps, cap)
		}
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i] < caps[j] })
	merged.Caps = caps
	return merged
}

// ValidateCapabilities 校验声明的合法性：名称不得为空，能力不得重复。
// 空名返回 ErrEmptyCapabilitiesName，重复能力返回 ErrDuplicateCapability，
// 两者都用 %w 包装以便调用方用 errors.Is 判断。
func ValidateCapabilities(c ServerCapabilities) error {
	if strings.TrimSpace(c.Name) == "" {
		return ErrEmptyCapabilitiesName
	}
	seen := make(map[Capability]struct{}, len(c.Caps))
	for _, cap := range c.Caps {
		if _, exists := seen[cap]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateCapability, cap)
		}
		seen[cap] = struct{}{}
	}
	return nil
}
