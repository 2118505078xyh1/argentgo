package security

import "strings"

// 沙箱违规分类结果中 kind 的取值。三类互斥：网络违规优先判定，
// 其次是权限违规（permission denied / EACCES），最后是文件系统违规
// （只读文件系统、seccomp/landlock/sandbox 等策略拦截）。
const (
	sandboxViolationFileSystem = "file-system"
	sandboxViolationNetwork    = "network"
	sandboxViolationPermission = "permission"
)

// 网络上下文词：必须和拒绝词同时出现才判定为网络违规。单独出现
// "connection refused" 之类的普通网络错误不应被当成沙箱拦截。
var sandboxNetworkContextWords = []string{
	"network", "proxy", "dns", "egress", "connection",
	"网络", "代理", "域名", "出网", "出口",
}

// 网络拒绝词。刻意不包含 "refused"/"rejected"：连接被对端拒绝是常见的
// 非沙箱错误，不能据此判定违规。
var sandboxNetworkDenialWords = []string{
	"denied", "deny", "blocked", "block", "not allowed", "forbidden",
	"拒绝", "禁止", "阻止", "不允许",
}

// 权限类拒绝的专用标记。这里只放精确的短语，不放 "not permitted"，
// 否则 "operation not permitted" 会被误判到权限类。
var sandboxPermissionMarkers = []string{
	"permission denied", "access denied", "eacces",
	"权限不足", "权限被拒绝", "拒绝访问", "没有权限",
}

// 文件系统类拒绝标记。"operation not permitted"（EPERM）按 codex 沙箱
// 的归类放入文件系统违规；泛化的 "sandbox" 标记也落在这里作为保守默认。
var sandboxFileSystemMarkers = []string{
	"operation not permitted", "eperm", "read-only file system",
	"readonly file system", "read-only filesystem", "failed to write file",
	"seccomp", "landlock", "sandbox", "file system", "filesystem",
	"只读文件系统", "不允许操作", "操作不允许", "沙箱", "写入失败",
}

// 被拒路径的提取标记：只识别 "路径: 原因" 这类明确格式，与分类关键词
// 分开维护。同时收录全角冒号变体以覆盖中文输出。
var sandboxDeniedPathMarkers = []string{
	": operation not permitted",
	": permission denied",
	": access denied",
	": read-only file system",
	": 操作不允许",
	": 权限被拒绝",
	": 拒绝访问",
	": 只读文件系统",
	"：操作不允许",
	"：权限被拒绝",
	"：拒绝访问",
	"：只读文件系统",
}

// 被拒路径长度上限：避免把异常长的输出整段复制进路径字段。
const maxDeniedPathBytes = 4096

// classifySandboxViolation 从一段工具错误输出中识别沙箱违规。
// 返回 kind（file-system / network / permission）、被拒路径（如有）以及
// 是否识别成功。任何已知模式都不匹配时 ok 为 false，两个字符串均为空。
// 该函数是纯函数：不访问状态，不产生副作用，不做任何脱敏或截断。
func classifySandboxViolation(output string) (kind, path string, ok bool) {
	if strings.TrimSpace(output) == "" {
		return "", "", false
	}
	// 网络优先：网络上下文与拒绝词必须成对出现，避免误判普通网络故障。
	if containsAnyFold(output, sandboxNetworkContextWords) &&
		containsAnyFold(output, sandboxNetworkDenialWords) {
		return sandboxViolationNetwork, "", true
	}
	if containsAnyFold(output, sandboxPermissionMarkers) {
		return sandboxViolationPermission, extractDeniedPath(output), true
	}
	if containsAnyFold(output, sandboxFileSystemMarkers) {
		return sandboxViolationFileSystem, extractDeniedPath(output), true
	}
	return "", "", false
}

