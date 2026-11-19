package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

// putFakePowerShellOnPATH creates a harmless file named pwsh(.exe) in a temp
// directory and makes that the only PATH entry. exec.LookPath finds it without
// ever executing it, so the resolution order is testable on any host.
func putFakePowerShellOnPATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "pwsh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return path
}

func TestResolvePowerShellProgramPrefersPATH(t *testing.T) {
	want := putFakePowerShellOnPATH(t)
	got, err := resolvePowerShellProgram()
	if err != nil {
		t.Fatalf("resolvePowerShellProgram: %v", err)
	}
	if got != want && !strings.EqualFold(got, want) {
		t.Fatalf("program = %q, want the PATH fake %q", got, want)
	}
}

func decodePowerShellEncodedCommand(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE payload has odd length %d", len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

func TestPowerShellEncodedCommandRoundTrip(t *testing.T) {
	// Quotes, newlines and non-ASCII text must survive the UTF-16LE encoding
	// byte-for-byte; that is what keeps a script safe behind -EncodedCommand.
	script := "Write-Output 'héllo'\r\nGet-ItemProperty 'HKCU:\\Software\\Example' | Select-Object -First 1"

	argv := powershellArgs(script)
	if len(argv) != 4 {
		t.Fatalf("args = %v, want four entries", argv)
	}
	if argv[0] != "-NoProfile" || argv[1] != "-NonInteractive" || argv[2] != "-EncodedCommand" {
		t.Fatalf("flags = %v, want -NoProfile -NonInteractive -EncodedCommand", argv[:3])
	}
	if got := decodePowerShellEncodedCommand(t, argv[3]); got != script {
		t.Fatalf("decoded script = %q, want %q", got, script)
	}
}

func TestPowerShellToolIsMutating(t *testing.T) {
	var tool powershellTool
	if !tool.Mutating() {
		t.Fatal("powershell must be mutating so the security chain and read-only mode see it")
	}
}

func TestPowerShellToolMetadata(t *testing.T) {
	var tool powershellTool
	if tool.Name() != "powershell" {
		t.Fatalf("Name() = %q, want powershell", tool.Name())
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Fatal("Description() is empty")
	}
	if !strings.Contains(tool.Description(), "Windows") {
		t.Fatal("Description() should say the tool runs on Windows")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(tool.Schema()), &schema); err != nil {
		t.Fatalf("Schema() is not valid JSON: %v", err)
	}
	if _, ok := schema.Properties["command"]; !ok {
		t.Fatalf("Schema() has no command property: %s", tool.Schema())
	}
	if _, ok := schema.Properties["script"]; !ok {
		t.Fatalf("Schema() has no script property: %s", tool.Schema())
	}
}

func TestPowerShellToolIsRegistered(t *testing.T) {
	registry := New(t.TempDir(), true)
	found := false
	for _, name := range registry.Names() {
		if name == "powershell" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("powershell is not in the built-in registry: %v", registry.Names())
	}
}

func TestPowerShellRunRejectsEmptyCommand(t *testing.T) {
	// This fails before program resolution, so it needs no PowerShell install.
	var tool powershellTool
	if _, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("Run accepted an empty command")
	}
}
