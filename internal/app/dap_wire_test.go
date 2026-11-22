package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestDapToolsEnabled(t *testing.T) {
	cases := []struct {
		env  func(string) string
		want bool
	}{
		{nil, true},
		{func(string) string { return "" }, true},
		{func(string) string { return " 1 " }, true},
		{func(string) string { return "0" }, false},
		{func(string) string { return " 0 " }, false},
	}
	for _, tc := range cases {
		if got := dapToolsEnabled(tc.env); got != tc.want {
			t.Errorf("dapToolsEnabled = %v, want %v", got, tc.want)
		}
	}
}

func dapToolNames() []string {
	return []string{
		"debug_start", "debug_breakpoint", "debug_continue", "debug_step",
		"debug_threads", "debug_stack", "debug_variables", "debug_stop",
	}
}

func TestWireDapRegistersAllToolsAndIsIdempotent(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}

	wireDap(cli.IO{}, set)
	wireDap(cli.IO{}, set) // 幂等：重复装配不重复注册

	count := map[string]int{}
	for _, name := range r.Names() {
		count[name]++
	}
	for _, name := range dapToolNames() {
		if count[name] != 1 {
			t.Fatalf("工具 %q 注册 %d 次，期望 1", name, count[name])
		}
	}
}

func TestWireDapDisabledRegistersNothing(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}
	wireDap(cli.IO{Env: func(string) string { return "0" }}, set)

	registered := map[string]bool{}
	for _, name := range r.Names() {
		registered[name] = true
	}
	for _, name := range dapToolNames() {
		if registered[name] {
			t.Fatalf("ARGENT_DAP=0 时不应注册 %q", name)
		}
	}
}

func TestWireDapNilToolsetIsNoop(t *testing.T) {
	wireDap(cli.IO{}, nil)
	wireDap(cli.IO{}, &toolset{})
}
