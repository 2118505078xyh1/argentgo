package proc

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// testShell returns the platform's plain command interpreter, bypassing the
// bash preference so the tests exercise Run itself rather than Git Bash.
func testShell(t *testing.T, line string) (string, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		found, err := Which("cmd.exe")
		if err != nil {
			t.Fatalf("cmd.exe not found: %v", err)
		}
		return found, []string{"/C", line}
	}
	found, err := Which("sh")
	if err != nil {
		t.Fatalf("sh not found: %v", err)
	}
	return found, []string{"-c", line}
}

func runLine(t *testing.T, line string, timeout time.Duration) (Result, error) {
	t.Helper()
	path, args := testShell(t, line)
	return Run(context.Background(), Command{Path: path, Args: args, Timeout: timeout})
}

func TestRunCapturesStdout(t *testing.T) {
	res, err := runLine(t, "echo hello-proc", 20*time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success() {
		t.Fatalf("exit %d: %s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hello-proc") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if res.Duration <= 0 {
		t.Fatal("duration must be positive")
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	res, err := runLine(t, "exit 3", 20*time.Second)
	if err != nil {
		t.Fatalf("a non-zero exit is not an error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	if res.Success() {
		t.Fatal("exit 3 must not report success")
	}
}

func TestRunCapturesStderrSeparately(t *testing.T) {
	line := "echo out-line & echo err-line 1>&2"
	if runtime.GOOS != "windows" {
		line = "echo out-line; echo err-line 1>&2"
	}
	res, err := runLine(t, line, 20*time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Stdout, "out-line") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "err-line") {
		t.Fatalf("stderr = %q", res.Stderr)
	}
	if !strings.Contains(res.Combined(), "err-line") {
		t.Fatalf("combined = %q", res.Combined())
	}
}

func TestRunFeedsStdin(t *testing.T) {
	// cat echoes stdin; the write end must be closed so it sees EOF.
	path, args := testShell(t, "cat")
	if runtime.GOOS == "windows" {
		path, args = testShell(t, "more")
	}
	res, err := Run(context.Background(), Command{
		Path:    path,
		Args:    args,
		Stdin:   strings.NewReader("payload-from-stdin\n"),
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Stdout, "payload-from-stdin") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestRunHonoursDir(t *testing.T) {
	dir := t.TempDir()
	path, args := testShell(t, "cd")
	if runtime.GOOS != "windows" {
		path, args = testShell(t, "pwd")
	}
	res, err := Run(context.Background(), Command{
		Path:    path,
		Args:    args,
		Dir:     dir,
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// cd prints the resolved path, which may differ in case on Windows.
	if !strings.EqualFold(strings.TrimSpace(res.Stdout), dir) &&
		!strings.Contains(strings.ToLower(res.Stdout), strings.ToLower(filepath.Base(dir))) {
		t.Fatalf("stdout = %q, want %q", res.Stdout, dir)
	}
}

func TestRunTimesOutAndKills(t *testing.T) {
	sleep := "sleep 30"
	if runtime.GOOS == "windows" {
		sleep = "ping -n 30 127.0.0.1"
	}
	cmd := Shell(sleep)
	cmd.Timeout = 500 * time.Millisecond
	cmd.KillTree = true
	started := time.Now()
	res, err := Run(context.Background(), cmd)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("timeout took %s, the child was not killed", elapsed)
	}
	if res.ExitCode == 0 {
		t.Fatalf("a timed-out child must not report a clean exit: %+v", res)
	}
	if res.Duration > 10*time.Second {
		t.Fatalf("duration = %s", res.Duration)
	}
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	sleep := "sleep 30"
	if runtime.GOOS == "windows" {
		sleep = "ping -n 30 127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	cmd := Shell(sleep)
	cmd.KillTree = true
	started := time.Now()
	_, err := Run(ctx, cmd)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a context error", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatalf("cancellation took %s", time.Since(started))
	}
}

func TestCappedBufferKeepsHeadAndTail(t *testing.T) {
	old := MaxOutputBytes
	MaxOutputBytes = 20
	defer func() { MaxOutputBytes = old }()

	line := strings.Repeat("0123456789", 10) // 100 bytes, cap 20
	path, args := testShell(t, "echo "+line)
	res, err := Run(context.Background(), Command{Path: path, Args: args, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("a 100-byte stream under a 20-byte cap must be truncated: %q", res.Stdout)
	}
	head, tail, found := strings.Cut(res.Stdout, truncatedMarker)
	if !found {
		t.Fatalf("no truncation marker in %q", res.Stdout)
	}
	if !strings.HasPrefix(head, "0123456789") {
		t.Fatalf("head = %q", head)
	}
	kept := strings.TrimSpace(tail)
	if kept == "" || strings.Trim(kept, "0123456789") != "" {
		t.Fatalf("tail = %q, want the last digits of the stream", tail)
	}
	if !strings.HasSuffix(kept, "9") {
		t.Fatalf("tail = %q, want the newest bytes", tail)
	}
	if len(head)+len(tail) > MaxOutputBytes {
		t.Fatalf("kept %d bytes, cap is %d", len(head)+len(tail), MaxOutputBytes)
	}
}

// TestCappedBufferTruncationKeepsRuneBoundaries guards the invariant that
// truncated capture never cuts a multi-byte rune in half. cappedBuffer keeps
// the head and tail on raw byte offsets; with zh-CN output those offsets can
// land inside a rune, leaving a dangling lead/continuation on either side of
// the truncation marker. Decoding such a string yields U+FFFD even though the
// child emitted well-formed UTF-8.
func TestCappedBufferTruncationKeepsRuneBoundaries(t *testing.T) {
	old := MaxOutputBytes
	MaxOutputBytes = 40
	defer func() { MaxOutputBytes = old }()

	cb := &cappedBuffer{max: MaxOutputBytes}
	cw := newUTF8CarryWriter(cb)
	chunk := []byte("中文测试输出")
	for i := 0; i < 16; i++ {
		for j := 0; j < len(chunk); j++ {
			// One byte at a time so the carry writer must assemble every rune
			// before the capped buffer sees it.
			_, _ = cw.Write(chunk[j : j+1])
		}
	}
	cw.Flush()

	if !cb.dropped {
		t.Fatal("output over the cap must be truncated")
	}
	got := cb.String()
	if !utf8.ValidString(got) {
		t.Fatalf("truncated capture splits a UTF-8 rune: %q", got)
	}
	head, tail, found := strings.Cut(got, truncatedMarker)
	if !found || head == "" || tail == "" {
		t.Fatalf("head/tail truncation broken: %q", got)
	}
	if !utf8.ValidString(head) || !utf8.ValidString(tail) {
		t.Fatalf("head %q or tail %q is invalid UTF-8", head, tail)
	}
	if !strings.Contains(got, "中") {
		t.Fatalf("CJK rune mangled by truncation: %q", got)
	}
}

// TestTrimIncompleteRuneHelpers pins the boundary cases the truncation fix
// relies on: a whole rune at the cut must survive, while a dangling lead or
// continuation on either side must be dropped.
func TestTrimIncompleteRuneHelpers(t *testing.T) {
	cases := []struct {
		name       string
		head, tail []byte
		wantHead   string
		wantTail   string
	}{
		{
			name:     "whole runes at both boundaries survive",
			head:     []byte("abc中"),
			tail:     []byte("文xyz"),
			wantHead: "abc中",
			wantTail: "文xyz",
		},
		{
			name:     "dangling lead at head end is dropped",
			head:     []byte("abc\xe4\xb8"),
			tail:     []byte("文xyz"),
			wantHead: "abc",
			wantTail: "文xyz",
		},
		{
			name:     "dangling continuation at tail start is dropped",
			head:     []byte("abc中"),
			tail:     []byte("\xad文xyz"),
			wantHead: "abc中",
			wantTail: "文xyz",
		},
		{
			name:     "encoded replacement rune is not mistaken for invalid",
			head:     []byte("a\xef\xbf\xbd"),
			tail:     []byte("\xef\xbf\xbdb"),
			wantHead: "a\ufffd",
			wantTail: "\ufffdb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(trimIncompleteRuneTail(tc.head)); got != tc.wantHead {
				t.Fatalf("head = %q, want %q", got, tc.wantHead)
			}
			if got := string(trimIncompleteRuneHead(tc.tail)); got != tc.wantTail {
				t.Fatalf("tail = %q, want %q", got, tc.wantTail)
			}
		})
	}
}

func TestCappedBufferWriteAccounting(t *testing.T) {
	b := &cappedBuffer{max: 10}
	if n, err := b.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	if b.dropped {
		t.Fatal("short output must not be truncated")
	}
	if got := b.String(); got != "abc" {
		t.Fatalf("String = %q", got)
	}
	if n, _ := b.Write([]byte("defghijklmnop")); n != 13 {
		t.Fatalf("Write must report every byte, got %d", n)
	}
	if !b.dropped {
		t.Fatal("output over the cap must be truncated")
	}
	head, tail, found := strings.Cut(b.String(), truncatedMarker)
	if !found {
		t.Fatalf("no marker in %q", b.String())
	}
	if len(head) < 3 || !strings.HasSuffix(tail, "nop") {
		t.Fatalf("head=%q tail=%q", head, tail)
	}
}

func TestWhichFindsAndMisses(t *testing.T) {
	name := "sh"
	if runtime.GOOS == "windows" {
		name = "cmd.exe"
	}
	found, err := Which(name)
	if err != nil {
		t.Fatalf("Which(%q): %v", name, err)
	}
	if !filepath.IsAbs(found) {
		t.Fatalf("Which returned a relative path: %q", found)
	}
	if !strings.EqualFold(filepath.Base(found), name) {
		t.Fatalf("Which = %q, want ...%s", found, name)
	}
	if _, err := Which("definitely-not-a-real-program-xyz"); err == nil {
		t.Fatal("a missing program must be an error")
	}
	if _, err := Which("  "); err == nil {
		t.Fatal("an empty name must be an error")
	}
}

func TestShellPicksBashOnThisMachine(t *testing.T) {
	sh := Shell("echo shell-ready")
	if runtime.GOOS == "windows" {
		if !strings.Contains(strings.ToLower(filepath.Base(sh.Path)), "bash") {
			t.Skipf("no working bash on this machine, fell back to %q", sh.Path)
		}
	}
	res, err := Run(context.Background(), Command{
		Path:    sh.Path,
		Args:    sh.Args,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run shell: %v", err)
	}
	if !strings.Contains(res.Stdout, "shell-ready") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if runtime.GOOS == "windows" && !sh.NoWindow {
		t.Fatal("Shell must hide the console window on Windows")
	}
}

func TestShellFallsBackWhenNoCandidateRuns(t *testing.T) {
	if _, ok := firstWorkingShell(nil); ok {
		t.Fatal("no candidates must not produce a shell")
	}
	if _, ok := firstWorkingShell([]string{"", filepath.Join(t.TempDir(), "bash.exe")}); ok {
		t.Fatal("a missing candidate must not be accepted")
	}
	fallback, args := fallbackShell()
	if fallback == "" || len(args) == 0 {
		t.Fatalf("fallback shell = %q %v", fallback, args)
	}
}

func TestRunRejectsAnEmptyPath(t *testing.T) {
	if _, err := Run(context.Background(), Command{}); err == nil {
		t.Fatal("an empty path must be an error")
	}
}

func TestRunReportsASpawnFailure(t *testing.T) {
	_, err := Run(context.Background(), Command{
		Path:    filepath.Join(t.TempDir(), "no-such-binary"),
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("a missing program must be an error")
	}
}

// blockingStdin records the first Read and then blocks until released, so a
// test can observe whether Run's stdin copier ever touched the reader.
type blockingStdin struct {
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingStdin() *blockingStdin {
	return &blockingStdin{read: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingStdin) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.read) })
	<-r.release
	return 0, io.EOF
}

// TestRunDoesNotCopyStdinWhenSpawnFails guards against leaking the stdin copier
// goroutine and the pipe it writes to when a command cannot be started. With a
// Stdin reader that never produces data, a copier started before cmd.Start
// blocks forever in Read after the spawn error; the caller has no handle to it
// and the process is gone, so both the goroutine and the pipe leak.
func TestRunDoesNotCopyStdinWhenSpawnFails(t *testing.T) {
	stdin := newBlockingStdin()
	defer close(stdin.release)

	_, err := Run(context.Background(), Command{
		Path:  filepath.Join(t.TempDir(), "no-such-binary"),
		Stdin: stdin,
	})
	if err == nil {
		t.Fatal("a missing program must be an error")
	}
	select {
	case <-stdin.read:
		t.Fatal("stdin copier read from Stdin even though the command never spawned")
	case <-time.After(20 * time.Millisecond):
	}
}
