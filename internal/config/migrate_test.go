package config

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// TestConfigMigrationsRegistryComplete 保证注册表在 1..最新版本之间没有空洞，
// 否则 migrateConfigTree 会提前停下，后续版本永远不会生效。
func TestConfigMigrationsRegistryComplete(t *testing.T) {
	for target := 1; target <= configSchemaVersion; target++ {
		if _, ok := configMigrations[target]; !ok {
			t.Errorf("迁移注册表缺少目标版本 %d", target)
		}
	}
	if configVersionKey != "version" {
		t.Errorf("版本键名 = %q，期望 version", configVersionKey)
	}
}

// TestMigrateConfigTreeByVersion 覆盖每个版本步骤：0 升到最新、1 升到最新、
// 已是最新版本、以及高于最新版本的未来配置。
func TestMigrateConfigTreeByVersion(t *testing.T) {
	tests := []struct {
		name       string
		version    int
		tree       map[string]any
		want       map[string]any
		wantVer    int
		wantChange bool
	}{
		{
			name:    "版本零的顶层别名改名到最新版本",
			version: 0,
			tree: map[string]any{
				"modelName": "legacy-model",
				"base_url":  "https://legacy",
				"max_turns": float64(3),
				"yolo":      true,
			},
			want: map[string]any{
				"model":    "legacy-model",
				"baseURL":  "https://legacy",
				"maxTurns": float64(3),
				"yolo":     true,
			},
			wantVer:    2,
			wantChange: true,
		},
		{
			name:    "版本一的嵌套别名改名到最新版本",
			version: 1,
			tree: map[string]any{
				"model": "m",
				"providers": map[string]any{
					"p1": map[string]any{
						"api_key":  "key",
						"base_url": "https://provider",
						"protocol": "openai-chat-completions",
					},
				},
				"models": map[string]any{
					"m": map[string]any{"context_window": float64(100)},
				},
			},
			want: map[string]any{
				"model": "m",
				"providers": map[string]any{
					"p1": map[string]any{
						"apiKey":   "key",
						"baseURL":  "https://provider",
						"protocol": "openai-chat-completions",
					},
				},
				"models": map[string]any{
					"m": map[string]any{"contextWindow": float64(100)},
				},
			},
			wantVer:    2,
			wantChange: true,
		},
		{
			name:    "最新版本无需迁移",
			version: configSchemaVersion,
			tree: map[string]any{
				"model":   "m",
				"yolo":    false,
				"numbers": []any{float64(1), float64(2)},
			},
			want: map[string]any{
				"model":   "m",
				"yolo":    false,
				"numbers": []any{float64(1), float64(2)},
			},
			wantVer:    configSchemaVersion,
			wantChange: false,
		},
		{
			name:    "未来版本保持原样不降级",
			version: configSchemaVersion + 1,
			tree: map[string]any{
				"modelName": "keep-legacy",
			},
			want: map[string]any{
				"modelName": "keep-legacy",
			},
			wantVer:    configSchemaVersion + 1,
			wantChange: false,
		},
		{
			name:    "负版本按版本零处理",
			version: -7,
			tree: map[string]any{
				"max_turns": float64(5),
			},
			want: map[string]any{
				"maxTurns": float64(5),
			},
			wantVer:    configSchemaVersion,
			wantChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ver, changed := migrateConfigTree(tt.tree, tt.version)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("迁移结果 = %#v，期望 %#v", got, tt.want)
			}
			if ver != tt.wantVer {
				t.Errorf("版本 = %d，期望 %d", ver, tt.wantVer)
			}
			if changed != tt.wantChange {
				t.Errorf("changed = %v，期望 %v", changed, tt.wantChange)
			}
		})
	}
}

// TestMigrateConfigTreeNoChange 验证已是规范键的旧版配置只推进版本，
// 内容不变，且复杂值（嵌套对象、数组、布尔、数字）原样保留。
func TestMigrateConfigTreeNoChange(t *testing.T) {
	tree := map[string]any{
		"model":        "m",
		"baseURL":      "https://api",
		"maxTurns":     float64(12),
		"yolo":         false,
		"stopTokenIds": []any{float64(1), float64(2)},
		"verifyCommands": map[string]any{
			"go": "go test ./...",
		},
		"providers": map[string]any{
			"p": map[string]any{"apiKey": "k"},
		},
	}

	got, ver, changed := migrateConfigTree(tree, 0)
	if changed {
		t.Error("规范键配置不应报告变化")
	}
	if ver != configSchemaVersion {
		t.Errorf("版本 = %d，期望 %d", ver, configSchemaVersion)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Errorf("无变化迁移不应改动内容：got = %#v，want = %#v", got, tree)
	}
}

// TestMigrateConfigTreeCJK 验证 CJK 键与值不被误改或破坏，包括位于
// 被迁移对象内部的 CJK 键；同时仍有 ASCII 别名被正确改名。
func TestMigrateConfigTreeCJK(t *testing.T) {
	tree := map[string]any{
		"模型":        "中文模型",
		"说明":        map[string]any{"文本": "你好，世界", "列表": []any{"甲", "乙"}},
		"modelName": "ascii-legacy",
		"providers": map[string]any{
			"中文提供方": map[string]any{
				"api_key": "密钥",
				"备注":      "中文备注",
			},
		},
	}
	want := map[string]any{
		"模型":    "中文模型",
		"说明":    map[string]any{"文本": "你好，世界", "列表": []any{"甲", "乙"}},
		"model": "ascii-legacy",
		"providers": map[string]any{
			"中文提供方": map[string]any{
				"apiKey": "密钥",
				"备注":     "中文备注",
			},
		},
	}

	got, ver, changed := migrateConfigTree(tree, 0)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CJK 迁移结果 = %#v，期望 %#v", got, want)
	}
	if ver != configSchemaVersion || !changed {
		t.Errorf("版本 = %d，changed = %v，期望 %d、true", ver, changed, configSchemaVersion)
	}
	if !utf8.ValidString(got["模型"].(string)) {
		t.Error("CJK 值不是合法 UTF-8")
	}
}

