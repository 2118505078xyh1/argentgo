package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ProbeResult 是由 ProbeEnvironment 产生的一条结构化环境观测。Value 保存原始
// 观测值（绝不会从 UTF-8 rune 中间切开，因此中文值能完整保留），OK 报告该观测
// 是否健康，Detail 以人类可读文本解释结果。字段导出以便调用方和测试直接检查。
type ProbeResult struct {
	Name   string
	Value  string
	OK     bool
	Detail string
}

// 探测名称。保留为常量，使 doctorAdvice、格式化器和测试共享同一套词汇，
// 而不是重复字符串字面量。
const (
	probeGoVersion = "go_version"
	probePlatform  = "platform"
	probeShell     = "shell"
	probeTerminal  = "terminal"
	probeGit       = "git"
	probeEnv       = "env"
)

// envGetter 是 probeEnvironment 使用的注入接缝，使测试能在不改变进程环境
// 的情况下提供受控环境。
type envGetter func(string) string

// lookPathFunc 镜像 exec.LookPath，使 git 检测能在测试中被伪造。
type lookPathFunc func(string) (string, error)

// doctorKeyEnvVars 是 doctor 在 "env" 探测中报告其存在性的环境变量。
// 顺序固定，因此探测的 Value 是确定的。
var doctorKeyEnvVars = []string{"PATH", "HOME", "USERPROFILE", "TMPDIR", "TEMP", "TMP"}

// doctorEnvGroups 定义了 "env" 探测健康时必须存在的 "至少其一" 分组。
// 一个平台通常通过不同变量满足每组（POSIX 上是 HOME，Windows 上是
// USERPROFILE），因此用分组而不是扁平的必需列表。
var doctorEnvGroups = []struct {
	vars []string
	desc string
}{
	{vars: []string{"PATH"}, desc: "PATH"},
	{vars: []string{"HOME", "USERPROFILE"}, desc: "HOME/USERPROFILE"},
	{vars: []string{"TMPDIR", "TEMP", "TMP"}, desc: "TMPDIR/TEMP/TMP"},
}

// ProbeEnvironment 探测本地运行时和环境，返回一个稳定、有序的观测切片。
// 它不向 doctor 的输出流写入任何内容，也不改变进程状态；只读取运行时值、
// 进程环境和 git 查找结果。
func ProbeEnvironment() []ProbeResult {
	return probeEnvironment(os.Getenv, exec.LookPath)
}

