// Package security 的命令静态安全分类原语。
//
// 本文件提供一套面向"自研 shell 任务"的命令风险分层。它与既有的
// FindFileEditingCommand / DetectThreat 完全 additive：不修改它们、不接入
// Chain.Check，也没有任何调用方，因此默认不改变"策略 → 审批 → 审计"安全链
// 的行为。
//
// 设计约束：
//   - 只用标准库，零 I/O、无全局状态，所有函数都是纯函数；
//   - 不修改入参（Go 字符串本身不可变，这里也绝不写回任何外部状态）；
//   - 基于常见命令名与 shell 元字符做启发式；
//   - 失败即保守（fail-closed）：未知命令一律视为需要审批的 mutating，
//     而非放行；引号内字面量与注释不会造成误报，但被掩盖的可执行载荷会
//     交给同包既有的感知 token 检测器处理。

package security

import "strings"

// CommandRisk 是一条命令的静态风险等级。取值按严重程度递增：
//
//	benign < read-only < mutating < destructive < forbidden
//
// 该排序由 commandRiskSeverity 编码，供 RequiresApprovalRisk 与分类过程中的
// "取最严格等级"使用；未知取值一律按 mutating 处理，保持失败即保守。
type CommandRisk string

const (
	// CommandRiskBenign 是无副作用的空命令或纯 stdout 回显（echo、true、: 等）。
	CommandRiskBenign CommandRisk = "benign"
	// CommandRiskReadOnly 只读状态，不修改文件系统或外部系统（ls、cat、git status 等）。
	CommandRiskReadOnly CommandRisk = "read-only"
	// CommandRiskMutating 会改变状态但通常是局部/可恢复的（mkdir、mv、git add 等）。
	CommandRiskMutating CommandRisk = "mutating"
	// CommandRiskDestructive 会删除或覆盖数据，难以恢复（rm、dd、mkfs、git clean 等）。
	CommandRiskDestructive CommandRisk = "destructive"
	// CommandRiskForbidden 是明确恶意或灾难性的模式（curl|sh、反弹 shell、根目录强制删除等）。
	CommandRiskForbidden CommandRisk = "forbidden"
)

// String 返回稳定的小写等级名，便于日志与测试断言。未知取值原样返回。
func (r CommandRisk) String() string { return string(r) }

// commandRiskSeverity 把等级映射到递增的严重程度。未知取值返回 mutating 的
// 严重度：既让未知命令在分类时落到"需审批"，也让 RequiresApprovalRisk 对
// 未知等级返回 true，保持失败即保守。
func commandRiskSeverity(r CommandRisk) int {
	switch r {
	case CommandRiskBenign:
		return 0
	case CommandRiskReadOnly:
		return 1
	case CommandRiskMutating:
		return 2
	case CommandRiskDestructive:
		return 3
	case CommandRiskForbidden:
		return 4
	default:
		return commandRiskSeverity(CommandRiskMutating)
	}
}

