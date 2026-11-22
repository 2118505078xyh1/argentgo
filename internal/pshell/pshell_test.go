package pshell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTransport 是可注入的内存传输：记录写入、从写入里提取 nonce，并按 render
// 回灌输出。blockRead 用于模拟永不产生哨兵的命令；Close 会解除阻塞的 Read。
type fakeTransport struct {
	mu        sync.Mutex
	pid       int
	startedAt int64
	written   []byte
	nonce     string
	render    func(nonce string) string
	blockRead bool
	onWrite   func()
	closed    bool
	unblock   chan struct{}
	closeOnce sync.Once
}

func newFake(pid int, startedAt int64) *fakeTransport {
	return &fakeTransport{pid: pid, startedAt: startedAt, unblock: make(chan struct{})}
}

func (f *fakeTransport) PID() int         { return f.pid }
func (f *fakeTransport) StartTime() int64 { return f.startedAt }

func (f *fakeTransport) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.written = append(f.written, p...)
	// 用 LastIndex 取最后一个哨兵，避免多轮写入时拿到过期的 nonce。
	if i := bytes.LastIndex(f.written, []byte(markerPrefix)); i >= 0 {
		start := i + len(markerPrefix)
		if start+32 <= len(f.written) {
			f.nonce = string(f.written[start : start+32])
		}
	}
	cb := f.onWrite
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
	return len(p), nil
}

func (f *fakeTransport) Read(p []byte) (int, error) {
	f.mu.Lock()
	render, block, nonce := f.render, f.blockRead && f.render == nil, f.nonce
	f.mu.Unlock()
	if block {
		<-f.unblock
		return 0, io.EOF
	}
	if render != nil {
		return copy(p, []byte(render(nonce))), nil
	}
	return 0, io.EOF
}

func (f *fakeTransport) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.unblock)
	})
	return nil
}

func (f *fakeTransport) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// canned 构造一行 stdout 加一条完整哨兵行的 canned 输出。stdout 后补 \n
// 保证哨兵位于行首，与真实探针的切分约定一致。
func canned(nonce, stdout, cwd, venv string, code int) string {
	return stdout + "\n" + markerPrefix + nonce + "|" + cwd + "|" + venv + "|" + strconv.Itoa(code) + "\n"
}

func TestSplitProbeExact(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	out := "line1\nline2\n" + markerPrefix + nonce + "|/tmp/work|myvenv|0\n"
	stdout, cwd, venv, code, ok := splitProbe(out, nonce)
	if !ok {
		t.Fatal("splitProbe: ok=false, want true")
	}
	if stdout != "line1\nline2\n" {
		t.Fatalf("stdout=%q", stdout)
	}
	if cwd != "/tmp/work" || venv != "myvenv" || code != 0 {
		t.Fatalf("cwd=%q venv=%q code=%d", cwd, venv, code)
	}
}

func TestSplitProbeIgnoresDecoySentinel(t *testing.T) {
	nonce := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	decoy := markerPrefix + "deadbeefdeadbeefdeadbeefdeadbeef" + "|/fake|evil|99\n"
	out := decoy + "echo hi\n" + markerPrefix + nonce + "|/real|venv|7\n"
	stdout, cwd, venv, code, ok := splitProbe(out, nonce)
	if !ok {
		t.Fatal("splitProbe: ok=false, want true")
	}
	if cwd != "/real" || venv != "venv" || code != 7 {
		t.Fatalf("cwd=%q venv=%q code=%d", cwd, venv, code)
	}
	if !strings.HasPrefix(stdout, decoy) {
		t.Fatalf("stdout 应保留命令的伪造哨兵输出，got %q", stdout)
	}
}

func TestSplitProbeRejectsTruncated(t *testing.T) {
	nonce := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	marker := markerPrefix + nonce + "|"
	tests := []struct {
		name string
		out  string
	}{
		{"no marker", "just output\n"},
		{"no trailing newline", "out\n" + marker + "/c|v|0"},
		{"too few segments", "out\n" + marker + "/c|v\n"},
		{"bad code", "out\n" + marker + "/c|v|not-a-number\n"},
		{"wrong nonce", "out\n" + markerPrefix + "cccccccccccccccccccccccccccccccc|/c|v|0\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, _, ok := splitProbe(tc.out, nonce); ok {
				t.Fatalf("splitProbe(%q): ok=true, want false", tc.out)
			}
		})
	}
}