// probeEnvironment 是 ProbeEnvironment 背后的纯核心。每一项外部输入都被注入，
// 因此结果在测试中完全确定。
func probeEnvironment(env envGetter, lookPath lookPathFunc) []ProbeResult {
	probes := make([]ProbeResult, 0, 6)

	// Go 版本和目标平台是运行中二进制的属性，因此始终健康；
	// 报告它们纯粹是为了提供上下文。
	probes = append(probes, ProbeResult{
		Name:   probeGoVersion,
		Value:  runtime.Version(),
		OK:     true,
		Detail: "Go runtime version",
	})
	probes = append(probes, ProbeResult{
		Name:   probePlatform,
		Value:  runtime.GOOS + "/" + runtime.GOARCH,
		OK:     true,
		Detail: "GOOS/GOARCH",
	})

	// Shell：POSIX shell 上是 SHELL，Windows 上是 COMSPEC。我们存储名称
	// （最后一段路径），使值保持简短，且不受绝对路径差异影响。
	shellRaw := firstNonEmpty(env("SHELL"), env("COMSPEC"))
	shellOK := shellRaw != ""
	shellDetail := "shell detected"
	if !shellOK {
		shellDetail = "SHELL/COMSPEC not set"
	}
	probes = append(probes, ProbeResult{
		Name:   probeShell,
		Value:  shellName(shellRaw),
		OK:     shellOK,
		Detail: shellDetail,
	})

	// 终端：POSIX 终端上是 TERM，Windows Terminal 内是 WT_SESSION。
	term := env("TERM")
	wtSession := env("WT_SESSION")
	terminalValue := firstNonEmpty(term, wtSession)
	terminalOK := terminalValue != ""
	terminalDetail := "TERM/WT_SESSION detected"
	if !terminalOK {
		terminalDetail = "TERM/WT_SESSION not set (non-interactive?)"
	}
	probes = append(probes, ProbeResult{
		Name:   probeTerminal,
		Value:  terminalValue,
		OK:     terminalOK,
		Detail: terminalDetail,
	})

	// Git 可用性。与 checkGit 一致：缺少 git 是警告而非失败，
	// 因此这里 OK=false 绝不会改变 doctor 的退出码。
	gitPath, err := lookPath("git")
	gitOK := err == nil
	gitDetail := "git on PATH"
	if !gitOK {
		gitDetail = "git not found on PATH"
	}
	probes = append(probes, ProbeResult{
		Name:   probeGit,
		Value:  gitPath,
		OK:     gitOK,
		Detail: gitDetail,
	})

	// 关键环境变量的存在性。"present" 报告为一个稳定的逗号连接列表；
	// "healthy" 要求每个分组至少设置了一个成员。
	present := make([]string, 0, len(doctorKeyEnvVars))
	for _, key := range doctorKeyEnvVars {
		if isSet(env(key)) {
			present = append(present, key)
		}
	}
	missing := make([]string, 0, len(doctorEnvGroups))
	for _, group := range doctorEnvGroups {
		satisfied := false
		for _, key := range group.vars {
			if isSet(env(key)) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, group.desc)
		}
	}
	envDetail := "all key variables present"
	if len(missing) > 0 {
		envDetail = "missing groups: " + strings.Join(missing, ", ")
	}
	probes = append(probes, ProbeResult{
		Name:   probeEnv,
		Value:  strings.Join(present, ","),
		OK:     len(missing) == 0,
		Detail: envDetail,
	})

	return probes
}

// shellName 返回 shell 值的最后一段路径。它显式地同时按 POSIX 和 Windows
// 分隔符切分（而不是用 filepath.Base），使结果在每台主机上都相同，从而在
// 跨平台测试中保持探测输出稳定。只在 ASCII 分隔符处切分是 rune 安全的。
func shellName(value string) string {
	if i := strings.LastIndexAny(value, `/\`); i >= 0 {
		return value[i+1:]
	}
	return value
}

func isSet(value string) bool {
	return strings.TrimSpace(value) != ""
}

// doctorAdvice 把不健康的探测结果转换为人类可读的修复建议。
// 输出遵循探测顺序，因此是确定的；健康的探测不产生建议。
func doctorAdvice(probes []ProbeResult) []string {
	var advice []string
	for _, p := range probes {
		if p.OK {
			continue
		}
		switch p.Name {
		case probeShell:
			advice = append(advice, "shell: set SHELL (POSIX) or COMSPEC (Windows) so the shell can be identified")
		case probeTerminal:
			advice = append(advice, "terminal: TERM/WT_SESSION is unset; run inside a real terminal for full TUI support")
		case probeGit:
			advice = append(advice, "git: install Git and add it to PATH (https://git-scm.com)")
		case probeEnv:
			advice = append(advice, "env: set the missing key variables ("+p.Detail+")")
		default:
			advice = append(advice, fmt.Sprintf("%s: review this environment probe", p.Name))
		}
	}
	return advice
}

// writeDoctorEnvironment 把结构化环境小节追加到 doctor 的输出。
// 它有意最后写入，使每一条已有的 doctor 行都保持为新输出逐字节一致的前缀。
// fmt 的 %-Ns 填充按 rune 计数，因此中文值和详情绝不会在 rune 中间被截断。
func writeDoctorEnvironment(w io.Writer, probes []ProbeResult, advice []string) {
	fmt.Fprintln(w, "\nenvironment:")
	for _, p := range probes {
		status := statusWarn
		if p.OK {
			status = statusOK
		}
		fmt.Fprintf(w, "  %-4s %-10s %s %s\n", status, p.Name, p.Value, p.Detail)
	}
	if len(advice) > 0 {
		fmt.Fprintln(w, "  advice:")
		for _, line := range advice {
			fmt.Fprintf(w, "    - %s\n", line)
		}
	}
}
