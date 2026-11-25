package cli

import (
	"flag"
	"io"
	"strconv"
	"testing"

	"example.com/argentgo/internal/agent"
)

// R16 行为改变：--max-turns 的编译默认值从 12 抬高到 100，与引擎
// agent.DefaultMaxTurns 对齐。本文件是守护测试：锁定 flag 默认值与引擎一致，
// 并确认所有覆盖渠道仍然生效。
// 字面量 100 是期望值（依赖默认值改动）。

// TestMaxTurnsFlagDefaultAlignsWithEngine 直接检查 flag 注册处的默认值。
func TestMaxTurnsFlagDefaultAlignsWithEngine(t *testing.T) {
	if agent.DefaultMaxTurns != 100 {
		t.Fatalf("agent.DefaultMaxTurns = %d, want 100", agent.DefaultMaxTurns)
	}
	fs := flag.NewFlagSet("argent-max-turns-guard", flag.ContinueOnError)
	newFlags(fs, &flagValues{})
	f := fs.Lookup("max-turns")
	if f == nil {
		t.Fatal("max-turns flag not registered")
	}
	want := strconv.Itoa(agent.DefaultMaxTurns)
	if f.DefValue != want {
		t.Fatalf("max-turns DefValue = %q, want %q", f.DefValue, want)
	}
}

// TestMaxTurnsParsedDefaultAlignsWithEngine 确认无参调用 parseArgs 时，
// Options.MaxTurns 携带与引擎对齐的默认值。
func TestMaxTurnsParsedDefaultAlignsWithEngine(t *testing.T) {
	stdio, _, _ := newTestIO("", nil)
	opts, _, err := parseArgs(nil, stdio.Env, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if opts.MaxTurns != agent.DefaultMaxTurns {
		t.Fatalf("MaxTurns = %d, want engine default %d", opts.MaxTurns, agent.DefaultMaxTurns)
	}
	// 期望值（依赖默认值改动）：显式锁定 100。
	if opts.MaxTurns != 100 {
		t.Fatalf("MaxTurns = %d, want 100", opts.MaxTurns)
	}
}

// TestMaxTurnsOverridesStillWin 验证抬高默认值没有剥夺任何既有覆盖渠道。
func TestMaxTurnsOverridesStillWin(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want int
	}{
		{name: "unset uses engine default", want: 100},
		{name: "flag", args: []string{"--max-turns", "7"}, want: 7},
		{name: "flag equals form", args: []string{"--max-turns=7"}, want: 7},
		{name: "environment", env: map[string]string{"ARGENT_MAX_TURNS": "8"}, want: 8},
		{name: "camel case override", args: []string{"-c", "maxTurns=9"}, want: 9},
		{
			name: "flag beats environment",
			args: []string{"--max-turns", "7"},
			env:  map[string]string{"ARGENT_MAX_TURNS": "8"},
			want: 7,
		},
		{
			name: "environment beats override",
			args: []string{"-c", "maxTurns=9"},
			env:  map[string]string{"ARGENT_MAX_TURNS": "8"},
			want: 8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdio, _, _ := newTestIO("", tc.env)
			opts, _, err := parseArgs(tc.args, stdio.Env, io.Discard)
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if opts.MaxTurns != tc.want {
				t.Fatalf("MaxTurns = %d, want %d", opts.MaxTurns, tc.want)
			}
		})
	}
}
