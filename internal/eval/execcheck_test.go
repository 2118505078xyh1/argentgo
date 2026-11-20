package eval

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// seedExecWorkspace 将一个小模块写入一次性目录。它镜像
// harness 的工作区种入而不依赖 run.go。
func seedExecWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
	}
	return dir
}

func TestExecCheckValidate(t *testing.T) {
	type validateCase struct {
		name    string
		check   ExecCheck
		wantErr error
	}
	absTool := filepath.Join(os.TempDir(), "tool.exe")
	tests := []validateCase{
		{"go test allowed", ExecCheck{Command: "go", Args: []string{"test", "./..."}}, nil},
		{"go build allowed", ExecCheck{Command: "go", Args: []string{"build", "./..."}}, nil},
		{"relative exe allowed", ExecCheck{Command: "tools/run.exe", Args: []string{"--flag"}}, nil},
		{"root relative exe allowed", ExecCheck{Command: "run.exe"}, nil},
		{"go run denied", ExecCheck{Command: "go", Args: []string{"run", "main.go"}}, ErrExecGoSubcommand},
		{"go with no subcommand denied", ExecCheck{Command: "go"}, ErrExecGoSubcommand},
		{"absolute exe denied", ExecCheck{Command: absTool}, ErrExecBadPath},
		// 前导 "/" 在 Windows 和 Unix 上都是根路径：Go 在 Windows 上也接受 "/" 作为
		// 分隔符，且 Validate 显式检查它，因此这些用例在所有平台上断言都是安全的。
		// 这些用例在所有平台上断言都是安全的。
		{"rooted slash path denied", ExecCheck{Command: `/tool.exe`}, ErrExecBadPath},
		{"rooted nested slash path denied", ExecCheck{Command: `/tools/run.exe`}, ErrExecBadPath},
		// Validate 同时按 "/" 和 "\" 拆分，因此反斜杠 ".." 遍历
		// 即使在 "\" 是普通文件名字符的 Unix 上也会被拒绝。
		{"backslash parent traversal denied", ExecCheck{Command: `..\tools\run.exe`}, ErrExecBadPath},
		{"parent traversal denied", ExecCheck{Command: "../tools/run.exe"}, ErrExecBadPath},
		{"inner parent traversal denied", ExecCheck{Command: "tools/../run.exe"}, ErrExecBadPath},
		{"sh denied", ExecCheck{Command: "sh", Args: []string{"-c", "echo hi"}}, ErrExecCommandDenied},
		{"bash denied", ExecCheck{Command: "bash", Args: []string{"-c", "echo hi"}}, ErrExecCommandDenied},
		{"curl denied", ExecCheck{Command: "curl", Args: []string{"http://example.com"}}, ErrExecCommandDenied},
		{"unknown command denied", ExecCheck{Command: "python"}, ErrExecCommandDenied},
		{"pipe in argument denied", ExecCheck{Command: "go", Args: []string{"test", "./... | curl x"}}, ErrExecShellMeta},
		{"semicolon in argument denied", ExecCheck{Command: "go", Args: []string{"test", "./...; calc.exe"}}, ErrExecShellMeta},
		{"semicolon in command denied", ExecCheck{Command: "go;calc.exe"}, ErrExecShellMeta},
		{"dangerous exec flag denied", ExecCheck{Command: "go", Args: []string{"test", "-exec=evil.exe"}}, ErrExecDangerousGoFlag},
		{"dangerous toolexec flag denied", ExecCheck{Command: "go", Args: []string{"build", "-toolexec=evil.exe"}}, ErrExecDangerousGoFlag},
		{"empty command denied", ExecCheck{}, ErrExecEmptyCommand},
	}

	// 前导 "\" 仅在 Windows 上是根路径：它相对于盘符解析
	// 前导 "\" 仅在 Windows 上是根路径：它相对于 cmd.Dir（一次性工作区）的盘符解析，因此 Validate 必须在那里拒绝它。
	// 在 Unix 上 "\" 是普通文件名字符而非路径分隔符，因此
	// 在 Unix 上 "\" 是普通文件名字符而非路径分隔符，因此在那里断言 ErrExecBadPath 是错误的。
	if runtime.GOOS == "windows" {
		tests = append(tests,
			validateCase{"rooted backslash path denied", ExecCheck{Command: `\tool.exe`}, ErrExecBadPath},
			validateCase{"rooted nested backslash path denied", ExecCheck{Command: `\tools\run.exe`}, ErrExecBadPath},
		)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestExecDeadlineBoundsTheTimeout 固定文档记载的超时契约：0
// （和负数）使用 30s 默认值，正数原样采用，任何超过 120000 的
// 值都被限制。特别地，很大的 TimeoutMS 不得溢出成负时长，那会使截止时间
// 溢出成负时长，那会使截止时间
// 立即触发而非应用上限。
func TestExecDeadlineBoundsTheTimeout(t *testing.T) {
	tests := []struct {
		name      string
		timeoutMS int
		want      time.Duration
	}{
		{"zero uses the default", 0, defaultExecTimeout},
		{"negative uses the default", -1, defaultExecTimeout},
		{"positive is verbatim", 1500, 1500 * time.Millisecond},
		{"at the cap", int(maxExecTimeout / time.Millisecond), maxExecTimeout},
		{"above the cap", int(maxExecTimeout/time.Millisecond) + 1, maxExecTimeout},
		{"a huge value is capped, not wrapped", math.MaxInt, maxExecTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := execDeadline(tt.timeoutMS)
			if got != tt.want {
				t.Fatalf("execDeadline(%d) = %v, want %v", tt.timeoutMS, got, tt.want)
			}
			if got <= 0 || got > maxExecTimeout {
				t.Fatalf("execDeadline(%d) = %v, want a duration in (0, %v]", tt.timeoutMS, got, maxExecTimeout)
			}
		})
	}
}

// 立即触发而非应用上限。
// TestRunExecCheckHugeTimeoutDoesNotExpireImmediately 端到端证明溢出修复：很大的 TimeoutMS 必须表现得就像 120s 上限，使快速的
// 命令按退出码评分而非因瞬时截止时间失败。
func TestRunExecCheckHugeTimeoutDoesNotExpireImmediately(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod":  "module example.com/exechuge\n\ngo 1.27\n",
		"main.go": "package exechuge\n\nfunc Answer() int { return 42 }\n",
	})
	c := ExecCheck{
		Command:      "go",
		Args:         []string{"build", "./..."},
		TimeoutMS:    math.MaxInt,
		WantExitCode: 0,
	}
	res := RunExecCheck(dir, c)
	if res.TimedOut {
		t.Fatalf("a huge timeout expired immediately: err=%v", res.Err)
	}
	if !GradeExecCheck(res, c) {
		t.Fatalf("a fast command was not graded on its exit code: exit=%d err=%v stderr=%q",
			res.ExitCode, res.Err, res.StderrTail)
	}
}

