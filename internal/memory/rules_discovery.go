// Package memory —— 规则文件发现原语（作用域 × 模式 × 按 basename 去重）。
//
// 本文件只提供纯发现能力：扫描用户级（home）与项目级（project）的规则目录、
// 按 basename 去重、按模式过滤、标记 legacy 路径并给出迁移提示。
// 它不接线到任何加载器或注入路径，也不产生任何磁盘写入。
//
// 去重优先级刻意沿用本包既有的注入语义（blockHeader / LoadIn 的 "nearer wins"）：
// 离工作目录最近、最具体的规则胜出，因此 project > global > legacy。
// 同一作用域内，通用规则（Mode 为空）先于模式专属规则占用 basename。
package memory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RuleScope 是规则文件的作用域层级。
type RuleScope string

const (
	RuleScopeGlobal  RuleScope = "global"  // 用户主目录下的全局规则
	RuleScopeProject RuleScope = "project" // 项目目录内的项目规则
	RuleScopeLegacy  RuleScope = "legacy"  // 已弃用的旧位置，仅作发现与提示
)

// RuleFile 是一条被发现的规则文件元数据。
type RuleFile struct {
	// Path 是清理后的候选路径（逻辑路径，不展开符号链接）。
	Path string
	// Basename 是文件名，也是去重键。
	Basename string
	// Scope 是该文件所属的作用域层级。
	Scope RuleScope
	// Mode 是模式专属目录所属的模式；空字符串表示通用规则。
	Mode string
}

// LegacyMigrationHint 是 legacy 规则的路径迁移提示。它是纯文本常量，
// 由调用方决定是否展示；本文件不向提示词注入任何内容。
const LegacyMigrationHint = "legacy 规则路径已弃用：请迁移到项目根的 AGENTS.md/CLAUDE.md 或 .argent/rules/ 下的规则文件"

// RuleMigrationHint 返回规则文件的迁移提示：只有 legacy 作用域返回非空。
func RuleMigrationHint(r RuleFile) string {
	if r.Scope == RuleScopeLegacy {
		return LegacyMigrationHint
	}
	return ""
}

// modeToken 在候选目录模板中代表当前模式，发现时被替换为 mode 实参。
const modeToken = "{mode}"

// fileCandidate 是一个指向具体文件的候选。
type fileCandidate struct {
	scope RuleScope
	rel   string // 相对 base 的路径，可含 modeToken
	mode  bool   // 是否为模式专属候选
}

// dirCandidate 是一个规则目录候选：读取其中的 .md/.mdc 规则文件。
type dirCandidate struct {
	scope RuleScope
	rel   string
	mode  bool
}

// 候选按发现顺序排列：同作用域内通用规则在前，模式专属目录在后。
// 切片为包级只读数据，DiscoverRules 从不修改它们。
var (
	globalFileCandidates = []fileCandidate{
		{RuleScopeGlobal, "AGENTS.md", false},
		{RuleScopeGlobal, "CLAUDE.md", false},
	}
	globalDirCandidates = []dirCandidate{
		{RuleScopeGlobal, filepath.Join(".argent", "rules"), false},
		{RuleScopeGlobal, filepath.Join(".kilocode", "rules"), false},
		{RuleScopeGlobal, filepath.Join(".argent", "rules-"+modeToken), true},
		{RuleScopeGlobal, filepath.Join(".kilocode", "rules-"+modeToken), true},
	}
	projectFileCandidates = []fileCandidate{
		{RuleScopeProject, "AGENTS.md", false},
		{RuleScopeProject, "CLAUDE.md", false},
	}
	projectDirCandidates = []dirCandidate{
		{RuleScopeProject, filepath.Join(".argent", "rules"), false},
		{RuleScopeProject, filepath.Join(".kilocode", "rules"), false},
		{RuleScopeProject, filepath.Join(".argent", "rules-"+modeToken), true},
		{RuleScopeProject, filepath.Join(".kilocode", "rules-"+modeToken), true},
	}
	legacyFileCandidates = []fileCandidate{
		{RuleScopeLegacy, ".cursorrules", false},
		{RuleScopeLegacy, ".claude.md", false},
		{RuleScopeLegacy, filepath.Join(".github", "copilot-instructions.md"), false},
	}
	legacyDirCandidates = []dirCandidate{
		{RuleScopeLegacy, filepath.Join(".cursor", "rules"), false},
		{RuleScopeLegacy, filepath.Join(".claude", "rules"), false},
	}
)

