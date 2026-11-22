package predict

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// 训练语料：故意重复 "func main"、"fmt.Println"、"return"，使这些转移在模型中占主导。
const trainCode = `package main

import "fmt"

func main() {
	fmt.Println("hello")
	return
}
`

func newTrainedModel(t *testing.T) *Model {
	t.Helper()
	m := New(3)
	m.Train(trainCode)
	m.Train(trainCode)
	return m
}

func containsToken(got []Suggestion, token string) (float64, bool) {
	for _, s := range got {
		if s.Token == token {
			return s.Score, true
		}
	}
	return 0, false
}

func TestNewAndEmptyModel(t *testing.T) {
	m := New(3)
	if m.Order() != 3 {
		t.Fatalf("Order()=%d, want 3", m.Order())
	}
	for _, prefix := range []string{"", "func ", "unknown", "\n\t"} {
		if got := m.Predict(prefix, 5); len(got) != 0 {
			t.Fatalf("empty model Predict(%q)=%v, want empty", prefix, got)
		}
	}
	if got := m.Predict("func ", 0); got != nil {
		t.Fatalf("Predict(limit=0)=%v, want nil", got)
	}
}

func TestNewClampsOrder(t *testing.T) {
	if got := New(0).Order(); got != 1 {
		t.Fatalf("New(0).Order()=%d, want 1", got)
	}
	if got := New(-5).Order(); got != 1 {
		t.Fatalf("New(-5).Order()=%d, want 1", got)
	}
	if got := New(99).Order(); got != maxOrder {
		t.Fatalf("New(99).Order()=%d, want %d", got, maxOrder)
	}
}

func TestTokenizer(t *testing.T) {
	got := tokenize("foo := bar()\n")
	want := []string{"foo", " ", ":=", " ", "bar", "()", "\n"} // 连续标点合并为一个 token
	if len(got) != len(want) {
		t.Fatalf("tokenize(%q)=%v, want %v", "foo := bar()\n", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokenize(%q)[%d]=%q, want %q", "foo := bar()\n", i, got[i], want[i])
		}
	}
	// NUL 仅作内部键分隔符，必须被跳过。
	if got := tokenize("a\x00b"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("tokenize with NUL=%v, want [a b]", got)
	}
}

func TestPredictGoCode(t *testing.T) {
	m := newTrainedModel(t)

	tests := []struct {
		prefix string
		want   string
	}{
		{"func ", "main"},   // 空白结尾：预测 func 之后的下一个 token
		{"fmt.", "Println"}, // 标点结尾：预测 fmt. 之后的字段/方法
		{"ret", "return"},   // 标识符结尾：按前缀补全标识符
		{"RET", "return"},   // 前缀匹配大小写不敏感
	}
	for _, tt := range tests {
		got := m.Predict(tt.prefix, 5)
		if len(got) == 0 {
			t.Fatalf("Predict(%q) returned no suggestions", tt.prefix)
		}
		score, ok := containsToken(got, tt.want)
		if !ok {
			t.Fatalf("Predict(%q)=%v, want token %q", tt.prefix, got, tt.want)
		}
		if score <= 0 {
			t.Fatalf("Predict(%q) score for %q = %v, want >0", tt.prefix, tt.want, score)
		}
		if got[0].Token != tt.want {
			t.Fatalf("Predict(%q)[0]=%q(%.4f), want top %q; all=%v",
				tt.prefix, got[0].Token, got[0].Score, tt.want, got)
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].Score < got[i].Score {
				t.Fatalf("Predict(%q) not sorted desc at %d: %v", tt.prefix, i, got)
			}
		}
	}
}

func TestPredictRespectsLimit(t *testing.T) {
	m := newTrainedModel(t)
	all := m.Predict("", 100)
	if len(all) < 2 {
		t.Fatalf("Predict(\"\") returned %d suggestions, want >=2", len(all))
	}
	one := m.Predict("", 1)
	if len(one) != 1 || one[0] != all[0] {
		t.Fatalf("Predict(limit=1)=%v, want single best %v", one, all[0])
	}
}

func TestPredictEmptyAndUnknown(t *testing.T) {
	m := newTrainedModel(t)
	// 训练过的模型：空前缀/纯空白是合法输入，返回高频 token；只有“未知前缀”才返回空。
	for _, prefix := range []string{"", "   "} {
		if got := m.Predict(prefix, 5); len(got) == 0 {
			t.Fatalf("Predict(%q) returned empty, want high-frequency tokens", prefix)
		}
	}
	for _, prefix := range []string{"zzzznotpresent", "fmt.zzzz"} {
		if got := m.Predict(prefix, 5); len(got) != 0 {
			t.Fatalf("Predict(%q)=%v, want empty", prefix, got)
		}
	}
	// 空模型对任何前缀（含空前缀）都返回空。
	empty := New(3)
	if got := empty.Predict("", 5); len(got) != 0 {
		t.Fatalf("empty model Predict(\"\")=%v, want empty", got)
	}
}

func TestDeterministic(t *testing.T) {
	m1 := newTrainedModel(t)
	m2 := newTrainedModel(t)
	for _, prefix := range []string{"func ", "fmt.", "ret", "", "package "} {
		a := m1.Predict(prefix, 10)
		b := m2.Predict(prefix, 10)
		if len(a) != len(b) {
			t.Fatalf("prefix %q: lengths differ: %d vs %d", prefix, len(a), len(b))
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("prefix %q: [%d]=%v vs %v", prefix, i, a[i], b[i])
			}
		}
		// 同一模型反复查询也应一致（无隐式状态、map 迭代不影响输出）。
		if again := m1.Predict(prefix, 10); fmt.Sprint(again) != fmt.Sprint(a) {
			t.Fatalf("prefix %q: repeated Predict differs: %v vs %v", prefix, a, again)
		}
	}
}

func TestBoundedEntries(t *testing.T) {
	m := New(3)
	m.SetMaxEntries(8)
	// 构造远超 8 个不同上下文的文本。
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "word%d field%d\n", i, i)
	}
	m.Train(b.String())
	if len(m.contexts) > m.maxEntries {
		t.Fatalf("contexts=%d, want <=%d", len(m.contexts), m.maxEntries)
	}
	if _, ok := m.contexts[""]; !ok {
		t.Fatal("unigram entry was evicted")
	}
	// 淘汰后仍可正常查询、且结果确定性。
	got := m.Predict("word", 5)
	if fmt.Sprint(got) != fmt.Sprint(m.Predict("word", 5)) {
		t.Fatalf("Predict non-deterministic after eviction: %v", got)
	}
}

func TestBoundedPrefix(t *testing.T) {
	m := newTrainedModel(t)
	// 超长前缀：截断只保留尾部，尾部以 "ret" 结尾，仍应预测出 "return"。
	prefix := strings.Repeat("x ", maxPrefixBytes+100) + "ret"
	got := m.Predict(prefix, 5)
	if len(got) == 0 || got[0].Token != "return" {
		t.Fatalf("Predict(long prefix) top=%v, want return; all=%v", firstToken(got), got)
	}
}

func firstToken(s []Suggestion) string {
	if len(s) == 0 {
		return "<empty>"
	}
	return s[0].Token
}

func TestConcurrentReadSafety(t *testing.T) {
	m := newTrainedModel(t)
	want := fmt.Sprint(m.Predict("fmt.", 10))
	const goroutines = 8
	const iterations = 200
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if got := fmt.Sprint(m.Predict("fmt.", 10)); got != want {
					errCh <- fmt.Sprintf("concurrent Predict=%s, want %s", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}
