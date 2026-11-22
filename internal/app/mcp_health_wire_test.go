package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/mcp"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// healthFakeClient 是带可选 HealthReporter 视图的假 MCP 客户端。它复用包内
// fakeMCPClient 的五个 Client 方法，只追加健康快照，从而验证 app 的选择性读取路径。
type healthFakeClient struct {
	fakeMCPClient
	snap mcp.HealthSnapshot
}

func (f *healthFakeClient) HealthSnapshot() mcp.HealthSnapshot { return f.snap }

// TestMCPHealthEnabled 表驱动锁定 opt-in 判定：仅首尾空白修剪后等于 "1" 才开启，
// env 为 nil 时退化为 os.Getenv（默认未设置即关闭）。
func TestMCPHealthEnabled(t *testing.T) {
	tests := []struct {
		name   string
		getenv func(string) string
		want   bool
	}{
		{name: "nil env", getenv: nil, want: false},
		{name: "unset", getenv: func(string) string { return "" }, want: false},
		{name: "zero", getenv: func(string) string { return "0" }, want: false},
		{name: "true literal", getenv: func(string) string { return "true" }, want: false},
		{name: "one", getenv: func(string) string { return "1" }, want: true},
		{name: "padded one", getenv: func(string) string { return " \t1\n" }, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpHealthEnabled(tt.getenv); got != tt.want {
				t.Fatalf("mcpHealthEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

// newHealthToolset 构造一个只用于健康接线的工具集：不加载配置/插件，避免任何
// 真实进程或网络；healthFile 指向临时目录，使落盘可断言且不污染仓库。
func newHealthToolset(t *testing.T, healthOn bool) (*toolset, string) {
	t.Helper()
	set := &toolset{}
	dir := t.TempDir()
	if healthOn {
		set.health = &mcp.HealthAggregator{}
		set.healthFile = filepath.Join(dir, mcp.MCPHealthReportFile)
	}
	return set, dir
}

// TestToolsetHealthTrackPollUntrackAndPersist 是端到端接线：一台健康（CJK 名称、
// 实现 HealthReporter）与一台连接失败（工厂直接报错）。断言 MCPHealthReport 含
// 失败服务名与 LastError、Close 落盘可被重新读取、已 Track 的服务在 Close 后 Untrack。
func TestToolsetHealthTrackPollUntrackAndPersist(t *testing.T) {
	boom := errors.New("握手超时 boom")
	healthyName := "健康服务"
	failedName := "失败服务"

	useFakeMCP(t, func(_ context.Context, spec mcp.Spec) (mcp.Client, error) {
		if spec.Name == failedName {
			return nil, boom
		}
		return &healthFakeClient{snap: mcp.HealthSnapshot{
			Healthy:   true,
			ToolCount: 3,
			Gen:       7,
		}}, nil
	})
	set, dir := newHealthToolset(t, true)
	stdio := cli.IO{Err: &bytes.Buffer{}}

	set.connect(context.Background(), stdio, mcp.Spec{Name: healthyName, Command: "fake"})
	set.connect(context.Background(), stdio, mcp.Spec{Name: failedName, Command: "fake"})

	report := set.MCPHealthReport()
	if len(report.Servers) != 2 {
		t.Fatalf("servers = %d, want 2: %+v", len(report.Servers), report.Servers)
	}
	// Servers 按 Name 字节序排序；失败服务在健康服务之前（CJK 首字不同）。
	var gotHealthy, gotFailed bool
	for _, server := range report.Servers {
		switch server.Name {
		case failedName:
			gotFailed = true
			if server.Healthy || !strings.Contains(server.LastError, boom.Error()) {
				t.Errorf("失败服务 = %+v, want 不健康且含 LastError %q", server, boom)
			}
		case healthyName:
			gotHealthy = true
			if !server.Healthy || server.ToolCount != 3 {
				t.Errorf("健康服务 = %+v, want healthy/toolCount=3", server)
			}
		}
	}
	if !gotHealthy || !gotFailed {
		t.Fatalf("报告缺少服务: healthy=%v failed=%v (%+v)", gotHealthy, gotFailed, report.Servers)
	}

	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close 已 Untrack，再次读取不应再含被 Track 的健康服务。
	if again := set.MCPHealthReport(); len(again.Servers) != 1 || again.Servers[0].Name != failedName {
		t.Fatalf("Untrack 后报告 = %+v, want 仅失败服务", again.Servers)
	}

	// 快照文件在 Untrack 之前写入，因此仍含两台服务的完整信息。
	data, err := os.ReadFile(filepath.Join(dir, mcp.MCPHealthReportFile))
	if err != nil {
		t.Fatalf("读取健康快照: %v", err)
	}
	var persisted mcp.MCPHealthReport
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("解析健康快照: %v", err)
	}
	if len(persisted.Servers) != 2 {
		t.Fatalf("落盘服务数 = %d, want 2: %s", len(persisted.Servers), data)
	}
	// CJK 服务名必须以原字节写盘，不能出现 \uXXXX 转义或截断。
	if !strings.Contains(string(data), healthyName) || !strings.Contains(string(data), failedName) {
		t.Fatalf("落盘快照丢失 CJK 服务名: %s", data)
	}
}

// TestToolsetHealthDefaultOff 锁定默认关闭：health 为 nil 时 connect 不观测、
// MCPHealthReport 返回空报告、Close 不写任何文件。
func TestToolsetHealthDefaultOff(t *testing.T) {
	var gotSpec mcp.Spec
	useFakeMCP(t, func(_ context.Context, spec mcp.Spec) (mcp.Client, error) {
		gotSpec = spec
		return &healthFakeClient{snap: mcp.HealthSnapshot{Healthy: true}}, nil
	})
	set, dir := newHealthToolset(t, false)
	set.connect(context.Background(), cli.IO{Err: &bytes.Buffer{}}, mcp.Spec{Name: "svc", Command: "fake"})

	if gotSpec.HealthTracking {
		t.Fatal("默认关闭时 Spec.HealthTracking 不应被设为 true")
	}
	if set.health != nil {
		t.Fatal("默认关闭时不应分配 HealthAggregator")
	}
	if report := set.MCPHealthReport(); len(report.Servers) != 0 || !report.OverallHealthy {
		t.Fatalf("默认报告 = %+v, want 空且 OverallHealthy", report)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, mcp.MCPHealthReportFile)); !os.IsNotExist(err) {
		t.Fatalf("默认关闭却写入了健康快照: %v", err)
	}
}

// TestNewToolsetHealthOptIn 验证装配层：env 开启时分配聚合器、healthFile 指向存储目录，
// 并把 HealthTracking=true 传给每一台客户端；env 关闭时两者都为空。
func TestNewToolsetHealthOptIn(t *testing.T) {
	root := t.TempDir()
	res := Resolved{
		Root:    root,
		Storage: t.TempDir(),
		Cfg: config.Config{Raw: map[string]any{
			"mcpServers": map[string]any{"svc": map[string]any{"command": "fake-server"}},
		}},
		WriteRoots: security.WriteRootsFrom(root, nil),
	}

	for _, tc := range []struct {
		name       string
		env        string
		wantHealth bool
	}{
		{name: "opt-in off", env: "0", wantHealth: false},
		{name: "opt-in on", env: "1", wantHealth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotSpec mcp.Spec
			useFakeMCP(t, func(_ context.Context, spec mcp.Spec) (mcp.Client, error) {
				gotSpec = spec
				return &healthFakeClient{snap: mcp.HealthSnapshot{Healthy: true}}, nil
			})
			store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			sess, err := store.NewSession("health")
			if err != nil {
				t.Fatal(err)
			}
			env := envFrom(map[string]string{mcp.MCPHealthEnv: tc.env})
			set := newToolset(context.Background(), cli.IO{Env: env, Err: &bytes.Buffer{}}, res, sess, nil, nil)
			t.Cleanup(func() { _ = set.Close() })

			if (set.health != nil) != tc.wantHealth {
				t.Fatalf("health 分配 = %v, want %v", set.health != nil, tc.wantHealth)
			}
			if tc.wantHealth {
				if !gotSpec.HealthTracking {
					t.Fatal("opt-in 开启时 Spec.HealthTracking 应为 true")
				}
				wantFile := filepath.Join(res.Storage, mcp.MCPHealthReportFile)
				if set.healthFile != wantFile {
					t.Fatalf("healthFile = %q, want %q", set.healthFile, wantFile)
				}
			} else if gotSpec.HealthTracking {
				t.Fatal("opt-in 关闭时 Spec.HealthTracking 不应为 true")
			}
		})
	}
}

// TestWriteMCPHealthReportNoEmptyFile 报告为空时不写文件，避免健康运行留下副作用。
func TestWriteMCPHealthReportNoEmptyFile(t *testing.T) {
	set, dir := newHealthToolset(t, true)
	set.writeMCPHealthReport()
	if _, err := os.Stat(filepath.Join(dir, mcp.MCPHealthReportFile)); !os.IsNotExist(err) {
		t.Fatalf("空报告却写入了文件: %v", err)
	}
}
