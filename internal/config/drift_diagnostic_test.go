package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestDiagnoseDrift 覆盖 DiagnoseDrift 的全部类别、稳定排序、CJK 安全性与
// 入参不变性。测试与源码同包，可直接断言返回的 DriftEntry 字段。
func TestDiagnoseDrift(t *testing.T) {
	tests := []struct {
		name   string
		before map[string]string
		after  map[string]string
		want   []DriftEntry
	}{
		{
			name:   "both nil yields no differences",
			before: nil,
			after:  nil,
			want:   nil,
		},
		{
			name:   "empty maps yield no differences",
			before: map[string]string{},
			after:  map[string]string{},
			want:   nil,
		},
		{
			name:   "identical maps yield no differences",
			before: map[string]string{"a": "1", "b": "2"},
			after:  map[string]string{"a": "1", "b": "2"},
			want:   nil,
		},
		{
			name:   "added key",
			before: map[string]string{},
			after:  map[string]string{"a": "1"},
			want:   []DriftEntry{{Key: "a", After: "1", Kind: "added"}},
		},
		{
			name:   "removed key",
			before: map[string]string{"a": "1"},
			after:  map[string]string{},
			want:   []DriftEntry{{Key: "a", Before: "1", Kind: "removed"}},
		},
		{
			name:   "changed value",
			before: map[string]string{"a": "1"},
			after:  map[string]string{"a": "2"},
			want:   []DriftEntry{{Key: "a", Before: "1", After: "2", Kind: "changed"}},
		},
		{
			name:   "all three kinds with stable sort by key",
			before: map[string]string{"zeta": "old", "beta": "same", "alpha": "gone", "mid": "v1"},
			after:  map[string]string{"zeta": "new", "beta": "same", "gamma": "fresh", "mid": "v2"},
			want: []DriftEntry{
				{Key: "alpha", Before: "gone", Kind: "removed"},
				{Key: "gamma", After: "fresh", Kind: "added"},
				{Key: "mid", Before: "v1", After: "v2", Kind: "changed"},
				{Key: "zeta", Before: "old", After: "new", Kind: "changed"},
			},
		},
		{
			name:   "CJK keys and values preserved",
			before: map[string]string{"模型": "旧值", "提供方.名称": "中文"},
			after:  map[string]string{"模型": "新值", "新增键": "值"},
			want: []DriftEntry{
				{Key: "提供方.名称", Before: "中文", Kind: "removed"},
				{Key: "新增键", After: "值", Kind: "added"},
				{Key: "模型", Before: "旧值", After: "新值", Kind: "changed"},
			},
		},
		{
			name:   "ASCII keys sort before CJK keys",
			before: map[string]string{"中文": "1", "apiKey": "2"},
			after:  nil,
			want: []DriftEntry{
				{Key: "apiKey", Before: "2", Kind: "removed"},
				{Key: "中文", Before: "1", Kind: "removed"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 快照两个入参，断言 DiagnoseDrift 不修改它们。
			beforeCopy := cloneStringMap(tc.before)
			afterCopy := cloneStringMap(tc.after)

			got := DiagnoseDrift(tc.before, tc.after)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DiagnoseDrift() = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.before, beforeCopy) {
				t.Errorf("DiagnoseDrift() mutated before: got %v, want %v", tc.before, beforeCopy)
			}
			if !reflect.DeepEqual(tc.after, afterCopy) {
				t.Errorf("DiagnoseDrift() mutated after: got %v, want %v", tc.after, afterCopy)
			}

			// 再次调用结果必须逐字节一致，验证纯函数的确定性。
			if again := DiagnoseDrift(tc.before, tc.after); !reflect.DeepEqual(again, got) {
				t.Errorf("DiagnoseDrift() not deterministic: %+v vs %+v", again, got)
			}
		})
	}
}

// TestDiagnoseDriftStableRegardlessOfMapOrder 验证 map 迭代顺序不影响结果：
// 用不同插入顺序构造语义相同的两个 map，输出必须完全一致。
func TestDiagnoseDriftStableRegardlessOfMapOrder(t *testing.T) {
	before := map[string]string{"d": "4", "b": "2", "a": "1", "c": "3"}
	after := map[string]string{"d": "X", "b": "2", "e": "5", "c": "Y"}

	first := DiagnoseDrift(before, after)
	second := DiagnoseDrift(before, after)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("DiagnoseDrift() unstable across calls: %+v vs %+v", first, second)
	}
	if !sortDriftKeysAscending(first) {
		t.Fatalf("DiagnoseDrift() result not sorted by Key: %+v", first)
	}
}