// extractDeniedPath 从输出中提取被拒绝的路径。找不到符合格式且形似路径的
// 片段时返回空字符串。逐行、逐标记扫描，命中后取标记前的最后一个冒号分隔
// 片段；没有冒号分隔符时退回取最后一个空白分隔词，再做路径形态校验。
func extractDeniedPath(output string) string {
	for _, line := range strings.Split(output, "\n") {
		for _, marker := range sandboxDeniedPathMarkers {
			idx := indexFold(line, marker)
			if idx < 0 {
				continue
			}
			prefix := strings.TrimSpace(line[:idx])
			candidates := []string{cutLastPathSeparator(prefix)}
			// 没有冒号分隔符时（例如 "write ./x: Read-only file system"），
			// 退回取最后一个以空白分隔的词；随后仍要经过路径形态校验。
			if token := lastPathToken(prefix); token != candidates[0] {
				candidates = append(candidates, token)
			}
			for _, candidate := range candidates {
				candidate = strings.Trim(strings.TrimSpace(candidate), "\"'`")
				if looksLikeDeniedPath(candidate) {
					return candidate
				}
			}
		}
	}
	return ""
}

// cutLastPathSeparator 返回 prefix 中最后一个路径分隔符之后的内容。
// 半角冒号要求后面跟空格（避免切开 Windows 盘符），全角冒号后若有空格则
// 一并跳过。返回的切片边界始终落在 ASCII 字节或完整的多字节字符上。
func cutLastPathSeparator(prefix string) string {
	half := -1
	if i := strings.LastIndex(prefix, ": "); i >= 0 {
		half = i + 2
	}
	full := -1
	if i := strings.LastIndex(prefix, "："); i >= 0 {
		full = i + len("：")
		if full < len(prefix) && prefix[full] == ' ' {
			full++
		}
	}
	if half > full {
		return prefix[half:]
	}
	if full >= 0 {
		return prefix[full:]
	}
	return prefix
}

// looksLikeDeniedPath 判断候选片段是否形似文件路径。接受 POSIX 绝对路径、
// "./" 与 "../" 相对路径、"~/" 家目录路径、POSIX 根路径、UNC 路径，
// 以及 Windows 盘符路径（C:\... 或 C:/...）。
func looksLikeDeniedPath(candidate string) bool {
	if candidate == "" || len(candidate) > maxDeniedPathBytes {
		return false
	}
	for i := 0; i < len(candidate); i++ {
		if candidate[i] < 0x20 || candidate[i] == 0x7f {
			return false
		}
	}
	if strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, "./") ||
		strings.HasPrefix(candidate, "../") || strings.HasPrefix(candidate, "~/") ||
		strings.HasPrefix(candidate, `\`) {
		return true
	}
	if len(candidate) >= 3 && isASCIILetter(candidate[0]) &&
		candidate[1] == ':' && (candidate[2] == '\\' || candidate[2] == '/') {
		return true
	}
	return false
}

// lastPathToken 返回 prefix 中最后一个以空白分隔的词，供没有冒号分隔符的
// 输出行作为路径候选。
func lastPathToken(prefix string) string {
	if i := strings.LastIndexAny(prefix, " \t"); i >= 0 {
		return prefix[i+1:]
	}
	return prefix
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// containsAnyFold 报告 s 是否包含 words 中的任意词（忽略 ASCII 大小写）。
func containsAnyFold(s string, words []string) bool {
	for _, word := range words {
		if indexFold(s, word) >= 0 {
			return true
		}
	}
	return false
}

// indexFold 返回 needle 在 s 中忽略 ASCII 大小写后的首次字节下标，找不到
// 返回 -1。它逐字节比较、只折叠 ASCII 大小写，保留原始字节偏移：使用
// strings.ToLower 后再取下标会因部分 Unicode 字符折叠后字节数变化而偏移，
// 从而在含非 ASCII 字符（如 CJK）的路径上切出错误结果。
func indexFold(s, needle string) int {
	if needle == "" {
		return 0
	}
	n := len(needle)
	for i := 0; i+n <= len(s); i++ {
		matched := true
		for j := 0; j < n; j++ {
			a, b := s[i+j], needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}
