package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 本文件是进程执行守护的增强测试层：在不改动生产逻辑的前提下，
// 把 exit_classify.go 的首因分类/标记与真实子进程的启动、超时、取消
// 三条执行路径串起来验证。所有辅助子进程都通过重新执行测试二进制实现，
// 因此不依赖外部命令、不联网、跨平台可用。

// TestExecGuardClassifyExitFullPriorityMatrix 穷举三个异常布尔标志的八种组合，
// 并叠加信号有无与退出码成败，锁定 spawn-error > aborted > timeout >
// signaled > success > failed 的完整优先级。另覆盖边界退出码与 CJK 信号串。
func TestExecGuardClassifyExitFullPriorityMatrix(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		signal   string
		timedOut bool
		spawnErr bool
		aborted  bool
		want     ExitClass
	}{
		// spawnErr 与其余全部标志叠加：永远由 spawn-error 决定。
		{"spawn+abort+timeout/no signal/code0", 0, "", true, true, true, ExitSpawnErr},
		{"spawn+abort+timeout/signal/code7", 7, "SIGTERM", true, true, true, ExitSpawnErr},
		{"spawn+abort/no timeout/no signal/code0", 0, "", false, true, true, ExitSpawnErr},
		{"spawn+abort/no timeout/cjk signal/code7", 7, "信号中断", false, true, true, ExitSpawnErr},
		{"spawn+timeout/no abort/no signal/code0", 0, "", true, true, false, ExitSpawnErr},
		{"spawn+timeout/no abort/signal/code7", 7, "SIGKILL", true, true, false, ExitSpawnErr},
		{"spawn only/no signal/code0", 0, "", false, true, false, ExitSpawnErr},
		{"spawn only/cjk signal/code7", 7, "中断", false, true, false, ExitSpawnErr},

		// 无 spawnErr 时 aborted 压过 timeout、signal、退出码。
		{"abort+timeout/no signal/code0", 0, "", true, false, true, ExitAborted},
		{"abort+timeout/signal/code7", 7, "SIGTERM", true, false, true, ExitAborted},
		{"abort only/no signal/code0", 0, "", false, false, true, ExitAborted},
		{"abort only/cjk signal/code7", 7, "用户取消", false, false, true, ExitAborted},

		// 既无 spawnErr 也无 aborted 时 timeout 压过 signal 与退出码。
		{"timeout only/no signal/code0", 0, "", true, false, false, ExitTimeout},
		{"timeout only/signal/code7", 7, "SIGKILL", true, false, false, ExitTimeout},

		// 无任何异常标志：signal > exitCode。
		{"signal/code0", 0, "SIGTERM", false, false, false, ExitSignaled},
		{"signal/code7", 7, "中断", false, false, false, ExitSignaled},

		// 边界退出码：0 成功；-1、124、128+SIG(137)、大数均为失败。
		{"clean exit code0", 0, "", false, false, false, ExitSuccess},
		{"negative code -1", -1, "", false, false, false, ExitFailed},
		{"timeout-like code 124", 124, "", false, false, false, ExitFailed},
		{"128 plus signal code 137", 137, "", false, false, false, ExitFailed},
		{"large code 2147483647", 2147483647, "", false, false, false, ExitFailed},

		// CJK 信号串只判空：既触发 signaled，也不会越过更高优先级。
		{"cjk signal alone", 0, "信号：进程已被用户终止", false, false, false, ExitSignaled},
		{"cjk signal below timeout", 0, "信号中断", true, false, false, ExitTimeout},
		{"cjk signal below abort", 0, "信号中断", false, false, true, ExitAborted},
		{"cjk signal below spawn", 0, "信号中断", false, true, false, ExitSpawnErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyExit(tt.exitCode, tt.signal, tt.timedOut, tt.spawnErr, tt.aborted)
			if got != tt.want {
				t.Fatalf("ClassifyExit(%d, %q, timedOut=%v, spawnErr=%v, aborted=%v) = %q, want %q",
					tt.exitCode, tt.signal, tt.timedOut, tt.spawnErr, tt.aborted, got, tt.want)
			}
		})
	}

	// 三种布尔标志的八种组合全部枚举，用独立写出的首因期望复核；
	// 末尾组合无异常标志，故由非空的 SIGTERM 决定为 signaled。
	comboWants := []struct {
		to, se, ab bool
		want       ExitClass
	}{
		{true, true, true, ExitSpawnErr},
		{true, true, false, ExitSpawnErr},
		{true, false, true, ExitAborted},
		{true, false, false, ExitTimeout},
		{false, true, true, ExitSpawnErr},
		{false, true, false, ExitSpawnErr},
		{false, false, true, ExitAborted},
		{false, false, false, ExitSignaled},
	}
	for _, c := range comboWants {
		for i := 0; i < 8; i++ {
			if got := ClassifyExit(0, "SIGTERM", c.to, c.se, c.ab); got != c.want {
				t.Fatalf("组合(timedOut=%v,spawnErr=%v,aborted=%v) 第 %d 次分类 = %q, want %q",
					c.to, c.se, c.ab, i, got, c.want)
			}
		}
	}
}

