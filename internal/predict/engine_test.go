package predict

import (
	"sync"
	"testing"
)

func TestEngineLearnAutocomplete(t *testing.T) {
	e := NewEngine(3)
	corpus := "func main() { fmt.Println(\"hello\") }\n" +
		"func main2() { fmt.Println(\"world\") }\n"
	e.Learn(corpus)

	got := e.Autocomplete("fmt.", 5)
	if len(got) == 0 {
		t.Fatal("expected suggestions after learning corpus")
	}
	// 训练语料里 fmt. 后跟 Println，应位列候选。
	found := false
	for _, s := range got {
		if s.Token == "Println" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Println not suggested for 'fmt.': %+v", got)
	}
}

func TestEngineEmpty(t *testing.T) {
	e := NewEngine(3)
	if got := e.Autocomplete("anything", 5); len(got) != 0 {
		t.Fatalf("empty engine should return no suggestions, got %+v", got)
	}
	e.Learn("")
	if got := e.Autocomplete("anything", 5); len(got) != 0 {
		t.Fatalf("empty text learn should be no-op, got %+v", got)
	}
}

// TestEngineConcurrent 验证并发 Learn/Autocomplete 不触发数据竞争
// （go test -race 下也会覆盖）。
func TestEngineConcurrent(t *testing.T) {
	e := NewEngine(4)
	corpus := "alpha beta gamma delta epsilon\nbeta gamma delta epsilon zeta\n"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				e.Learn(corpus)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = e.Autocomplete("beta ", 3)
			}
		}()
	}
	wg.Wait()
	if got := e.Autocomplete("beta ", 3); len(got) == 0 {
		t.Fatal("expected suggestions after concurrent learning")
	}
}
