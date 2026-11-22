package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

type schemaTool struct{ out string }

func (s schemaTool) Name() string        { return "x" }
func (s schemaTool) Description() string { return "" }
func (s schemaTool) Schema() string      { return `{}` }
func (s schemaTool) Mutating() bool      { return false }
func (s schemaTool) Run(context.Context, string, json.RawMessage) (string, error) {
	return s.out, nil
}
func (s schemaTool) OutputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func TestPluginOutputSchemaValidation(t *testing.T) {
	old, had := os.LookupEnv(pluginOutputSchemaEnv)
	defer func() {
		if had {
			os.Setenv(pluginOutputSchemaEnv, old)
		} else {
			os.Unsetenv(pluginOutputSchemaEnv)
		}
	}()
	os.Unsetenv(pluginOutputSchemaEnv)
	// 关闭时不校验。
	if out, err := runTool(schemaTool{out: "not json"}, context.Background(), "", "x", nil); err != nil || out != "not json" {
		t.Fatalf("off should pass through: %q %v", out, err)
	}
	os.Setenv(pluginOutputSchemaEnv, "1")
	if _, err := runTool(schemaTool{out: "not json"}, context.Background(), "", "x", nil); err == nil {
		t.Fatal("invalid JSON should be rejected")
	}
	if out, err := runTool(schemaTool{out: `{"ok":true}`}, context.Background(), "", "x", nil); err != nil || out != `{"ok":true}` {
		t.Fatalf("valid JSON should pass: %q %v", out, err)
	}
}
