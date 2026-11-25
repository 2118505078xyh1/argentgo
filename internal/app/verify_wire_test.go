package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/config"
)

// TestValidateVerifyArgvAllowlist 锁定 eval exec 白名单的本地副本：只接受 `go test|build ...` 和相对的 .exe，
// 每一种已知的逃逸（shell、绝对路径、遍历、元字符、-exec/-toolexec）都被拒绝。
// （接上行）
// （接上行）
func TestValidateVerifyArgvAllowlist(t *testing.T) {
	tests := []struct {
		name   string
		argv   []string
		wantOK bool
	}{
		{"go test", []string{"go", "test", "./..."}, true},
		{"go build", []string{"go", "build", "./..."}, true},
		{"relative exe backslash", []string{`tools\check.exe`}, true},
		{"relative exe slash with args", []string{"tools/check.exe", "--quiet"}, true},
		{"empty", nil, false},
		{"empty command", []string{"  "}, false},
		{"sh", []string{"sh", "-c", "echo hi"}, false},
		{"bash", []string{"bash", "script.sh"}, false},
		{"curl", []string{"curl", "http://example.com/x"}, false},
		{"absolute windows", []string{`C:\Windows\System32\cmd.exe`}, false},
		{"absolute unix", []string{"/usr/bin/go", "test", "./..."}, false},
		{"traversal", []string{"../evil.exe"}, false},
		{"nested traversal", []string{"bin/../evil.exe"}, false},
		{"metachar semicolon", []string{"go", "test", "./...;echo hi"}, false},
		{"metachar pipe", []string{"go", "test", "./...|cat"}, false},
		{"metachar substitution", []string{"go", "test", "$(whoami)"}, false},
		{"exec flag", []string{"go", "test", "-exec", "evil"}, false},
		{"toolexec flag", []string{"go", "build", "-toolexec", "evil"}, false},
		{"toolexec equals", []string{"go", "test", "-toolexec=evil"}, false},
		{"go run", []string{"go", "run", "."}, false},
		{"go without subcommand", []string{"go"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVerifyArgv(tc.argv)
			if tc.wantOK && err != nil {
				t.Fatalf("validateVerifyArgv(%q) = %v, want allowed", tc.argv, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("validateVerifyArgv(%q) = nil, want rejected", tc.argv)
			}
		})
	}
}

// TestNewVerifyFuncMisconfigurationIsUnrunnable 检查空命令或被拒绝的命令会产生 ok=false，
// 使循环不注入任何反馈。
func TestNewVerifyFuncMisconfigurationIsUnrunnable(t *testing.T) {
	for _, argv := range [][]string{nil, {"bash", "-c", "echo hi"}} {
		verify := NewVerifyFunc(argv, 0, t.TempDir())
		label, passed, output, ok := verify(context.Background(), []string{"main.go"})
		if ok {
			t.Errorf("argv %q: ok = true, want false", argv)
		}
		if passed {
			t.Errorf("argv %q: passed = true, want false", argv)
		}
		if output == "" {
			t.Errorf("argv %q: empty rejection reason", argv)
		}
		if len(argv) > 0 && label == "" {
			t.Errorf("argv %q: empty label", argv)
		}
	}
}

func TestVerifyEnvironmentStripsGOFLAGS(t *testing.T) {
	t.Setenv("GOFLAGS", "-toolexec=evil")
	for _, entry := range verifyEnvironment() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			t.Fatalf("verifyEnvironment leaked %q", entry)
		}
	}
}

// verifyProbeProgram 是一个用来在不使用 shell 的情况下演练运行器的微型临时 Go 模块。
// 模式：默认退出 0，"fail" 退出 1 并带一个植入的机密，
// "huge" 写入 200KB，"sleep" 阻塞直到被截止时间杀掉。
const verifyProbeProgram = `package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "fail":
			fmt.Println("BOOM sk-abcdefghijklmnop1234")
			os.Exit(1)
		case "huge":
			fmt.Println(strings.Repeat("x", 200000))
			return
		case "sleep":
			time.Sleep(30 * time.Second)
			return
		}
	}
	fmt.Println("PASS")
}
`

// buildVerifyProbe 在工作区内构建探针并返回其相对 .exe 名称。
// Go 工具链不可用时跳过。
func buildVerifyProbe(t *testing.T, workspace string) string {
	t.Helper()
	writeVerifyFile(t, workspace, "go.mod", "module verifyprobe\n\ngo 1.21\n")
	writeVerifyFile(t, workspace, "main.go", verifyProbeProgram)

	const exe = "verifyprobe.exe"
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = workspace
	cmd.Env = verifyEnvironment()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Go toolchain unavailable for probe build: %v: %s", err, out)
	}
	return exe
}

func writeVerifyFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestNewVerifyFuncOutcomes 通过从微型临时 Go 模块构建的 .exe，演练通过、失败（带脱敏）、有界输出和超时。
// （接上行）
func TestNewVerifyFuncOutcomes(t *testing.T) {
	workspace := t.TempDir()
	exe := buildVerifyProbe(t, workspace)

	t.Run("pass", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe}, 5000, workspace)
		label, passed, output, ok := verify(context.Background(), nil)
		if !ok || !passed {
			t.Fatalf("got (label=%q, passed=%v, output=%q, ok=%v), want a passing verdict", label, passed, output, ok)
		}
		if label != exe || !strings.Contains(output, "PASS") {
			t.Fatalf("label = %q, output = %q", label, output)
		}
	})

	t.Run("fail is redacted", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe, "fail"}, 5000, workspace)
		_, passed, output, ok := verify(context.Background(), nil)
		if !ok || passed {
			t.Fatalf("got (passed=%v, output=%q, ok=%v), want a failing verdict", passed, output, ok)
		}
		if !strings.Contains(output, "BOOM") {
			t.Fatalf("output lost failure context: %q", output)
		}
		if strings.Contains(output, "sk-abcdefghijklmnop1234") {
			t.Fatalf("output leaked planted secret: %q", output)
		}
	})

	t.Run("output is bounded", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe, "huge"}, 5000, workspace)
		_, passed, output, ok := verify(context.Background(), nil)
		if !ok || !passed {
			t.Fatalf("got (passed=%v, ok=%v), want a passing verdict", passed, ok)
		}
		if len(output) > verifyOutputCap+len(verifyTruncMarker) {
			t.Fatalf("output length = %d, want <= %d", len(output), verifyOutputCap+len(verifyTruncMarker))
		}
		if !strings.Contains(output, verifyTruncMarker) {
			t.Fatalf("large output was not marked as truncated")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe, "sleep"}, 150, workspace)
		start := time.Now()
		_, passed, output, ok := verify(context.Background(), nil)
		if !ok || passed {
			t.Fatalf("got (passed=%v, output=%q, ok=%v), want a timed-out failure", passed, output, ok)
		}
		if !strings.Contains(output, "timed out") {
			t.Fatalf("timeout detail missing: %q", output)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("timeout took %s, want a bounded kill", elapsed)
		}
	})
}

// TestNewVerifyFuncAcceptsGoTest 用真实的（微型）临时模块演练 `go` 分支。
// go 不在 PATH 上时跳过。
func TestNewVerifyFuncAcceptsGoTest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	workspace := t.TempDir()
	writeVerifyFile(t, workspace, "go.mod", "module verifygo\n\ngo 1.21\n")
	writeVerifyFile(t, workspace, "main_test.go", "package verifygo\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")

	verify := NewVerifyFunc([]string{"go", "test", "./..."}, 120000, workspace)
	_, passed, output, ok := verify(context.Background(), nil)
	if !ok || !passed {
		t.Fatalf("go test ./... = (passed=%v, ok=%v, output=%q), want a passing verdict", passed, ok, output)
	}
}

func verifyTestEnv(home string) func(string) string {
	return func(key string) string {
		switch key {
		case "HOME", "USERPROFILE":
			return home
		}
		return ""
	}
}

// TestConfigDecodeVerifySettings 锁定文件层和覆盖层（CLI 标志的落点）的 config -> Config 字段解码。
// （接上行）
func TestConfigDecodeVerifySettings(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeVerifyFile(t, root, "argent.json",
		`{"verifyCommand":["go","test","./..."],"verifyTimeoutMS":1234,"verifyMaxReflections":5}`)

	cfg, err := config.Load(root, verifyTestEnv(home))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := strings.Join(cfg.VerifyCommand, " "); got != "go test ./..." {
		t.Fatalf("VerifyCommand = %v", cfg.VerifyCommand)
	}
	if cfg.VerifyTimeoutMS != 1234 {
		t.Fatalf("VerifyTimeoutMS = %d, want 1234", cfg.VerifyTimeoutMS)
	}
	if cfg.VerifyMaxReflections != 5 {
		t.Fatalf("VerifyMaxReflections = %d, want 5", cfg.VerifyMaxReflections)
	}

	// CLI 标志写入带类型的覆盖；同样的形状必须优先于文件。
	cfg, err = config.LoadWithOverrides(root, verifyTestEnv(home), map[string]any{
		"verifyCommand":   []any{"go", "build", "./..."},
		"verifyTimeoutMS": 4321,
	})
	if err != nil {
		t.Fatalf("config.LoadWithOverrides: %v", err)
	}
	if got := strings.Join(cfg.VerifyCommand, " "); got != "go build ./..." {
		t.Fatalf("override VerifyCommand = %v", cfg.VerifyCommand)
	}
	if cfg.VerifyTimeoutMS != 4321 {
		t.Fatalf("override VerifyTimeoutMS = %d, want 4321", cfg.VerifyTimeoutMS)
	}
}

