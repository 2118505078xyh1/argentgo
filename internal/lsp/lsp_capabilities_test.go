package lsp

import (
	"errors"
	"reflect"
	"testing"
)

func TestSupports(t *testing.T) {
	tests := []struct {
		name  string
		caps  []Capability
		query Capability
		want  bool
	}{
		{
			name:  "命中首个能力",
			caps:  []Capability{CapabilityHover, CapabilityDefinition},
			query: CapabilityHover,
			want:  true,
		},
		{
			name:  "命中末个能力",
			caps:  []Capability{CapabilityHover, CapabilityRename},
			query: CapabilityRename,
			want:  true,
		},
		{
			name:  "未声明的能力",
			caps:  []Capability{CapabilityHover, CapabilityDefinition},
			query: CapabilityFormat,
			want:  false,
		},
		{
			name:  "空能力列表",
			caps:  nil,
			query: CapabilityHover,
			want:  false,
		},
		{
			name:  "空查询不误命中",
			caps:  []Capability{CapabilityHover},
			query: Capability(""),
			want:  false,
		},
		{
			name:  "自定义能力值亦可查询",
			caps:  []Capability{"代码操作"},
			query: "代码操作",
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ServerCapabilities{Name: "s", Caps: tt.caps}
			if got := Supports(c, tt.query); got != tt.want {
				t.Fatalf("Supports(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestMergeCapabilities(t *testing.T) {
	tests := []struct {
		name string
		a    ServerCapabilities
		b    ServerCapabilities
		want ServerCapabilities
	}{
		{
			name: "并集去重并按字符串值升序",
			a:    ServerCapabilities{Name: "gopls", Caps: []Capability{CapabilityHover, CapabilityDefinition}, Order: 1},
			b:    ServerCapabilities{Name: "gopls", Caps: []Capability{CapabilityRename, CapabilityHover, CapabilityFormat}, Order: 2},
			want: ServerCapabilities{
				Name:  "gopls",
				Caps:  []Capability{CapabilityDefinition, CapabilityFormat, CapabilityHover, CapabilityRename},
				Order: 1,
			},
		},
		{
			name: "两侧完全重复",
			a:    ServerCapabilities{Name: "x", Caps: []Capability{CapabilityHover, CapabilityHover}},
			b:    ServerCapabilities{Name: "x", Caps: []Capability{CapabilityHover}},
			want: ServerCapabilities{Name: "x", Caps: []Capability{CapabilityHover}},
		},
		{
			name: "空并空",
			a:    ServerCapabilities{Name: "x"},
			b:    ServerCapabilities{Name: "x"},
			want: ServerCapabilities{Name: "x"},
		},
		{
			name: "仅 b 有名称与顺序",
			a:    ServerCapabilities{Caps: []Capability{CapabilityRename}},
			b:    ServerCapabilities{Name: "rust-analyzer", Caps: []Capability{CapabilityHover}, Order: 7},
			want: ServerCapabilities{Name: "rust-analyzer", Caps: []Capability{CapabilityHover, CapabilityRename}, Order: 7},
		},
		{
			name: "两侧均无名称时保留空名并取 b 的顺序",
			a:    ServerCapabilities{Caps: []Capability{CapabilityFormat}, Order: 3},
			b:    ServerCapabilities{Caps: []Capability{CapabilityHover}, Order: 9},
			want: ServerCapabilities{Caps: []Capability{CapabilityFormat, CapabilityHover}, Order: 9},
		},
		{
			name: "CJK 名称保留",
			a:    ServerCapabilities{Name: "中文服务器", Caps: []Capability{"悬停"}, Order: 4},
			b:    ServerCapabilities{Name: "中文服务器", Caps: []Capability{"定义", "悬停"}},
			want: ServerCapabilities{Name: "中文服务器", Caps: []Capability{"定义", "悬停"}, Order: 4},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeCapabilities(tt.a, tt.b)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MergeCapabilities() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMergeCapabilitiesDoesNotMutateInputs(t *testing.T) {
	a := ServerCapabilities{Name: "a", Caps: []Capability{CapabilityHover, CapabilityDefinition}, Order: 1}
	b := ServerCapabilities{Name: "b", Caps: []Capability{CapabilityRename}, Order: 2}
	aBefore := append([]Capability(nil), a.Caps...)
	bBefore := append([]Capability(nil), b.Caps...)

	merged := MergeCapabilities(a, b)
	if !reflect.DeepEqual(a.Caps, aBefore) || !reflect.DeepEqual(b.Caps, bBefore) {
		t.Fatalf("MergeCapabilities mutated inputs: a=%v b=%v", a.Caps, b.Caps)
	}
	// 修改结果不得反向影响入参。
	merged.Caps[0] = Capability("changed")
	merged.Name = "changed"
	if !reflect.DeepEqual(a.Caps, aBefore) || !reflect.DeepEqual(b.Caps, bBefore) || a.Name != "a" || b.Name != "b" {
		t.Fatalf("mutating merged result affected inputs: a=%+v b=%+v", a, b)
	}
}

func TestValidateCapabilities(t *testing.T) {
	tests := []struct {
		name string
		c    ServerCapabilities
		want error
	}{
		{
			name: "合法声明",
			c:    ServerCapabilities{Name: "gopls", Caps: []Capability{CapabilityHover, CapabilityDefinition}},
			want: nil,
		},
		{
			name: "空能力列表合法",
			c:    ServerCapabilities{Name: "gopls"},
			want: nil,
		},
		{
			name: "CJK 名称合法",
			c:    ServerCapabilities{Name: "中文服务器", Caps: []Capability{"悬停"}},
			want: nil,
		},
		{
			name: "空名称",
			c:    ServerCapabilities{Name: "", Caps: []Capability{CapabilityHover}},
			want: ErrEmptyCapabilitiesName,
		},
		{
			name: "纯空白名称",
			c:    ServerCapabilities{Name: "   "},
			want: ErrEmptyCapabilitiesName,
		},
		{
			name: "重复能力",
			c:    ServerCapabilities{Name: "x", Caps: []Capability{CapabilityHover, CapabilityRename, CapabilityHover}},
			want: ErrDuplicateCapability,
		},
		{
			name: "CJK 名称下重复能力",
			c:    ServerCapabilities{Name: "中文服务器", Caps: []Capability{"定义", "定义"}},
			want: ErrDuplicateCapability,
		},
		{
			name: "空名优先于重复能力",
			c:    ServerCapabilities{Name: "", Caps: []Capability{CapabilityHover, CapabilityHover}},
			want: ErrEmptyCapabilitiesName,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capsBefore := append([]Capability(nil), tt.c.Caps...)
			err := ValidateCapabilities(tt.c)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("ValidateCapabilities() = %v, want nil", err)
				}
			} else if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateCapabilities() = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(tt.c.Caps, capsBefore) {
				t.Fatalf("ValidateCapabilities mutated input: %v, want %v", tt.c.Caps, capsBefore)
			}
		})
	}
}
