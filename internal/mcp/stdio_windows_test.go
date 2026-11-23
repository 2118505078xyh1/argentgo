package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecSpecForOSPassesThroughNonWindowsAndNativeCommands(t *testing.T) {
	// 非 Windows 宿主必须完全不受影响。
	name, args := execSpecForOS("linux", "npx", []string{"-y", "server"})
	if name != "npx" || !reflect.DeepEqual(args, []string{"-y", "server"}) {
		t.Fatalf("non-Windows command changed: %q %v", name, args)
	}

	// 在 Windows 上，普通可执行文件或 .cmd 包装已由 Go 处理。
	name, args = execSpecForOS("windows", "node.exe", []string{"server.js"})
	if name != "node.exe" || !reflect.DeepEqual(args, []string{"server.js"}) {
		t.Fatalf("native command changed: %q %v", name, args)
	}
}

func TestExecSpecForOSWrapsPowerShellScript(t *testing.T) {
	for _, script := range []string{`C:\tools\server.ps1`, `.\server.PS1`, "server.ps1"} {
		name, args := execSpecForOS("windows", script, []string{"--stdio", "a b"})
		if name != "powershell.exe" {
			t.Fatalf("%q: executable = %q, want powershell.exe", script, name)
		}
		want := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "--stdio", "a b"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("%q: args = %v, want %v", script, args, want)
		}
	}
}

func TestExecSpecForOSResolvesBarePS1OnPath(t *testing.T) {
	dir := t.TempDir()
	const name = "mcp-only-ps1-tool"
	if err := os.WriteFile(filepath.Join(dir, name+".ps1"), []byte("Write-Output ok\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("PATH", dir)

	exe, args := execSpecForOS("windows", name, []string{"run"})
	if exe != "powershell.exe" {
		t.Fatalf("executable = %q, want powershell.exe", exe)
	}
	if len(args) != 6 || args[4] != filepath.Join(dir, name+".ps1") || args[5] != "run" {
		t.Fatalf("bare .ps1 was not resolved on PATH: %v", args)
	}
}

func TestExecSpecForOSLeavesUnresolvableCommandAlone(t *testing.T) {
	// PATH 上任何地方都没有 .ps1：照常往下走，让 Connect 报告通常的
	// "executable file not found" 错误，而不是凭空造一个包装器。
	t.Setenv("PATH", t.TempDir())
	name, args := execSpecForOS("windows", "no-such-mcp-tool-xyz", []string{"x"})
	if name != "no-such-mcp-tool-xyz" || !reflect.DeepEqual(args, []string{"x"}) {
		t.Fatalf("unresolvable command changed: %q %v", name, args)
	}
}

// ps1MCPServer 是一个最小的基于 stdio 的 JSON-RPC 服务器，用于证明
// .ps1 命令在 Windows 上确实可以作为 MCP 服务器启动。
const ps1MCPServer = `
while ($true) {
    $line = [Console]::In.ReadLine()
    if ($null -eq $line) { break }
    $line = $line.Trim()
    if ($line.Length -eq 0) { continue }
    try {
        $msg = $line | ConvertFrom-Json
        if ($null -eq $msg.id) { continue }
        $result = @{
            protocolVersion = "2024-11-05"
            serverInfo = @{ name = "ps1fake"; version = "1.0" }
            capabilities = @{}
        }
        $response = @{ jsonrpc = "2.0"; id = $msg.id; result = $result }
        [Console]::Out.WriteLine(($response | ConvertTo-Json -Compress -Depth 8))
        [Console]::Out.Flush()
    } catch {
        break
    }
}
`

func TestStdioTransportLaunchesPowerShellScript(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell scripts are a Windows-only concern")
	}
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skipf("powershell.exe is not available: %v", err)
	}

	script := filepath.Join(t.TempDir(), "server.ps1")
	if err := os.WriteFile(script, []byte(ps1MCPServer), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	c, err := Connect(context.Background(), Spec{
		Name:      "ps1",
		Transport: "stdio",
		Command:   script,
		// CI Windows runner 上 PowerShell 冷启动 + 脚本编译可能超过 10s，
		// 放宽到 30s 避免慢机器误报；本地仍快速通过。
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect to .ps1 server: %v", err)
	}
	defer c.Close()

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize .ps1 server: %v", err)
	}
	if info.Name != "ps1fake" || info.ProtocolVersion != "2024-11-05" {
		t.Fatalf("unexpected server info: %+v", info)
	}
	if tail := Stderr(c); strings.Contains(tail, "not a valid Win32") {
		t.Fatalf("PowerShell wrapper failed: %q", tail)
	}
}