func TestConfigVerifyDefaultsAreOptIn(t *testing.T) {
	cfg, err := config.Load(t.TempDir(), verifyTestEnv(t.TempDir()))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if len(cfg.VerifyCommand) != 0 {
		t.Fatalf("VerifyCommand = %v, want empty (feature off)", cfg.VerifyCommand)
	}
	if cfg.VerifyTimeoutMS != 30000 {
		t.Fatalf("VerifyTimeoutMS = %d, want 30000", cfg.VerifyTimeoutMS)
	}
}

// TestVerifyWiringDefaultOffAndConfigured 锁定 app_execute.go 中 Options 构造所用的辅助函数：
// 没有命令意味着 Verify 为 nil 且上限为零（Options 逐字节不变）；
// 配置的命令则启用带上限的循环。
func TestVerifyWiringDefaultOffAndConfigured(t *testing.T) {
	if got := verifyFuncFor(Resolved{}); got != nil {
		t.Fatal("verifyFuncFor(empty) = non-nil, want nil")
	}
	if got := verifyReflectionsFor(Resolved{}); got != 0 {
		t.Fatalf("verifyReflectionsFor(empty) = %d, want 0", got)
	}

	res := Resolved{
		Cfg:  config.Config{VerifyCommand: []string{"go", "test", "./..."}},
		Root: t.TempDir(),
	}
	if got := verifyFuncFor(res); got == nil {
		t.Fatal("verifyFuncFor(configured) = nil, want callback")
	}
	if got := verifyReflectionsFor(res); got != agent.DefaultMaxVerifyReflections {
		t.Fatalf("verifyReflectionsFor(configured) = %d, want %d", got, agent.DefaultMaxVerifyReflections)
	}

	res.Cfg.VerifyMaxReflections = 7
	if got := verifyReflectionsFor(res); got != 7 {
		t.Fatalf("verifyReflectionsFor(explicit) = %d, want 7", got)
	}
}

// TestVerifyTailBufferKeepsRuneAlignedTailClean 锁定上限边界切断多字节 rune 时的截断路径：
// 缓冲区必须丢弃开头的不完整 rune，且绝不能追加保留窗口之前的过期字节
// （Truncate 仍看到原始的、更大的长度）。
// （接上行）
func TestVerifyTailBufferKeepsRuneAlignedTailClean(t *testing.T) {
	tail := strings.Repeat("A", verifyOutputCap-1)
	for _, tc := range []struct {
		name   string
		writes []string
	}{
		{"single write", []string{"é" + tail}}, // 上限边界切断开头的 2 字节 rune
		{"split writes", []string{"é", tail}},  // 相同情形，但尾部是增量到达的
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w verifyTailBuffer
			for _, chunk := range tc.writes {
				if _, err := w.Write([]byte(chunk)); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}
			out := strings.TrimPrefix(w.String(), verifyTruncMarker)
			if out != tail {
				t.Fatalf("tail length = %d, want %d clean bytes (stale bytes appended?)", len(out), len(tail))
			}
			if !utf8.ValidString(out) {
				t.Fatal("tail is not valid UTF-8")
			}
		})
	}
}

// joinedArgvs 把选择结果渲染成便于表格比较的字符串。
func joinedArgvs(argvs [][]string) []string {
	out := make([]string, len(argvs))
	for i, argv := range argvs {
		out[i] = strings.Join(argv, " ")
	}
	return out
}

func assertArgvs(t *testing.T, got [][]string, want ...string) {
	t.Helper()
	g := joinedArgvs(got)
	if strings.Join(g, "|") != strings.Join(want, "|") {
		t.Fatalf("selected = %q, want %q", g, want)
	}
}

