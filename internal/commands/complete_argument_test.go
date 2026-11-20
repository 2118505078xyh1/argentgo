package commands

import (
	"reflect"
	"testing"
)

func TestCompleteArgumentCandidateParsing(t *testing.T) {
	tests := []struct {
		name string
		hint string
		want []string
	}{
		{name: "bare alternatives", hint: "dev|stage|prod", want: []string{"dev", "stage", "prod"}},
		{name: "square bracket alternatives", hint: "[dev|stage|prod]", want: []string{"dev", "stage", "prod"}},
		{name: "angle bracket alternatives", hint: "<dev|stage|prod>", want: []string{"dev", "stage", "prod"}},
		{name: "label then alternatives", hint: "环境: 开发|测试|生产", want: []string{"开发", "测试", "生产"}},
		{
			name: "multiple groups",
			hint: "<path> [one|two] mode: fast|slow",
			want: []string{"one", "two", "fast", "slow"},
		},
		{name: "empty alternatives dropped", hint: "a||b|", want: []string{"a", "b"}},
		{name: "duplicates dropped", hint: "a|b|a", want: []string{"a", "b"}},
		{name: "single value after cleanup is not a choice", hint: "a|", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompleteArgument(Command{ArgumentHint: tt.hint}, "")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompleteArgument(hint=%q) = %v, want %v", tt.hint, got, tt.want)
			}
		})
	}
}

func TestCompleteArgumentPrefixFiltering(t *testing.T) {
	cmd := Command{ArgumentHint: "dev|stage|prod"}
	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{name: "empty prefix lists all", prefix: "", want: []string{"dev", "stage", "prod"}},
		{name: "partial prefix", prefix: "de", want: []string{"dev"}},
		{name: "second candidate", prefix: "s", want: []string{"stage"}},
		{name: "case insensitive", prefix: "PR", want: []string{"prod"}},
		{name: "full width prefix folds", prefix: "ＤＥ", want: []string{"dev"}},
		{name: "no match", prefix: "z", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompleteArgument(cmd, tt.prefix)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompleteArgument(prefix=%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestCompleteArgumentCJK(t *testing.T) {
	cmd := Command{ArgumentHint: "环境: 开发|测试|生产"}
	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{name: "single cjk rune", prefix: "开", want: []string{"开发"}},
		{name: "full cjk candidate", prefix: "测试", want: []string{"测试"}},
		{name: "no cjk match", prefix: "其", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompleteArgument(cmd, tt.prefix)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompleteArgument(prefix=%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}

	// Full-width ASCII typed through an IME must match half-width candidates.
	fullWidthCmd := Command{ArgumentHint: "DEV|Stage"}
	got := CompleteArgument(fullWidthCmd, "ｓｔａ")
	if !reflect.DeepEqual(got, []string{"Stage"}) {
		t.Errorf("full-width prefix = %v, want [Stage]", got)
	}
}

func TestCompleteArgumentEmptyHint(t *testing.T) {
	if got := CompleteArgument(Command{}, "x"); got != nil {
		t.Errorf("empty hint = %v, want nil", got)
	}
	if got := CompleteArgument(Command{ArgumentHint: "   "}, "x"); got != nil {
		t.Errorf("whitespace hint = %v, want nil", got)
	}
}

func TestCompleteArgumentNoCandidates(t *testing.T) {
	tests := []struct {
		name string
		hint string
	}{
		{name: "angle placeholder", hint: "<env>"},
		{name: "square placeholder", hint: "[file]"},
		{name: "multiple placeholders", hint: "<path> [focus]"},
		{name: "plain description", hint: "the target file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompleteArgument(Command{ArgumentHint: tt.hint}, ""); got != nil {
				t.Errorf("CompleteArgument(hint=%q) = %v, want nil", tt.hint, got)
			}
		})
	}
}
