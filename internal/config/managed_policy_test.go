package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyManagedPolicyOverridesUserValue(t *testing.T) {
	tree := map[string]any{"model": "user-model", "providers": map[string]any{
		"openai": map[string]any{"apiKey": "user-key", "baseURL": "https://user.example"},
	}}
	path := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(path, []byte(`{"model":"managed-model","providers":{"openai":{"baseURL":"https://corp.example"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedPolicy(tree, path); err != nil {
		t.Fatal(err)
	}
	if tree["model"] != "managed-model" {
		t.Fatalf("model = %v, want managed-model", tree["model"])
	}
	providers := tree["providers"].(map[string]any)
	openai := providers["openai"].(map[string]any)
	if openai["baseURL"] != "https://corp.example" {
		t.Fatalf("baseURL = %v, want corp", openai["baseURL"])
	}
	if openai["apiKey"] != "user-key" {
		t.Fatalf("托管层应逐键合并，apiKey 被抹掉: %v", openai)
	}
}

func TestApplyManagedPolicyNoOpAndFailClosed(t *testing.T) {
	tree := map[string]any{"model": "m"}
	// 未设置：空操作
	if err := ApplyManagedPolicy(tree, ""); err != nil || tree["model"] != "m" {
		t.Fatalf("unset policy changed state: %v %v", err, tree)
	}
	// 文件不存在：未部署，空操作
	if err := ApplyManagedPolicy(tree, filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatalf("missing policy should be no-op: %v", err)
	}
	// 损坏 JSON：fail closed
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedPolicy(tree, bad); err == nil {
		t.Fatal("corrupt managed policy must fail closed")
	}
	// 非对象：fail closed
	arr := filepath.Join(t.TempDir(), "arr.json")
	if err := os.WriteFile(arr, []byte(`[1,2]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedPolicy(tree, arr); err == nil {
		t.Fatal("non-object managed policy must fail closed")
	}
}
