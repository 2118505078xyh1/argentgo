package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRegisterModelsKeepsDefaultGolden(t *testing.T) {
	baseline := New(t.TempDir(), true).Names()
	r := New(t.TempDir(), true)
	data := []ModelInfo{{Name: "zeta", Protocol: "openai", DefaultModel: "m1"}}
	RegisterModels(r, data)
	RegisterModels(r, data) // 幂等
	want := append(append([]string(nil), baseline...), "models")
	if got := r.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got := New(t.TempDir(), true).Names(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("默认注册表被污染: %v", got)
	}
	RegisterModels(nil, nil)
}

func TestModelsToolDescriptionIsStatic(t *testing.T) {
	// 描述必须明确"静态、不联网"，避免模型误以为能查实时在线模型。
	tool := modelsTool{}
	if !strings.Contains(tool.Description(), "Static") || !strings.Contains(tool.Description(), "offline") {
		t.Fatalf("description must state static/offline: %s", tool.Description())
	}
}

func TestModelsToolRun(t *testing.T) {
	data := []ModelInfo{
		{Name: "zeta", Protocol: "openai", BaseURL: "https://z", DefaultModel: "m1"},
		{Name: "alpha", Protocol: "anthropic", BaseURL: "https://a", DefaultModel: "m2"},
	}
	tool := modelsTool{models: data}

	got, err := tool.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 稳定按 name 排序。
	if !strings.HasPrefix(got, "[\n  {\n    \"name\": \"alpha\"") {
		t.Fatalf("not sorted or shape wrong: %s", got)
	}
	// 不含敏感字段。
	for _, forbidden := range []string{"api_key", "APIKey", "key_env"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("result leaks %q: %s", forbidden, got)
		}
	}

	// provider 过滤。
	raw, _ := json.Marshal(map[string]string{"provider": "zeta"})
	got, err = tool.Run(context.Background(), t.TempDir(), raw)
	if err != nil {
		t.Fatalf("Run(filter): %v", err)
	}
	if !strings.Contains(got, `"name": "zeta"`) || strings.Contains(got, "alpha") {
		t.Fatalf("filter failed: %s", got)
	}
	raw, _ = json.Marshal(map[string]string{"provider": "missing"})
	got, err = tool.Run(context.Background(), t.TempDir(), raw)
	if err != nil || got != "" {
		t.Fatalf("missing filter = %q, err=%v", got, err)
	}
}
