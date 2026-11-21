package server

import (
	"reflect"
	"testing"
)

// TestMatches 是 Matches 的主表驱动测试，覆盖 kind 匹配/通配、scope 通配/精确、
// terminal-only、各维度组合、零值兼容与 CJK 字符串精确比较。
func TestMatches(t *testing.T) {
	cases := []struct {
		name     string
		sub      Subscription
		kind     string
		scopeID  uint64
		terminal bool
		want     bool
	}{
		{
			name: "nil kinds wildcard any kind",
			sub:  Subscription{Kinds: nil},
			kind: "anything",
			want: true,
		},
		{
			name: "empty kinds wildcard any kind",
			sub:  Subscription{Kinds: []string{}},
			kind: "text_delta",
			want: true,
		},
		{
			name: "exact kind hit",
			sub:  Subscription{Kinds: []string{"turn_start", "tool_end"}},
			kind: "tool_end",
			want: true,
		},
		{
			name: "exact kind miss",
			sub:  Subscription{Kinds: []string{"turn_start", "tool_end"}},
			kind: "tool_start",
			want: false,
		},
		{
			name: "kind comparison is case sensitive",
			sub:  Subscription{Kinds: []string{"TextDelta"}},
			kind: "text_delta",
			want: false,
		},
		{
			name:    "scope zero wildcard any scope",
			sub:     Subscription{ScopeID: 0},
			scopeID: 42,
			want:    true,
		},
		{
			name:    "scope exact hit",
			sub:     Subscription{ScopeID: 42},
			scopeID: 42,
			want:    true,
		},
		{
			name:    "scope exact miss",
			sub:     Subscription{ScopeID: 42},
			scopeID: 43,
			want:    false,
		},
		{
			name:     "terminal only rejects non terminal",
			sub:      Subscription{TerminalOnly: true},
			terminal: false,
			want:     false,
		},
		{
			name:     "terminal only accepts terminal",
			sub:      Subscription{TerminalOnly: true},
			terminal: true,
			want:     true,
		},
		{
			name:     "non terminal only accepts non terminal",
			sub:      Subscription{TerminalOnly: false},
			terminal: false,
			want:     true,
		},
		{
			name:     "non terminal only also accepts terminal",
			sub:      Subscription{TerminalOnly: false},
			terminal: true,
			want:     true,
		},
		{
			name: "combination all dimensions hit",
			sub: Subscription{
				Kinds:        []string{"tool_end"},
				ScopeID:      7,
				TerminalOnly: true,
			},
			kind:     "tool_end",
			scopeID:  7,
			terminal: true,
			want:     true,
		},
		{
			name: "combination kind miss",
			sub: Subscription{
				Kinds:        []string{"tool_end"},
				ScopeID:      7,
				TerminalOnly: true,
			},
			kind:     "tool_start",
			scopeID:  7,
			terminal: true,
			want:     false,
		},
		{
			name: "combination scope miss",
			sub: Subscription{
				Kinds:        []string{"tool_end"},
				ScopeID:      7,
				TerminalOnly: true,
			},
			kind:     "tool_end",
			scopeID:  8,
			terminal: true,
			want:     false,
		},
		{
			name: "combination terminal miss",
			sub: Subscription{
				Kinds:        []string{"tool_end"},
				ScopeID:      7,
				TerminalOnly: true,
			},
			kind:     "tool_end",
			scopeID:  7,
			terminal: false,
			want:     false,
		},
		{
			name: "zero value subscription matches all",
			sub:  Subscription{},
			want: true,
		},
		{
			name:     "zero value subscription matches terminal in any scope",
			sub:      Subscription{},
			kind:     "stop",
			scopeID:  999,
			terminal: true,
			want:     true,
		},
		{
			name: "cjk kind exact hit",
			sub:  Subscription{Kinds: []string{"中文事件"}},
			kind: "中文事件",
			want: true,
		},
		{
			name: "cjk kind exact miss",
			sub:  Subscription{Kinds: []string{"中文事件"}},
			kind: "另一种事件",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.sub, tc.kind, tc.scopeID, tc.terminal); got != tc.want {
				t.Fatalf("Matches(%+v, %q, %d, %v) = %v, want %v",
					tc.sub, tc.kind, tc.scopeID, tc.terminal, got, tc.want)
			}
		})
	}
}

// subIDs 抽取订阅者 ID 序列，便于断言过滤结果的相对顺序。
func subIDs(subs []Subscription) []string {
	ids := make([]string, len(subs))
	for i := range subs {
		ids[i] = subs[i].ID
	}
	return ids
}

