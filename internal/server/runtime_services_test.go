package server

import (
	"reflect"
	"strings"
	"testing"
)

// TestRuntimeServicesPrompt 是主表驱动测试，覆盖有/无服务、可用/不可用、
// 端口格式（合法、未分配、越界、IPv6）以及排序与转义。
func TestRuntimeServicesPrompt(t *testing.T) {
	open := runtimeServicesOpenTag + "\n"
	close := runtimeServicesCloseTag

	cases := []struct {
		name     string
		services []RuntimeService
		want     string
	}{
		{
			name:     "nil services",
			services: nil,
			want:     open + close,
		},
		{
			name:     "empty services",
			services: []RuntimeService{},
			want:     open + close,
		},
		{
			name: "single available service",
			services: []RuntimeService{
				{Name: "http", Address: "0.0.0.0", Port: 8080, Available: true},
			},
			want: open + `<service name="http" address="0.0.0.0" port="8080" endpoint="http://0.0.0.0:8080" available="true" />` + "\n" + close,
		},
		{
			name: "single unavailable service",
			services: []RuntimeService{
				{Name: "rpc", Address: "127.0.0.1", Port: 9000, Available: false},
			},
			want: open + `<service name="rpc" address="127.0.0.1" port="9000" endpoint="http://127.0.0.1:9000" available="false" />` + "\n" + close,
		},
		{
			name: "mixed availability sorted by name",
			services: []RuntimeService{
				{Name: "rpc", Address: "127.0.0.1", Port: 9000, Available: false},
				{Name: "http", Address: "0.0.0.0", Port: 8080, Available: true},
			},
			want: open +
				`<service name="http" address="0.0.0.0" port="8080" endpoint="http://0.0.0.0:8080" available="true" />` + "\n" +
				`<service name="rpc" address="127.0.0.1" port="9000" endpoint="http://127.0.0.1:9000" available="false" />` + "\n" + close,
		},
		{
			name: "duplicate name sorted by address then port",
			services: []RuntimeService{
				{Name: "http", Address: "127.0.0.1", Port: 2, Available: true},
				{Name: "http", Address: "0.0.0.0", Port: 9, Available: true},
				{Name: "http", Address: "0.0.0.0", Port: 1, Available: false},
			},
			want: open +
				`<service name="http" address="0.0.0.0" port="1" endpoint="http://0.0.0.0:1" available="false" />` + "\n" +
				`<service name="http" address="0.0.0.0" port="9" endpoint="http://0.0.0.0:9" available="true" />` + "\n" +
				`<service name="http" address="127.0.0.1" port="2" endpoint="http://127.0.0.1:2" available="true" />` + "\n" + close,
		},
		{
			name: "port zero means unassigned",
			services: []RuntimeService{
				{Name: "unix", Address: "localhost", Port: 0, Available: true},
			},
			want: open + `<service name="unix" address="localhost" port="0" endpoint="http://localhost" available="true" />` + "\n" + close,
		},
		{
			name: "negative port normalized to zero",
			services: []RuntimeService{
				{Name: "bad", Address: "127.0.0.1", Port: -1, Available: true},
			},
			want: open + `<service name="bad" address="127.0.0.1" port="0" endpoint="http://127.0.0.1" available="true" />` + "\n" + close,
		},
		{
			name: "out of range port normalized to zero",
			services: []RuntimeService{
				{Name: "bad", Address: "127.0.0.1", Port: 65536, Available: false},
			},
			want: open + `<service name="bad" address="127.0.0.1" port="0" endpoint="http://127.0.0.1" available="false" />` + "\n" + close,
		},
		{
			name: "port lower and upper bounds kept",
			services: []RuntimeService{
				{Name: "a", Address: "127.0.0.1", Port: 1, Available: true},
				{Name: "b", Address: "127.0.0.1", Port: 65535, Available: true},
			},
			want: open +
				`<service name="a" address="127.0.0.1" port="1" endpoint="http://127.0.0.1:1" available="true" />` + "\n" +
				`<service name="b" address="127.0.0.1" port="65535" endpoint="http://127.0.0.1:65535" available="true" />` + "\n" + close,
		},
		{
			name: "ipv6 without brackets gets bracketed",
			services: []RuntimeService{
				{Name: "http", Address: "::1", Port: 1234, Available: true},
			},
			want: open + `<service name="http" address="::1" port="1234" endpoint="http://[::1]:1234" available="true" />` + "\n" + close,
		},
		{
			name: "ipv6 with brackets not double wrapped",
			services: []RuntimeService{
				{Name: "http", Address: "[::1]", Port: 1234, Available: true},
			},
			want: open + `<service name="http" address="[::1]" port="1234" endpoint="http://[::1]:1234" available="true" />` + "\n" + close,
		},
		{
			name: "special characters and cjk are escaped",
			services: []RuntimeService{
				{Name: `a<b>&"中`, Address: "主机<本地>", Port: 80, Available: true},
			},
			want: open + `<service name="a&lt;b&gt;&amp;\"中" address="主机&lt;本地&gt;" port="80" endpoint="http://主机&lt;本地&gt;:80" available="true" />` + "\n" + close,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeServicesPrompt(tc.services); got != tc.want {
				t.Fatalf("runtimeServicesPrompt() mismatch\ngot:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// TestRuntimeServicesPromptDeterministic 验证输出确定且不重排调用方切片：
// 同一集合以不同顺序传入得到相同文本，调用前后入参切片保持不变。
func TestRuntimeServicesPromptDeterministic(t *testing.T) {
	a := RuntimeService{Name: "rpc", Address: "127.0.0.1", Port: 9000, Available: false}
	b := RuntimeService{Name: "http", Address: "0.0.0.0", Port: 8080, Available: true}
	c := RuntimeService{Name: "acp", Address: "::1", Port: 7000, Available: true}

	in1 := []RuntimeService{a, b, c}
	in2 := []RuntimeService{c, a, b}
	snapshot1 := append([]RuntimeService(nil), in1...)
	snapshot2 := append([]RuntimeService(nil), in2...)

	got1 := runtimeServicesPrompt(in1)
	got2 := runtimeServicesPrompt(in2)
	for i := 0; i < 10; i++ {
		if got := runtimeServicesPrompt(in1); got != got1 {
			t.Fatalf("non-deterministic output on repeated call:\n%s\n%s", got1, got)
		}
	}
	if got1 != got2 {
		t.Fatalf("input order changed output:\n%s\n%s", got1, got2)
	}
	if !reflect.DeepEqual(in1, snapshot1) || !reflect.DeepEqual(in2, snapshot2) {
		t.Fatalf("input slice was mutated: in1=%v in2=%v", in1, in2)
	}
}

// TestRuntimeServicesPromptStableSchema 验证任意条目都带齐五个字段，
// 且空集合仍输出成对的容器标签（调用方可无分支拼接）。
func TestRuntimeServicesPromptStableSchema(t *testing.T) {
	got := runtimeServicesPrompt([]RuntimeService{
		{Name: "rpc", Address: "127.0.0.1", Port: 9000},
	})
	for _, field := range []string{`name=`, `address=`, `port=`, `endpoint=`, `available=`} {
		if !strings.Contains(got, field) {
			t.Errorf("missing field %q in %s", field, got)
		}
	}
	if !strings.HasPrefix(got, runtimeServicesOpenTag) || !strings.HasSuffix(got, runtimeServicesCloseTag) {
		t.Fatalf("container tags missing in %s", got)
	}
}
