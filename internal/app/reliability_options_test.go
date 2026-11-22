package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/cli"
)

// TestParseStreamIdleTimeout 表驱动验证流空闲超时的解析与诊断文本：
// 合法值（纯秒数与 Go 时长）静默生效，非法/非正值禁用并返回警告文本。
func TestParseStreamIdleTimeout(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		want     time.Duration
		wantWarn bool
	}{
		{name: "unset", raw: "", want: 0, wantWarn: false},
		{name: "plain seconds", raw: "2", want: 2 * time.Second, wantWarn: false},
		{name: "duration milliseconds", raw: "500ms", want: 500 * time.Millisecond, wantWarn: false},
		{name: "duration seconds", raw: "30s", want: 30 * time.Second, wantWarn: false},
		{name: "illegal text", raw: "abc", want: 0, wantWarn: true},
		{name: "negative integer", raw: "-1", want: 0, wantWarn: true},
		{name: "zero integer", raw: "0", want: 0, wantWarn: true},
		{name: "negative duration", raw: "-5s", want: 0, wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning := parseStreamIdleTimeout(tc.raw)
			if got != tc.want {
				t.Fatalf("parseStreamIdleTimeout(%q) duration = %v, want %v", tc.raw, got, tc.want)
			}
			if (warning != "") != tc.wantWarn {
				t.Fatalf("parseStreamIdleTimeout(%q) warning = %q, wantWarn=%v", tc.raw, warning, tc.wantWarn)
			}
			if tc.wantWarn && !strings.Contains(warning, streamIdleTimeoutEnv) {
				t.Errorf("warning %q should name env %q", warning, streamIdleTimeoutEnv)
			}
		})
	}
}

// TestStreamIdleTimeoutWarningViaResolve 验证诊断经 Resolve 的 warn 通道输出：
// 非法值写入 stderr，合法值/未设置保持静默；返回时长与合法值一致。
func TestStreamIdleTimeoutWarningViaResolve(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		want     time.Duration
		wantWarn bool
	}{
		{name: "illegal", raw: "abc", want: 0, wantWarn: true},
		{name: "negative", raw: "-1", want: 0, wantWarn: true},
		{name: "legal ms", raw: "500ms", want: 500 * time.Millisecond, wantWarn: false},
		{name: "legal seconds", raw: "2", want: 2 * time.Second, wantWarn: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			env := isolatedEnv(home, map[string]string{streamIdleTimeoutEnv: tc.raw})
			var errBuf bytes.Buffer
			if _, err := Resolve(cli.IO{Env: env, Err: &errBuf}, cli.Options{Cwd: root}); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			// Resolve 本身不缓存时长，这里用同一 env 经纯函数复核解析结果。
			got, _ := streamIdleTimeoutFromEnv(env)
			if got != tc.want {
				t.Errorf("streamIdleTimeoutFromEnv = %v, want %v", got, tc.want)
			}
			warned := strings.Contains(errBuf.String(), streamIdleTimeoutEnv)
			if warned != tc.wantWarn {
				t.Errorf("stderr = %q, wantWarn=%v", errBuf.String(), tc.wantWarn)
			}
		})
	}
}
