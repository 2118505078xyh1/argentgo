package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// TestSwitchModelToolAllowlist 锁定 #47：只允许配置中声明的 profile；不在白名单时
// 返回带纠正指引的错误；成功时回灌 active_model 与 reason。
func TestSwitchModelToolAllowlist(t *testing.T) {
	reg := tools.NewWithLimit(t.TempDir(), false, 0)
	var gotProfile, gotReason string
	profiles := []string{"strong", "cheap"}
	tools.RegisterSwitchModel(reg, profiles, func(profile, reason string) (string, error) {
		gotProfile, gotReason = profile, reason
		return "gpt-4o", nil
	})
	specs := reg.Specs()
	found := false
	for _, spec := range specs {
		if spec.Function.Name == "switch_model" {
			found = true
		}
	}
	if !found {
		t.Fatal("switch_model not registered")
	}

	good, _ := json.Marshal(map[string]string{"profile_name": "STRONG", "reason": "need deeper reasoning"})
	out, err := reg.Run(context.Background(), "switch_model", string(good))
	if err != nil {
		t.Fatalf("allowed profile: %v", err)
	}
	var result map[string]string
	if json.Unmarshal([]byte(out), &result) != nil || result["active_model"] != "gpt-4o" || result["reason"] != "need deeper reasoning" {
		t.Fatalf("result = %s", out)
	}
	if gotProfile != "STRONG" || gotReason != "need deeper reasoning" {
		t.Fatalf("apply args = %q / %q", gotProfile, gotReason)
	}

	bad, _ := json.Marshal(map[string]string{"profile_name": "unknown", "reason": "x"})
	if _, err := reg.Run(context.Background(), "switch_model", string(bad)); err == nil || !strings.Contains(err.Error(), "not in the allowlist") || !strings.Contains(err.Error(), "strong") {
		t.Fatalf("want allowlist error listing profiles, got %v", err)
	}
	noReason, _ := json.Marshal(map[string]string{"profile_name": "strong"})
	if _, err := reg.Run(context.Background(), "switch_model", string(noReason)); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("want reason required error, got %v", err)
	}
}

// TestProfileModelLookup 锁定 profile → model 的白名单取值来源。
func TestProfileModelLookup(t *testing.T) {
	cfg := config.Config{Profiles: map[string]map[string]any{
		"fast": {"model": "gpt-4o-mini"},
		"deep": {"modelName": "o3"},
		"none": {},
	}}
	if got := profileModel(cfg.Profiles["fast"]); got != "gpt-4o-mini" {
		t.Fatalf("fast model = %q", got)
	}
	if got := profileModel(cfg.Profiles["deep"]); got != "o3" {
		t.Fatalf("deep model = %q", got)
	}
	if got := profileModel(cfg.Profiles["none"]); got != "" {
		t.Fatalf("none model = %q", got)
	}
}
