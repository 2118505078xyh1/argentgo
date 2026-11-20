package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// descCapFakeTool 是一个描述由调用方提供的 Tool。
type descCapFakeTool struct {
	name string
	desc string
}

func (t descCapFakeTool) Name() string        { return t.name }
func (t descCapFakeTool) Description() string { return t.desc }
func (descCapFakeTool) Schema() string        { return `{"type":"object","properties":{}}` }
func (descCapFakeTool) Mutating() bool        { return false }
func (descCapFakeTool) Run(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}

func TestClipDescriptionLeavesShortStringsUnchanged(t *testing.T) {
	for _, s := range []string{"", "short", strings.Repeat("x", maxToolDescLen)} {
		if got := clipDescription(s); got != s {
			t.Fatalf("clipDescription(len %d) = len %d, want unchanged", len(s), len(got))
		}
	}
}

func TestClipDescriptionClipsLongASCII(t *testing.T) {
	s := strings.Repeat("x", maxToolDescLen+100)
	got := clipDescription(s)
	if !utf8.ValidString(got) {
		t.Fatal("clipped description is not valid UTF-8")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped description has no ellipsis suffix (len %d)", len(got))
	}
	if n := utf8.RuneCountInString(got); n != maxToolDescLen {
		t.Fatalf("clipped description has %d runes, want %d", n, maxToolDescLen)
	}
	if want := strings.Repeat("x", maxToolDescLen-1) + "…"; got != want {
		t.Fatal("clipped description does not keep the expected head")
	}
}

func TestClipDescriptionUTF8Safe(t *testing.T) {
	// 截断点落在 rune 之间，因此按字节切片会破坏它们。
	s := strings.Repeat("中😀", maxToolDescLen)
	got := clipDescription(s)
	if !utf8.ValidString(got) {
		t.Fatal("clipped description is not valid UTF-8")
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatal("clipped description contains a replacement rune")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("clipped description does not end with an ellipsis")
	}
	if n := utf8.RuneCountInString(got); n != maxToolDescLen {
		t.Fatalf("clipped description has %d runes, want %d", n, maxToolDescLen)
	}
}

func TestSpecsClampsLongToolDescription(t *testing.T) {
	long := strings.Repeat("中😀", maxToolDescLen)
	const name = "desc_cap_fake"
	r := New(t.TempDir(), false)
	r.tools[name] = descCapFakeTool{name: name, desc: long}
	r.order = append(r.order, name)

	var got string
	for _, spec := range r.Specs() {
		if spec.Function.Name == name {
			got = spec.Function.Description
		}
	}
	if got == "" {
		t.Fatal("fake tool spec missing from Specs()")
	}
	if want := clipDescription(long); got != want {
		t.Fatal("Specs did not clamp the long description")
	}
	if n := utf8.RuneCountInString(got); n != maxToolDescLen {
		t.Fatalf("Specs description has %d runes, want %d", n, maxToolDescLen)
	}
}
