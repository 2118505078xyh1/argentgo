package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
)

func TestInlayKindName(t *testing.T) {
	cases := map[int]string{1: "type", 2: "parameter", 0: "", 99: ""}
	for k, want := range cases {
		if got := inlayKindName(k); got != want {
			t.Errorf("inlayKindName(%d) = %q, want %q", k, got, want)
		}
	}
}

// TestDecodeSemanticTokens 验证 LSP 增量 5 元组解码为绝对坐标的算法。
// 这是 adapter.SemanticTokens 的核心逻辑，抽成纯函数便于离线测试。
func TestDecodeSemanticTokens(t *testing.T) {
	// 两个 token：
	//  t1: dl=2, ds=3, len=4, type=5, mods=6  → line=2,char=3
	//  t2: dl=0, ds=2, len=1, type=7, mods=0  → 同一行,char=3+2=5
	data := []uint32{2, 3, 4, 5, 6, 0, 2, 1, 7, 0}
	got := decodeSemanticTokenData(data)
	if len(got) != 2 {
		t.Fatalf("decoded %d tokens, want 2", len(got))
	}
	if got[0].Line != 2 || got[0].Char != 3 || got[0].Length != 4 || got[0].TokenType != 5 || got[0].Modifiers != 6 {
		t.Fatalf("token[0] = %+v", got[0])
	}
	if got[1].Line != 2 || got[1].Char != 5 || got[1].TokenType != 7 {
		t.Fatalf("token[1] = %+v", got[1])
	}
	// 跨行时 char 重置为绝对列。
	data2 := []uint32{1, 0, 1, 0, 0, 1, 9, 1, 0, 0} // t1 line1 char0; t2 line2 char9
	got2 := decodeSemanticTokenData(data2)
	if got2[1].Line != 2 || got2[1].Char != 9 {
		t.Fatalf("cross-line token[1] = %+v", got2[1])
	}
	if len(decodeSemanticTokenData([]uint32{1, 2, 3})) != 0 {
		t.Fatal("incomplete tuple should be skipped")
	}
	// 编译期引用 lsp 包，确保测试与真实类型形状同步（Data 是 []uint32）。
	var _ = lsp.SemanticTokens{Data: data}
}
