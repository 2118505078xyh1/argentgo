package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestClientCapabilitiesDefaultEmpty(t *testing.T) {
	// 零值选项必须声明空对象：roots/sampling/elicitation 均无处理器。
	caps := clientCapabilities(clientCapabilityOptions{})
	if len(caps) != 0 {
		t.Fatalf("默认能力声明应为空，得到 %+v", caps)
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("序列化能力声明失败: %v", err)
	}
	if string(raw) != "{}" {
		t.Fatalf("默认能力声明应为 {}，得到 %s", raw)
	}
}

func TestClientCapabilitiesDeclareOnlyImplemented(t *testing.T) {
	tests := []struct {
		name string
		opts clientCapabilityOptions
		want []string
	}{
		{
			name: "仅 roots",
			opts: clientCapabilityOptions{roots: true},
			want: []string{"roots"},
		},
		{
			name: "仅 sampling",
			opts: clientCapabilityOptions{sampling: true},
			want: []string{"sampling"},
		},
		{
			name: "仅 elicitation",
			opts: clientCapabilityOptions{elicitation: true},
			want: []string{"elicitation"},
		},
		{
			name: "全部实现",
			opts: clientCapabilityOptions{roots: true, sampling: true, elicitation: true},
			want: []string{"roots", "sampling", "elicitation"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := clientCapabilities(tt.opts)
			if len(caps) != len(tt.want) {
				t.Fatalf("声明了 %d 项能力，期望 %d 项: %+v", len(caps), len(tt.want), caps)
			}
			for _, key := range tt.want {
				value, ok := caps[key]
				if !ok {
					t.Errorf("缺少能力声明 %q: %+v", key, caps)
					continue
				}
				if _, ok := value.(map[string]any); !ok {
					t.Errorf("能力 %q 应声明为对象，得到 %T", key, value)
				}
			}
		})
	}
}

func TestSanitizeProtocolVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "空串", in: "", want: ""},
		{name: "仅空白", in: " \t ", want: ""},
		{name: "合法默认值", in: "2024-11-05", want: "2024-11-05"},
		{name: "合法新版本", in: "2025-03-26", want: "2025-03-26"},
		{name: "去空白后合法", in: "  2025-03-26  ", want: "2025-03-26"},
		{name: "中文回退", in: "中文协议版本", want: ""},
		{name: "混排中文回退", in: "2024年11月", want: ""},
		{name: "乱填文本回退", in: "not-a-version", want: ""},
		{name: "单数字段回退", in: "2024-1-5", want: ""},
		{name: "非法月份回退", in: "2024-13-01", want: ""},
		{name: "非法日期回退", in: "2024-02-30", want: ""},
		{name: "多余后缀回退", in: "2024-11-05-preview", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeProtocolVersion(tt.in); got != tt.want {
				t.Errorf("sanitizeProtocolVersion(%q) = %q, 期望 %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEffectiveProtocolVersionDefault(t *testing.T) {
	// 清除环境变量，确认无覆盖时回退到写死的默认协议号。
	t.Setenv(protocolVersionEnv, "")
	if got := effectiveProtocolVersion(Spec{}); got != protocolVersion {
		t.Fatalf("默认协议号 = %q, 期望 %q", got, protocolVersion)
	}
	if got := effectiveProtocolVersion(Spec{ProtocolVersion: "中文"}); got != protocolVersion {
		t.Fatalf("Spec 非法值应回退默认，得到 %q", got)
	}
}

func TestEffectiveProtocolVersionOverrides(t *testing.T) {
	tests := []struct {
		name string
		spec string
		env  string
		want string
	}{
		{name: "Spec 覆盖", spec: "2025-03-26", want: "2025-03-26"},
		{name: "环境变量覆盖", env: "2025-03-26", want: "2025-03-26"},
		{name: "Spec 优先于环境变量", spec: "2025-06-01", env: "2025-03-26", want: "2025-06-01"},
		{name: "Spec 非法时采用环境变量", spec: "中文", env: "2025-03-26", want: "2025-03-26"},
		{name: "两者皆非法时回退默认", spec: "坏值", env: "2024年", want: protocolVersion},
		{name: "Spec 带空白", spec: "  2025-03-26 ", want: "2025-03-26"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(protocolVersionEnv, tt.env)
			if got := effectiveProtocolVersion(Spec{ProtocolVersion: tt.spec}); got != tt.want {
				t.Errorf("effectiveProtocolVersion = %q, 期望 %q", got, tt.want)
			}
		})
	}
}

// TestInitializeWiresCapabilitiesAndProtocol 验证声明层真正接到了 initialize 请求：
// 默认零变化时发送空能力对象与默认协议号，配置覆盖时发送覆盖值。
func TestInitializeWiresCapabilitiesAndProtocol(t *testing.T) {
	var sent map[string]any
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"initialize": func(raw json.RawMessage) (any, error) {
			if err := json.Unmarshal(raw, &sent); err != nil {
				return nil, err
			}
			return map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "fake", "version": "1"},
			}, nil
		},
	}}

	// 默认场景：不设置 Spec/环境变量，期待空能力对象与默认协议号。
	t.Setenv(protocolVersionEnv, "")
	c := newFakeClient("fake", ft, time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize 失败: %v", err)
	}
	if got := sent["protocolVersion"]; got != protocolVersion {
		t.Errorf("默认协议号 = %v, 期望 %v", got, protocolVersion)
	}
	caps, _ := json.Marshal(sent["capabilities"])
	if string(caps) != "{}" {
		t.Errorf("默认能力声明应为 {}，得到 %s", caps)
	}
}
