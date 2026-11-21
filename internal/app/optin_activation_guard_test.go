package app

import (
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// 本文件守护 R16「收口激活面」的两条总契约：
//  1. app / cli / config 三处 opt-in 词汇表统一——app 的 14 键清单与 config
//     白名单（补键后）逐键一致；cli 因分层约束不能被 app import，其词汇表由 cli
//     包内的测试守护，三方一致由「app≡config + cli≡app」两段传递保证。
//  2. 配置文件能真正激活 opt-in——configOptIns 读取配置开关，mergeOptIns 与环境
//     激活面按逻辑或合并；无配置无环境时所有键保持默认关闭（fail-closed）。
//
// 依赖说明：本文件依赖其它代理的改动——config 白名单补键至 14 个、以及
// app_config_optins.go 提供的 configOptIns/mergeOptIns 原语；编写时二者均已落盘。
// 所有用例不联网、不读进程环境、不用真实模型；本文件仅守护契约，不直接改动生产代码。

// guardKeySet 把开关键切片收敛为键集合（值恒为 struct{}），便于用 reflect 比较，
// 与切片顺序无关。
func guardKeySet(keys []OptInKey) map[OptInKey]struct{} {
	out := make(map[OptInKey]struct{}, len(keys))
	for _, key := range keys {
		out[key] = struct{}{}
	}
	return out
}

// TestOptInVocabulariesAgree 验证 app 的 allOptInKeys（14 键）与
// config.ParseOptInDisplay(nil) 产出的键集合逐键一致。ParseOptInDisplay 是纯函数，
// 不读进程环境，其每一项恰好对应白名单里的一个键。
func TestOptInVocabulariesAgree(t *testing.T) {
	appKeys := guardKeySet(allOptInKeys)

	items := config.ParseOptInDisplay(nil)
	configKeys := make(map[OptInKey]struct{}, len(items))
	for _, item := range items {
		configKeys[OptInKey(item.Key)] = struct{}{}
	}

	if !reflect.DeepEqual(appKeys, configKeys) {
		t.Fatalf("opt-in 词汇表不一致: app=%v, config=%v", appKeys, configKeys)
	}
	if len(appKeys) != 14 {
		t.Fatalf("app opt-in 键数 = %d, want 14: %v", len(appKeys), appKeys)
	}
	// 集合比较只能证明无缺失；这里再守护白名单无重复键导致的计数异常。
	if len(items) != len(configKeys) {
		t.Fatalf("config.ParseOptInDisplay 返回重复键: items=%d, unique=%d", len(items), len(configKeys))
	}
}

// guardWant 以 DefaultOptIns()（全 false）为基线，仅把 on 中的键设为 true，
// 让用例只需声明「哪些开关应开启」。
func guardWant(on ...OptInKey) map[OptInKey]bool {
	m := DefaultOptIns()
	for _, key := range on {
		m[key] = true
	}
	return m
}

// TestConfigOptInsActivate 验证配置文件能激活 opt-in：optins 块中显式为 true 的键
// 经 configOptIns 后为 true，并经 mergeOptIns 与环境面按逻辑或合并。
func TestConfigOptInsActivate(t *testing.T) {
	// apply_patch 落在 app 与 config 白名单的交集内，必须能被配置激活。
	cfg := configOptIns(map[string]any{"optins": map[string]any{
		"apply_patch": true,
	}})
	if !cfg[OptInApplyPatch] {
		t.Fatalf("configOptIns(apply_patch=true)[OptInApplyPatch] = %v, want true: %v", cfg[OptInApplyPatch], cfg)
	}

	// 环境面缺省时，合并结果应恰好只打开 apply_patch，其余键保持默认关闭。
	got := mergeOptIns(ParseOptInEnv(nil), cfg)
	if want := guardWant(OptInApplyPatch); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeOptIns(env=nil, cfg) = %v, want %v", got, want)
	}

	// 环境面开启另一键时，并集同时保留环境键与配置键（逻辑或）；
	// 环境里的 "apply_patch=false" 不是合法键名，会被 ParseOptInEnv 忽略，
	// 因而不会反过来关闭配置已开启的 apply_patch。
	env := ParseOptInEnv(func(string) string {
		return "refusal_reason, apply_patch=false"
	})
	got = mergeOptIns(env, cfg)
	if want := guardWant(OptInApplyPatch, OptInRefusalReason); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeOptIns(env, cfg) = %v, want %v", got, want)
	}
}

