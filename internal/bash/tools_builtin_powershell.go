package bash

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/2118505078xyh1/argentgo/internal/proc"
)

// ---- powershell ----

// powerShellFixedPath 是 Windows 上 PowerShell 7 的默认安装位置。
// 只有在两次 PATH 查找都失败后才会查询它，因此 scoop/winget/用户级
// 安装（它们把 pwsh 放进 PATH 但没放进这里）优先于这个固定路径。
const powerShellFixedPath = `C:\Program Files\PowerShell\7\pwsh.exe`

// powerShellUtf8Prefix 会在每段脚本被 UTF-16LE 编码进
// -EncodedCommand 之前前置。Windows PowerShell 5.1 的控制台默认使用
// OEM 代码页，会把非 ASCII 输出（如中文）弄成 '?'；
// 把控制台的输出编码和 $OutputEncoding（管道与原生命令通信所用）
// 都固定为 UTF-8 即可解决。PowerShell 7
// 已经默认使用 UTF-8，因此这些赋值在那里无害。
//
// 前缀放在脚本内部，而不是单独的 -Command
// 参数里，使整个载荷保持为一个编码参数，任何外层 shell 都
// 无法重新解释。这与 dsh 的做法一致；代价是首条语句为 `using` 或
// `param` 的脚本会被推离第一行，
// 而 PowerShell 会拒绝这种情况 —— 对 agent 生成的脚本可以接受。
const powerShellUtf8Prefix = "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $OutputEncoding=[Text.Encoding]::UTF8; "

// powerShellEnvOverrides 返回由 proc.Run 追加到
// 继承环境中的每次调用环境。NO_COLOR 和 pager 覆盖让捕获到的
// 输出具有确定性：没有 ANSI 颜色码，也没有在 stdin 上等待的交互式 pager，
// 否则后者会扰乱或拖住解析结果。
func powerShellEnvOverrides() []string {
	return []string{
		"NO_COLOR=1",
		"PAGER=cat",
		"GIT_PAGER=cat",
	}
}

// resolvePowerShellProgram 查找 PowerShell 可执行文件，优先使用 PATH。
// 把查找顺序集中在一个小函数里，使其可以通过 PATH 上的假
// 可执行文件来测试，而无需真实安装 PowerShell。
func resolvePowerShellProgram() (string, error) {
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return path, nil
	}
	if _, err := os.Stat(powerShellFixedPath); err == nil {
		return powerShellFixedPath, nil
	}
	return "", fmt.Errorf("powershell: no PowerShell found (tried pwsh and powershell on PATH, then %s)", powerShellFixedPath)
}

// encodePowerShellCommand 是如下 Go 等价实现：
// base64.StdEncoding.EncodeToString(utf16.EncodeToLE([]byte(script)))。
// Go 1.27 的标准库没有 utf16.EncodeToLE，因此把
// utf16.Encode 得到的 UTF-16 码元按小端序显式写出。
//
// 脚本作为单个 -EncodedCommand 参数传入，而不是像 rpi 那样暂存
// 到临时 .ps1 文件。这偏离了 rpi 的包装方式
// （后者可避免首条语句为 `using` 或 `param` 的脚本出现 ParserError，
// 见上），但让本工具不依赖任何东西，也不留下临时文件
// 需要清理；PowerShell 的 -EncodedCommand 接受任何完整脚本。
func encodePowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// powershellArgs 构建通过 PowerShell 运行脚本的 argv。
// UTF-8 前缀和调用方的脚本被一起编码，因此引号、
// 换行和主机路径字符都不会被外层 shell 重新解释。
func powershellArgs(script string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShellCommand(powerShellUtf8Prefix + script)}
}

// powershellProcCommand 构建 Run 执行的 proc.Command。它与 Run 分开
// 保留，使 UTF-8 前缀和环境覆盖可以在
// 单元测试中验证，而无需真实安装 PowerShell。
func powershellProcCommand(program, script, root string) proc.Command {
	return proc.Command{
		Path:     program,
		Args:     powershellArgs(script),
		Dir:      root,
		Env:      powerShellEnvOverrides(),
		KillTree: true,
		NoWindow: runtime.GOOS == "windows",
	}
}

type powershellTool struct{}

func (powershellTool) Name() string { return "powershell" }

func (powershellTool) Description() string {
	return "Execute a PowerShell script on Windows (pwsh 7 or Windows PowerShell). " +
		"Use it for PowerShell object operations the shell cannot do — registry, COM/WMI, " +
		"scheduled tasks and .NET. Prefer bash for file and text work. " +
		"Returns stdout and stderr, clipped to the output limit. " +
		"A non-zero exit code is a command result, not a tool failure: read the output before deciding whether to retry."
}

func (powershellTool) Mutating() bool { return true }

func (powershellTool) Schema() string {
	return `{"type":"object","properties":{"command":{"type":"string","description":"The PowerShell script to execute."},"script":{"type":"string","description":"Alias for command."},"timeout_seconds":{"type":"integer","description":"Timeout in seconds; defaults to the shell timeout."}},"anyOf":[{"required":["command"]},{"required":["script"]}]}`
}

func (powershellTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Command        string `json:"command"`
		Script         string `json:"script"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("powershell: bad arguments: %w", err)
	}
	script := args.Command
	if strings.TrimSpace(script) == "" {
		script = args.Script
	}
	if strings.TrimSpace(script) == "" {
		return "", fmt.Errorf("powershell: command must not be empty")
	}

	program, err := resolvePowerShellProgram()
	if err != nil {
		return "", err
	}

	var requested time.Duration
	if args.TimeoutSeconds > 0 {
		requested = time.Duration(args.TimeoutSeconds) * time.Second
	}
	// 复用 shell 预算，使 PowerShell 遵守与 bash 相同的默认值、下限、
	// 上限和注册表级上限。守卫与继承环境
	// 分别由注册表和 proc.Run 处理，与 bash 完全一致。
	timeout := clampTimeout("bash", requested, maxTimeout(ctx))
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, runErr := proc.Run(runCtx, powershellProcCommand(program, script, root))

	text := sanitizeShellOutput(res.Combined())
	if res.ExitCode != 0 {
		text += fmt.Sprintf("\nPowerShell exited with code %d", res.ExitCode)
	}
	text = clip(ctx, text)
	if errors.Is(runErr, context.DeadlineExceeded) {
		return text, fmt.Errorf("powershell: timed out after %s", timeout)
	}
	if runErr != nil {
		return text, fmt.Errorf("powershell: %w", runErr)
	}
	if strings.TrimSpace(text) == "" {
		return "(no output)", nil
	}
	return text, nil
}
