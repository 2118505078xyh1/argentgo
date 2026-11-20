package tools

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

// powerShellFixedPath is the default PowerShell 7 install location on Windows.
// It is only consulted after both PATH lookups fail, so scoop/winget/user
// installs (which put pwsh on PATH but not here) win over this fixed path.
const powerShellFixedPath = `C:\Program Files\PowerShell\7\pwsh.exe`

// powerShellUtf8Prefix is prepended to every script before it is UTF-16LE
// encoded into -EncodedCommand. Windows PowerShell 5.1 defaults its console to
// the OEM code page, which mangles non-ASCII output (e.g. Chinese) into '?';
// pinning both the console's output encoding and $OutputEncoding (what the
// pipeline uses to talk to native commands) to UTF-8 fixes that. PowerShell 7
// already defaults to UTF-8, so the assignments are harmless there.
//
// The prefix lives inside the script rather than in a separate -Command
// argument so the whole payload stays one encoded argument that no outer shell
// can reinterpret. This mirrors dsh; the tradeoff is that a script whose very
// first statement is `using` or `param` would be pushed off the first line,
// which PowerShell rejects — acceptable for agent-generated scripts.
const powerShellUtf8Prefix = "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $OutputEncoding=[Text.Encoding]::UTF8; "

// powerShellEnvOverrides returns the per-call environment appended to the
// inherited one by proc.Run. NO_COLOR and the pager overrides keep captured
// output deterministic: no ANSI colour codes and no interactive pager waiting
// on stdin, which would otherwise garble or stall the parsed result.
func powerShellEnvOverrides() []string {
	return []string{
		"NO_COLOR=1",
		"PAGER=cat",
		"GIT_PAGER=cat",
	}
}

// resolvePowerShellProgram finds a PowerShell executable, preferring PATH.
// Keeping the lookup order in one small function makes it testable with a fake
// executable on PATH without requiring a real PowerShell installation.
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

// encodePowerShellCommand is the Go equivalent of
// base64.StdEncoding.EncodeToString(utf16.EncodeToLE([]byte(script))).
// Go 1.27's stdlib has no utf16.EncodeToLE, so the UTF-16 code units from
// utf16.Encode are written explicitly in little-endian order.
//
// The script is passed as one -EncodedCommand argument instead of being staged
// in a temp .ps1 file like rpi does. That deviates from rpi's wrapper approach
// (which avoids ParserError for scripts whose first statement is `using` or
// `param`) but keeps this tool dependency-free and leaves no temp files to
// clean up; PowerShell's -EncodedCommand accepts any complete script.
func encodePowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// powershellArgs builds the argv that runs script through PowerShell. The
// UTF-8 prefix and the caller's script are encoded together, so quotes,
// newlines and host-path characters cannot be reinterpreted by an outer shell.
func powershellArgs(script string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShellCommand(powerShellUtf8Prefix + script)}
}

// powershellProcCommand builds the proc.Command that Run executes. Kept apart
// from Run so the UTF-8 prefix and the environment overrides are verifiable in
// a unit test without needing a real PowerShell installation.
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
	// Reuse the shell budget so PowerShell obeys the same default, floor,
	// ceiling and registry-wide cap as bash. The guard and inherited environment
	// are handled by the registry and proc.Run respectively, exactly as for bash.
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