// commandNameRisk 是单个命令名到基础风险的映射。只列常见命令；凡未列出的
// 名字都由 ClassifyCommand 保守地归为 mutating。
var commandNameRisk = map[string]CommandRisk{
	// benign：只写 stdout 或纯粹的空操作。
	":": CommandRiskBenign, "true": CommandRiskBenign, "false": CommandRiskBenign,
	"echo": CommandRiskBenign, "printf": CommandRiskBenign, "cd": CommandRiskBenign,

	// read-only：读取本地状态或发起只读网络请求。
	"ls": CommandRiskReadOnly, "dir": CommandRiskReadOnly, "pwd": CommandRiskReadOnly,
	"cat": CommandRiskReadOnly, "head": CommandRiskReadOnly, "tail": CommandRiskReadOnly,
	"less": CommandRiskReadOnly, "more": CommandRiskReadOnly, "wc": CommandRiskReadOnly,
	"grep": CommandRiskReadOnly, "egrep": CommandRiskReadOnly, "fgrep": CommandRiskReadOnly,
	"rg": CommandRiskReadOnly, "find": CommandRiskReadOnly, "locate": CommandRiskReadOnly,
	"which": CommandRiskReadOnly, "whereis": CommandRiskReadOnly, "type": CommandRiskReadOnly,
	"file": CommandRiskReadOnly, "stat": CommandRiskReadOnly, "du": CommandRiskReadOnly,
	"df": CommandRiskReadOnly, "ps": CommandRiskReadOnly, "top": CommandRiskReadOnly,
	"printenv": CommandRiskReadOnly, "date": CommandRiskReadOnly, "whoami": CommandRiskReadOnly,
	"id": CommandRiskReadOnly, "uname": CommandRiskReadOnly, "hostname": CommandRiskReadOnly,
	"history": CommandRiskReadOnly, "env": CommandRiskReadOnly,
	"curl": CommandRiskReadOnly, "wget": CommandRiskReadOnly,

	// mutating：改变状态但通常局部、可恢复；也可能覆盖目标，故需审批。
	"mkdir": CommandRiskMutating, "touch": CommandRiskMutating, "cp": CommandRiskMutating,
	"mv": CommandRiskMutating, "ln": CommandRiskMutating, "chmod": CommandRiskMutating,
	"chown": CommandRiskMutating, "chgrp": CommandRiskMutating, "tee": CommandRiskMutating,
	"patch": CommandRiskMutating, "install": CommandRiskMutating, "sed": CommandRiskMutating,
	"awk": CommandRiskMutating, "tar": CommandRiskMutating, "unzip": CommandRiskMutating,
	"zip": CommandRiskMutating, "gzip": CommandRiskMutating, "gunzip": CommandRiskMutating,
	"apply": CommandRiskMutating, "systemctl": CommandRiskMutating, "service": CommandRiskMutating,
	// 能执行任意载荷的解释器/工具一律视为会改变状态；bash -c 的载荷由
	// DetectThreat 递归检查，命中高危模式时会升级为 forbidden。
	"sh": CommandRiskMutating, "bash": CommandRiskMutating, "dash": CommandRiskMutating,
	"zsh": CommandRiskMutating, "ksh": CommandRiskMutating, "ash": CommandRiskMutating,
	"python": CommandRiskMutating, "python3": CommandRiskMutating, "node": CommandRiskMutating,
	"ruby": CommandRiskMutating, "perl": CommandRiskMutating, "java": CommandRiskMutating,
	"dotnet": CommandRiskMutating, "go": CommandRiskMutating, "npm": CommandRiskMutating,
	"npx": CommandRiskMutating, "pip": CommandRiskMutating, "pip3": CommandRiskMutating,
	"cargo": CommandRiskMutating, "make": CommandRiskMutating, "cmake": CommandRiskMutating,
	"docker": CommandRiskMutating, "kubectl": CommandRiskMutating, "git": CommandRiskMutating,

	// destructive：删除或覆写数据，通常不可恢复。
	"rm": CommandRiskDestructive, "rmdir": CommandRiskDestructive,
	"unlink": CommandRiskDestructive, "shred": CommandRiskDestructive,
	"truncate": CommandRiskDestructive, "dd": CommandRiskDestructive,
	"mkfs": CommandRiskDestructive, "format": CommandRiskDestructive,
	"del": CommandRiskDestructive, "erase": CommandRiskDestructive,
	"remove-item": CommandRiskDestructive, "ri": CommandRiskDestructive,
}