// TestFilterSubscriptions 是过滤函数的主表驱动测试，覆盖空输入、无命中、全命中、
// 穿插命中与三维组合，并断言稳定顺序与空结果为非 nil 空切片。
func TestFilterSubscriptions(t *testing.T) {
	sub := func(id string, kinds []string, scopeID uint64, terminalOnly bool) Subscription {
		return Subscription{ID: id, Kinds: kinds, ScopeID: scopeID, TerminalOnly: terminalOnly}
	}

	cases := []struct {
		name     string
		subs     []Subscription
		kind     string
		scopeID  uint64
		terminal bool
		wantIDs  []string
	}{
		{
			name:    "nil subs yields non nil empty slice",
			subs:    nil,
			kind:    "any",
			wantIDs: []string{},
		},
		{
			name:    "empty subs yields non nil empty slice",
			subs:    []Subscription{},
			kind:    "any",
			wantIDs: []string{},
		},
		{
			name: "no match yields empty slice",
			subs: []Subscription{
				sub("a", []string{"tool_start"}, 0, false),
				sub("b", []string{"tool_end"}, 9, false),
			},
			kind:    "text_delta",
			scopeID: 9,
			wantIDs: []string{},
		},
		{
			name: "all match keeps original order",
			subs: []Subscription{
				sub("first", nil, 0, false),
				sub("second", nil, 0, false),
				sub("third", nil, 0, false),
			},
			kind:    "text_delta",
			scopeID: 123,
			wantIDs: []string{"first", "second", "third"},
		},
		{
			name: "interleaved matches keep stable order",
			subs: []Subscription{
				sub("skip-1", []string{"text_delta"}, 0, false),
				sub("keep-1", []string{"tool_end"}, 0, false),
				sub("skip-2", []string{"tool_end"}, 8, false),
				sub("keep-2", []string{"tool_end"}, 9, false),
				sub("skip-3", []string{"tool_end"}, 9, true),
				sub("keep-3", nil, 9, false),
			},
			kind:    "tool_end",
			scopeID: 9,
			wantIDs: []string{"keep-1", "keep-2", "keep-3"},
		},
		{
			name: "combination of kind scope and terminal",
			subs: []Subscription{
				sub("wrong-kind", []string{"turn_start"}, 7, true),
				sub("wrong-scope", []string{"stop"}, 6, true),
				sub("terminal-ok", []string{"stop"}, 7, false),
				sub("hit-1", []string{"stop"}, 7, true),
				sub("hit-2", nil, 0, true),
			},
			kind:     "stop",
			scopeID:  7,
			terminal: true,
			wantIDs:  []string{"terminal-ok", "hit-1", "hit-2"},
		},
		{
			name: "cjk ids and kinds preserved",
			subs: []Subscription{
				sub("中文订阅甲", []string{"中文事件"}, 0, false),
				sub("skip", []string{"别的事件"}, 0, false),
				sub("中文订阅乙", []string{"中文事件"}, 3, false),
			},
			kind:    "中文事件",
			scopeID: 3,
			wantIDs: []string{"中文订阅甲", "中文订阅乙"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterSubscriptions(tc.subs, tc.kind, tc.scopeID, tc.terminal)
			if got == nil {
				t.Fatal("FilterSubscriptions 返回 nil，期望非 nil 切片")
			}
			if ids := subIDs(got); !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Fatalf("过滤结果 ID = %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

// cloneSubs 深拷贝订阅切片（含 Kinds 底层数组），用于入参不变性断言。
func cloneSubs(subs []Subscription) []Subscription {
	out := make([]Subscription, len(subs))
	copy(out, subs)
	for i := range subs {
		if subs[i].Kinds != nil {
			out[i].Kinds = append([]string(nil), subs[i].Kinds...)
		}
	}
	return out
}

// TestFilterSubscriptionsDoesNotMutateInput 验证过滤不重排、不覆盖入参，
// 且输出切片拥有独立底层数组——修改输出不会回写入参。
func TestFilterSubscriptionsDoesNotMutateInput(t *testing.T) {
	in := []Subscription{
		{ID: "a", Kinds: []string{"text_delta", "tool_end"}, ScopeID: 1, TerminalOnly: false},
		{ID: "b", Kinds: []string{"tool_end"}, ScopeID: 2, TerminalOnly: true},
		{ID: "c", Kinds: nil, ScopeID: 0, TerminalOnly: false},
	}
	snapshot := cloneSubs(in)

	got := FilterSubscriptions(in, "tool_end", 2, false)
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("过滤调用修改了入参：got %v, want %v", in, snapshot)
	}

	for i := range got {
		got[i].ID = "modified"
	}
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("修改输出切片影响了入参：got %v, want %v", in, snapshot)
	}
}
