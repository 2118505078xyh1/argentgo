package app

import "testing"

// TestOutputRedactionOptInKey 锁定新键的激活面：默认关闭，只能由 ARGENT_OPT_INS
// 的环境列表打开；它是 app 专属键，配置白名单无法激活（见 optin_activation_guard_test.go）。
func TestOutputRedactionOptInKey(t *testing.T) {
	if on := DefaultOptIns()[OptInOutputRedaction]; on {
		t.Fatal("DefaultOptIns()[OptInOutputRedaction] 默认应为 false")
	}
	if on := ParseOptInEnv(nil)[OptInOutputRedaction]; on {
		t.Fatal("无 ARGENT_OPT_INS 时 output_redaction 应为 false")
	}

	got := ParseOptInEnv(func(key string) string {
		if key == optInEnvVar {
			return "output_redaction,unknown_key"
		}
		return ""
	})
	if !got[OptInOutputRedaction] {
		t.Fatalf("ARGENT_OPT_INS=output_redaction 未打开新键: %v", got)
	}
	if got[OptInKey("unknown_key")] {
		t.Fatal("未知键不应被解析为 true（fail-closed）")
	}

	// 配置面无法激活该键：configOptIns 的交集表里没有它。
	cfg := configOptIns(map[string]any{"optins": map[string]any{"output_redaction": true}})
	if cfg[OptInOutputRedaction] {
		t.Fatal("configOptIns 不应激活 app 专属键 output_redaction")
	}
}
