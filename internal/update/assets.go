package update

import (
	"runtime"
	"sort"
	"strings"
)

var osAliases = map[string][]string{
	"windows": {"windows", "win64", "win32", "win"},
	"darwin":  {"darwin", "macos", "osx", "mac"},
	"linux":   {"linux"},
	"freebsd": {"freebsd"},
}

var archAliases = map[string][]string{
	"amd64": {"amd64", "x86_64", "x86-64", "x64"},
	"arm64": {"arm64", "aarch64"},
	"386":   {"386", "i386", "x86"},
	"arm":   {"armv7", "armv6", "arm"},
}

// FindAsset 为某个平台挑选发布资源。它理解常见的
// 命名方案：argent_1.2.3_windows_amd64.zip、argent-windows-x64.zip、
// argent_1.2.3_linux_arm64.tar.gz。仅指定操作系统的资源在没有
// 架构专属文件时会被接受；而指定了不同架构的资源
// 则绝不被接受。
func FindAsset(assets []Asset, goos, goarch string) (Asset, bool) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	goos = strings.ToLower(strings.TrimSpace(goos))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	if goos == "" || goarch == "" {
		return Asset{}, false
	}

	best := Asset{}
	bestScore := -1
	for _, asset := range assets {
		info := inspectAsset(asset, goos, goarch)
		if !info.eligible || info.score <= bestScore {
			continue
		}
		best, bestScore = asset, info.score
	}
	if bestScore < 0 {
		return Asset{}, false
	}
	return best, true
}

// assetInspection 是 FindAsset 和
// DiagnoseAssetMatch 共用的决策逻辑。两者都依赖这个辅助函数，使诊断
// 不会与实际选中的资源产生偏差。
type assetInspection struct {
	osMatched   bool
	foreignOS   bool
	archMatched bool
	foreignArch bool
	score       int
	eligible    bool
	reason      AssetMatchReason
}

// inspectAsset 为已经规范化的目标平台分类单个资源。
func inspectAsset(asset Asset, goos, goarch string) assetInspection {
	lower := strings.ToLower(asset.Name)
	if asset.URL == "" {
		return assetInspection{reason: AssetMatchReasonNoURL}
	}
	if isNoise(lower) {
		return assetInspection{reason: AssetMatchReasonNoise}
	}
	name := normalizeName(asset.Name)
	matchedOS, foreignOS := classify(name, goos, osAliases)
	if !matchedOS || foreignOS {
		return assetInspection{
			osMatched: matchedOS, foreignOS: foreignOS,
			reason: AssetMatchReasonWrongOS,
		}
	}
	matchedArch, foreignArch := classify(name, goarch, archAliases)
	if foreignArch {
		return assetInspection{
			osMatched: true, archMatched: matchedArch, foreignArch: true,
			reason: AssetMatchReasonWrongArch,
		}
	}
	score := 10 + extensionBonus(lower, goos)
	if matchedArch {
		score += 100
	}
	return assetInspection{
		osMatched: true, archMatched: matchedArch,
		score: score, eligible: true, reason: AssetMatchReasonEligible,
	}
}

// normalizeName 将文件名转为小写并把分隔符替换为点，以便
// 别名能在标记边界上匹配："arg-x86_64.zip" -> ".arg.x86.64.zip."
func normalizeName(name string) string {
	var builder strings.Builder
	builder.WriteByte('.')
	for _, char := range strings.ToLower(name) {
		if char == '_' || char == '-' {
			builder.WriteByte('.')
			continue
		}
		builder.WriteRune(char)
	}
	builder.WriteByte('.')
	return builder.String()
}

// classify 报告名称是否提到目标平台，以及是否
// 提到了同类的不同平台。最长别名优先，因此
// "x86_64" 算作 amd64 而非 386。若名称用等长的别名提到两个不同
// 平台，则视为有歧义并始终当作
// 外来资源：猜测会让 FindAsset 依赖 Go 随机的 map 遍历
// 顺序，并可能装错架构。
func classify(name, target string, table map[string][]string) (matched, foreign bool) {
	if _, known := table[target]; !known {
		// 无法识别的目标在表中没有别名，但名称若提到了
		// *已知* 平台，那仍然是另一个平台。将其当作
		// "未指定平台" 会让 FindAsset 回退到例如 amd64
		// 构建给 riscv64 机器。显式提到目标平台者优先；
		// 否则任何已知别名都会使该资源成为外来资源。
		if mentions(name, []string{target}) {
			return true, false
		}
		_, mentioned, ambiguous := longestAlias(name, table)
		if mentioned || ambiguous {
			return false, true
		}
		return false, false
	}
	key, matched, ambiguous := longestAlias(name, table)
	if ambiguous {
		return false, true
	}
	if !matched {
		return false, false
	}
	return key == target, key != target
}

// longestAlias 找出其别名与 name 匹配、且标记边界针最长的平台键。
// 不同键按排序顺序访问，以便审慎处理等长情况：
// 当两个*不同*键匹配到相同长度的针时，会设置 ambiguous，
// 调用方必须拒绝该资源，而不能让随机的 map 遍历
// 决定它代表哪个平台。
func longestAlias(name string, table map[string][]string) (key string, matched, ambiguous bool) {
	keys := make([]string, 0, len(table))
	for candidate := range table {
		keys = append(keys, candidate)
	}
	sort.Strings(keys)

	bestLength := -1
	for _, candidate := range keys {
		needles := make([]string, 0, len(table[candidate])+1)
		for _, alias := range table[candidate] {
			needles = append(needles, "."+separatorFree(alias)+".")
		}
		needles = append(needles, "."+separatorFree(candidate)+".")
		for _, needle := range needles {
			if len(needle) < bestLength || !strings.Contains(name, needle) {
				continue
			}
			if len(needle) > bestLength {
				bestLength = len(needle)
				key, matched, ambiguous = candidate, true, false
				continue
			}
			// 同一键的等长匹配只是一种不同的
			// 拼写（例如 x86_64 和 x86-64）。不同的键意味着
			// 资源名称指向两个平台，不可信任。
			if candidate != key {
				ambiguous = true
			}
		}
	}
	if ambiguous {
		return "", false, true
	}
	return key, matched, false
}

func mentions(name string, aliases []string) bool {
	for _, alias := range aliases {
		if strings.Contains(name, "."+separatorFree(alias)+".") {
			return true
		}
	}
	return false
}

func separatorFree(alias string) string {
	return strings.NewReplacer("_", ".", "-", ".").Replace(strings.ToLower(alias))
}

// isNoise 过滤掉校验和、签名和元数据文件。
func isNoise(lower string) bool {
	for _, suffix := range []string{
		".sha256", ".sha256sum", ".sha512", ".sig", ".asc", ".json", ".txt",
		".pem", ".sbom", ".spdx", ".md",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	for _, word := range []string{"checksum", "sha256sums", "source"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func extensionBonus(lower, goos string) int {
	if goos == "windows" {
		switch {
		case strings.HasSuffix(lower, ".zip"):
			return 3
		case strings.HasSuffix(lower, ".exe"):
			return 2
		case strings.HasSuffix(lower, ".msi"):
			return 1
		}
		return 0
	}
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		return 3
	case strings.HasSuffix(lower, ".tgz"):
		return 2
	case strings.HasSuffix(lower, ".zip"):
		return 1
	}
	return 0
}
