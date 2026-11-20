package commands

import (
	"reflect"
	"testing"
)

func TestCompleteCommandNames(t *testing.T) {
	available := []string{"review", "revive", "context", "审查"}
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "plain prefix", input: "/rev", want: []string{"review", "revive"}},
		{name: "case insensitive", input: "/RE", want: []string{"review", "revive"}},
		{name: "exact name", input: "/context", want: []string{"context"}},
		{name: "cjk prefix", input: "/审", want: []string{"审查"}},
		{name: "bare slash lists all sorted", input: "/", want: []string{"context", "review", "revive", "审查"}},
		{name: "no command match", input: "/zz", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertComplete(t, tt.input, available, tt.want)
		})
	}
}

func TestCompleteAmbiguousPrefixReturnsAllMatches(t *testing.T) {
	available := []string{"deploy", "deploy-prod", "deploy-dev", "review"}
	assertComplete(t, "/deploy", available, []string{"deploy", "deploy-dev", "deploy-prod"})
}

func TestCompleteArgumentCandidates(t *testing.T) {
	available := []string{"dev", "stage", "prod"}
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "argument prefix", input: "/deploy st", want: []string{"stage"}},
		{name: "single letter", input: "/deploy d", want: []string{"dev"}},
		{name: "trailing space lists all", input: "/deploy ", want: []string{"dev", "prod", "stage"}},
		{name: "free text argument", input: "st", want: []string{"stage"}},
		{name: "no argument match", input: "/deploy zz", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertComplete(t, tt.input, available, tt.want)
		})
	}
}

func TestCompleteIdentifierFallback(t *testing.T) {
	// 含空白的值无法通过参数提示往返传递，
	// 因此在简单候选少于两个时会进入第 3 级（标识符）。
	tests := []struct {
		name      string
		input     string
		available []string
		want      []string
	}{
		{
			name:      "multi word identifiers",
			input:     "my",
			available: []string{"my file.txt", "config.yaml", "my notes.md"},
			want:      []string{"my file.txt", "my notes.md"},
		},
		{
			name:      "single identifier",
			input:     "conf",
			available: []string{"config.yaml", "审 查.md"},
			want:      []string{"config.yaml"},
		},
		{
			name:      "cjk identifiers",
			input:     "审",
			available: []string{"审 查.md", "审核.md", "my file.txt"},
			want:      []string{"审 查.md", "审核.md"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertComplete(t, tt.input, tt.available, tt.want)
		})
	}
}

func TestCompleteCJKAndFullWidth(t *testing.T) {
	available := []string{"review", "审查", "审核日志"}
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "full width command prefix", input: "/ＲＥＶ", want: []string{"review"}},
		{name: "full width slash", input: "／审", want: []string{"审查", "审核日志"}},
		{name: "ideographic space ends command token", input: "/审查　额外", want: []string{"审查"}},
		{name: "cjk without slash", input: "审", want: []string{"审查", "审核日志"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertComplete(t, tt.input, available, tt.want)
		})
	}
}

func TestCompleteEmptyInputListsEverything(t *testing.T) {
	available := []string{"zeta", "alpha", "alpha", "审查"}
	want := []string{"alpha", "zeta", "审查"}
	assertComplete(t, "", available, want)
	assertComplete(t, "   ", available, want)
	assertComplete(t, "\u3000\t", available, want)
	assertComplete(t, "", nil, nil)
}

func TestCompleteStableSortAndDedup(t *testing.T) {
	available := []string{"Beta", "alpha", "beta", "Alpha", "context"}
	// 按大小写折叠的排序键，以原字符串作并列决胜项，去除
	// 完全重复项；每次调用都会产生相同顺序。
	assertComplete(t, "", available, []string{"Alpha", "alpha", "Beta", "beta", "context"})
	assertComplete(t, "/al", available, []string{"Alpha", "alpha"})
	for i := 0; i < 5; i++ {
		got := Complete("", available)
		if !reflect.DeepEqual(got, []string{"Alpha", "alpha", "Beta", "beta", "context"}) {
			t.Fatalf("unstable result on call %d: %v", i, got)
		}
	}
}

func TestCompleteDoesNotMutateInput(t *testing.T) {
	available := []string{"review", "revive", "context"}
	before := append([]string(nil), available...)

	got := Complete("/rev", available)
	if !reflect.DeepEqual(available, before) {
		t.Errorf("Complete modified its input: %v", available)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	got[0] = "tampered"
	if !reflect.DeepEqual(available, before) {
		t.Errorf("result aliases the input slice: %v", available)
	}
}

func TestCompleteFallsBackWhenNoCommandMatches(t *testing.T) {
	// "/cmd ze" 形似命令，但没有名为 "cmd" 的命令，所以第 1 级
	// 失败，末尾标记通过第 2 级补全。
	assertComplete(t, "/cmd ze", []string{"zebra", "zone", "other"}, []string{"zebra"})
}

func assertComplete(t *testing.T, input string, available, want []string) {
	t.Helper()
	got := Complete(input, available)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Complete(%q, %v) = %v, want %v", input, available, got, want)
	}
}