func TestSplitProbeCJKMultiline(t *testing.T) {
	nonce := "dddddddddddddddddddddddddddddddd"
	want := "你好，世界\n第二行 café\n"
	out := want + markerPrefix + nonce + "|/工作区|环境|42\n"
	stdout, cwd, venv, code, ok := splitProbe(out, nonce)
	if !ok {
		t.Fatal("splitProbe: ok=false")
	}
	if stdout != want {
		t.Fatalf("stdout=%q, want %q", stdout, want)
	}
	if cwd != "/工作区" || venv != "环境" || code != 42 {
		t.Fatalf("cwd=%q venv=%q code=%d", cwd, venv, code)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	f := newFake(11, 100)
	f.render = func(nonce string) string { return canned(nonce, "hello", "/w", "", 0) }
	m := NewManager(func(shellKind, workspace string) (transport, error) { return f, nil })
	defer m.Close()

	stdout, code, err := m.Exec(context.Background(), "/w", "bash", "echo hello")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if stdout != "hello\n" || code != 0 {
		t.Fatalf("stdout=%q code=%d", stdout, code)
	}

	f.mu.Lock()
	written := string(f.written)
	f.mu.Unlock()
	if !strings.HasPrefix(written, "echo hello\n") {
		t.Fatalf("写入应以命令开头: %q", written)
	}
	if !strings.Contains(written, "__ps_rc=$?; printf '") {
		t.Fatalf("写入应含 bash 探针: %q", written)
	}
}

func TestSessionCarriesCwdVenv(t *testing.T) {
	f := newFake(12, 100)
	f.render = func(nonce string) string { return canned(nonce, "ok", "/newcwd", "venv-x", 5) }
	var spawnCount atomic.Int32
	m := NewManager(func(shellKind, workspace string) (transport, error) {
		spawnCount.Add(1)
		return f, nil
	})
	defer m.Close()

	ctx := context.Background()
	if _, code, err := m.Exec(ctx, "/w", "bash", "cd /newcwd"); err != nil || code != 5 {
		t.Fatalf("first Exec: code=%d err=%v", code, err)
	}
	m.mu.Lock()
	s := m.sessions["/w"]
	m.mu.Unlock()
	if s == nil {
		t.Fatal("会话未登记")
	}
	s.mu.Lock()
	cwd, venv := s.cwd, s.venv
	s.mu.Unlock()
	if cwd != "/newcwd" || venv != "venv-x" {
		t.Fatalf("cwd=%q venv=%q", cwd, venv)
	}

	// 第二条命令必须复用同一会话（cwd/venv 随 shell 状态延续）。
	if _, _, err := m.Exec(ctx, "/w", "bash", "pwd"); err != nil {
		t.Fatalf("second Exec: %v", err)
	}
	if spawnCount.Load() != 1 {
		t.Fatalf("spawn 次数=%d, want 1", spawnCount.Load())
	}
}

func TestTimeoutDiscardsSession(t *testing.T) {
	f1 := newFake(21, 100)
	f1.blockRead = true // 永不产生哨兵
	f2 := newFake(22, 101)
	f2.render = func(nonce string) string { return canned(nonce, "ok", "/w", "", 0) }

	var n atomic.Int32
	m := NewManager(func(shellKind, workspace string) (transport, error) {
		if n.Add(1) == 1 {
			return f1, nil
		}
		return f2, nil
	})
	m.timeout = 30 * time.Millisecond
	m.sameProcessFn = func(pid int, startedAt int64) bool { return true }
	defer m.Close()

	ctx := context.Background()
	if _, _, err := m.Exec(ctx, "/w", "bash", "sleep 10"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Exec err=%v, want ErrTimeout", err)
	}
	if !f1.isClosed() {
		t.Fatal("超时会话应被销毁")
	}

	// 下条命令必须重建会话。
	if _, code, err := m.Exec(ctx, "/w", "bash", "echo ok"); err != nil || code != 0 {
		t.Fatalf("after-timeout Exec: code=%d err=%v", code, err)
	}
	if n.Load() != 2 {
		t.Fatalf("spawn 次数=%d, want 2", n.Load())
	}
}

func TestManagerSerializesConcurrentExec(t *testing.T) {
	f := newFake(31, 100)
	f.render = func(nonce string) string { return canned(nonce, "ok", "/w", "", 0) }

	var active, spawnCount atomic.Int32
	var concurrent atomic.Bool
	f.onWrite = func() {
		if active.Add(1) != 1 {
			concurrent.Store(true)
		}
		time.Sleep(3 * time.Millisecond) // 放大重叠窗口
		active.Add(-1)
	}
	m := NewManager(func(shellKind, workspace string) (transport, error) {
		spawnCount.Add(1)
		return f, nil
	})
	defer m.Close()

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := m.Exec(context.Background(), "/w", "bash", "echo ok")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发 Exec: %v", err)
		}
	}
	if concurrent.Load() {
		t.Fatal("检测到并发写入共享管道")
	}
	if spawnCount.Load() != 1 {
		t.Fatalf("spawn 次数=%d, want 1", spawnCount.Load())
	}
}

func TestIdleReaps(t *testing.T) {
	f := newFake(41, 100)
	f.render = func(nonce string) string { return canned(nonce, "ok", "/w", "", 0) }
	m := NewManager(func(shellKind, workspace string) (transport, error) { return f, nil })
	defer m.Close()
	m.idleTTL = 10 * time.Millisecond
	m.sameProcessFn = func(pid int, startedAt int64) bool { return true }

	if _, _, err := m.Exec(context.Background(), "/w", "bash", "echo ok"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	m.mu.Lock()
	s := m.sessions["/w"]
	m.mu.Unlock()
	if s == nil {
		t.Fatal("会话未登记")
	}
	s.lastUsed.Store(time.Now().Add(-time.Hour).UnixMilli())

	m.reapIdle()
	if !f.isClosed() {
		t.Fatal("空闲会话应被回收")
	}
	if !s.dead.Load() {
		t.Fatal("回收后会话应标记 dead")
	}
	m.mu.Lock()
	_, exists := m.sessions["/w"]
	m.mu.Unlock()
	if exists {
		t.Fatal("回收后会话应从池中移除")
	}
}

func TestCloseKillsOnlyOwnPid(t *testing.T) {
	own := newFake(100, 1000)
	foreign := newFake(200, 2000)
	m := NewManager(func(shellKind, workspace string) (transport, error) {
		return nil, errors.New("不应触发 spawn")
	})
	m.sameProcessFn = func(pid int, startedAt int64) bool { return pid == 100 }
	m.sessions["own"] = &Session{t: own, workspace: "own"}
	m.sessions["foreign"] = &Session{t: foreign, workspace: "foreign"}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !own.isClosed() {
		t.Fatal("自有会话应被杀")
	}
	if foreign.isClosed() {
		t.Fatal("身份不匹配的会话不应被杀")
	}
	if m.ctx.Err() == nil {
		t.Fatal("Close 应取消 Manager 生命周期")
	}

	// 幂等：再次 Close 不应有副作用。
	m.Close()
}