// TestDriftSeverity 覆盖敏感键命中（high）与各级数量分级（none/low/medium）。
func TestDriftSeverity(t *testing.T) {
	tests := []struct {
		name          string
		entries       []DriftEntry
		sensitiveKeys map[string]bool
		want          string
	}{
		{
			name:          "nil entries yields none",
			entries:       nil,
			sensitiveKeys: nil,
			want:          "none",
		},
		{
			name:          "empty entries yields none",
			entries:       []DriftEntry{},
			sensitiveKeys: map[string]bool{"apiKey": true},
			want:          "none",
		},
		{
			name:          "one entry is low",
			entries:       []DriftEntry{{Key: "model", Kind: "changed"}},
			sensitiveKeys: nil,
			want:          "low",
		},
		{
			name: "two entries are low",
			entries: []DriftEntry{
				{Key: "model", Kind: "changed"},
				{Key: "locale", Kind: "changed"},
			},
			sensitiveKeys: nil,
			want:          "low",
		},
		{
			name: "three entries are medium",
			entries: []DriftEntry{
				{Key: "model", Kind: "changed"},
				{Key: "locale", Kind: "changed"},
				{Key: "maxTurns", Kind: "changed"},
			},
			sensitiveKeys: nil,
			want:          "medium",
		},
		{
			name: "sensitive key with single entry is high",
			entries: []DriftEntry{
				{Key: "apiKey", Kind: "changed"},
			},
			sensitiveKeys: map[string]bool{"apiKey": true},
			want:          "high",
		},
		{
			name: "sensitive key wins over count",
			entries: []DriftEntry{
				{Key: "a", Kind: "added"},
				{Key: "b", Kind: "removed"},
				{Key: "c", Kind: "changed"},
			},
			sensitiveKeys: map[string]bool{"b": true},
			want:          "high",
		},
		{
			name: "sensitive key listed with false does not trigger high",
			entries: []DriftEntry{
				{Key: "apiKey", Kind: "changed"},
			},
			sensitiveKeys: map[string]bool{"apiKey": false},
			want:          "low",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 快照入参，保留 nil 与空切片的区分，避免误判为"被修改"。
			var snapshot []DriftEntry
			if tc.entries != nil {
				snapshot = append([]DriftEntry{}, tc.entries...)
			}
			if got := DriftSeverity(tc.entries, tc.sensitiveKeys); got != tc.want {
				t.Errorf("DriftSeverity() = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(tc.entries, snapshot) {
				t.Errorf("DriftSeverity() mutated entries: got %+v, want %+v", tc.entries, snapshot)
			}
		})
	}
}

// TestDriftSummary 覆盖摘要的稳定排序、中文标签、CJK、换行压缩，以及最关键的
// "不泄露 Before/After 原文"约束。
func TestDriftSummary(t *testing.T) {
	tests := []struct {
		name    string
		entries []DriftEntry
		want    string
	}{
		{
			name:    "nil entries returns fixed literal",
			entries: nil,
			want:    driftOK,
		},
		{
			name:    "empty entries returns fixed literal",
			entries: []DriftEntry{},
			want:    driftOK,
		},
		{
			name:    "single added entry",
			entries: []DriftEntry{{Key: "newKey", After: "secret-value", Kind: "added"}},
			want: "配置漂移: 1 处\n" +
				"  [新增] newKey",
		},
		{
			name: "all kinds sorted by key",
			entries: []DriftEntry{
				{Key: "zeta", Before: "old-secret", After: "new-secret", Kind: "changed"},
				{Key: "alpha", Before: "gone-secret", Kind: "removed"},
				{Key: "mid", After: "added-secret", Kind: "added"},
			},
			want: "配置漂移: 3 处\n" +
				"  [删除] alpha\n" +
				"  [新增] mid\n" +
				"  [变更] zeta",
		},
		{
			name: "CJK keys preserved intact and sorted",
			entries: []DriftEntry{
				{Key: "模型", Before: "敏感旧值XYZ", After: "敏感新值ABC", Kind: "changed"},
				{Key: "apiKey", Before: "秘密数值123", After: "秘密数值456", Kind: "changed"},
			},
			want: "配置漂移: 2 处\n" +
				"  [变更] apiKey\n" +
				"  [变更] 模型",
		},
		{
			name: "newlines in key compressed to one line",
			entries: []DriftEntry{
				{Key: "多\n行\r\n键", After: "值", Kind: "added"},
			},
			want: "配置漂移: 1 处\n" +
				"  [新增] 多 行 键",
		},
		{
			name:    "unknown kind falls back to raw label",
			entries: []DriftEntry{{Key: "k", Kind: "moved"}},
			want: "配置漂移: 1 处\n" +
				"  [moved] k",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 快照入参，保留 nil 与空切片的区分，避免误判为"被修改"。
			var snapshot []DriftEntry
			if tc.entries != nil {
				snapshot = append([]DriftEntry{}, tc.entries...)
			}
			got := DriftSummary(tc.entries)
			if got != tc.want {
				t.Errorf("DriftSummary() = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(tc.entries, snapshot) {
				t.Errorf("DriftSummary() mutated entries: got %+v, want %+v", tc.entries, snapshot)
			}

			// 摘要绝不能包含任何 Before/After 原文。
			for _, e := range tc.entries {
				if e.Before != "" && strings.Contains(got, e.Before) {
					t.Errorf("DriftSummary() leaked Before value %q: %q", e.Before, got)
				}
				if e.After != "" && strings.Contains(got, e.After) {
					t.Errorf("DriftSummary() leaked After value %q: %q", e.After, got)
				}
			}
		})
	}
}

// TestDriftSummaryStableRegardlessOfInputOrder 验证摘要对同一组条目的不同
// 初始顺序产生逐字节一致的文本。
func TestDriftSummaryStableRegardlessOfInputOrder(t *testing.T) {
	a := []DriftEntry{
		{Key: "c", After: "x", Kind: "added"},
		{Key: "a", Before: "y", Kind: "removed"},
		{Key: "b", Before: "1", After: "2", Kind: "changed"},
	}
	b := []DriftEntry{
		{Key: "b", Before: "1", After: "2", Kind: "changed"},
		{Key: "c", After: "x", Kind: "added"},
		{Key: "a", Before: "y", Kind: "removed"},
	}

	sa := DriftSummary(a)
	sb := DriftSummary(b)
	if sa != sb {
		t.Errorf("DriftSummary() order-dependent: %q vs %q", sa, sb)
	}
}

// cloneStringMap 返回 map 的浅拷贝；nil 仍返回 nil，便于保留"未修改"的语义。
func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// sortDriftKeysAscending 辅助检查条目是否已按 Key 字典序升序排列。
func sortDriftKeysAscending(entries []DriftEntry) bool {
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Key > entries[i].Key {
			return false
		}
	}
	return true
}
