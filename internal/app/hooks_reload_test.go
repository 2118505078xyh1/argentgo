package app

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/argentgo/internal/config"
	"example.com/argentgo/internal/hooks"
)

// configCaptureDispatcher 记录每次 Dispatch 拿到的 *hooks.Config，使测试能断言
// fire 用的是 store 里的最新快照，而不是启动时的静态指针。
type configCaptureDispatcher struct {
	mu      sync.Mutex
	configs []*hooks.Config
}

func (d *configCaptureDispatcher) Dispatch(_ context.Context, cfg *hooks.Config, _ hooks.Payload) (hooks.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.configs = append(d.configs, cfg)
	return hooks.Result{}, nil
}

func (d *configCaptureDispatcher) lastConfig() *hooks.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.configs) == 0 {
		return nil
	}
	return d.configs[len(d.configs)-1]
}

// mutableSource 是线程安全的内存来源，测试可在两次 poll 之间替换字节，
// 模拟编辑了钩子文档；读侧加锁使 -race 下无数据竞争。
type mutableSource struct {
	key  string
	mu   sync.RWMutex
	data []byte
}

func (s *mutableSource) Key() string { return s.key }

func (s *mutableSource) Bytes() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.data...), nil
}

func (s *mutableSource) set(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
}

func firstCommand(cfg *hooks.Config) string {
	if cfg == nil || len(cfg.Commands) == 0 {
		return ""
	}
	return cfg.Commands[0].Command
}

// reloadTestFixture 构造一个已把 initialDoc 作为 last-good 采纳进 store 的 bridge。
// 信任键用反斜杠授予、来源键用正斜杠，顺带覆盖 CJK 路径的分隔符归一化。
func reloadTestFixture(t *testing.T, key, grantKey, initialDoc string, alsoTrust ...string) (
	*hookBridge, *configCaptureDispatcher, *mutableSource, *bytes.Buffer,
) {
	t.Helper()
	trust := hooks.NewTrustStore()
	trust.Trust(grantKey, hooks.Digest([]byte(initialDoc)))
	for _, doc := range alsoTrust {
		trust.Trust(grantKey, hooks.Digest([]byte(doc)))
	}
	src := &mutableSource{key: key, data: []byte(initialDoc)}
	initialConfig, err := hooks.ParseConfig([]byte(initialDoc))
	if err != nil {
		t.Fatal(err)
	}
	store := &hooks.SnapshotStore{}
	store.Swap(hooks.ConfigSnapshot{Config: initialConfig, Generation: 1}, 1)
	d := &configCaptureDispatcher{}
	var warn bytes.Buffer
	bridge := &hookBridge{
		dispatcher:    d,
		config:        initialConfig,
		store:         store,
		sources:       []hooks.Source{src},
		trust:         trust,
		reloadEnabled: true,
		warn:          &warn,
	}
	return bridge, d, src, &warn
}

