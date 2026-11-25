package app

// 编辑后 LSP 文件变更通知的离线测试。两个接缝（mux 查找与 didChangeWatchedFiles
// 发送）都被替换成桩，不触碰进程单例、不派生语言服务进程、不联网。

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"example.com/argentgo/internal/lsp"
)

// lspNotifyCall 记录一次批量通知的目标客户端与变更列表。
type lspNotifyCall struct {
	client  *lsp.Client
	changes []lsp.FileChange
}

// swapLSPNotifySeams 替换查找与发送接缝，并在用例结束时恢复，避免污染其他用例。
func swapLSPNotifySeams(
	t *testing.T,
	lookup func(string, string) (*lsp.Client, bool),
	send func(context.Context, *lsp.Client, []lsp.FileChange) error,
) {
	t.Helper()
	oldLookup, oldSend := muxClientForFile, didChangeWatchedFiles
	muxClientForFile, didChangeWatchedFiles = lookup, send
	t.Cleanup(func() {
		muxClientForFile, didChangeWatchedFiles = oldLookup, oldSend
	})
}

func TestNotifyLSPFileChangesBatchesPerClient(t *testing.T) {
	root := t.TempDir()
	goClient, textClient := &lsp.Client{}, &lsp.Client{}

	// 按扩展名模拟 mux 的语言路由：.go 归 goClient，.txt 归 textClient。
	lookup := func(_, abs string) (*lsp.Client, bool) {
		switch filepath.Ext(abs) {
		case ".go":
			return goClient, true
		case ".txt":
			return textClient, true
		default:
			return nil, false
		}
	}
	var calls []lspNotifyCall
	swapLSPNotifySeams(t, lookup, func(_ context.Context, c *lsp.Client, changes []lsp.FileChange) error {
		calls = append(calls, lspNotifyCall{client: c, changes: append([]lsp.FileChange(nil), changes...)})
		return nil
	})

	absGo := filepath.Join(root, "outside", "abs.go") // 绝对路径输入应被原样采用。
	notifyLSPFileChanges(root, []string{
		"main.go",
		"sub/util.go",
		"notes.txt",
		absGo,
		"",        // 空路径跳过。
		"unk.xyz", // 无客户端的路径跳过。
	})

	// 同一客户端的多条变更合并为一次通知，且按首次命中顺序发送。
	if len(calls) != 2 {
		t.Fatalf("notify calls = %d, want 2 (one per client)", len(calls))
	}
	if calls[0].client != goClient || calls[1].client != textClient {
		t.Fatalf("notify order = %p, %p; want goClient, textClient", calls[0].client, calls[1].client)
	}

	wantGo := []lsp.FileChange{
		{URI: lsp.PathToURI(filepath.Join(root, "main.go")), Type: lsp.FileChangeChanged},
		{URI: lsp.PathToURI(filepath.Join(root, "sub", "util.go")), Type: lsp.FileChangeChanged},
		{URI: lsp.PathToURI(absGo), Type: lsp.FileChangeChanged},
	}
	if !reflect.DeepEqual(calls[0].changes, wantGo) {
		t.Errorf("go client changes = %#v, want %#v", calls[0].changes, wantGo)
	}
	wantText := []lsp.FileChange{
		{URI: lsp.PathToURI(filepath.Join(root, "notes.txt")), Type: lsp.FileChangeChanged},
	}
	if !reflect.DeepEqual(calls[1].changes, wantText) {
		t.Errorf("text client changes = %#v, want %#v", calls[1].changes, wantText)
	}
}

func TestNotifyLSPFileChangesSkipsWithoutClient(t *testing.T) {
	lookups := 0
	var sends int
	swapLSPNotifySeams(t,
		func(string, string) (*lsp.Client, bool) {
			lookups++
			return nil, false // 模拟没有已启动的客户端：绝不懒启动。
		},
		func(context.Context, *lsp.Client, []lsp.FileChange) error {
			sends++
			return nil
		},
	)

	notifyLSPFileChanges(t.TempDir(), []string{"main.go", "sub/util.go"})
	if lookups != 2 {
		t.Errorf("client lookups = %d, want 2", lookups)
	}
	if sends != 0 {
		t.Errorf("notifications = %d, want 0 when no client is running", sends)
	}

	// 空路径列表连查找都不应发生。
	notifyLSPFileChanges(t.TempDir(), nil)
	if lookups != 2 {
		t.Errorf("client lookups after empty paths = %d, want 2", lookups)
	}
}