// TestNoOptInsKeepsAllFalse 验证无配置、无环境时所有键保持 false，且结果逐键
// 等于 DefaultOptIns，证明默认关闭不被任何一条激活面意外打开。
func TestNoOptInsKeepsAllFalse(t *testing.T) {
	got := mergeOptIns(ParseOptInEnv(nil), configOptIns(nil))
	want := DefaultOptIns()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("无配置无环境合并结果 = %v, want DefaultOptIns %v", got, want)
	}
	for key, enabled := range got {
		if enabled {
			t.Errorf("无配置无环境时键 %q 不应开启: %v", key, got)
		}
	}
}

// TestActivationGuardNoMutation 验证两个激活原语均为纯函数：不修改入参（含嵌套
// 配置块），且修改一次结果不影响后续调用。CJK 未知键只作为不改入参的样本。
func TestActivationGuardNoMutation(t *testing.T) {
	raw := map[string]any{"optins": map[string]any{
		"apply_patch": true,
		"另一个键":        true,
	}}
	rawBefore := map[string]any{"optins": map[string]any{
		"apply_patch": true,
		"另一个键":        true,
	}}

	first := configOptIns(raw)
	if !reflect.DeepEqual(raw, rawBefore) {
		t.Fatalf("configOptIns 修改了入参: got %+v, want %+v", raw, rawBefore)
	}
	// 保存首次结果的副本，再篡改它；第二次调用必须与副本逐键一致，
	// 用副本而非写死清单可兼容交集键数后续变化。
	firstCopy := make(map[OptInKey]bool, len(first))
	for key, enabled := range first {
		firstCopy[key] = enabled
	}
	first[OptInApplyPatch] = false
	first[OptInGrepEx] = true
	second := configOptIns(raw)
	if !reflect.DeepEqual(second, firstCopy) {
		t.Fatalf("第二次 configOptIns 被第一次结果污染: got %v, want %v", second, firstCopy)
	}

	envOpt := map[OptInKey]bool{OptInRefusalReason: true}
	cfgOpt := map[OptInKey]bool{OptInApplyPatch: true}
	envBefore := map[OptInKey]bool{OptInRefusalReason: true}
	cfgBefore := map[OptInKey]bool{OptInApplyPatch: true}

	merged := mergeOptIns(envOpt, cfgOpt)
	merged[OptInRefusalReason] = false
	merged[OptInGrepEx] = true
	if !reflect.DeepEqual(envOpt, envBefore) {
		t.Fatalf("mergeOptIns 修改了 env 入参: %v, want %v", envOpt, envBefore)
	}
	if !reflect.DeepEqual(cfgOpt, cfgBefore) {
		t.Fatalf("mergeOptIns 修改了 cfg 入参: %v, want %v", cfgOpt, cfgBefore)
	}
}

// TestActivationGuardDeterministicAndCJK 验证确定性输出与 CJK 文本的完整 UTF-8：
// 同一入参多次解析结果逐字节一致；CJK 未知键被忽略、不影响已知键；中文标签是
// 完整的多字节序列，不会在字符中间被截断。
func TestActivationGuardDeterministicAndCJK(t *testing.T) {
	first := config.ParseOptInDisplay(nil)
	second := config.ParseOptInDisplay(nil)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ParseOptInDisplay 非确定性: first=%v, second=%v", first, second)
	}
	for _, item := range first {
		if item.Label == "" || !utf8.ValidString(item.Label) {
			t.Errorf("键 %q 的中文标签为空或非完整 UTF-8: %q", item.Key, item.Label)
		}
	}

	raw := map[string]any{"optins": map[string]any{
		"补丁应用":        true,
		"apply_patch": true,
	}}
	cfgA := configOptIns(raw)
	cfgB := configOptIns(raw)
	if !reflect.DeepEqual(cfgA, cfgB) {
		t.Fatalf("configOptIns 非确定性: A=%v, B=%v", cfgA, cfgB)
	}
	if !cfgA[OptInApplyPatch] {
		t.Fatalf("CJK 未知键不应影响已知键的激活: %v", cfgA)
	}
	for key := range cfgA {
		if string(key) == "补丁应用" {
			t.Fatalf("CJK 未知键不应进入激活面: %v", cfgA)
		}
	}
}
