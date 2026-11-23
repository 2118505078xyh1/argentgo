package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSplitMeta 表驱动锁定 PS1 哨兵解析：完整行切出 stdout 与
// cwd/pid/exit_code；缺行、截断、非数字 pid 时原样返回全部输出并
// 报 ok=false，让调用方保留上一轮元数据。
func TestSplitMeta(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantOut  string
		wantCWD  string
		wantPID  int
		wantCode int
		wantOK   bool
	}{
		{"complete", "line1\nline2\n" + obsMarker + "/work|1234|7\n", "line1\nline2\n", "/work", 1234, 7, true},
		{"crlf", "out\r\n" + obsMarker + `C:\work|5|0` + "\r\n", "out\r\n", `C:\work`, 5, 0, true},
		{"no marker", "just output\n", "just output\n", "", 0, 0, false},
		{"truncated", "out\n" + obsMarker + "/work|12", "out\n" + obsMarker + "/work|12", "", 0, 0, false},
		{"bad pid", "out\n" + obsMarker + "/work|abc|0\n", "out\n" + obsMarker + "/work|abc|0\n", "", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, cwd, pid, code, ok := splitMeta(tc.raw)
			if ok != tc.wantOK || out != tc.wantOut || cwd != tc.wantCWD || pid != tc.wantPID || code != tc.wantCode {
				t.Fatalf("splitMeta = (%q, %q, %d, %d, %v), want (%q, %q, %d, %d, %v)",
					out, cwd, pid, code, ok, tc.wantOut, tc.wantCWD, tc.wantPID, tc.wantCode, tc.wantOK)
			}
		})
	}
}

// TestObservationFromOutput 锁定软超时与已退出两种观察：仍在运行时
// exit_code=-1 且沿用已知 pid/cwd；哨兵出现时以哨兵值覆盖并把退出码
// 还原为真实值。
func TestObservationFromOutput(t *testing.T) {
	known := sessionMeta{PID: 99, CWD: "/start"}
	raw := "done\n" + obsMarker + "/end|1234|7\n"

	stdout, obs, ok := observationFromOutput(raw, true, known)
	if !ok || stdout != "done\n" {
		t.Fatalf("split result = (%q, %v)", stdout, ok)
	}
	if !obs.StillRunning || obs.ExitCode != -1 || obs.PID != 1234 || obs.CWD != "/end" {
		t.Fatalf("running observation = %+v", obs)
	}

	stdout, obs, ok = observationFromOutput(raw, false, sessionMeta{})
	if !ok || stdout != "done\n" {
		t.Fatalf("split result = (%q, %v)", stdout, ok)
	}
	if obs.StillRunning || obs.ExitCode != 7 || obs.PID != 1234 || obs.CWD != "/end" {
		t.Fatalf("exited observation = %+v", obs)
	}
}

// TestSoftTimeoutHelper 是 TestSoftTimeoutReadMoreLogs 的替身：先打印
// ready，短暂停顿后再打印 done，用于制造软超时后才有新日志的场景。
func TestSoftTimeoutHelper(t *testing.T) {
	if os.Getenv("ARGENT_SOFT_TIMEOUT_HELPER") != "1" {
		return
	}
	fmt.Println("ready")
	time.Sleep(1200 * time.Millisecond)
	fmt.Println("done")
	os.Exit(0)
}

const softTimeoutHelperCommand = `"$ARGENT_SOFT_TIMEOUT_HELPER_EXE" -test.run=TestSoftTimeoutHelper`

// closeTestSession 在测试结束时显式关闭会话，避免残留进程占用 TempDir
// 目录导致 Windows 上 t.TempDir 的清理失败。
func closeTestSession(t *testing.T, id string) {
	t.Helper()
	writer := writeStdinTool{}
	_, _ = writer.Run(context.Background(), "", json.RawMessage(fmt.Sprintf(`{"session_id":%q,"close":true}`, id)))
}

// TestSoftTimeoutReadMoreLogs 验证 #32 的核心验收：软超时回报
// still_running=true/exit_code=-1 且带 pid/cwd；随后空 command 续读到
// 进程后来产生的尾部日志（done），并转为 still_running=false/exit_code=0。
func TestSoftTimeoutReadMoreLogs(t *testing.T) {
	t.Setenv("ARGENT_EXEC_SESSION", "1")
	t.Setenv("ARGENT_SOFT_TIMEOUT_HELPER", "1")
	t.Setenv("ARGENT_SOFT_TIMEOUT_HELPER_EXE", os.Args[0])

	ctx := context.Background()
	tool := bashTool{}
	root := t.TempDir()
	startRaw := json.RawMessage(fmt.Sprintf(`{"command":%q,"yield_time_ms":600}`, softTimeoutHelperCommand))
	first, err := tool.Run(ctx, root, startRaw)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	for _, want := range []string{"still_running=true", "exit_code=-1", "pid=", "cwd=", "ready"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first observation missing %q: %q", want, first)
		}
	}
	id := sessionIDFromOutput(t, first)
	defer closeTestSession(t, id)

	// 空 command 不写 stdin，只续读会话尾部日志。
	readRaw := json.RawMessage(fmt.Sprintf(`{"command":"","session_id":%q,"yield_time_ms":1200}`, id))
	second, err := tool.Run(ctx, "", readRaw)
	if err != nil {
		t.Fatalf("read more logs: %v", err)
	}
	for _, want := range []string{"done", "still_running=false", "exit_code=0"} {
		if !strings.Contains(second, want) {
			t.Fatalf("second observation missing %q: %q", want, second)
		}
	}
	if strings.Contains(second, "ready") {
		t.Fatalf("read more should not replay consumed logs: %q", second)
	}
}

// TestSoftTimeoutCtrlCRetainsSession 验证 C-c 走 isSessionInterrupt
// 但保留会话：中断后仍可用空 command 续读，不报 session not found。
func TestSoftTimeoutCtrlCRetainsSession(t *testing.T) {
	setExecSessionEnv(t)
	ctx := context.Background()
	tool := bashTool{}
	startRaw := json.RawMessage(fmt.Sprintf(`{"command":%q,"yield_time_ms":300}`, helperCommand))
	start, err := tool.Run(ctx, t.TempDir(), startRaw)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	id := sessionIDFromOutput(t, start)
	defer closeTestSession(t, id)

	interruptRaw := json.RawMessage(fmt.Sprintf(`{"command":"C-c","session_id":%q,"yield_time_ms":200}`, id))
	interrupted, err := tool.Run(ctx, "", interruptRaw)
	if err != nil || !strings.Contains(interrupted, "session retained") {
		t.Fatalf("C-c = %q, %v", interrupted, err)
	}

	readRaw := json.RawMessage(fmt.Sprintf(`{"command":"","session_id":%q,"yield_time_ms":100}`, id))
	read, err := tool.Run(ctx, "", readRaw)
	if err != nil {
		t.Fatalf("session not retained after C-c: %v", err)
	}
	if !strings.Contains(read, "still_running=") {
		t.Fatalf("read after C-c missing observation: %q", read)
	}
}