// TestMigrateConfigTreeIdempotent 验证二次迁移不再变化，且对同一输入
// 重复调用得到完全一致的结果（纯函数性质）。
func TestMigrateConfigTreeIdempotent(t *testing.T) {
	legacy := map[string]any{
		"modelName": "m",
		"base_url":  "https://api",
		"providers": map[string]any{
			"p": map[string]any{"api_key": "k"},
		},
	}

	first, firstVer, firstChanged := migrateConfigTree(legacy, 0)
	if !firstChanged || firstVer != configSchemaVersion {
		t.Fatalf("首次迁移 changed = %v，版本 = %d", firstChanged, firstVer)
	}

	second, secondVer, secondChanged := migrateConfigTree(first, firstVer)
	if secondChanged {
		t.Error("二次迁移不应报告变化")
	}
	if secondVer != firstVer {
		t.Errorf("二次迁移版本 = %d，期望 %d", secondVer, firstVer)
	}
	if !reflect.DeepEqual(second, first) {
		t.Errorf("二次迁移改变了内容：got = %#v，want = %#v", second, first)
	}

	again, againVer, againChanged := migrateConfigTree(legacy, 0)
	if !reflect.DeepEqual(again, first) || againVer != firstVer || againChanged != firstChanged {
		t.Error("对同一输入重复调用的结果不一致")
	}
}

// TestMigrateConfigTreeDoesNotMutateInput 验证迁移只写副本，
// 入参及其嵌套对象完全不变；改动结果的嵌套对象也不会回写进参。
func TestMigrateConfigTreeDoesNotMutateInput(t *testing.T) {
	input := map[string]any{
		"modelName": "m",
		"providers": map[string]any{
			"p": map[string]any{"api_key": "secret"},
		},
	}
	before := cloneTree(input)

	got, _, changed := migrateConfigTree(input, 0)
	if !changed {
		t.Fatal("期望发生改名")
	}
	if !reflect.DeepEqual(input, before) {
		t.Errorf("迁移修改了入参：got = %#v，want = %#v", input, before)
	}

	gotProviders := got["providers"].(map[string]any)
	gotEntry := gotProviders["p"].(map[string]any)
	gotEntry["apiKey"] = "被改动的副本"
	if entry := input["providers"].(map[string]any)["p"].(map[string]any); entry["api_key"] != "secret" {
		t.Error("改动结果影响了入参的嵌套对象")
	}
}

// TestMigrateConfigTreeEmpty 覆盖空 map 与 nil 入参：返回非 nil 空树，
// 版本正常推进，且不报告变化。
func TestMigrateConfigTreeEmpty(t *testing.T) {
	for _, version := range []int{0, configSchemaVersion, configSchemaVersion + 1} {
		got, ver, changed := migrateConfigTree(map[string]any{}, version)
		if got == nil || len(got) != 0 {
			t.Errorf("空树返回 = %#v，期望非 nil 的空 map", got)
		}
		wantVer := version
		if version < 0 {
			wantVer = configSchemaVersion
		}
		if version < configSchemaVersion {
			wantVer = configSchemaVersion
		}
		if ver != wantVer || changed {
			t.Errorf("空树版本 %d：ver = %d，changed = %v，期望 %d、false", version, ver, changed, wantVer)
		}

		gotNil, nilVer, nilChanged := migrateConfigTree(nil, version)
		if gotNil == nil || len(gotNil) != 0 || nilVer != ver || nilChanged != changed {
			t.Errorf("nil 入参结果与空树不一致：%#v / %d / %v", gotNil, nilVer, nilChanged)
		}
	}
}

// TestMigrateConfigTreeConflict 验证新旧键并存时保留规范键的值，
// 删除旧键，与 decode 中规范键优先的行为一致。
func TestMigrateConfigTreeConflict(t *testing.T) {
	tree := map[string]any{
		"baseUrl":  "规范值",
		"base_url": "旧值",
	}
	got, _, changed := migrateConfigTree(tree, 0)
	want := map[string]any{"baseURL": "规范值"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("冲突处理 = %#v，期望 %#v", got, want)
	}
	if !changed {
		t.Error("删除旧键应报告变化")
	}
}

// TestMigrateConfigTreePreservesNestedShapes 验证数组里的对象与多层级
// 嵌套结构在迁移中保持结构，仅目标对象内的别名被改名。
func TestMigrateConfigTreePreservesNestedShapes(t *testing.T) {
	tree := map[string]any{
		"列表": []any{
			map[string]any{"api_key": "k1", "名称": "甲"},
			map[string]any{"apiKey": "k2", "名称": "乙"},
		},
		"外层": map[string]any{
			"内层": map[string]any{"modelName": "deep-model"},
		},
	}
	want := map[string]any{
		"列表": []any{
			map[string]any{"api_key": "k1", "名称": "甲"},
			map[string]any{"apiKey": "k2", "名称": "乙"},
		},
		"外层": map[string]any{
			"内层": map[string]any{"modelName": "deep-model"},
		},
	}

	got, _, changed := migrateConfigTree(tree, 0)
	if changed {
		t.Error("注册表未登记的数组内层与自定义对象不应被改写")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("嵌套结构 = %#v，期望 %#v", got, want)
	}
}
