package security

import (
	"regexp"
	"strings"
)

// ThreatCategory 命名一类 FindFileEditingCommand 中感知 token 的文件编辑分类器
// 不覆盖的危险命令（管道 shell、反弹 shell、编码执行和通过包装器发起的删除）。
// 这些常量是附加性的：现有分类器及其语义完全保持不变。
type ThreatCategory string

const (
	ThreatNone         ThreatCategory = ""
	ThreatForcedRM     ThreatCategory = "forced-rm"
	ThreatWrapper      ThreatCategory = "wrapper"
	ThreatDownloadExec ThreatCategory = "download-exec"
	ThreatReverseShell ThreatCategory = "reverse-shell"
	ThreatEncodedExec  ThreatCategory = "encoded-exec"
)

// ThreatConfidence 是有序的置信度等级。值越高越确定；护栏只对 ConfidenceHigh
// 的威胁采取行动。
type ThreatConfidence int

const (
	ConfidenceNone ThreatConfidence = iota
	ConfidenceLow
	ConfidenceMedium
	ConfidenceHigh
)

// ThreatMatch 是检查命令中已知危险模式得到的纯结果。Evidence 始终是被检查命令
// 的子串，因此调用方可以直接把它作为拦截决定中的违规 token。
type ThreatMatch struct {
	Category   ThreatCategory
	Confidence ThreatConfidence
	Evidence   string
}

// Detected 报告是否识别出了任何威胁。
func (m ThreatMatch) Detected() bool { return m.Category != ThreatNone }

// IsHigh 报告该匹配是否足够可信、可以据此采取行动。
func (m ThreatMatch) IsHigh() bool { return m.Confidence >= ConfidenceHigh }

type threatPattern struct {
	category   ThreatCategory
	confidence ThreatConfidence
	re         *regexp.Regexp
}

// 所有模式都在包初始化时编译一次。它们刻意保守且符合 RE2（不使用反向引用或
// 环视）。
var (
	// 核心模式运行之前会剥离包装器前缀：隐藏在 sudo/env/nohup/command 后面的危险
	// 命令仍然是危险的。这是"降维"步骤；第 1 组保留前面的分隔符，因此用 ; | &
	// 连接的命令仍能正确拆分。
	wrapperPrefixRe = regexp.MustCompile(`(?i)(^|[\s;|&(])(sudo|env|nohup|command|builtin|exec)\s+`)

	// trap 载荷通常位于引号内，而 sanitizeCommand 会把引号置为空白。从 trap 调用中
	// 恢复带引号的载荷并单独分类。RE2 没有反向引用，因此单引号和双引号使用不同
	// 的正则表达式。
	trapSingleRe = regexp.MustCompile(`(?i)\btrap\s+'([^']*)'`)
	trapDoubleRe = regexp.MustCompile(`(?i)\btrap\s+"([^"]*)"`)

	// 针对根目录/大范围目标的强制 rm 是灾难性的，置信度提升为 HIGH。具体路径保持
	// MEDIUM，除非包装器将其提升。
	wideTargetRe = regexp.MustCompile(`(^|\s)(/(\s|$)|/\*|/(etc|usr|var|bin|boot|home|opt|root|srv)(/|\s|$)|~(/|\s|$)|\$home(/|\s|$)|\.\.(/|\s|$))`)
	forcedRMRe   = regexp.MustCompile(`\brm\b[^\n]*\s(-[a-zA-Z]*f[a-zA-Z]*|--force)\b`)

	corePatterns = []threatPattern{
		// 反弹 shell。
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`/dev/tcp/`)},
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`\b(nc|netcat|ncat)\b[^\n]*\s-(e|exec)\b`)},
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`\b(nc|netcat|ncat)\b[^\n]*\s--(sh-)?exec\b`)},
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`\bsocat\b[^\n]*exec:`)},
		// 下载即执行（curl/wget 通过管道传给 shell）。
		{ThreatDownloadExec, ConfidenceHigh, regexp.MustCompile(`\b(curl|wget)\b[^\n]*\|\s*(sudo\s+)?(ba)?sh\b`)},
		// 编码执行（base64/hex 解码后传给 shell）。
		{ThreatEncodedExec, ConfidenceHigh, regexp.MustCompile(`\bbase64\s+(-d|--decode|-di)\b[^\n]*\|\s*(sudo\s+)?(ba)?sh\b`)},
		{ThreatEncodedExec, ConfidenceHigh, regexp.MustCompile(`\bxxd\s+-r\b[^\n]*\|\s*(sudo\s+)?(ba)?sh\b`)},
	}
)