// DiscoverRules 扫描 home（global）与 project（project）下的规则文件候选，
// 按 basename 去重后返回稳定排序的结果。
//
// 去重优先级（先到先得）：project > global > legacy；同作用域内通用规则
// 先于模式专属规则。这与本包 "nearer wins" 的注入语义一致：离工作目录
// 越近、越具体的规则在冲突时保留。
//
// mode 非空时额外发现 *-rules-<mode> / rules-<mode> 模式专属目录，并把
// mode 写入 RuleFile.Mode；mode 为空时完全跳过这些目录。
// legacy 路径的 Scope 为 RuleScopeLegacy，迁移提示见 RuleMigrationHint。
//
// 该函数是纯函数：只读磁盘、不修改入参、不写任何文件。返回路径经过清理；
// 符号链接会被解析，解析结果逃逸出 base 的候选会被丢弃，因此不会跟随
// 逃逸链接读取外部内容。结果按 (作用域, 通用/模式, basename, path) 稳定排序。
func DiscoverRules(home, project, mode string) []RuleFile {
	mode = strings.TrimSpace(mode)

	logicalHome, realHome := splitBase(home)
	logicalProject, realProject := splitBase(project)

	seen := make(map[string]bool)
	var out []RuleFile

	addFile := func(logicalBase, realBase string, c fileCandidate) {
		rel := c.rel
		if c.mode {
			if mode == "" {
				return
			}
			rel = strings.ReplaceAll(rel, modeToken, mode)
		}
		path := filepath.Join(logicalBase, rel)
		if _, ok := resolveFileWithin(realBase, path); !ok {
			return
		}
		name := filepath.Base(path)
		key := normalizePath(name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, RuleFile{
			Path:     filepath.Clean(path),
			Basename: name,
			Scope:    c.scope,
			Mode:     modeOf(c.mode, mode),
		})
	}

	addDir := func(logicalBase, realBase string, c dirCandidate) {
		rel := c.rel
		if c.mode {
			if mode == "" {
				return
			}
			rel = strings.ReplaceAll(rel, modeToken, mode)
		}
		dir := filepath.Join(logicalBase, rel)
		resolvedDir, ok := resolveDirWithin(realBase, dir)
		if !ok {
			return
		}
		entries, err := os.ReadDir(resolvedDir)
		if err != nil {
			return
		}
		// os.ReadDir 已按文件名排序，这里只是显式保证稳定。
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !isRuleFileName(name) {
				continue
			}
			key := normalizePath(name)
			if seen[key] {
				continue
			}
			candidate := filepath.Join(dir, name)
			if _, ok := resolveFileWithin(realBase, candidate); !ok {
				continue
			}
			seen[key] = true
			out = append(out, RuleFile{
				Path:     filepath.Clean(candidate),
				Basename: name,
				Scope:    c.scope,
				Mode:     modeOf(c.mode, mode),
			})
		}
	}

	// 先到先得地占用 basename：project > global > legacy。
	if logicalProject != "" {
		for _, c := range projectFileCandidates {
			addFile(logicalProject, realProject, c)
		}
		for _, c := range projectDirCandidates {
			addDir(logicalProject, realProject, c)
		}
	}
	if logicalHome != "" {
		for _, c := range globalFileCandidates {
			addFile(logicalHome, realHome, c)
		}
		for _, c := range globalDirCandidates {
			addDir(logicalHome, realHome, c)
		}
	}
	// legacy 优先级最低：项目旧位置先于主目录旧位置。
	if logicalProject != "" {
		for _, c := range legacyFileCandidates {
			addFile(logicalProject, realProject, c)
		}
		for _, c := range legacyDirCandidates {
			addDir(logicalProject, realProject, c)
		}
	}
	if logicalHome != "" {
		for _, c := range legacyFileCandidates {
			addFile(logicalHome, realHome, c)
		}
		for _, c := range legacyDirCandidates {
			addDir(logicalHome, realHome, c)
		}
	}

	scopeRank := func(s RuleScope) int {
		switch s {
		case RuleScopeProject:
			return 0
		case RuleScopeGlobal:
			return 1
		case RuleScopeLegacy:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := scopeRank(a.Scope), scopeRank(b.Scope); ra != rb {
			return ra < rb
		}
		// 通用规则（Mode 为空）排在模式专属规则之前。
		ca, cb := a.Mode == "", b.Mode == ""
		if ca != cb {
			return ca
		}
		if a.Basename != b.Basename {
			return a.Basename < b.Basename
		}
		return a.Path < b.Path
	})
	return out
}

// splitBase 返回 base 的逻辑绝对规范路径与解析符号链接后的真实路径。
// 逻辑路径用于构造返回的 Path，真实路径用于逃逸检查。
func splitBase(p string) (logical, real string) {
	if strings.TrimSpace(p) == "" {
		return "", ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	logical = filepath.Clean(abs)
	real = logical
	if resolved, err := filepath.EvalSymlinks(logical); err == nil {
		real = filepath.Clean(resolved)
	}
	return logical, real
}

// resolveFileWithin 解析 candidate 的全部符号链接，仅当结果仍是 base 内的
// 普通文件时返回其真实路径。base 为空表示未配置，直接拒绝。
func resolveFileWithin(base, candidate string) (string, bool) {
	if base == "" {
		return "", false
	}
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	if !withinWorkspace(real, base) {
		return "", false
	}
	info, err := os.Lstat(real)
	if err != nil || info.IsDir() {
		return "", false
	}
	return real, true
}

// resolveDirWithin 与 resolveFileWithin 相同，但要求解析结果是目录。
func resolveDirWithin(base, candidate string) (string, bool) {
	if base == "" {
		return "", false
	}
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	if !withinWorkspace(real, base) {
		return "", false
	}
	info, err := os.Lstat(real)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return real, true
}

// isRuleFileName 报告文件名是否为规则目录中的规则文件（.md/.mdc，大小写不敏感）。
func isRuleFileName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".mdc")
}

// modeOf 返回模式专属候选的 mode，通用候选返回空字符串。
func modeOf(specific bool, mode string) string {
	if specific {
		return mode
	}
	return ""
}
