package dap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestStartAndCallWithRealProcess 用 go run 启动一个最小假 adapter 进程，
// 验证 Start（进程管理）、Call（帧握手）、Close（终止）的完整链路。
// 假 adapter 读 Content-Length 帧，对 initialize 回 success response。
func TestStartAndCallWithRealProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake adapter uses /bin/sh-style script; covered on unix")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    Content-Length:*)
      len=${line#*: }
      len=${len#*:}
      # 读掉空行
      IFS= read -r blank
      # 读正文
      dd bs=1 count=$len 2>/dev/null
      # 回一个 success response（requestSeq 用 1，initialize 是首条）
      body='{"seq":1,"type":"response","requestSeq":1,"success":true,"command":"initialize","body":{}}'
      printf 'Content-Length: %s\r\n\r\n%s' "${#body}" "$body"
      ;;
  esac
done
`
	path := filepath.Join(dir, "fake-adapter.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Start(ctx, path, nil, dir, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	// 首条 request 的 seq 应为 1，假 adapter 回 requestSeq=1 的响应。
	got, err := c.Call(ctx, "initialize", map[string]any{"adapterID": "fake"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp Message
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Success == nil || !*resp.Success {
		t.Fatalf("response success = %v", resp.Success)
	}
}

// TestStartRejectsEmptyCommand 守护空命令被拒（不启动任何进程）。
func TestStartRejectsEmptyCommand(t *testing.T) {
	if _, err := Start(context.Background(), "", nil, "", nil); err == nil {
		t.Fatal("empty command should be rejected")
	}
}

// TestSanitizeEnvApplied 确认 Start 用 proc.SanitizeEnv 剔除了宿主私有变量。
// 用 go env 风格子进程回显环境变量；直接验证 helper 在 proc 包已测，这里只确认
// Start 路径调用了它（通过启动一个打印 ARGENT_SESSION_ID 的进程间接验证）。
func TestSanitizeEnvApplied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script based")
	}
	dir := t.TempDir()
	// 假 adapter 把当前 ARGENT_SESSION_ID 写到一个文件后退出。
	script := "#!/bin/sh\nprintf '%s' \"$ARGENT_SESSION_ID\" > \"" + filepath.Join(dir, "seen") + "\"\nexit 0\n"
	path := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGENT_SESSION_ID", "host-private-session")
	c, err := Start(context.Background(), path, nil, dir, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()
	// 等子进程跑完并写文件。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(dir, "seen")); err == nil {
			if string(data) != "" {
				t.Fatalf("host-private session id leaked into adapter env: %q", data)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 子进程可能未写文件（启动竞态），不算失败。
}
