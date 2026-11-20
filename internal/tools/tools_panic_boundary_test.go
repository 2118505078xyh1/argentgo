package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// panicBoundaryTool is a Tool whose Run is supplied by the test, so a fake can
// panic without touching any real filesystem or process.
type panicBoundaryTool struct {
	name string
	run  func(ctx context.Context, root string, args json.RawMessage) (string, error)
}

func (t panicBoundaryTool) Name() string        { return t.name }
func (t panicBoundaryTool) Description() string { return "test tool" }
func (panicBoundaryTool) Schema() string        { return `{"type":"object","properties":{}}` }
func (panicBoundaryTool) Mutating() bool        { return false }
func (t panicBoundaryTool) Run(ctx context.Context, root string, args json.RawMessage) (string, error) {
	if t.run != nil {
		return t.run(ctx, root, args)
	}
	return "", nil
}

func TestToolPanicBecomesError(t *testing.T) {
	r := New(t.TempDir(), true)
	const name = "panic_tool"
	r.tools[name] = panicBoundaryTool{name: name, run: func(context.Context, string, json.RawMessage) (string, error) {
		panic("boom from tool")
	}}

	out, err := r.Run(context.Background(), name, `{}`)
	if err == nil {
		t.Fatal("panic tool returned a nil error; want a recovered panic")
	}
	if out != "" {
		t.Fatalf("output = %q, want empty on panic", out)
	}
	msg := err.Error()
	for _, want := range []string{name, "panicked", "boom from tool"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}

func TestToolPanicValueIsSanitized(t *testing.T) {
	r := New(t.TempDir(), true)
	const name = "panic_tool"
	payload := "line1\nline2\x1b[31mred" + strings.Repeat("x", maxPanicDescLen*2)
	r.tools[name] = panicBoundaryTool{name: name, run: func(context.Context, string, json.RawMessage) (string, error) {
		panic(payload)
	}}

	_, err := r.Run(context.Background(), name, `{}`)
	if err == nil {
		t.Fatal("want a recovered panic")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\n\r\t\x1b\x7f") {
		t.Fatalf("panic error still contains a control character: %q", msg)
	}
	if !strings.Contains(msg, "…") {
		t.Fatalf("long panic value was not clipped: %q", msg)
	}
	prefix := fmt.Sprintf("tool %q panicked: ", name)
	if got, want := len([]rune(msg)), len([]rune(prefix))+maxPanicDescLen+1; got > want {
		t.Fatalf("panic error has %d runes, want at most %d", got, want)
	}
}

func TestToolPanicWithMetaReturnsEmptyMeta(t *testing.T) {
	r := New(t.TempDir(), true)
	const name = "panic_tool"
	r.tools[name] = panicBoundaryTool{name: name, run: func(context.Context, string, json.RawMessage) (string, error) {
		panic("boom")
	}}

	_, meta, err := r.RunWithMeta(context.Background(), name, `{}`)
	if err == nil {
		t.Fatal("want a recovered panic")
	}
	if meta != (ToolMeta{}) {
		t.Fatalf("meta = %+v, want empty on panic", meta)
	}
}

func TestNormalToolStillRunsWithPanicBoundary(t *testing.T) {
	r := New(t.TempDir(), true)
	const name = "ok_tool"
	r.tools[name] = panicBoundaryTool{name: name, run: func(_ context.Context, root string, args json.RawMessage) (string, error) {
		return "ok:" + root + ":" + string(args), nil
	}}

	out, err := r.Run(context.Background(), name, `{"i":"hello"}`)
	if err != nil {
		t.Fatalf("normal tool: %v", err)
	}
	want := "ok:" + r.root + `:` + `{"i":"hello"}`
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}
