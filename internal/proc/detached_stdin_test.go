package proc

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingDetachedStdin 在 Read 中永久阻塞直到被释放。
// 它使测试能观察分离的 stdin 拷贝器，而不依赖真实 I/O 时序。
type blockingDetachedStdin struct {
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingDetachedStdin() *blockingDetachedStdin {
	return &blockingDetachedStdin{read: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingDetachedStdin) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.read) })
	<-r.release
	return 0, io.EOF
}

// TestDetachedDoneIsNotBlockedByStdinCopier 守护分离生成路径的
// stdin 接线。对于非 *os.File 的 Stdin，exec.Cmd.Wait 会等待它启动的
// 拷贝 goroutine。Stdin reader 永不返回的分离作业，在子进程退出或
// 被终止后仍必须被回收（Done 关闭），否则被提升的后台作业
// 永远无法被观察为已完成，其日志文件也永远
// 不会关闭。Run 已经自己拥有 stdin 管道；分离路径必须
// 照做，而不是把任意 io.Reader 直接交给 exec。
func TestDetachedDoneIsNotBlockedByStdinCopier(t *testing.T) {
	path, args := testShell(t, "echo detached-stdin-ready")
	stdin := newBlockingDetachedStdin()
	defer close(stdin.release)

	p, err := StartDetached(Command{Path: path, Args: args, Stdin: stdin})
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}

	// 在判断 Done 之前，先确保拷贝器确实已启动。
	select {
	case <-stdin.read:
	case <-time.After(2 * time.Second):
		t.Fatal("detached stdin copier never read from Stdin")
	}

	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done never closed: cmd.Wait blocked on the stdin copy goroutine")
	}
}

const detachedStdinEchoMarker = "ARGENT_PROC_DETACHED_STDIN_ECHO"

// TestDetachedStdinEchoHelper 是一个把 stdin 复制到 stdout 的
// 重新执行的测试二进制。当分离的子进程（不继承控制台）必须消费
// 管道输入时，它避免了依赖控制台的工具（如 Windows 的 more.exe）。
func TestDetachedStdinEchoHelper(t *testing.T) {
	if os.Getenv(detachedStdinEchoMarker) == "" {
		return
	}
	_, _ = io.Copy(os.Stdout, os.Stdin)
}

// TestDetachedStillFeedsStdin 确保自己拥有管道不会破坏
// 有用的情形：有限 Stdin reader 的字节必须到达子进程。
func TestDetachedStillFeedsStdin(t *testing.T) {
	p, err := StartDetached(Command{
		Path:  os.Args[0],
		Args:  []string{"-test.run=^TestDetachedStdinEchoHelper$"},
		Env:   []string{detachedStdinEchoMarker + "=1"},
		Stdin: strings.NewReader("detached-stdin-payload\n"),
	})
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}
	if code := p.Wait(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out := p.Output(); !strings.Contains(out, "detached-stdin-payload") {
		t.Fatalf("captured output = %q, want the stdin payload", out)
	}
}
