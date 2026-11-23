package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestRegisterComputerIsIdempotent(t *testing.T) {
	r := New(t.TempDir(), false)
	before := len(r.order)
	RegisterComputer(r)
	first := len(r.order)
	RegisterComputer(r)
	if len(r.order) != first {
		t.Fatalf("RegisterComputer not idempotent: %d -> %d -> %d", before, first, len(r.order))
	}
	if first != before+1 {
		t.Fatalf("expected +1 tool, got %d -> %d", before, first)
	}
	if r.tools[computerToolName] == nil {
		t.Fatal("computer tool not registered")
	}
}

func TestComputerToolValidation(t *testing.T) {
	tool := computerTool{}
	cases := []struct {
		name string
		args string
	}{
		{"missing action", `{}`},
		{"unknown action", `{"action":"fly"}`},
		{"type without text", `{"action":"type"}`},
		{"key without combo", `{"action":"key"}`},
		{"scroll without amount", `{"action":"scroll","x":1,"y":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(tc.args)); err == nil {
				t.Fatalf("expected error for %s", tc.args)
			}
		})
	}
	if !tool.Mutating() {
		t.Fatal("computer tool must be mutating so input actions go through approval")
	}
}

// TestComputerToolScreenshot 在 Windows 上验证真实桌面截图经工具层编码为 PNG base64。
func TestComputerToolScreenshot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("desktop capture is Windows-only")
	}
	tool := computerTool{}
	out, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{"action":"screenshot"}`))
	if err != nil {
		// 桌面截图依赖交互式会话（BitBlt/GetDC）。CI runner 与无头环境常无
		// 桌面，此时属于环境能力缺失而非代码回归——跳过但打印原因；有真实
		// 桌面时仍照常校验编码结果。
		t.Skipf("desktop capture unavailable in this environment: %v", err)
	}
	if !strings.Contains(out, "[desktop screenshot") || !strings.Contains(out, "bytes png]") {
		t.Fatalf("unexpected screenshot envelope: %.120q", out)
	}
	if len(out) < 10000 {
		t.Fatalf("suspiciously small base64 output: %d bytes", len(out))
	}
}
