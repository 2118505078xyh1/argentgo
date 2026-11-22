package app

import "testing"

func TestMCPToolCacheTTLSeconds(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 0},
		{"0", 0},
		{"-1", 0},
		{"abc", 0},
		{"30", 30},
		{" 60 ", 60},
	}
	for _, tc := range cases {
		got := mcpToolCacheTTLSeconds(func(string) string { return tc.env })
		if got != tc.want {
			t.Errorf("env %q -> %d, want %d", tc.env, got, tc.want)
		}
	}
	if mcpToolCacheTTLSeconds(nil) != 0 {
		t.Error("nil env should be 0")
	}
}

func TestMCPSpecsFromConfigDefaultTTL(t *testing.T) {
	specs := mcpSpecsFromConfig(map[string]any{
		"mcpServers": map[string]any{
			"srv": map[string]any{"command": "echo", "args": []any{}},
		},
	})
	if len(specs) == 0 {
		t.Fatal("expected a spec")
	}
	// 未设置 env 时 TTL 为零（逐字节兼容默认路径）。
	if specs[0].ToolCacheTTL != 0 {
		t.Fatalf("default TTL = %v, want 0", specs[0].ToolCacheTTL)
	}
}