// TestExecGuardExitMarkdownStableBytes 逐字节固定每个分类的标记文本：
// 重复调用稳定、必为纯 ASCII（便于日志检索）、与中文相邻拼接不破坏字节。
func TestExecGuardExitMarkdownStableBytes(t *testing.T) {
	tests := []struct {
		class ExitClass
		code  int
		want  string
	}{
		{ExitSuccess, 0, ""},
		{ExitFailed, 0, "[failed: 0]"},
		{ExitFailed, -1, "[failed: -1]"},
		{ExitFailed, 124, "[failed: 124]"},
		{ExitFailed, 2147483647, "[failed: 2147483647]"},
		{ExitTimeout, 124, "[timed out]"},
		{ExitTimeout, 137, "[timed out]"},
		{ExitSignaled, 9, "[killed by signal: 9]"},
		{ExitSignaled, 137, "[killed by signal: 137]"},
		{ExitSpawnErr, -1, "[spawn error]"},
		{ExitAborted, 0, "[aborted]"},
		{ExitClass("bogus"), 9, ""},
		{ExitClass(""), 9, ""},
	}
	for _, tt := range tests {
		first := ExitMarkdown(tt.class, tt.code)
		if first != tt.want {
			t.Fatalf("ExitMarkdown(%q, %d) = %q, want %q", tt.class, tt.code, first, tt.want)
		}
		wantBytes := []byte(tt.want)
		for i := 0; i < 32; i++ {
			got := []byte(ExitMarkdown(tt.class, tt.code))
			if !bytes.Equal(got, wantBytes) {
				t.Fatalf("ExitMarkdown(%q, %d) 第 %d 次字节漂移: %q != %q",
					tt.class, tt.code, i, got, wantBytes)
			}
		}
		for _, b := range []byte(first) {
			if b > 0x7f {
				t.Fatalf("标记 %q 含非 ASCII 字节 0x%02x，不够稳定", first, b)
			}
		}
	}

	// 与中文前缀拼接后，标记部分逐字节保持不变。
	prefix := "命令执行失败：进程被强制终止 "
	mark := ExitMarkdown(ExitSignaled, 137)
	combined := prefix + mark
	if !strings.HasPrefix(combined, prefix) {
		t.Fatalf("中文前缀被破坏: %q", combined)
	}
	if got := []byte(strings.TrimPrefix(combined, prefix)); !bytes.Equal(got, []byte(mark)) {
		t.Fatalf("拼接后标记字节改变: %q != %q", got, mark)
	}
	if !utf8.ValidString(combined) {
		t.Fatalf("拼接结果含非法 UTF-8: %q", combined)
	}
}

// 辅助子进程的握手常量。正常运行中 TestExecGuardHelper 是空操作；
// 重新执行的测试二进制在标记环境变量下变成可控的小型子进程。
const (
	execGuardHelperEnv = "ARGENT_PROC_EXEC_GUARD_HELPER"
	execGuardModeEnv   = "ARGENT_PROC_EXEC_GUARD_MODE"
	execGuardFileEnv   = "ARGENT_PROC_EXEC_GUARD_FILE"

	execGuardOutToken = "EXEC_GUARD_STDOUT"
	execGuardErrToken = "EXEC_GUARD_STDERR"
)

