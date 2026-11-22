package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRegisterMemoryConsolidateKeepsDefaultGolden(t *testing.T) {
	baseline := New(t.TempDir(), true).Names()
	r := New(t.TempDir(), true)
	RegisterMemoryConsolidate(r)
	RegisterMemoryConsolidate(r) // 幂等
	want := append(append([]string(nil), baseline...), memoryConsolidateName)
	if got := r.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got := New(t.TempDir(), true).Names(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("默认注册表被污染: %v", got)
	}
	RegisterMemoryConsolidate(nil)
}

func TestMemoryConsolidateToolRunOnEmptyBank(t *testing.T) {
	tool := &memoryConsolidateTool{}
	out, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 空库整合不应报错，返回零统计。
	if out == "" {
		t.Fatal("empty result")
	}
}
