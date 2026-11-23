package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// rankExposureFakeTool 是一个显式声明暴露级别的工具，用于验证 deferred 接线。
type rankExposureFakeTool struct {
	name     string
	exposure pluginapi.Exposure
}

func (f rankExposureFakeTool) Name() string        { return f.name }
func (f rankExposureFakeTool) Description() string { return "special capability named " + f.name }
func (f rankExposureFakeTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string"}}}`
}
func (f rankExposureFakeTool) Mutating() bool { return false }
func (f rankExposureFakeTool) Run(context.Context, string, json.RawMessage) (string, error) {
	return "ok", nil
}
func (f rankExposureFakeTool) Exposure() pluginapi.Exposure { return f.exposure }

func exposureSpec(name string) llm.ToolSpec {
	s := llm.ToolSpec{Type: "function"}
	s.Function.Name = name
	s.Function.Description = "desc " + name
	return s
}

func TestToolExposureDefaultsAndUnwrap(t *testing.T) {
	r := New(t.TempDir(), true)
	if got := r.Exposure("read"); got != pluginapi.ExposureDirect {
		t.Fatalf("default exposure = %q, want direct", got)
	}

	r.tools["deferred_probe"] = wrapIntentSchema(rankExposureFakeTool{
		name: "deferred_probe", exposure: pluginapi.ExposureDeferred,
	})
	r.tools["hidden_probe"] = wrapIntentSchema(rankExposureFakeTool{
		name: "hidden_probe", exposure: pluginapi.ExposureHidden,
	})
	if got := r.Exposure("deferred_probe"); got != pluginapi.ExposureDeferred {
		t.Fatalf("deferred exposure = %q", got)
	}
	if got := r.Exposure("hidden_probe"); got != pluginapi.ExposureHidden {
		t.Fatalf("hidden exposure = %q", got)
	}
}

func TestFilterByExposureHiddenAndRevealedDeferred(t *testing.T) {
	full := []llm.ToolSpec{exposureSpec("direct"), exposureSpec("deferred"), exposureSpec("hidden")}
	exposure := func(name string) pluginapi.Exposure {
		switch name {
		case "deferred":
			return pluginapi.ExposureDeferred
		case "hidden":
			return pluginapi.ExposureHidden
		}
		return pluginapi.ExposureDirect
	}
	// 精简目录里只剩 direct；deferred 命中后应从 full 补回，hidden 永不出现。
	visible := []llm.ToolSpec{exposureSpec("direct")}
	got := FilterByExposure(visible, full, exposure, map[string]bool{"deferred": true})
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Function.Name)
	}
	want := []string{"direct", "deferred"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("FilterByExposure = %v, want %v", names, want)
	}

	// 未命中时 deferred 不并入，默认目录逐字节不变。
	got = FilterByExposure(visible, full, exposure, nil)
	if len(got) != 1 || got[0].Function.Name != "direct" {
		t.Fatalf("FilterByExposure(no reveal) = %v", got)
	}
}

func TestToolSearchRevealsDeferred(t *testing.T) {
	r := New(t.TempDir(), true)
	r.tools["deferred_probe"] = wrapIntentSchema(rankExposureFakeTool{
		name: "deferred_probe", exposure: pluginapi.ExposureDeferred,
	})
	r.order = append(r.order, "deferred_probe")
	out, err := r.Run(context.Background(), "tool_search", `{"query":"deferred_probe"}`)
	if err != nil || !strings.Contains(out, "deferred_probe:") {
		t.Fatalf("tool_search = %q, err = %v", out, err)
	}
	if !r.RevealedNames()["deferred_probe"] {
		t.Fatalf("deferred tool was not revealed: %v", r.RevealedNames())
	}
	ClearRevealed(r)
	if len(r.RevealedNames()) != 0 {
		t.Fatalf("ClearRevealed left entries: %v", r.RevealedNames())
	}
}