// TestSelectVerifyCommandsByExtension 锁定按语言分派：带点和裸的键、别名以及大小写差异
// 都能把文件扩展名映射到配置的命令，且不派生进程。
// （接上行）
func TestSelectVerifyCommandsByExtension(t *testing.T) {
	commands := map[string]string{
		".go":        "go test ./go/...",
		"py":         "go test ./py/...",
		"TypeScript": "go test ./ts/...",
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{"dotted key", "internal/app/main.go", "go test ./go/..."},
		{"bare key", "internal/app/main.GO", "go test ./go/..."},
		{"alias py", "scripts/tool.py", "go test ./py/..."},
		{"python alias", "scripts/tool.python", "go test ./py/..."},
		{"case insensitive key", "web/app.tsx", "go test ./ts/..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertArgvs(t, selectVerifyArgvs(commands, nil, []string{tc.path}), tc.want)
		})
	}
}

// TestSelectVerifyCommandsUnknownExtensionFallback 锁定回退：没有匹配语言的路径使用单一的 VerifyCommand；
// 完全没有回退时不选择任何内容，循环会跳过该批次的验证。
// （接上行）
func TestSelectVerifyCommandsUnknownExtensionFallback(t *testing.T) {
	commands := map[string]string{".go": "go test ./go/..."}
	fallback := []string{"go", "build", "./..."}

	assertArgvs(t, selectVerifyArgvs(commands, fallback, []string{"docs/readme.txt"}),
		"go build ./...")
	assertArgvs(t, selectVerifyArgvs(commands, fallback, []string{"main.go", "docs/readme.txt"}),
		"go test ./go/...", "go build ./...")
	assertArgvs(t, selectVerifyArgvs(commands, nil, []string{"docs/readme.txt"}))
	// 没有检查点路径时仍会演练一次回退。
	assertArgvs(t, selectVerifyArgvs(commands, fallback, nil), "go build ./...")
}

// TestSelectVerifyCommandsMultipleFiles 锁定多文件分派：每个不同的语言命令只选择一次，按首次出现顺序，
// 与多少个文件共享该语言无关。
// （接上行）
func TestSelectVerifyCommandsMultipleFiles(t *testing.T) {
	commands := map[string]string{
		".go": "go test ./go/...",
		".py": "go test ./py/...",
		".rs": "go test ./rs/...",
	}
	paths := []string{"a.go", "b.go", "x.py", "c.go", "d.rs", "y.py"}
	assertArgvs(t, selectVerifyArgvs(commands, nil, paths),
		"go test ./go/...", "go test ./py/...", "go test ./rs/...")
}

// TestSelectVerifyAllLanguagesFallback 锁定 "*" 兜底契约：扩展名没有专用命令的文件使用配置的全语言命令。
// 它对任意数量的此类文件只运行一次（去重），也覆盖完全没有路径的检查点。
// （接上行）
// （接上行）
func TestSelectVerifyAllLanguagesFallback(t *testing.T) {
	commands := map[string]string{"*": "go test ./..."}

	assertArgvs(t, selectVerifyArgvs(commands, nil, []string{"docs/readme.txt"}),
		"go test ./...")
	assertArgvs(t, selectVerifyArgvs(commands, nil, []string{"main.go", "notes.md"}),
		"go test ./...")
	assertArgvs(t, selectVerifyArgvs(commands, nil, []string{"a.txt", "b.md", "c.py"}),
		"go test ./...")
	assertArgvs(t, selectVerifyArgvs(commands, nil, nil), "go test ./...")
}

// TestSelectVerifySpecificOverridesAllLanguages 确认语言专用命令优先于 "*"：对已有自己命令的文件不运行兜底，
// 但仍覆盖同一批次中的其他文件。
// （接上行）
func TestSelectVerifySpecificOverridesAllLanguages(t *testing.T) {
	commands := map[string]string{
		".go": "go test ./go/...",
		"*":   "go test ./...",
	}
	assertArgvs(t, selectVerifyArgvs(commands, nil, []string{"a.go", "b.GO"}),
		"go test ./go/...")
	assertArgvs(t, selectVerifyArgvs(commands, nil, []string{"a.go", "readme.txt"}),
		"go test ./go/...", "go test ./...")
}

// TestSelectVerifyAllLanguagesBeatsVerifyCommand 锁定同时配置 "*" 键和旧版 VerifyCommand 字段时的回退优先级：
// "*" 胜出作为唯一的兜底。没有 "*" 键时 VerifyCommand 仍是回退，因此旧版行为不变。
// （接上行）
// （接上行）
func TestSelectVerifyAllLanguagesBeatsVerifyCommand(t *testing.T) {
	fallback := []string{"go", "build", "./..."}

	assertArgvs(t,
		selectVerifyArgvs(map[string]string{"*": "go test ./all/..."}, fallback, []string{"readme.txt"}),
		"go test ./all/...")
	// 没有 "*"：VerifyCommand 仍是未匹配路径的回退。
	assertArgvs(t,
		selectVerifyArgvs(map[string]string{".go": "go test ./go/..."}, fallback, []string{"readme.txt"}),
		"go build ./...")
}

