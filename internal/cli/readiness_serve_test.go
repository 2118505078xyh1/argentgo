package cli

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteServeReadiness(t *testing.T) {
	// 表驱动覆盖 opt-in 门禁与摘要格式：关闭时既不探测也不输出，
	// 开启时恰好写入一行 aggregateReadiness(...).String()。
	allReady := []readinessCheck{
		{name: readinessComponentConfig, ready: true},
		{name: readinessComponentWorkspace, ready: true},
		{name: readinessComponentProvider, ready: true},
	}
	cases := []struct {
		name     string
		enabled  bool
		checks   []readinessCheck
		want     string
		mustFail bool // 关闭时构建函数若被调用则直接失败
	}{
		{
			name:     "disabled writes nothing and never probes",
			enabled:  false,
			checks:   allReady,
			want:     "",
			mustFail: true,
		},
		{
			name:    "enabled all ready",
			enabled: true,
			checks:  allReady,
			want:    "ready\n",
		},
		{
			name:    "enabled empty checks",
			enabled: true,
			checks:  nil,
			want:    "ready\n",
		},
		{
			name:    "enabled single not ready",
			enabled: true,
			checks: []readinessCheck{
				{name: readinessComponentConfig, ready: true},
				{name: readinessComponentProvider, ready: false, reason: "未配置 base URL"},
			},
			want: "未就绪：provider：未配置 base URL\n",
		},
		{
			name:    "enabled multiple not ready preserve order",
			enabled: true,
			checks: []readinessCheck{
				{name: readinessComponentConfig, ready: false, reason: "配置加载失败"},
				{name: readinessComponentWorkspace, ready: false, reason: "工作区不可访问"},
				{name: readinessComponentProvider, ready: true},
			},
			want: "未就绪：config：配置加载失败, workspace：工作区不可访问\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			build := func(context.Context) []readinessCheck {
				if tc.mustFail {
					t.Fatal("readiness checks must not be built when disabled")
				}
				return tc.checks
			}
			writeServeReadiness(&buf, context.Background(), tc.enabled, build)
			if got := buf.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServeStderrDefaultUnchanged(t *testing.T) {
	// 模拟 serve 中 listening 行之后的接线：默认关闭时 stderr 只有既有一行。
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "argentgo listening on http://%s (bearer token required)\n", "127.0.0.1:7777")
	writeServeReadiness(&buf, context.Background(), false, func(context.Context) []readinessCheck {
		t.Fatal("probe must not run by default")
		return nil
	})
	want := "argentgo listening on http://127.0.0.1:7777 (bearer token required)\n"
	if got := buf.String(); got != want {
		t.Fatalf("default stderr = %q, want %q", got, want)
	}
}

func TestReadinessProbesChecks(t *testing.T) {
	// 表驱动覆盖每个组件的就绪/未就绪映射与固定顺序，以及 nil 探测被跳过。
	probe := func(ready bool, reason string) readinessProbe {
		return func(context.Context) (bool, string) { return ready, reason }
	}
	cases := []struct {
		name   string
		probes readinessProbes
		want   []readinessCheck
	}{
		{
			name: "all ready in fixed order",
			probes: readinessProbes{
				config:    probe(true, ""),
				workspace: probe(true, ""),
				provider:  probe(true, ""),
			},
			want: []readinessCheck{
				{name: readinessComponentConfig, ready: true},
				{name: readinessComponentWorkspace, ready: true},
				{name: readinessComponentProvider, ready: true},
			},
		},
		{
			name: "config not ready",
			probes: readinessProbes{
				config:    probe(false, "配置加载失败"),
				workspace: probe(true, ""),
				provider:  probe(true, ""),
			},
			want: []readinessCheck{
				{name: readinessComponentConfig, ready: false, reason: "配置加载失败"},
				{name: readinessComponentWorkspace, ready: true},
				{name: readinessComponentProvider, ready: true},
			},
		},
		{
			name: "workspace not ready",
			probes: readinessProbes{
				config:    probe(true, ""),
				workspace: probe(false, "工作区不可访问"),
				provider:  probe(true, ""),
			},
			want: []readinessCheck{
				{name: readinessComponentConfig, ready: true},
				{name: readinessComponentWorkspace, ready: false, reason: "工作区不可访问"},
				{name: readinessComponentProvider, ready: true},
			},
		},
		{
			name: "provider not ready",
			probes: readinessProbes{
				config:    probe(true, ""),
				workspace: probe(true, ""),
				provider:  probe(false, "提供方不可用"),
			},
			want: []readinessCheck{
				{name: readinessComponentConfig, ready: true},
				{name: readinessComponentWorkspace, ready: true},
				{name: readinessComponentProvider, ready: false, reason: "提供方不可用"},
			},
		},
		{
			name: "all not ready",
			probes: readinessProbes{
				config:    probe(false, "c"),
				workspace: probe(false, "w"),
				provider:  probe(false, "p"),
			},
			want: []readinessCheck{
				{name: readinessComponentConfig, ready: false, reason: "c"},
				{name: readinessComponentWorkspace, ready: false, reason: "w"},
				{name: readinessComponentProvider, ready: false, reason: "p"},
			},
		},
		{
			name: "nil probe skipped",
			probes: readinessProbes{
				config: probe(true, ""),
			},
			want: []readinessCheck{
				{name: readinessComponentConfig, ready: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.probes.checks(context.Background())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("checks = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func readinessCheckByName(t *testing.T, checks []readinessCheck, name string) readinessCheck {
	t.Helper()
	for _, c := range checks {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, checks)
	return readinessCheck{}
}

func TestAppReadinessProbesLocal(t *testing.T) {
	// 真实依赖探测，但不接触网络：env 不含 base URL，provider 探测会在
	// checkProvider 之前就返回未就绪。覆盖工作区存在与不存在两种情形。
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cases := []struct {
		name            string
		cwd             string
		wantWorkspace   bool
		wantConfigReady bool
	}{
		{name: "existing workspace", cwd: t.TempDir(), wantWorkspace: true, wantConfigReady: true},
		{name: "missing workspace", cwd: missing, wantWorkspace: false, wantConfigReady: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := App{IO: IO{Env: func(string) string { return "" }}}
			opts := &Options{Cwd: tc.cwd}
			checks := app.appReadinessProbes(opts).checks(context.Background())

			ws := readinessCheckByName(t, checks, readinessComponentWorkspace)
			if ws.ready != tc.wantWorkspace {
				t.Fatalf("workspace ready = %v, want %v (reason %q)", ws.ready, tc.wantWorkspace, ws.reason)
			}
			cfg := readinessCheckByName(t, checks, readinessComponentConfig)
			if cfg.ready != tc.wantConfigReady {
				t.Fatalf("config ready = %v, want %v (reason %q)", cfg.ready, tc.wantConfigReady, cfg.reason)
			}
			prov := readinessCheckByName(t, checks, readinessComponentProvider)
			if prov.ready || prov.reason != "未配置 base URL" {
				t.Fatalf("provider = %+v, want not-ready without base URL", prov)
			}
		})
	}
}

func TestServeReadinessFlagParsing(t *testing.T) {
	// 表驱动验证 opt-in flag 默认关闭，且支持显式 true/false。
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "default off", args: nil, want: false},
		{name: "bare flag", args: []string{"--serve-readiness"}, want: true},
		{name: "explicit true", args: []string{"--serve-readiness=true"}, want: true},
		{name: "explicit false", args: []string{"--serve-readiness=false"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, err := parseArgs(tc.args, func(string) string { return "" }, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if opts.ServeReadiness != tc.want {
				t.Fatalf("ServeReadiness = %v, want %v", opts.ServeReadiness, tc.want)
			}
		})
	}
}