// TestExecGuardHelper 是被测子进程的载体：quick/fail 模式打印两条流，
// sleep 模式先把自己的 PID 写入文件再休眠，供父进程验证取消后无残留。
func TestExecGuardHelper(t *testing.T) {
	if os.Getenv(execGuardHelperEnv) == "" {
		return
	}
	switch os.Getenv(execGuardModeEnv) {
	case "quick":
		_, _ = io.WriteString(os.Stdout, execGuardOutToken+"\n")
		_, _ = io.WriteString(os.Stderr, execGuardErrToken+"\n")
	case "fail":
		_, _ = io.WriteString(os.Stdout, execGuardOutToken+"\n")
		_, _ = io.WriteString(os.Stderr, execGuardErrToken+"\n")
		os.Exit(7)
	case "sleep":
		pid := os.Getpid()
		if path := os.Getenv(execGuardFileEnv); path != "" {
			_ = os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600)
		}
		_, _ = fmt.Fprintf(os.Stdout, "exec-guard-pid=%d\n", pid)
		time.Sleep(30 * time.Second)
	default:
		os.Exit(2)
	}
}

// execGuardCommand 构造重新执行测试二进制的 Command。
func execGuardCommand(mode, pidFile string) Command {
	env := []string{execGuardHelperEnv + "=1", execGuardModeEnv + "=" + mode}
	if pidFile != "" {
		env = append(env, execGuardFileEnv+"="+pidFile)
	}
	return Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestExecGuardHelper$"},
		Env:  env,
	}
}

// execGuardReadPID 等待辅助进程把 PID 落到握手文件。
func execGuardReadPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("辅助进程未在 %s 写入 PID", path)
			return 0
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// execGuardWaitGone 轮询直到 pid 不再存在，确认进程树没有残留。
func execGuardWaitGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := ProcessStartTime(pid); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d 在杀树后仍然存活", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertExecGuardCombined 断言两条流互不串扰，且 Combined 严格按
// stdout 在前、stderr 在后的顺序拼接。
func assertExecGuardCombined(t *testing.T, res Result) {
	t.Helper()
	if !strings.Contains(res.Stdout, execGuardOutToken) {
		t.Fatalf("stdout = %q, want %q", res.Stdout, execGuardOutToken)
	}
	if !strings.Contains(res.Stderr, execGuardErrToken) {
		t.Fatalf("stderr = %q, want %q", res.Stderr, execGuardErrToken)
	}
	if strings.Contains(res.Stdout, execGuardErrToken) {
		t.Fatalf("stderr 串入 stdout: %q", res.Stdout)
	}
	if strings.Contains(res.Stderr, execGuardOutToken) {
		t.Fatalf("stdout 串入 stderr: %q", res.Stderr)
	}
	if got, want := res.Combined(), res.Stdout+res.Stderr; got != want {
		t.Fatalf("Combined() = %q, want stdout+stderr = %q", got, want)
	}
	iOut := strings.Index(res.Combined(), execGuardOutToken)
	iErr := strings.Index(res.Combined(), execGuardErrToken)
	if iOut < 0 || iErr < 0 || iOut >= iErr {
		t.Fatalf("Combined 顺序错误: stdout@%d stderr@%d in %q", iOut, iErr, res.Combined())
	}
}

// TestExecGuardRunQuickProcess 快速退出命令：覆盖平台原生 shell 与自辅助
// 进程两条路径，断言 ExitCode、Stdout、Stderr 与 Combined 的顺序。
func TestExecGuardRunQuickProcess(t *testing.T) {
	t.Run("platform shell echo", func(t *testing.T) {
		sep := "&"
		if runtime.GOOS != "windows" {
			sep = ";"
		}
		line := "echo " + execGuardOutToken + " " + sep + " echo " + execGuardErrToken + " 1>&2"
		path, args := testShell(t, line)
		res, err := Run(context.Background(), Command{Path: path, Args: args, Timeout: 20 * time.Second})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.ExitCode != 0 || !res.Success() {
			t.Fatalf("exit code = %d, want 0", res.ExitCode)
		}
		assertExecGuardCombined(t, res)
	})

	t.Run("self helper streams in order", func(t *testing.T) {
		res, err := Run(context.Background(), execGuardCommand("quick", ""))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("exit code = %d, want 0", res.ExitCode)
		}
		assertExecGuardCombined(t, res)
	})

	t.Run("self helper nonzero exit", func(t *testing.T) {
		res, err := Run(context.Background(), execGuardCommand("fail", ""))
		if err != nil {
			t.Fatalf("非零退出不应报错: %v", err)
		}
		if res.ExitCode != 7 {
			t.Fatalf("exit code = %d, want 7", res.ExitCode)
		}
		if got := ClassifyExit(res.ExitCode, "", false, false, false); got != ExitFailed {
			t.Fatalf("ClassifyExit(7, ...) = %q, want %q", got, ExitFailed)
		}
		assertExecGuardCombined(t, res)
	})
}