// TestHookReloadPollAdoptsChangedDocument 验证：来源字节变化后一次 poll 即采纳新
// 配置，随后 fire 用新命令；CJK 信任键（授予用 "\"、来源用 "/"）也能通过信任门。
func TestHookReloadPollAdoptsChangedDocument(t *testing.T) {
	const key = `配置/钩子.json`
	const grantKey = `配置\钩子.json`
	const oldDoc = `{"hooks":{"PostToolUse":[{"command":"echo old"}]}}`
	const newDoc = `{"hooks":{"PostToolUse":[{"command":"echo new"}]}}`

	bridge, d, src, warn := reloadTestFixture(t, key, grantKey, oldDoc, newDoc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := bridge.fire(ctx, hooks.PostToolUse, hooks.Payload{}); err != nil {
		t.Fatalf("fire before reload: %v", err)
	}
	if got := firstCommand(d.lastConfig()); got != "echo old" {
		t.Fatalf("config before reload = %q, want echo old", got)
	}

	src.set([]byte(newDoc))
	bridge.poll(ctx)

	if _, err := bridge.fire(ctx, hooks.PostToolUse, hooks.Payload{}); err != nil {
		t.Fatalf("fire after reload: %v", err)
	}
	if got := firstCommand(d.lastConfig()); got != "echo new" {
		t.Fatalf("config after reload = %q, want echo new", got)
	}
	if got := warn.String(); got != "" {
		t.Fatalf("successful reload wrote warn: %q", got)
	}
	if got := bridge.store.Load().Generation; got != 2 {
		t.Fatalf("generation = %d, want 2", got)
	}
}

// TestHookReloadFailureFallsBackToLastGood 验证：新字节未授信时 poll 只记账并写
// warn，fire 仍使用上一次成功的快照（last-good）。
func TestHookReloadFailureFallsBackToLastGood(t *testing.T) {
	const key = `配置/钩子.json`
	const grantKey = `配置\钩子.json`
	const oldDoc = `{"hooks":{"PostToolUse":[{"command":"echo old"}]}}`
	const goodDoc = `{"hooks":{"PostToolUse":[{"command":"echo good"}]}}`
	// 合法 JSON 且命令不同，但没有授予它的摘要——必须通过信任门被拒。
	const badDoc = `{"hooks":{"PostToolUse":[{"command":"echo evil"}]}}`

	bridge, d, src, warn := reloadTestFixture(t, key, grantKey, oldDoc, goodDoc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 先成功采纳一次，建立 last-good = echo good。
	src.set([]byte(goodDoc))
	bridge.poll(ctx)
	if got := firstCommand(bridge.store.Load().Config); got != "echo good" {
		t.Fatalf("setup snapshot = %q, want echo good", got)
	}
	if _, err := bridge.fire(ctx, hooks.PostToolUse, hooks.Payload{}); err != nil {
		t.Fatalf("fire good: %v", err)
	}
	if got := firstCommand(d.lastConfig()); got != "echo good" {
		t.Fatalf("adopted config = %q, want echo good", got)
	}

	// 未授信的新内容触发失败：store 保留 last-good，warn 有记录。
	src.set([]byte(badDoc))
	bridge.poll(ctx)
	if bridge.store.LastError() == nil {
		t.Fatal("failure was not recorded via NoteFailure")
	}
	if !strings.Contains(warn.String(), "hooks reload") {
		t.Fatalf("warn = %q, want a hooks reload failure note", warn.String())
	}
	good := bridge.store.LastGood()
	if got := firstCommand(good.Config); got != "echo good" {
		t.Fatalf("LastGood = %q, want echo good", got)
	}
	if loaded := bridge.store.Load(); loaded.Fingerprint != good.Fingerprint {
		t.Fatalf("Load() != LastGood() after failure: %+v vs %+v", loaded, good)
	}
	if _, err := bridge.fire(ctx, hooks.PostToolUse, hooks.Payload{}); err != nil {
		t.Fatalf("fire after failure: %v", err)
	}
	if got := firstCommand(d.lastConfig()); got != "echo good" {
		t.Fatalf("fire used %q after failure, want last-good echo good", got)
	}
}

// TestHookReloadDisabledByDefault 验证默认关闭：纯门控函数为 false，真实构造的
// bridge 不接 store/sources，fire 逐字节沿用静态 config 路径。
func TestHookReloadDisabledByDefault(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, false},
		{"missing", func(string) string { return "" }, false},
		{"zero", func(string) string { return "0" }, false},
		{"true word", func(string) string { return "true" }, false},
		{"one", func(string) string { return "1" }, true},
		{"padded one", func(string) string { return " 1 " }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := hookReloadEnabled(tt.env); got != tt.want {
				t.Fatalf("hookReloadEnabled = %v, want %v", got, tt.want)
			}
		})
	}

	// 真实构造路径：确保进程环境变量未开启时 store/sources 保持零值。
	t.Setenv(hookReloadEnv, "0")
	root := t.TempDir()
	storage := t.TempDir()
	path := root + "/hooks.json"
	writeFile(t, path, `{"hooks":{"Stop":[{"command":"echo stop"}]}}`)
	trust := hooks.NewTrustStore()
	if _, err := trust.TrustFile(path); err != nil {
		t.Fatal(err)
	}
	if err := trust.SaveFile(storage + "/hooks-trust.json"); err != nil {
		t.Fatal(err)
	}
	d := &configCaptureDispatcher{}
	res := Resolved{
		Root: root, Storage: storage, Home: t.TempDir(),
		Cfg:            config.Config{Raw: map[string]any{"hooks": path}},
		HookDispatcher: d,
	}
	bridge := newHookBridge(res, "s", nil)
	if bridge == nil {
		t.Fatal("trusted document produced no bridge")
	}
	if bridge.reloadEnabled || bridge.store != nil || len(bridge.sources) != 0 {
		t.Fatalf("reload must be off by default: enabled=%v store=%v sources=%d",
			bridge.reloadEnabled, bridge.store, len(bridge.sources))
	}
	// 即使人为塞入一个内容不同的 store，关闭状态下 fire 也必须读静态 config。
	static := bridge.config
	other := &hooks.SnapshotStore{}
	other.Swap(hooks.ConfigSnapshot{Config: &hooks.Config{Commands: []hooks.Command{{Command: "echo other"}}}}, 1)
	bridge.store = other
	if _, err := bridge.fire(context.Background(), hooks.Stop, hooks.Payload{}); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if got := d.lastConfig(); got != static {
		t.Fatalf("fire did not use the static config on the default path")
	}
}

// TestHookReloadGoroutineStopsOnCancel 验证轮询 goroutine 会随 ctx 取消退出，
// 且轮询期间 fire 与 poll 并发访问 store 时 -race 下无数据竞争。
func TestHookReloadGoroutineStopsOnCancel(t *testing.T) {
	const key = `配置/钩子.json`
	const grantKey = `配置\钩子.json`
	const oldDoc = `{"hooks":{"PostToolUse":[{"command":"echo old"}]}}`
	const newDoc = `{"hooks":{"PostToolUse":[{"command":"echo new"}]}}`

	bridge, d, src, _ := reloadTestFixture(t, key, grantKey, oldDoc, newDoc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge.reloadEvery = 5 * time.Millisecond

	done := make(chan struct{})
	go bridge.runReloadLoop(ctx, bridge.reloadEvery, done)

	// 改字节后等待循环采纳（上限 1 秒，避免挂死）。
	src.set([]byte(newDoc))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if firstCommand(bridge.store.Load().Config) == "echo new" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := firstCommand(bridge.store.Load().Config); got != "echo new" {
		t.Fatalf("poll never adopted the new config: %q", got)
	}

	// 轮询与 fire 并发时也只看到 store 的快照。
	if _, err := bridge.fire(ctx, hooks.PostToolUse, hooks.Payload{}); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if got := firstCommand(d.lastConfig()); got != "echo new" {
		t.Fatalf("concurrent fire config = %q, want echo new", got)
	}

	// 取消后循环必须在合理时间内退出，不泄漏 goroutine。
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload goroutine did not stop after ctx cancel")
	}
}