// DetectThreat 检查 command 中危险的非文件编辑模式，返回置信度最高的匹配。
// 它是纯函数：引号字符串、heredoc 正文和注释都使用与 FindFileEditingCommand
// 相同的感知 token 清洗器进行遮盖，因此字面量文本绝不会产生误报。空命令返回零值
// ThreatMatch。
func DetectThreat(command string) ThreatMatch {
	return detectThreatDepth(command, 0)
}

// detectThreatDepth 是带显式递归预算的 DetectThreat，用于从包装器（trap 和
// shell -c）恢复的字面量载荷。每一层至少消耗包装器文本，因此预算是防御性上界，
// 而非终止的必要条件。
func detectThreatDepth(command string, depth int) ThreatMatch {
	if strings.TrimSpace(command) == "" {
		return ThreatMatch{}
	}

	best := ThreatMatch{}

	// 遮盖引号字符串、heredoc 和注释，然后在匹配核心模式之前归约包装器。
	sanitized := strings.ToLower(sanitizeCommand(command))
	reduced, hadWrapper := reduceWrappers(sanitized)
	if m := detectCore(reduced, hadWrapper); m.Confidence > best.Confidence {
		best = m
	}

	if depth < maxShellLiteralDepth {
		// trap 载荷已被清洗器置为空白；从原始命令中检查它们。trap 关键字本身是包装器，
		// 因此在 trap 中发现的中等置信度核心威胁会被提升为 HIGH。
		for _, payload := range trapPayloads(command) {
			if m := detectCore(strings.ToLower(payload), true); m.Confidence > best.Confidence {
				best = m
			}
			// trap 载荷本身可以启动 `bash -c ...`；递归处理，使嵌套的字面量不会被两层引号
			// 隐藏。
			if m := detectThreatDepth(payload, depth+1); m.Confidence > best.Confidence {
				best = m
			}
		}

		// P0 修复：`bash -c`/`sh -c`/`bash -lc`（及类似形式）会包裹一个带引号的命令，
		// sanitizeCommand 在分类器运行前会把它清空。从原始命令中恢复字面量载荷，
		// 并同时运行感知包装器的核心模式（把中等威胁提升为 HIGH）和完整的递归分类。
		// 提取是保守的：只返回命令位置真正带引号的载荷，因此良性字符串永不匹配。
		for _, payload := range shellLiteralPayloads(command) {
			if m := detectCore(strings.ToLower(payload), true); m.Confidence > best.Confidence {
				best = m
			}
			if m := detectThreatDepth(payload, depth+1); m.Confidence > best.Confidence {
				best = m
			}
		}
	}

	return best
}

// detectCore 对单条规范化命令运行编译好的模式。
func detectCore(s string, hadWrapper bool) ThreatMatch {
	best := ThreatMatch{}

	for _, p := range corePatterns {
		if loc := p.re.FindStringIndex(s); loc != nil {
			m := ThreatMatch{
				Category:   p.category,
				Confidence: p.confidence,
				Evidence:   s[loc[0]:loc[1]],
			}
			if m.Confidence > best.Confidence {
				best = m
			}
		}
	}

	if loc := forcedRMRe.FindStringIndex(s); loc != nil {
		confidence := ConfidenceMedium
		if wideTargetRe.MatchString(s) {
			confidence = ConfidenceHigh
		}
		// 通过包装器发起强制删除本身就可疑，即使目标路径很具体。
		if hadWrapper && confidence < ConfidenceHigh {
			confidence = ConfidenceHigh
		}
		m := ThreatMatch{
			Category:   ThreatForcedRM,
			Confidence: confidence,
			Evidence:   s[loc[0]:loc[1]],
		}
		if m.Confidence > best.Confidence {
			best = m
		}
	}

	return best
}

// reduceWrappers 剥离开头的 sudo/env/nohup/command/builtin/exec 前缀，重复直到
// 稳定，从而处理堆叠的包装器（sudo env rm ...）。它报告是否移除了任何包装器。
func reduceWrappers(s string) (string, bool) {
	reduced := s
	found := false
	for {
		next := wrapperPrefixRe.ReplaceAllString(reduced, "$1")
		if next == reduced {
			return reduced, found
		}
		found = true
		reduced = next
	}
}

// trapPayloads 返回每次 trap 调用的带引号命令载荷。
func trapPayloads(command string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{trapSingleRe, trapDoubleRe} {
		for _, m := range re.FindAllStringSubmatch(command, -1) {
			if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
				out = append(out, m[1])
			}
		}
	}
	return out
}
