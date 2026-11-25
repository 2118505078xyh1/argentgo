package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestConnectHealthTrackingOptIn 验证 Connect 按 Spec.HealthTracking 决定是否
// 挂接追踪器：默认关闭时 health 保持 nil，开启时才分配。同时验证 *client
// 始终实现可选的 HealthReporter 接口，且未发现前快照为零值。
func TestConnectHealthTrackingOptIn(t *testing.T) {
	tests := []struct {
		name     string
		tracking bool
	}{
		{name: "默认关闭不挂接追踪器", tracking: false},
		{name: "开启时挂接追踪器", tracking: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := helperSpec("echo", 5*time.Second)
			spec.HealthTracking = tt.tracking
			c, err := Connect(context.Background(), spec)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer c.Close()

			concrete, ok := c.(*client)
			if !ok {
				t.Fatalf("Connect 应返回 *client, got %T", c)
			}
			if got := concrete.health != nil; got != tt.tracking {
				t.Errorf("health 挂接 = %v, want %v", got, tt.tracking)
			}

			// 可选接口断言：不修改 Client 接口，*client 始终实现 HealthReporter。
			reporter, ok := c.(HealthReporter)
			if !ok {
				t.Fatal("Connect 返回的客户端应实现 HealthReporter")
			}
			if got := reporter.HealthSnapshot(); got != (HealthSnapshot{}) {
				t.Errorf("发现前快照 = %+v, want 零值", got)
			}
		})
	}
}

// TestHealthTrackingObserveOnDiscoverTools 表驱动覆盖 discoverTools 两处出口的
// 观测接线：默认关闭时成功/失败都不观测（Gen=0）；开启时成功记录过滤后工具数、
// 失败记录错误并保留 last-good；并通过 HealthReporter 读取快照。
func TestHealthTrackingObserveOnDiscoverTools(t *testing.T) {
	boom := errors.New("boom")

	// threeTools 返回三个工具；配合 DeniedTools={"blocked"} 时过滤后剩两个。
	threeTools := func(json.RawMessage) (any, error) {
		return map[string]any{"tools": []any{
			map[string]any{"name": "a", "inputSchema": map[string]any{"type": "object"}},
			map[string]any{"name": "blocked", "inputSchema": map[string]any{"type": "object"}},
			map[string]any{"name": "c", "inputSchema": map[string]any{"type": "object"}},
		}}, nil
	}
	fail := func(json.RawMessage) (any, error) { return nil, boom }

	tests := []struct {
		name        string
		tracking    bool
		handler     func(json.RawMessage) (any, error)
		denied      []string
		preObserve  bool // 先做一次成功发现，制造 last-good（三工具）
		wantErr     bool
		want        HealthSnapshot // 校验 Healthy/ToolCount/Gen
		wantLastErr string         // 非空时对 LastError 做子串校验
	}{
		{
			name:    "默认关闭：成功不观测",
			handler: threeTools,
			want:    HealthSnapshot{},
		},
		{
			name:    "默认关闭：失败不观测",
			handler: fail,
			wantErr: true,
			want:    HealthSnapshot{},
		},
		{
			name:     "开启：成功观测过滤后工具数",
			tracking: true,
			handler:  threeTools,
			denied:   []string{"blocked"},
			want:     HealthSnapshot{Healthy: true, ToolCount: 2, Gen: 1},
		},
		{
			name:        "开启：失败观测错误",
			tracking:    true,
			handler:     fail,
			wantErr:     true,
			want:        HealthSnapshot{Healthy: false, Gen: 1},
			wantLastErr: "boom",
		},
		{
			name:        "开启：失败保留 last-good 工具数",
			tracking:    true,
			handler:     fail,
			preObserve:  true,
			wantErr:     true,
			want:        HealthSnapshot{Healthy: false, ToolCount: 3, Gen: 2},
			wantLastErr: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := tt.handler
			ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
				"tools/list": func(p json.RawMessage) (any, error) { return current(p) },
			}}
			c := &client{
				spec:    Spec{Name: "srv", DeniedTools: tt.denied, HealthTracking: tt.tracking},
				tr:      ft,
				timeout: time.Second,
			}
			// 与 Connect 的挂接逻辑一致：仅开启时分配 tracker。
			if tt.tracking {
				c.health = &HealthTracker{}
			}
			if tt.preObserve {
				current = threeTools
				if _, err := c.ListTools(context.Background()); err != nil {
					t.Fatalf("预置成功发现: %v", err)
				}
				current = tt.handler
			}

			_, err := c.ListTools(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("ListTools err = %v, wantErr = %v", err, tt.wantErr)
			}

			var reporter HealthReporter = c
			got := reporter.HealthSnapshot()
			if got.Healthy != tt.want.Healthy || got.ToolCount != tt.want.ToolCount || got.Gen != tt.want.Gen {
				t.Errorf("快照 = %+v, want Healthy=%v/ToolCount=%d/Gen=%d",
					got, tt.want.Healthy, tt.want.ToolCount, tt.want.Gen)
			}
			if tt.wantLastErr != "" {
				if !strings.Contains(got.LastError, tt.wantLastErr) {
					t.Errorf("LastError = %q, want 包含 %q", got.LastError, tt.wantLastErr)
				}
			} else if got.LastError != "" {
				t.Errorf("健康快照不应带错误文本, got %q", got.LastError)
			}
		})
	}
}

// TestHealthReporterNotInClientInterface 守护约束：HealthReporter 是可选接口，
// Client 接口本身不得被改动（fake 实现仍可只实现原有五个方法）。
func TestHealthReporterNotInClientInterface(t *testing.T) {
	var c Client = &minimalClient{}
	if _, ok := c.(HealthReporter); ok {
		t.Error("仅实现 Client 的 fake 不应自动满足 HealthReporter")
	}
}

// minimalClient 是只实现 Client 接口方法的 fake，用于确认可选接口的隔离性。
type minimalClient struct{}

func (*minimalClient) Initialize(context.Context) (ServerInfo, error) { return ServerInfo{}, nil }
func (*minimalClient) ListTools(context.Context) ([]Tool, error)      { return nil, nil }
func (*minimalClient) CallTool(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}
func (*minimalClient) ListResources(context.Context) ([]Resource, error) { return nil, nil }
func (*minimalClient) ListResourceTemplates(context.Context) ([]ResourceTemplate, error) {
	return nil, nil
}
func (*minimalClient) ReadResource(context.Context, string) ([]ResourceContent, error) {
	return nil, nil
}
func (*minimalClient) Close() error { return nil }