// commandSubcommandRisk 按"主命令 + 第二 token"细化风险。只读子命令（git
// status、go test 等）刻意不随主命令的默认 mutating 一起升级；破坏性子命令
// （git reset --hard、git clean 等）则覆盖主命令的默认值。
var commandSubcommandRisk = map[string]map[string]CommandRisk{
	"git": {
		"status": CommandRiskReadOnly, "log": CommandRiskReadOnly, "diff": CommandRiskReadOnly,
		"show": CommandRiskReadOnly, "branch": CommandRiskReadOnly, "remote": CommandRiskReadOnly,
		"tag": CommandRiskReadOnly, "ls-files": CommandRiskReadOnly, "blame": CommandRiskReadOnly,
		"rev-parse": CommandRiskReadOnly, "describe": CommandRiskReadOnly,
		"add": CommandRiskMutating, "commit": CommandRiskMutating, "push": CommandRiskMutating,
		"fetch": CommandRiskMutating, "pull": CommandRiskMutating, "checkout": CommandRiskMutating,
		"merge": CommandRiskMutating, "rebase": CommandRiskMutating, "stash": CommandRiskMutating,
		"restore": CommandRiskMutating, "revert": CommandRiskMutating, "cherry-pick": CommandRiskMutating,
		"apply": CommandRiskMutating, "am": CommandRiskMutating, "config": CommandRiskMutating,
		"reset": CommandRiskDestructive, "clean": CommandRiskDestructive, "rm": CommandRiskDestructive,
	},
	"go": {
		"test": CommandRiskReadOnly, "vet": CommandRiskReadOnly, "list": CommandRiskReadOnly,
		"version": CommandRiskReadOnly, "env": CommandRiskReadOnly, "doc": CommandRiskReadOnly,
		"build": CommandRiskMutating, "install": CommandRiskMutating, "get": CommandRiskMutating,
		"mod": CommandRiskMutating, "fmt": CommandRiskMutating, "generate": CommandRiskMutating,
		"clean": CommandRiskDestructive,
	},
	"npm": {
		"view": CommandRiskReadOnly, "list": CommandRiskReadOnly, "ls": CommandRiskReadOnly,
		"outdated": CommandRiskReadOnly, "audit": CommandRiskReadOnly,
		"install": CommandRiskMutating, "i": CommandRiskMutating, "ci": CommandRiskMutating,
		"add": CommandRiskMutating, "remove": CommandRiskMutating, "rm": CommandRiskMutating,
		"uninstall": CommandRiskMutating, "update": CommandRiskMutating, "publish": CommandRiskMutating,
		"run": CommandRiskMutating, "test": CommandRiskMutating,
	},
	"pip":   {"install": CommandRiskMutating, "uninstall": CommandRiskMutating, "list": CommandRiskReadOnly, "freeze": CommandRiskReadOnly, "show": CommandRiskReadOnly},
	"pip3":  {"install": CommandRiskMutating, "uninstall": CommandRiskMutating, "list": CommandRiskReadOnly, "freeze": CommandRiskReadOnly, "show": CommandRiskReadOnly},
	"cargo": {"build": CommandRiskMutating, "install": CommandRiskMutating, "add": CommandRiskMutating, "remove": CommandRiskMutating, "update": CommandRiskMutating, "publish": CommandRiskMutating, "fmt": CommandRiskMutating, "clean": CommandRiskDestructive, "check": CommandRiskReadOnly, "test": CommandRiskReadOnly},
}

