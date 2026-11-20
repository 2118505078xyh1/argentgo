package hooks

import (
	"reflect"
	"testing"
)

func TestPendingEventSummary(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want []EventSummary
	}{
		{
			name: "empty config",
			doc:  `{"hooks":{}}`,
			want: nil,
		},
		{
			name: "CJK command names",
			doc: `{"hooks":{"PreToolUse":[
				{"matcher":"写入","hooks":[{"command":"echo 开始","async":true}]},
				{"matcher":"*","hooks":[{"command":"echo 通配"}]},
				{"hooks":[{"command":"echo 结束"}]}
			]}}`,
			want: []EventSummary{{Event: PreToolUse, Count: 3, WithMatcher: 1, Async: 1}},
		},
		{
			name: "multiple events in AllEvents order",
			doc: `{"hooks":{
				"FileChanged":[{"matcher":"*.go","hooks":[{"command":"go-hook"}]}],
				"PreToolUse":[
					{"matcher":"bash","hooks":[{"command":"echo a"},{"command":"echo b","async":true}]},
					{"hooks":[{"command":"echo bare"}]}
				],
				"Stop":[{"command":"echo stop","async":true}],
				"SesionStart":[{"command":"ignored typo"}]
			}}`,
			want: []EventSummary{
				{Event: PreToolUse, Count: 3, WithMatcher: 2, Async: 1},
				{Event: Stop, Count: 1, WithMatcher: 0, Async: 1},
				{Event: FileChanged, Count: 1, WithMatcher: 1, Async: 0},
			},
		},
		{
			name: "empty group contributes no hooks",
			doc: `{"hooks":{"Notification":[
				{"hooks":[{"type":"prompt","command":"echo ignored"}]},
				{"hooks":[{"command":"   "}]}
			]}}`,
			want: nil,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config, err := ParseConfig([]byte(test.doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := config.PendingEventSummary()
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("PendingEventSummary() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestPendingEventSummaryNilConfig(t *testing.T) {
	var config *Config
	if got := config.PendingEventSummary(); got != nil {
		t.Fatalf("nil config = %+v, want nil", got)
	}
}

func TestPendingEventSummaryProgrammaticEventsSortedLast(t *testing.T) {
	// 名和文件路径可以携带字面 '*'，因此无条件把模式中的 '*' 当作通配符。
	// 未知事件被文档解析器跳过，但可由编程调用方设置；它们仍必须以确定的顺序出现。
	config := &Config{Commands: []Command{
		{Event: "Zeta", Command: "echo z"},
		{Event: PreToolUse, Command: "echo pre"},
		{Event: "Alpha", Command: "echo a", Matcher: "*", Async: true},
		{Event: "Zeta", Command: "echo z2", Matcher: "bash"},
	}}
	want := []EventSummary{
		{Event: PreToolUse, Count: 1, WithMatcher: 0, Async: 0},
		{Event: "Alpha", Count: 1, WithMatcher: 0, Async: 1},
		{Event: "Zeta", Count: 2, WithMatcher: 1, Async: 0},
	}
	if got := config.PendingEventSummary(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingEventSummary() = %+v, want %+v", got, want)
	}
}
