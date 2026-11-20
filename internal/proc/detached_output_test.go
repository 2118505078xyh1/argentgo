package proc

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDetachedSpawnCapturesChildOutput 守护 startOS 中的 writer 接线：
// exec 把子进程的 stdout/stderr 拷贝进一个 switchWriter，返回的
// Process 必须暴露那同一个 writer。NewProcess 另分配的第二个 writer
// 过去会吞掉每个字节，使 RunWithTimeout 的部分输出和
// 被提升作业的日志为空。
func TestDetachedSpawnCapturesChildOutput(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"stdout", "echo DETACHED_STDOUT", "DETACHED_STDOUT"},
		{"stderr", "echo DETACHED_STDERR 1>&2", "DETACHED_STDERR"},
		{"both streams", "echo DETACHED_STDOUT & echo DETACHED_STDERR 1>&2", "DETACHED_STDOUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, args := testShell(t, tc.line)
			if runtime.GOOS != "windows" {
				// 双流情形的 POSIX shell 写法。
				args[len(args)-1] = strings.Replace(args[len(args)-1], " & ", "; ", 1)
			}
			p, err := StartDetached(Command{Path: path, Args: args})
			if err != nil {
				t.Fatalf("StartDetached: %v", err)
			}
			if code := p.Wait(); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if out := p.Output(); !strings.Contains(out, tc.want) {
				t.Fatalf("captured output = %q, want it to contain %q", out, tc.want)
			}
		})
	}
}

// TestRunWithTimeoutReturnsPartialOutputOnKeepOnTimeout 演练真实的 OS
// 启动器：在阻塞前打印的子进程必须把该前缀交回
// Result，且被提升的 Process 仍必须拥有同一批捕获字节。
func TestRunWithTimeoutReturnsPartialOutputOnKeepOnTimeout(t *testing.T) {
	line := "echo PARTIAL_OUTPUT && sleep 30"
	if runtime.GOOS == "windows" {
		line = "echo PARTIAL_OUTPUT && ping -n 30 127.0.0.1"
	}
	path, args := testShell(t, line)

	res, live, err := RunWithTimeout(context.Background(), Command{
		Path:          path,
		Args:          args,
		Timeout:       100 * time.Millisecond,
		KeepOnTimeout: true,
	})
	if live != nil {
		defer live.Kill()
	}
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if !strings.Contains(res.Stdout, "PARTIAL_OUTPUT") {
		t.Fatalf("result lost the partial output: %q", res.Stdout)
	}
	if live == nil {
		t.Fatal("KeepOnTimeout must return the live child")
	}
	if !strings.Contains(live.Output(), "PARTIAL_OUTPUT") {
		t.Fatalf("live child lost the partial output: %q", live.Output())
	}
}