// ClassifyCommand 对 command 做静态风险分类，返回一个 CommandRisk。
//
// 它是纯函数：不执行、不写文件、不读环境变量（同包 DetectThreat 与清洗器也
// 不读），不修改入参，对任意字节都不 panic。分类顺序是：
//
//  1. 空命令 → benign；
//  2. 同包高危威胁检测命中（curl|sh、反弹 shell、根目录 rm -rf 等）→ forbidden；
//  3. 遮盖引号字符串、heredoc 与注释后，剥掉 sudo/env 等包装器，按 shell
//     分隔符拆段，逐段按命令名与子命令取最严格等级；
//  4. shell 元字符启发式：输出重定向、命令替换与"管道进 shell"至少提升到
//     mutating，其中"管道进 shell"直接判定为 forbidden；
//  5. 未被任何表识别的命令名保守地落到 mutating。
//
// 清洗让字符串里的字面量 "rm" 不产生误报；CJK 参数（如 echo 你好）不会破坏
// 分词，也不会按字节被截断出半个 UTF-8 字符。
func ClassifyCommand(command string) CommandRisk {
	if strings.TrimSpace(command) == "" {
		return CommandRiskBenign
	}

	// 已知高危模式优先。DetectThreat 与 ClassifyCommand 共享同一套感知 token
	// 清洗器，故引号内的字面量不会误报，而 shell -c/trap 载荷会被递归检查。
	if m := DetectThreat(command); m.IsHigh() {
		return CommandRiskForbidden
	}

	cleaned := sanitizeCommand(command)
	reduced, _ := reduceWrappers(cleaned)

	risk := CommandRiskBenign
	for _, segment := range splitCommandSegments(reduced) {
		segmentRisk := classifyCommandSegment(segment)
		if commandRiskSeverity(segmentRisk) > commandRiskSeverity(risk) {
			risk = segmentRisk
		}
	}

	// 管道进 shell 是把任意字节当脚本执行，明确归类为 forbidden。
	if pipesIntoShell(cleaned) {
		return CommandRiskForbidden
	}

	// 其余元字符只做保守提升，不会把已知的可信命令降到更低等级。
	if commandRiskSeverity(risk) < commandRiskSeverity(CommandRiskMutating) {
		if hasOutputRedirection(cleaned) || hasCommandSubstitution(command) {
			risk = CommandRiskMutating
		}
	}
	return risk
}

