package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMigrateHookConfigLegacyField 验证内置示例规则：旧字段 timeout 被改名为 timeoutMs，
// 且迁移后的文档仍能被 ParseConfig 正常解析，并携带迁移过来的超时值。
func TestMigrateHookConfigLegacyField(t *testing.T) {
	raw := json.RawMessage(`{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write|Edit",
        "hooks": [
          {"type":"command","command":"echo ok","timeout":500,"onError":"block"}
        ]
      }
    ]
  }
}`)
	out, changed := migrateHookConfig(raw)
	if !changed {
		t.Fatalf("期望发生迁移，实际未变化: %s", out)
	}
	text := string(out)
	if strings.Contains(text, `"timeout"`) {
		t.Fatalf("输出仍含旧字段 timeout: %s", text)
	}
	if !strings.Contains(text, `"timeoutMs":500`) {
		t.Fatalf("输出缺少迁移后的 timeoutMs:500: %s", text)
	}
	config, err := ParseConfig(out)
	if err != nil {
		t.Fatalf("迁移结果应可被解析: %v", err)
	}
	if len(config.Commands) != 1 || config.Commands[0].Timeout != 500*time.Millisecond {
		t.Fatalf("迁移后超时值不正确: %+v", config.Commands)
	}
}

// TestMigrateHookConfigNoChange 验证当前 schema 的文档不被改动：
// 返回同一字节切片且 changed 为 false（含 CJK 内容时字节完全一致）。
func TestMigrateHookConfigNoChange(t *testing.T) {
	raw := json.RawMessage(`{"hooks":{"Stop":[{"hooks":[{"command":"echo 中文命令","timeoutMs":1000}]}]}}`)
	out, changed := migrateHookConfig(raw)
	if changed {
		t.Fatalf("当前 schema 不应触发迁移: %s", out)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("无变化时必须原样返回原字节，得到: %s", out)
	}
}

// TestMigrateHookConfigCJK 验证真正发生迁移时 CJK 文本按原样保留，
// 不被转义成 \uXXXX，且前后字节里都能直接看到原始汉字。
func TestMigrateHookConfigCJK(t *testing.T) {
	raw := json.RawMessage(`{"hooks":{"Stop":[{"hooks":[{"command":"回声 你好世界","timeout":250}]}]}}`)
	out, changed := migrateHookConfig(raw)
	if !changed {
		t.Fatalf("期望发生迁移，实际未变化: %s", out)
	}
	text := string(out)
	if !strings.Contains(text, "回声 你好世界") {
		t.Fatalf("CJK 文本未原样保留: %s", text)
	}
	if strings.Contains(text, `\u`) {
		t.Fatalf("CJK 被转义成了 \\uXXXX: %s", text)
	}
}

// TestMigrateHookConfigInvalidJSON 验证非法 JSON（包括残缺文档与尾随垃圾）
// 一律原样返回且不报告迁移，迁移器从不把无效输入变成"已迁移"。
func TestMigrateHookConfigInvalidJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "残缺对象", raw: `{"hooks":`},
		{name: "尾随垃圾", raw: `{"hooks":{}} trailing`},
		{name: "空输入", raw: ``},
		{name: "裸标量", raw: `123`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(tc.raw)
			out, changed := migrateHookConfig(raw)
			if changed {
				t.Fatalf("非法 JSON 不应报告迁移: %s", out)
			}
			if !bytes.Equal(out, raw) {
				t.Fatalf("非法 JSON 必须原样返回，得到: %s", out)
			}
		})
	}
}

// TestMigrateHookConfigIdempotent 验证幂等：第二次迁移不再命中规则，
// 返回 false，且字节与第一次迁移的结果完全一致。
func TestMigrateHookConfigIdempotent(t *testing.T) {
	raw := json.RawMessage(`{"hooks":{"Stop":[{"hooks":[{"command":"echo","timeout":800}]}]}}`)
	first, changed := migrateHookConfig(raw)
	if !changed {
		t.Fatalf("第一次迁移应生效: %s", first)
	}
	second, changed := migrateHookConfig(first)
	if changed {
		t.Fatalf("第二次迁移不应再变化: %s", second)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("两次迁移结果应字节一致:\nfirst=%s\nsecond=%s", first, second)
	}
}

// TestMigrateHookConfigPreservesNumbers 验证解码使用 UseNumber：
// 超出 float64 精度的大整数按原始字面量保留，不会被四舍五入而损坏。
func TestMigrateHookConfigPreservesNumbers(t *testing.T) {
	raw := json.RawMessage(`{"hooks":{"Stop":[{"hooks":[{"command":"echo","timeout":9007199254740993}]}]}}`)
	out, changed := migrateHookConfig(raw)
	if !changed {
		t.Fatalf("期望发生迁移，实际未变化: %s", out)
	}
	if !strings.Contains(string(out), `"timeoutMs":9007199254740993`) {
		t.Fatalf("大整数未被精确保留: %s", out)
	}
}

// TestApplyHookMigrationsInjectedRules 验证规则表可注入：自定义规则作用于
// 任意层级的对象；当新旧字段同时存在时不覆盖新字段，但旧字段仍被删除。
func TestApplyHookMigrationsInjectedRules(t *testing.T) {
	rules := []migrationRule{renameRule("legacy", "modern")}
	raw := json.RawMessage(`{"legacy":"x","nested":{"legacy":2},"both":{"legacy":"old","modern":"new"}}`)
	out, changed := applyHookMigrations(raw, rules)
	if !changed {
		t.Fatalf("注入规则应命中: %s", out)
	}
	text := string(out)
	if strings.Contains(text, `"legacy"`) {
		t.Fatalf("旧字段仍存在: %s", text)
	}
	if !strings.Contains(text, `"modern":"x"`) || !strings.Contains(text, `"modern":2`) {
		t.Fatalf("嵌套对象的旧字段未被迁移: %s", text)
	}
	if !strings.Contains(text, `"modern":"new"`) {
		t.Fatalf("新字段不应被旧字段覆盖: %s", text)
	}
	// 同一规则集重复套用应幂等。
	again, changed := applyHookMigrations(out, rules)
	if changed || !bytes.Equal(out, again) {
		t.Fatalf("注入规则应幂等: %s", again)
	}
}

// TestParseConfigMigratedOptIn 验证 opt-in 入口：migrate=false 保持既有解析行为
// （旧字段被忽略、使用默认超时）；migrate=true 先迁移再解析，得到迁移后的超时值。
func TestParseConfigMigratedOptIn(t *testing.T) {
	raw := []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"echo","timeout":250}]}]}}`)
	plain, err := ParseConfigMigrated(raw, false)
	if err != nil {
		t.Fatalf("默认路径解析失败: %v", err)
	}
	if plain.Commands[0].Timeout != defaultTimeout {
		t.Fatalf("migrate=false 应使用默认超时，得到: %v", plain.Commands[0].Timeout)
	}
	migrated, err := ParseConfigMigrated(raw, true)
	if err != nil {
		t.Fatalf("迁移路径解析失败: %v", err)
	}
	if migrated.Commands[0].Timeout != 250*time.Millisecond {
		t.Fatalf("migrate=true 应得到迁移后的超时，得到: %v", migrated.Commands[0].Timeout)
	}
}