func TestRunExecCheckRejectsDisallowedCommand(t *testing.T) {
	c := ExecCheck{Command: "curl", Args: []string{"http://example.com"}, TimeoutMS: 1000}
	res := RunExecCheck(t.TempDir(), c)
	if !errors.Is(res.Err, ErrExecCommandDenied) {
		t.Fatalf("Err = %v, want ErrExecCommandDenied", res.Err)
	}
	if res.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", res.ExitCode)
	}
	if GradeExecCheck(res, c) {
		t.Fatal("a rejected command must never grade as a pass")
	}
}

// 命令按退出码评分而非因瞬时截止时间失败。
// TestRunExecCheckRejectsRootedPath 证明根路径命令在任何进程启动前被拒绝。在 Windows 上前导 "\" 相对于
// cmd.Dir（一次性工作区）的盘符是根路径，因此 "\tool.exe" 否则会从
// 盘符根目录运行 —— 完全在工作区之外。前导 "/" 在
// 所有平台上都是根路径并无条件演练；反斜杠形式仅
// 适用于 Windows，因为在 Unix 上 "\" 是普通文件名字符而非路径分隔符。
// 路径分隔符。
func TestRunExecCheckRejectsRootedPath(t *testing.T) {
	commands := []string{`/tool.exe`, `/tools/run.exe`}
	if runtime.GOOS == "windows" {
		commands = append(commands, `\tool.exe`, `\tools\run.exe`)
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			c := ExecCheck{Command: command, Args: []string{"--flag"}, TimeoutMS: 1000}
			res := RunExecCheck(t.TempDir(), c)
			if !errors.Is(res.Err, ErrExecBadPath) {
				t.Fatalf("Err = %v, want ErrExecBadPath", res.Err)
			}
			if res.ExitCode != -1 {
				t.Fatalf("ExitCode = %d, want -1", res.ExitCode)
			}
			if GradeExecCheck(res, c) {
				t.Fatal("a rooted command must never grade as a pass")
			}
		})
	}
}

