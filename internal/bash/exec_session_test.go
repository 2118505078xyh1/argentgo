package bash

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/pty"
)

// TestExecSessionHelper 是各测试共用的 REPL 替身：打印 ready，随后逐行回显。
// 它以标准 go test helper 进程的方式启动，不依赖 python 或其它外部程序。
func TestExecSessionHelper(t *testing.T) {
	if os.Getenv("ARGENT_EXEC_SESSION_HELPER") != "1" {
		return
	}
	fmt.Println("ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "quit" {
			break
		}
		fmt.Println("echo:" + line)
	}
	os.Exit(0)
}

const oldBashSchema = `{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"},"background_after_ms":{"type":"integer","description":"If the command is still running after this many milliseconds, promote it to a background job instead of killing it on timeout."}},"required":["command"]}`

func setExecSessionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ARGENT_EXEC_SESSION", "1")
	t.Setenv("ARGENT_EXEC_SESSION_HELPER", "1")
	t.Setenv("ARGENT_EXEC_SESSION_HELPER_EXE", os.Args[0])
}

// helperCommand 通过 shell 变量引用测试二进制，避开 Windows 路径中
// 反斜杠与引号在 shell -c 里的转义问题。
const helperCommand = `"$ARGENT_EXEC_SESSION_HELPER_EXE" -test.run=TestExecSessionHelper`

func sessionIDFromOutput(t *testing.T, out string) string {
	t.Helper()
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == "session" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	t.Fatalf("cannot find session id in output: %q", out)
	return ""
}

func writeToSession(t *testing.T, writer writeStdinTool, id, input string) string {
	t.Helper()
	return writeToSessionWithYield(t, writer, id, input, 500)
}

func writeToSessionWithYield(t *testing.T, writer writeStdinTool, id, input string, yield int) string {
	t.Helper()
	raw := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"input":%q,"yield_time_ms":%d}`, id, input, yield))
	out, err := writer.Run(context.Background(), "", raw)
	if err != nil {
		t.Fatalf("write_stdin(%q): %v", input, err)
	}
	return out
}

func TestUnifiedExecDisabledKeepsBashUnchanged(t *testing.T) {
	t.Setenv("ARGENT_EXEC_SESSION", "0")
	tool := bashTool{}
	if got := tool.Schema(); got != oldBashSchema {
		t.Fatalf("schema changed when opt-in is off:\n%s", got)
	}
	// 即使参数里带了新增字段，关闭时也必须忽略并走原有 bash 路径。
	raw := json.RawMessage(`{"command":"echo hello","session_id":"ignored","stdin":"ignored","tty":true,"yield_time_ms":50}`)
	out, err := tool.Run(context.Background(), t.TempDir(), raw)
	if err != nil {
		t.Fatalf("bash.Run: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("bash output = %q, want hello", out)
	}
}

func TestExecSessionWriteStdinKeepsProcess(t *testing.T) {
	setExecSessionEnv(t)
	ctx := context.Background()
	tool := bashTool{}
	raw := json.RawMessage(fmt.Sprintf(`{"command":%q,"yield_time_ms":500}`, helperCommand))
	out, err := tool.Run(ctx, t.TempDir(), raw)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	id := sessionIDFromOutput(t, out)
	if !strings.Contains(out, "ready") {
		t.Fatalf("start output = %q, want ready banner", out)
	}

	writer := writeStdinTool{}
	first := writeToSession(t, writer, id, "one\n")
	if !strings.Contains(first, "echo:one") {
		t.Fatalf("first write output = %q", first)
	}
	second := writeToSession(t, writer, id, "two\n")
	if !strings.Contains(second, "echo:two") || strings.Contains(second, "echo:one") {
		t.Fatalf("second write output = %q", second)
	}
	if strings.Contains(second, "[exec session exited]") {
		t.Fatalf("session exited between writes: %q", second)
	}
	// 显式关闭后会话立刻从注册表消失，不留进程。
	closeRaw := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"close":true}`, id))
	if _, err := writer.Run(ctx, "", closeRaw); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if _, err := writer.Run(ctx, "", json.RawMessage(fmt.Sprintf(`{"session_id":%q}`, id))); err == nil {
		t.Fatal("write after close unexpectedly succeeded")
	}
}

func TestExecSessionTTYInterruptIsolated(t *testing.T) {
	if !pty.Supported() {
		t.Skip("pseudoterminals are not supported on this platform")
	}
	setExecSessionEnv(t)
	ctx := context.Background()
	tool := bashTool{}
	start := func() string {
		t.Helper()
		raw := json.RawMessage(fmt.Sprintf(`{"command":%q,"tty":true,"yield_time_ms":2000}`, helperCommand))
		out, err := tool.Run(ctx, t.TempDir(), raw)
		if err != nil {
			// GitHub Linux runner 常无有效控制终端，PTY 启动会报 Setctty/Ctty
			// 错误——这是环境能力缺失而非代码回归，跳过；有 PTY 的环境照常测。
			msg := err.Error()
			if strings.Contains(msg, "Setctty") || strings.Contains(msg, "Ctty") || strings.Contains(msg, "tty") {
				t.Skipf("interactive terminal unavailable: %v", err)
			}
			t.Fatalf("start tty session: %v", err)
		}
		return sessionIDFromOutput(t, out)
	}
	first, second := start(), start()
	if first == second {
		t.Fatalf("two sessions share id %q", first)
	}
	writer := writeStdinTool{}
	defer func() {
		_, _ = writer.Run(context.Background(), "", json.RawMessage(fmt.Sprintf(`{"session_id":%q,"close":true}`, second)))
	}()

	// Ctrl-C 只终止第一条会话。
	ctrlC := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"input":"\u0003"}`, first))
	if _, err := writer.Run(ctx, "", ctrlC); err != nil {
		t.Fatalf("interrupt first session: %v", err)
	}
	// ConPTY / ptmx 的规范模式下回车是 \r，而不是 \n。
	out := writeToSessionWithYield(t, writer, second, "still-here\r\n", 2000)
	if !strings.Contains(out, "echo:still-here") {
		t.Fatalf("second session died with the first: %q", out)
	}
}

func TestWriteStdinDisabled(t *testing.T) {
	t.Setenv("ARGENT_EXEC_SESSION", "0")
	writer := writeStdinTool{}
	_, err := writer.Run(context.Background(), "", json.RawMessage(`{"session_id":"x","input":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("write_stdin when disabled = %v, want disabled error", err)
	}
}