// splitCommandSegments 与 splitOnSeparators 类似，但不把文件描述符重定向里的
// '&'（如 2>&1、&>out）当作命令分隔符。否则 "echo hi 2>&1" 会被拆出一段孤立
// 的 "1"，被未知命令规则保守地升级为 mutating，造成只读命令误报。
func splitCommandSegments(s string) []string {
	out := []string{}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '&' && i > 0 && s[i-1] == '>' { // 2>&1 之类
			b.WriteByte(c)
			continue
		}
		if c == '&' && i+1 < len(s) && s[i+1] == '>' { // &>out 之类
			b.WriteByte(c)
			continue
		}
		switch c {
		case ';', '&', '|', '(', ')', '\n', '\r':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(out, b.String())
}

// classifyCommandSegment 对单条（已剥包装器的）命令取基础风险。空段为
// benign；未知命令名保守地归为 mutating。
func classifyCommandSegment(segment string) CommandRisk {
	fields := strings.Fields(segment)
	if len(fields) == 0 {
		return CommandRiskBenign
	}
	// 跳过开头的 VAR=value 赋值，找到真正要执行的名字。
	cmdAt := 0
	for cmdAt < len(fields) && isAssignmentWord(fields[cmdAt]) {
		cmdAt++
	}
	if cmdAt >= len(fields) {
		return CommandRiskBenign
	}

	exe := normalizeCommandName(fields[cmdAt])

	risk, known := commandNameRisk[exe]
	// mkfs.*（mkfs.ext4、mkfs.vfat 等）与 format.* 统一按破坏性命令处理。
	if !known && (strings.HasPrefix(exe, "mkfs.") || strings.HasPrefix(exe, "format.")) {
		risk, known = CommandRiskDestructive, true
	}

	// 已知工具的只读/破坏性子命令覆盖其默认值；未知子命令沿用默认值。
	if subs, ok := commandSubcommandRisk[exe]; ok && cmdAt+1 < len(fields) {
		if subRisk, ok := subs[strings.ToLower(fields[cmdAt+1])]; ok {
			risk = subRisk
			known = true
		}
	}

	// curl/wget 带写盘或请求体选项时不再是只读。
	if (exe == "curl" || exe == "wget") && networkFetchWrites(fields[cmdAt+1:]) {
		risk = CommandRiskMutating
	}

	if !known {
		return CommandRiskMutating // 失败即保守
	}
	return risk
}

// normalizeCommandName 把路径与 Windows 扩展名归一到裸命令名：/usr/bin/rm →
// rm、C:\Windows\System32\cmd.exe → cmd、Remove-Item.ps1 → remove-item。
func normalizeCommandName(field string) string {
	exe := strings.ToLower(field)
	if idx := strings.LastIndexAny(exe, `/\`); idx >= 0 {
		exe = exe[idx+1:]
	}
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".ps1", ".com"} {
		exe = strings.TrimSuffix(exe, suffix)
	}
	// 工具型点号后缀：mkfs.ext4 / fsck.ext4 归到主命令 mkfs/fsck，
	// 使基于命令名的分级与细分不会因为文件系统类型后缀而漏判。
	if i := strings.IndexByte(exe, '.'); i > 0 {
		head := exe[:i]
		switch head {
		case "mkfs", "fsck", "mkswap", "fdisk":
			exe = head
		}
	}
	return exe
}

// networkFetchWrites 报告 curl/wget 的参数是否包含写盘或带请求体的选项。
func networkFetchWrites(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		switch lower {
		case "-o", "--output", "-O", "--output-document", "-d", "--data", "--data-raw",
			"-T", "--upload-file", "-F", "--form", "-X", "--request":
			return true
		}
		// -XPOST / --request=POST 形式也按写处理。
		if strings.HasPrefix(lower, "--request=") || strings.HasPrefix(lower, "--output=") ||
			strings.HasPrefix(lower, "--data") || strings.HasPrefix(lower, "--upload-file=") {
			return true
		}
	}
	return false
}

// hasOutputRedirection 检查清洗后的命令是否把输出写入真实文件。>/dev/null
// 与 2>&1 这类不写文件的形式会被忽略；其余重定向至少意味着状态改变。
func hasOutputRedirection(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '>' {
			continue
		}
		j := i + 1
		for j < len(s) && s[j] == '>' { // >> 追加重定向
			j++
		}
		if j >= len(s) {
			continue
		}
		// 跳过目标前的空白。
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j >= len(s) {
			continue
		}
		if s[j] == '&' { // 2>&1 之类，不是文件写入
			continue
		}
		// 解析目标 token（到空白或分隔符为止）。
		end := j
		for end < len(s) && !isSeparatorByte(s[end]) {
			end++
		}
		target := strings.ToLower(strings.Trim(s[j:end], `"'`))
		switch target {
		case "", "null", "/dev/null", "nul", "nul:":
			continue
		}
		return true
	}
	return false
}

// hasCommandSubstitution 在原始命令上检测命令替换。$() 会被 splitOnSeparators
// 按括号拆开而反引号会被 sanitizeCommand 遮盖，因此这里显式检测两种写法，
// 确保被隐藏的载荷至少被归类为 mutating（失败即保守）。
func hasCommandSubstitution(command string) bool {
	return strings.Contains(command, "`") || strings.Contains(command, "$(")
}

// pipesIntoShell 报告是否存在"管道进 shell"结构（... | sh / bash / dash ...）。
// 入参应已通过 sanitizeCommand，使引号内的字面竖线不产生误报。
func pipesIntoShell(s string) bool {
	parts := strings.Split(s, "|")
	for i := 1; i < len(parts); i++ {
		fields := strings.Fields(parts[i])
		if len(fields) == 0 {
			continue
		}
		switch normalizeCommandName(fields[0]) {
		case "sh", "bash", "dash", "zsh", "ksh", "ash":
			return true
		}
	}
	return false
}

// IsReadOnlyCommand 报告 command 是否为只读命令（benign 或 read-only）。
// 未知命令一律返回 false，保持失败即保守。
func IsReadOnlyCommand(command string) bool {
	r := ClassifyCommand(command)
	return r == CommandRiskBenign || r == CommandRiskReadOnly
}

// RequiresApprovalRisk 报告风险等级是否需要审批：mutating 及以上为 true。
// 未知等级同样返回 true（失败即保守）。
func RequiresApprovalRisk(r CommandRisk) bool {
	return commandRiskSeverity(r) >= commandRiskSeverity(CommandRiskMutating)
}
