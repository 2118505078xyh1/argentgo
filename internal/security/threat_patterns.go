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
	//
	// 这里只匹配关键字本身；紧随其后的选项（如 `sudo -u root ...`、
	// `env -i ...`）由 consumeWrapperArgs 按 token 处理，因为选项是否带独立
	// 参数无法用正则简洁而准确地表达。若只删掉关键字，`sudo -u root rm file`
	// 会归约成 `-u root rm file`，token 分类器把 `-u` 当作可执行文件，从而让
	// 一次明显的删除绕过计划模式守卫与自动审批。
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

	// 编码注入：长 base64 串、\x 十六进制转义与 Unicode 转义可以在没有任何
	// 解码管道的情况下隐藏危险命令。这里检查原始命令，避免 ToLower 破坏 base64
	// 的大小写；递归会从 trap 与 shell -c 载荷中再次调用到这里。
	if evidence := findEncodedPayload(command); evidence != "" {
		m := ThreatMatch{
			Category:   ThreatEncodedExec,
			Confidence: ConfidenceHigh,
			Evidence:   evidence,
		}
		if m.Confidence > best.Confidence {
			best = m
		}
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

// wrapperValueShortOpts 列出"值在下一个 token"的短选项，按包装器区分。
// 只列真正带值的选项很重要：把一个无值选项误判为带值会吞掉后面的可执行文件，
// 反而让修改类命令漏过。未在表中的短选项（-n、-i、-p、-- 等）被视为无值。
var wrapperValueShortOpts = map[string]string{
	"sudo": "ugpCtTrUac",
	"env":  "uCS",
	// command -v/-V 只是在查询某个名字，后面的 token 不是要执行的可执行文件；
	// 把它当作选项值消耗掉，避免 `command -v rm` 这种只读查询被误报。
	"command": "vV",
}

// wrapperValueLongOpts 列出同样带独立参数的长选项（--opt value 形式）。
// --opt=value 天然是一个 token，不需要在这里列出。
var wrapperValueLongOpts = map[string]bool{
	"--user": true, "--group": true, "--unset": true,
	"--chdir": true, "--split-string": true,
}

// reduceWrappers 剥离开头的 sudo/env/nohup/command/builtin/exec 前缀，重复直到
// 稳定，从而处理堆叠的包装器（sudo env rm ...）。它也消耗包装器自身的选项和
// 参数，使 `sudo -u root rm file` 归约成 `rm file` 而不是 `-u root rm file`。
// 它报告是否移除了任何包装器。
func reduceWrappers(s string) (string, bool) {
	reduced := s
	found := false
	for {
		next, changed := stripWrapperPrefix(reduced)
		if !changed {
			return reduced, found
		}
		found = true
		reduced = next
	}
}

// stripWrapperPrefix 如果 s 的开头（可能带一个分隔符）是包装器关键字，就移除
// 该关键字及其选项，返回剩余部分。第 1 组分隔符被原样保留，使 ; | & 连接的命令
// 仍能正确拆分。
func stripWrapperPrefix(s string) (string, bool) {
	loc := wrapperPrefixRe.FindStringSubmatchIndex(s)
	if loc == nil {
		return s, false
	}
	before := s[:loc[0]]          // 匹配之前的文本必须原样保留
	separator := s[loc[2]:loc[3]] // 第 1 组：前导分隔符，可能为空
	keyword := strings.ToLower(s[loc[4]:loc[5]])
	rest := consumeWrapperArgs(keyword, s[loc[1]:])
	return before + separator + rest, true
}

// consumeWrapperArgs 跳过包装器关键字之后、可执行文件之前的选项及其参数。
// 只消耗以 '-' 开头的 token、sudo/env 的赋值，以及已知带值选项的下一个 token；
// 遇到第一个普通 token（被包装的可执行文件）就停止。失败即安全：任何未被识别
// 的选项形态都会让后面的 token 原样保留，交给 token 分类器继续检查。
func consumeWrapperArgs(wrapper, rest string) string {
	pos := 0
	for {
		for pos < len(rest) && isWrapperBlank(rest[pos]) {
			pos++
		}
		if pos >= len(rest) || isWrapperSep(rest[pos]) {
			break
		}
		start := pos
		for pos < len(rest) && !isWrapperBlank(rest[pos]) && !isWrapperSep(rest[pos]) {
			pos++
		}
		token := rest[start:pos]
		lower := strings.ToLower(token)
		// env 的 NAME=value 赋值属于包装器参数，跳过后继续找可执行文件。
		if wrapper == "env" && isAssignmentWord(token) {
			continue
		}
		if !strings.HasPrefix(token, "-") {
			return rest[start:] // 这就是被包装的可执行文件，原样保留
		}
		// 带 '=' 的选项（--opt=value）自带值，不需要消费下一个 token。
		if strings.Contains(lower, "=") {
			continue
		}
		needValue := wrapperValueLongOpts[lower]
		if !needValue && !strings.HasPrefix(lower, "--") {
			for _, ch := range lower[1:] {
				if strings.Contains(wrapperValueShortOpts[wrapper], string(ch)) {
					needValue = true
					break
				}
			}
		}
		if !needValue {
			continue
		}
		// 选项的值通常是下一个 token；若它是另一个选项或分隔符则不消费，
		// 避免把可执行文件误当成值吞掉。
		next := pos
		for next < len(rest) && isWrapperBlank(rest[next]) {
			next++
		}
		if next >= len(rest) || isWrapperSep(rest[next]) || rest[next] == '-' {
			continue
		}
		pos = next
		for pos < len(rest) && !isWrapperBlank(rest[pos]) && !isWrapperSep(rest[pos]) {
			pos++
		}
	}
	return rest[pos:]
}

func isWrapperBlank(c byte) bool { return c == ' ' || c == '\t' }
func isWrapperSep(c byte) bool   { return isShellSeparator(c) || c == '\r' }

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