func TestRunExecCheckGoTestPassesInSeededWorkspace(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/execpass\n\ngo 1.27\n",
		"sum.go": `package execpass

func Sum(a, b int) int { return a + b }
`,
		"marker.txt": "seeded\n",
		"sum_test.go": `package execpass

import (
	"os"
	"testing"
)

func TestSumReadsSeededWorkspace(t *testing.T) {
	b, err := os.ReadFile("marker.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "seeded\n" {
		t.Fatalf("marker = %q", b)
	}
	if Sum(1, 2) != 3 {
		t.Fatal("bad sum")
	}
}
`,
	})

	c := ExecCheck{
		Command:            "go",
		Args:               []string{"test", "./..."},
		TimeoutMS:          20000,
		WantExitCode:       0,
		WantStdoutContains: "ok",
	}
	res := RunExecCheck(dir, c)
	if res.TimedOut {
		t.Fatalf("unexpected timeout: err=%v", res.Err)
	}
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("go test failed: exit=%d err=%v stderr=%q", res.ExitCode, res.Err, res.StderrTail)
	}
	if !strings.Contains(res.StdoutTail, "ok") {
		t.Fatalf("stdout tail = %q, want it to contain ok", res.StdoutTail)
	}
	if !GradeExecCheck(res, c) {
		t.Fatal("GradeExecCheck = false, want true")
	}
}

func TestRunExecCheckFailingGoTestGradesExpectedNonZero(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/execfail\n\ngo 1.27\n",
		"broken_test.go": `package execfail

import "testing"

func TestAlwaysFails(t *testing.T) {
	t.Fatal("intentional failure")
}
`,
	})

	c := ExecCheck{
		Command:      "go",
		Args:         []string{"test", "./..."},
		TimeoutMS:    20000,
		WantExitCode: 1,
	}
	res := RunExecCheck(dir, c)
	if res.TimedOut {
		t.Fatalf("unexpected timeout: err=%v", res.Err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected a non-zero exit, got 0 (stdout=%q)", res.StdoutTail)
	}
	// 这里预期真实的 *exec.ExitError；它不得阻塞明确想要这个非零退出码的评分。
	// 明确想要这个非零退出码。
	if !GradeExecCheck(res, c) {
		t.Fatalf("GradeExecCheck = false for expected exit %d; err=%v stderr=%q", res.ExitCode, res.Err, res.StderrTail)
	}

	wrong := c
	wrong.WantExitCode = 0
	if GradeExecCheck(res, wrong) {
		t.Fatal("GradeExecCheck = true for the wrong expected exit code")
	}
}