// TestSelectVerifyFallbackPriorityChain 锁定混合批次中的完整优先级：先是语言专用命令（按首次出现顺序），
// 然后 "*" 覆盖其他所有语言，一旦存在 "*"，VerifyCommand 永不运行。
// （接上行）
func TestSelectVerifyFallbackPriorityChain(t *testing.T) {
	commands := map[string]string{
		".go": "go test ./go/...",
		".py": "go test ./py/...",
		"*":   "go test ./all/...",
	}
	fallback := []string{"go", "build", "./..."}
	paths := []string{"a.go", "b.py", "readme.txt", "c.go", "notes.md", "d.rs"}
	assertArgvs(t, selectVerifyArgvs(commands, fallback, paths),
		"go test ./go/...", "go test ./py/...", "go test ./all/...")
}

// TestVerifyWiringAllLanguagesDispatch 确认只有 "*" 的映射会启用循环，且被拒绝的兜底命令仍会被运行器边界的白名单拒绝
// （选择本身从不校验）。
// （接上行）
func TestVerifyWiringAllLanguagesDispatch(t *testing.T) {
	res := Resolved{
		Cfg:  config.Config{VerifyCommands: map[string]string{"*": "bash -c echo hi"}},
		Root: t.TempDir(),
	}
	fn := verifyFuncFor(res)
	if fn == nil {
		t.Fatal("verifyFuncFor(* only) = nil, want dispatcher")
	}
	_, _, _, ok := fn(context.Background(), []string{"readme.txt"})
	if ok {
		t.Fatal("dispatcher with disallowed '*' command returned ok=true, want false")
	}
}

// TestVerifyWiringPerLanguageDispatch 确认按语言的映射自身就能启用循环（无需单一命令）、保持反思接线，
// 且单独配置的一条命令仍走旧版路径。
// （接上行）
func TestVerifyWiringPerLanguageDispatch(t *testing.T) {
	root := t.TempDir()
	res := Resolved{
		Cfg:  config.Config{VerifyCommands: map[string]string{".go": "go test ./..."}},
		Root: root,
	}
	if got := verifyFuncFor(res); got == nil {
		t.Fatal("verifyFuncFor(map only) = nil, want dispatcher")
	}
	if got := verifyReflectionsFor(res); got != agent.DefaultMaxVerifyReflections {
		t.Fatalf("verifyReflectionsFor(map only) = %d, want %d", got, agent.DefaultMaxVerifyReflections)
	}
	// 被拒绝的命令字符串产生 ok=false，但不得使分派崩溃。
	res.Cfg.VerifyCommands = map[string]string{".go": "bash -c echo hi"}
	_, _, _, ok := verifyFuncFor(res)(context.Background(), []string{"main.go"})
	if ok {
		t.Fatal("dispatcher with disallowed command returned ok=true, want false")
	}
}

// TestConfigDecodeVerifyCommands 锁定新映射在文件和覆盖层的 config -> Config 解码，以及旧的单一 argv。
// （接上行）
func TestConfigDecodeVerifyCommands(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeVerifyFile(t, root, "argent.json", `{
		"verifyCommand": ["go", "test", "./..."],
		"verifyCommands": {".go": "go test ./go/...", "py": "go test ./py/..."}
	}`)

	cfg, err := config.Load(root, verifyTestEnv(home))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := cfg.VerifyCommands[".go"]; got != "go test ./go/..." {
		t.Fatalf("VerifyCommands[.go] = %q", got)
	}
	if got := cfg.VerifyCommands["py"]; got != "go test ./py/..." {
		t.Fatalf("VerifyCommands[py] = %q", got)
	}
	if len(cfg.VerifyCommand) == 0 {
		t.Fatal("VerifyCommand = empty, want legacy argv preserved")
	}

	cfg, err = config.LoadWithOverrides(root, verifyTestEnv(home), map[string]any{
		"verifyCommands": map[string]any{".go": "go build ./go/..."},
	})
	if err != nil {
		t.Fatalf("config.LoadWithOverrides: %v", err)
	}
	if got := cfg.VerifyCommands[".go"]; got != "go build ./go/..." {
		t.Fatalf("override VerifyCommands[.go] = %q", got)
	}
}
