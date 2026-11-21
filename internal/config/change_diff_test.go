package config

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestDiffConfigAddRemoveModify 覆盖新增、删除、修改三类变更同时出现，
// 并校验结果按 Key 升序排列、空侧用空串表示。
func TestDiffConfigAddRemoveModify(t *testing.T) {
	oldRaw := []byte(`{"model":"gpt","old":true,"keep":"same"}`)
	newRaw := []byte(`{"model":"claude","fresh":1,"keep":"same"}`)

	got := DiffConfig(oldRaw, newRaw)
	want := []ConfigChange{
		{Key: "fresh", Old: "", New: "1"},
		{Key: "model", Old: "gpt", New: "claude"},
		{Key: "old", Old: "true", New: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffConfig() = %#v, want %#v", got, want)
	}
}

// TestDiffConfigNestedKeys 覆盖嵌套 map 展开为点分隔路径，以及子树的
// 新增与删除要落到叶子键上。
func TestDiffConfigNestedKeys(t *testing.T) {
	oldRaw := []byte(`{
		"model":"a",
		"providers":{"openai":{"model":"x"},"anthropic":{"model":"y"}}
	}`)
	newRaw := []byte(`{
		"model":"b",
		"providers":{"openai":{"model":"z"},"new":{"key":"k"}}
	}`)

	got := DiffConfig(oldRaw, newRaw)
	want := []ConfigChange{
		{Key: "model", Old: "a", New: "b"},
		{Key: "providers.anthropic.model", Old: "y", New: ""},
		{Key: "providers.new.key", Old: "", New: "k"},
		{Key: "providers.openai.model", Old: "x", New: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffConfig() = %#v, want %#v", got, want)
	}
}

// TestDiffConfigSensitiveRedaction 覆盖敏感键（含嵌套、下划线拼写、
// 复合后缀）的脱敏：原文绝不出现在结果中，Sensitive 标志为真，
// 且新增/删除的方向信息仍保留。
func TestDiffConfigSensitiveRedaction(t *testing.T) {
	oldRaw := []byte(`{
		"apiKey":"sk-old","token":"tok-old","password":"pw-old",
		"model":"a","nested":{"api_key":"nested-old","secret":"s-old"}
	}`)
	newRaw := []byte(`{
		"apiKey":"sk-new","token":"tok-new","password":"pw-new",
		"accessToken":"acc-new","model":"b",
		"nested":{"api_key":"nested-new","secret":"s-new"}
	}`)

	got := DiffConfig(oldRaw, newRaw)
	want := []ConfigChange{
		{Key: "accessToken", Old: "", New: changeDiffRedacted, Sensitive: true},
		{Key: "apiKey", Old: changeDiffRedacted, New: changeDiffRedacted, Sensitive: true},
		{Key: "model", Old: "a", New: "b"},
		{Key: "nested.api_key", Old: changeDiffRedacted, New: changeDiffRedacted, Sensitive: true},
		{Key: "nested.secret", Old: changeDiffRedacted, New: changeDiffRedacted, Sensitive: true},
		{Key: "password", Old: changeDiffRedacted, New: changeDiffRedacted, Sensitive: true},
		{Key: "token", Old: changeDiffRedacted, New: changeDiffRedacted, Sensitive: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffConfig() = %#v, want %#v", got, want)
	}

	// 敏感原文一个都不能漏进输出。
	secrets := []string{"sk-old", "sk-new", "tok-old", "tok-new", "pw-old", "pw-new",
		"acc-new", "nested-old", "nested-new", "s-old", "s-new"}
	for _, change := range got {
		for _, secret := range secrets {
			if strings.Contains(change.Old, secret) || strings.Contains(change.New, secret) {
				t.Errorf("键 %q 泄露了敏感原文 %q: %#v", change.Key, secret, change)
			}
		}
	}
}

// TestDiffConfigSensitiveRemoved 校验仅删除敏感键时旧值被脱敏、新值为空。
func TestDiffConfigSensitiveRemoved(t *testing.T) {
	got := DiffConfig([]byte(`{"token":"abc","model":"a"}`), []byte(`{"model":"a"}`))
	want := []ConfigChange{{Key: "token", Old: changeDiffRedacted, New: "", Sensitive: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffConfig() = %#v, want %#v", got, want)
	}
}

// TestDiffConfigSortedByKey 用乱序输入校验输出键按 UTF-8 字节序稳定升序，
// 同时混入 ASCII 与 CJK 键，验证排序不依赖 map 的遍历顺序。
func TestDiffConfigSortedByKey(t *testing.T) {
	oldRaw := []byte(`{"模型":"一","zeta":1,"alpha":1,"嵌套":{"键":"旧"}}`)
	newRaw := []byte(`{"模型":"二","zeta":2,"alpha":2,"beta":1,"嵌套":{"键":"新"}}`)

	got := DiffConfig(oldRaw, newRaw)
	keys := make([]string, 0, len(got))
	for _, change := range got {
		keys = append(keys, change.Key)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("变更未按 Key 升序排列: %v", keys)
	}
	wantKeys := []string{"alpha", "beta", "zeta", "嵌套.键", "模型"}
	sort.Strings(wantKeys)
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("键集合 = %v, want %v", keys, wantKeys)
	}
}

// TestDiffConfigCJKKeysAndValues 覆盖 CJK 键与 CJK 值：键路径完整保留，
// 值按原文比较（不因 UTF-8 多字节而误判或截断）。
func TestDiffConfigCJKKeysAndValues(t *testing.T) {
	oldRaw := []byte(`{"模型":"一","嵌套":{"键":"旧"}}`)
	newRaw := []byte(`{"模型":"二","嵌套":{"键":"新","新键":"值"}}`)

	got := DiffConfig(oldRaw, newRaw)
	want := []ConfigChange{
		{Key: "嵌套.新键", Old: "", New: "值"},
		{Key: "嵌套.键", Old: "旧", New: "新"},
		{Key: "模型", Old: "一", New: "二"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffConfig() = %#v, want %#v", got, want)
	}
}

// TestDiffConfigInvalidInput 固定"非法输入返回空"的契约：任一侧不是
// 可解析的 JSON 对象时都返回 nil，不返回部分结果也不 panic。
func TestDiffConfigInvalidInput(t *testing.T) {
	valid := []byte(`{"model":"gpt"}`)
	tests := []struct {
		name   string
		oldRaw []byte
		newRaw []byte
	}{
		{"两侧皆空", nil, nil},
		{"旧侧空字节", []byte(""), valid},
		{"截断对象", []byte(`{"a":`), valid},
		{"缺少闭合括号", []byte(`{"a":1`), valid},
		{"非 JSON 文本", []byte(`not json`), valid},
		{"顶层数组", []byte(`[]`), valid},
		{"顶层标量", []byte(`"scalar"`), valid},
		{"顶层 null", []byte(`null`), valid},
		{"顶层 true", []byte(`true`), valid},
		{"未闭合注释", []byte(`{"a":1 /*`), valid},
		{"新侧非法", valid, []byte(`{`)},
		{"两侧皆非法", []byte(`x`), []byte(`y`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DiffConfig(tt.oldRaw, tt.newRaw); got != nil {
				t.Fatalf("DiffConfig() = %#v, want nil", got)
			}
		})
	}
}

// TestDiffConfigNoChanges 校验相同配置无差异，并确认 JSONC 的注释与
// BOM 会被 StripJSONC 清理后参与比较，不产生虚假变更。
func TestDiffConfigNoChanges(t *testing.T) {
	tests := []struct {
		name   string
		oldRaw []byte
		newRaw []byte
	}{
		{"逐字节相同", []byte(`{"model":"gpt","n":1}`), []byte(`{"model":"gpt","n":1}`)},
		{"键顺序不同", []byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)},
		{"注释与 BOM", []byte("\ufeff{\"model\":\"gpt\" // 注释\n}"), []byte(`{"model":"gpt"}`)},
		{"尾随逗号", []byte(`{"model":"gpt",}`), []byte(`{"model":"gpt"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DiffConfig(tt.oldRaw, tt.newRaw); got != nil {
				t.Fatalf("DiffConfig() = %#v, want nil", got)
			}
		})
	}
}

// TestDiffConfigDoesNotMutateInputs 校验纯函数性质：调用前后两份原始
// 字节完全一致（既不改写、也不截断入参切片）。
func TestDiffConfigDoesNotMutateInputs(t *testing.T) {
	oldRaw := []byte(`{"apiKey":"old","model":"a","nested":{"k":1}}`)
	newRaw := []byte(`{"apiKey":"new","model":"b","nested":{"k":2}}`)
	oldCopy := append([]byte(nil), oldRaw...)
	newCopy := append([]byte(nil), newRaw...)

	_ = DiffConfig(oldRaw, newRaw)

	if !bytes.Equal(oldRaw, oldCopy) {
		t.Errorf("oldRaw 被修改: %q, want %q", oldRaw, oldCopy)
	}
	if !bytes.Equal(newRaw, newCopy) {
		t.Errorf("newRaw 被修改: %q, want %q", newRaw, newCopy)
	}
}

// TestDiffConfigDeterministic 校验确定性：同一输入重复调用得到逐字段
// 相同的结果，包含 CJK 键与敏感键时依然如此。
func TestDiffConfigDeterministic(t *testing.T) {
	oldRaw := []byte(`{"模型":"一","apiKey":"a","zeta":1,"嵌套":{"键":"旧","token":"t"}}`)
	newRaw := []byte(`{"模型":"二","apiKey":"b","zeta":2,"嵌套":{"键":"新","token":"u"}}`)

	first := DiffConfig(oldRaw, newRaw)
	for i := 0; i < 20; i++ {
		got := DiffConfig(oldRaw, newRaw)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次结果漂移: 首次 %#v, 本次 %#v", i+2, first, got)
		}
	}
}
