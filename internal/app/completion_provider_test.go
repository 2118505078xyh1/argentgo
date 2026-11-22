package app

import (
	"testing"
)

// TestCompletionProviderLearnAndComplete 验证 app adapter 把训练文本喂给引擎后，
// Autocomplete 返回被适配的（server 类型）建议。
func TestCompletionProviderLearnAndComplete(t *testing.T) {
	p := NewCompletionProvider(4)
	corpus := "func main() { fmt.Println(\"a\") }\n" +
		"func other() { fmt.Println(\"b\") }\n" +
		"func third() { fmt.Printf(\"c\") }\n"
	p.Learn(corpus)

	sugs := p.Autocomplete("fmt.", 5)
	if len(sugs) == 0 {
		t.Fatal("expected suggestions after learning")
	}
	foundPrintln := false
	for _, s := range sugs {
		if s.Token == "Println" && s.Score > 0 {
			foundPrintln = true
		}
	}
	if !foundPrintln {
		t.Fatalf("Println not suggested: %+v", sugs)
	}
}

// TestCompletionProviderEmpty 验证未学习时返回空切片（非 nil），
// 使 RPC 能稳定序列化为 []。
func TestCompletionProviderEmpty(t *testing.T) {
	p := NewCompletionProvider(4)
	sugs := p.Autocomplete("anything", 5)
	if sugs == nil {
		t.Fatal("nil suggestions; want empty non-nil slice")
	}
	if len(sugs) != 0 {
		t.Fatalf("empty engine should yield no suggestions: %+v", sugs)
	}
}