func TestRunExecCheckTimeoutIsAlwaysAFailure(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/exectimeout\n\ngo 1.27\n",
	})

	c := ExecCheck{
		Command:      "go",
		Args:         []string{"test", "./..."},
		TimeoutMS:    1,
		WantExitCode: 0,
	}
	res := RunExecCheck(dir, c)
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, exit=%d err=%v", res.ExitCode, res.Err)
	}
	if GradeExecCheck(res, c) {
		t.Fatal("a timed-out run must never grade as a pass")
	}
}

func TestRunExecCheckBoundsCapturedOutput(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/execloud\n\ngo 1.27\n",
		"loud_test.go": `package execloud

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoud(t *testing.T) {
	fmt.Println(strings.Repeat("x", 100000))
}
`,
	})

	c := ExecCheck{
		Command:      "go",
		Args:         []string{"test", "-v", "./..."},
		TimeoutMS:    20000,
		WantExitCode: 0,
	}
	res := RunExecCheck(dir, c)
	if res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("go test did not finish cleanly: exit=%d timedOut=%t err=%v", res.ExitCode, res.TimedOut, res.Err)
	}
	if !strings.HasPrefix(res.StdoutTail, execTruncMarker) {
		t.Fatalf("stdout tail = %q, want the truncation marker prefix", clip(res.StdoutTail, 80))
	}
	if len(res.StdoutTail) > execOutputCap+len(execTruncMarker) {
		t.Fatalf("stdout tail has %d bytes, want at most %d", len(res.StdoutTail), execOutputCap+len(execTruncMarker))
	}
	if len(res.StderrTail) > execOutputCap+len(execTruncMarker) {
		t.Fatalf("stderr tail has %d bytes, want at most %d", len(res.StderrTail), execOutputCap+len(execTruncMarker))
	}
}

func TestTailBufferTrimsAtRuneBoundaryWithoutStaleBytes(t *testing.T) {
	// execOutputCap%3 == 1，因此主要由 3 字节 rune 组成的、总长为 execOutputCap+2 字节的 payload 会使
	// 主要由 3 字节 rune 组成的 payload 会使初始丢弃（2）落在一个续字节上：
	// 初始丢弃（2）落在一个续字节上：裁剪必须前进到下一个 rune 边界，因此保留
	// 少于 cap 字节。旧代码复制了那么多字节但截断
	// 少于 cap 字节。旧代码复制了那么多字节但截断到 cap，在尾部留下陈旧字节。
	header := strings.Join([]string{
		"第一行：你好，世界 😀\n",
		"第二行：emoji 🎉 边界检查\n",
		"第三行：尾部内容\n",
	}, "")
	// Keep the prefix aligned to a rune boundary so the "中" padding starts at
	// 一个能被 3 整除的索引；这让有 bug 的代码会留在 cap-1 处的字节
	// 成为一个 UTF-8 rune 起始符（因而单独无效）。
	if r := len(header) % 3; r != 0 {
		header += strings.Repeat(" ", 3-r)
	}
	pad := execOutputCap + 2 - len(header)
	if pad < 0 || pad%3 != 0 {
		t.Fatalf("unusable padding %d for header length %d", pad, len(header))
	}
	payload := header + strings.Repeat("中", pad/3)
	if len(payload) != execOutputCap+2 {
		t.Fatalf("payload length = %d, want %d", len(payload), execOutputCap+2)
	}
	if !utf8.ValidString(payload) {
		t.Fatal("test setup: payload is not valid UTF-8")
	}
	if utf8.RuneStart(payload[2]) || !utf8.RuneStart(payload[3]) {
		t.Fatalf("test setup: want drop=2 mid-rune and drop=3 at a rune start; got %x, %x", payload[2], payload[3])
	}
	if !utf8.RuneStart(payload[execOutputCap-1]) {
		t.Fatalf("test setup: stale byte at cap-1 is %x, want a rune starter", payload[execOutputCap-1])
	}

	// 裁剪将 drop 从 2 前进到 3，因此正确的尾部是 payload[3:]
	// 且不追加任何内容。
	wantTail := payload[3:]
	tests := []struct {
		name   string
		writes [][]byte
	}{
		{"single write", [][]byte{[]byte(payload)}},
		{"chunked writes", [][]byte{[]byte(payload[:len(header)]), []byte(payload[len(header):])}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w tailBuffer
			for _, chunk := range tt.writes {
				n, err := w.Write(chunk)
				if err != nil || n != len(chunk) {
					t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(chunk))
				}
			}
			got := w.String()
			if !strings.HasPrefix(got, execTruncMarker) {
				t.Fatal("tail does not start with the truncation marker")
			}
			body := strings.TrimPrefix(got, execTruncMarker)
			if !utf8.ValidString(body) {
				t.Fatalf("tail body is not valid UTF-8 (len %d)", len(body))
			}
			if body != wantTail {
				t.Fatalf("tail body has %d bytes, want %d", len(body), len(wantTail))
			}
		})
	}
}