// TestExecGuardTimeoutErrorClassifiesAsTimeout 演练 RunWithTimeout 的
// KeepOnTimeout 路径：返回的 *TimeoutError 必须能被 ClassifyExit 识别为
// timeout，且被提升的活进程在测试结束前必须被杀掉，不泄漏子进程。
func TestExecGuardTimeoutErrorClassifiesAsTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := execGuardCommand("sleep", pidFile)
	cmd.Timeout = 2 * time.Second
	cmd.KeepOnTimeout = true

	res, live, err := RunWithTimeout(context.Background(), cmd)
	var toKill *Process
	defer func() {
		if toKill == nil {
			return
		}
		toKill.Kill()
		select {
		case <-toKill.Done():
		case <-time.After(5 * time.Second):
			t.Error("被提升的子进程在 Kill 后未退出")
		}
	}()
	toKill = live

	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want errors.Is(err, ErrTimeout)", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("超时错误不应被当作用户取消: %v", err)
	}
	if live == nil {
		t.Fatal("KeepOnTimeout 必须交回仍在运行的子进程")
	}

	// 把真实错误喂给分类器：超时标志由 DeadlineExceeded 推出。
	timedOut := errors.Is(err, context.DeadlineExceeded)
	if got := ClassifyExit(res.ExitCode, "", timedOut, false, false); got != ExitTimeout {
		t.Fatalf("ClassifyExit(res.ExitCode=%d, timedOut=%v) = %q, want %q",
			res.ExitCode, timedOut, got, ExitTimeout)
	}
	if got := ClassifyExit(res.ExitCode, "", ClassifyCancel(err) == CancelTimeout, false, false); got != ExitTimeout {
		t.Fatalf("经 ClassifyCancel 识别超时的分类 = %q, want %q", got, ExitTimeout)
	}

	// 确认超时时确有子进程被留下，随后由 defer 清理并验证无残留。
	pid := execGuardReadPID(t, pidFile)
	if _, e := ProcessStartTime(pid); e != nil {
		t.Fatalf("被提升的子进程 pid %d 不存活: %v", pid, e)
	}
	toKill.Kill()
	select {
	case <-toKill.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("被提升的子进程在 Kill 后未退出")
	}
	toKill = nil
	execGuardWaitGone(t, pid, 5*time.Second)
}

// TestExecGuardCancelStopsWithinOneRound 用 context.WithCancel 取消一个
// 正在 sleep 的自辅助进程：Run 必须在一轮内返回，错误可判为 CancelUser，
// 分类为 aborted，且借助 KillTree 语义确保没有子进程残留。
func TestExecGuardCancelStopsWithinOneRound(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := execGuardCommand("sleep", pidFile)
	cmd.KillTree = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		res     Result
		err     error
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	go func() {
		started := time.Now()
		res, err := Run(ctx, cmd)
		done <- outcome{res: res, err: err, elapsed: time.Since(started)}
	}()

	// 等到辅助进程真正写入 PID，确保取消的是活进程而非尚未启动的壳。
	pid := execGuardReadPID(t, pidFile)
	if _, e := ProcessStartTime(pid); e != nil {
		t.Fatalf("辅助进程 pid %d 在取消前已消失: %v", pid, e)
	}
	cancel()

	select {
	case o := <-done:
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", o.err)
		}
		if got := ClassifyCancel(o.err); got != CancelUser {
			t.Fatalf("ClassifyCancel(%v) = %v, want %v", o.err, got, CancelUser)
		}
		if got := ClassifyExit(o.res.ExitCode, "", false, false, true); got != ExitAborted {
			t.Fatalf("取消的分类 = %q, want %q", got, ExitAborted)
		}
		if o.elapsed > 3*time.Second {
			t.Fatalf("取消耗时 %s，未在一轮内停止", o.elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run 在取消后迟迟未返回")
	}

	execGuardWaitGone(t, pid, 5*time.Second)
}