func TestNotifyLSPFileChangesNotifyErrorIgnored(t *testing.T) {
	root := t.TempDir()
	first, second := &lsp.Client{}, &lsp.Client{}
	lookups, sends := 0, 0
	swapLSPNotifySeams(t,
		func(_, abs string) (*lsp.Client, bool) {
			lookups++
			if filepath.Ext(abs) == ".go" {
				return first, true
			}
			return second, true
		},
		func(_ context.Context, c *lsp.Client, _ []lsp.FileChange) error {
			sends++
			if c == first {
				return errors.New("server unavailable")
			}
			return nil
		},
	)

	// 第一个客户端通知失败不应中断后续客户端；通知结果被完全丢弃，不 panic。
	notifyLSPFileChanges(root, []string{"main.go", "notes.txt"})
	if lookups != 2 || sends != 2 {
		t.Errorf("lookups = %d, sends = %d; want 2, 2 (error must not abort the loop)", lookups, sends)
	}
}

func TestNotifyLSPFileChangesOptOut(t *testing.T) {
	envFor := func(value string) func(string) string {
		return func(key string) string {
			if key == lspWatchOptOutEnv {
				return value
			}
			return ""
		}
	}
	cases := []struct {
		name    string
		env     func(string) string
		wantNil bool
	}{
		{name: "nil env keeps default", env: nil, wantNil: false},
		{name: "unset keeps default", env: envFor(""), wantNil: false},
		{name: "one keeps default", env: envFor("1"), wantNil: false},
		{name: "zero disables", env: envFor("0"), wantNil: true},
		{name: "padded zero disables", env: envFor(" 0 "), wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lspWatchNotifier(tc.env)
			if (got == nil) != tc.wantNil {
				t.Errorf("lspWatchNotifier() nil = %v, want %v", got == nil, tc.wantNil)
			}
		})
	}
}

func TestHookedToolsNotifiesLSPFileChangesOnlyOnSuccess(t *testing.T) {
	// 关闭编辑后诊断，避免 Run 成功分支去做与本用例无关的 LSP 拉取。
	previous := EnableEditDiagnostics
	EnableEditDiagnostics = false
	t.Cleanup(func() { EnableEditDiagnostics = previous })

	root := t.TempDir()
	paths := []string{"main.go", "sub/util.go"}
	var gotRoot string
	var gotPaths []string
	calls := 0

	newHooked := func(inner *stubToolRunner, notifier func(string, []string)) *hookedTools {
		return &hookedTools{
			inner:           inner,
			bridge:          &hookBridge{cwd: root},
			mutationPaths:   func(_, _ string) []string { return paths },
			lspFileNotifier: notifier,
		}
	}
	capture := func(gotRootOut string, gotPathsOut []string) {
		calls++
		gotRoot = gotRootOut
		gotPaths = append([]string(nil), gotPathsOut...)
	}

	// 成功分支：notifier 收到 bridge 中的根与 mutationPaths 报告的路径。
	successH := newHooked(&stubToolRunner{out: "ok"}, capture)
	if _, err := successH.Run(context.Background(), "edit", `{"path":"main.go"}`); err != nil {
		t.Fatalf("Run success: %v", err)
	}
	if calls != 1 || gotRoot != root || !reflect.DeepEqual(gotPaths, paths) {
		t.Fatalf("notifier call = %d, root = %q, paths = %#v; want 1, %q, %#v",
			calls, gotRoot, gotPaths, root, paths)
	}

	// 失败分支：绝不能触发通知。
	calls = 0
	failureH := newHooked(&stubToolRunner{out: "", err: errors.New("write failed")}, capture)
	if _, err := failureH.Run(context.Background(), "edit", `{"path":"main.go"}`); err == nil {
		t.Fatal("Run failure: want error")
	}
	if calls != 0 {
		t.Errorf("notifier calls on failure = %d, want 0", calls)
	}

	// nil notifier：成功分支也不产生任何调用（零副作用默认行为）。
	calls = 0
	noNotifierH := newHooked(&stubToolRunner{out: "ok"}, nil)
	if _, err := noNotifierH.Run(context.Background(), "edit", `{"path":"main.go"}`); err != nil {
		t.Fatalf("Run without notifier: %v", err)
	}
	if calls != 0 {
		t.Errorf("notifier calls with nil notifier = %d, want 0", calls)
	}
}