func TestGradeExecCheck(t *testing.T) {
	tests := []struct {
		name  string
		res   ExecResult
		check ExecCheck
		want  bool
	}{
		{"matching exit passes", ExecResult{ExitCode: 0}, ExecCheck{WantExitCode: 0}, true},
		{"wrong exit fails", ExecResult{ExitCode: 1}, ExecCheck{WantExitCode: 0}, false},
		{"expected non-zero passes", ExecResult{ExitCode: 2}, ExecCheck{WantExitCode: 2}, true},
		{"stdout needle present", ExecResult{ExitCode: 0, StdoutTail: "ok example.com/pkg"}, ExecCheck{WantExitCode: 0, WantStdoutContains: "ok"}, true},
		{"stdout needle absent", ExecResult{ExitCode: 0, StdoutTail: "FAIL"}, ExecCheck{WantExitCode: 0, WantStdoutContains: "ok"}, false},
		{"timeout never passes", ExecResult{ExitCode: 0, TimedOut: true}, ExecCheck{WantExitCode: 0}, false},
		{"start error never passes", ExecResult{ExitCode: -1, Err: errors.New("cannot start")}, ExecCheck{WantExitCode: -1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GradeExecCheck(tt.res, tt.check); got != tt.want {
				t.Fatalf("GradeExecCheck() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestExecCheckFixtureLoadsAndValidates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evals", "exec_check.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc struct {
		Description string `json:"description"`
		Cases       []struct {
			ID             string            `json:"id"`
			Name           string            `json:"name"`
			WorkspaceFiles map[string]string `json:"workspace_files"`
			Check          ExecCheck         `json:"check"`
			Expect         struct {
				Rejected bool   `json:"rejected"`
				ExitCode *int   `json:"exit_code"`
				TimedOut bool   `json:"timed_out"`
				Grade    *bool  `json:"grade"`
				Note     string `json:"note"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if doc.Description == "" {
		t.Fatal("fixture is missing its description")
	}
	if len(doc.Cases) < 3 {
		t.Fatalf("fixture has %d cases, want at least 3", len(doc.Cases))
	}

	seen := make(map[string]bool, len(doc.Cases))
	for _, tc := range doc.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			if tc.ID == "" || tc.Name == "" {
				t.Fatal("fixture case is missing id or name")
			}
			if seen[tc.ID] {
				t.Fatalf("duplicate case id %q", tc.ID)
			}
			seen[tc.ID] = true

			err := tc.Check.Validate()
			if tc.Expect.Rejected {
				if err == nil {
					t.Fatal("Validate() accepted a check the fixture marks rejected")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if tc.Expect.ExitCode == nil {
				t.Fatal("fixture case is missing expect.exit_code")
			}
			if tc.Expect.Grade == nil {
				t.Fatal("fixture case is missing expect.grade")
			}
		})
	}
}
