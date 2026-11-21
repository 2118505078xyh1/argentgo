package pluginapi

import (
	"fmt"
	"sync"
	"testing"
)

func TestRegisterAndFactoriesOrderByOrderThenName(t *testing.T) {
	var r Registry[int]
	// 故意用乱序注册，验证输出按 (order, name) 稳定排序。
	if err := r.Register("charlie", 2, func() int { return 3 }); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("alpha", 1, func() int { return 1 }); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("bravo", 1, func() int { return 2 }); err != nil {
		t.Fatal(err)
	}
	got := r.Names()
	want := []string{"alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
	fs := r.Factories()
	if len(fs) != 3 || fs[0]() != 1 || fs[1]() != 2 || fs[2]() != 3 {
		t.Fatalf("Factories() order wrong: %v", fs)
	}
}

func TestRegisterRejectsDuplicateAndEmpty(t *testing.T) {
	var r Registry[int]
	if err := r.Register("", 0, func() int { return 0 }); err == nil {
		t.Fatal("empty name should fail")
	}
	if err := r.Register("x", 0, nil); err == nil {
		t.Fatal("nil factory should fail")
	}
	if err := r.Register("x", 0, func() int { return 0 }); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("x", 5, func() int { return 1 }); err == nil {
		t.Fatal("duplicate name should fail regardless of order")
	}
	if r.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", r.Len())
	}
}

func TestMustRegisterPanicsOnDuplicate(t *testing.T) {
	var r Registry[int]
	r.MustRegister("x", 0, func() int { return 0 })
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister should panic on duplicate")
		}
	}()
	r.MustRegister("x", 1, func() int { return 1 })
}

func TestZeroValueRegistry(t *testing.T) {
	var r Registry[string]
	if r.Len() != 0 || r.Names() != nil || r.Factories() != nil {
		t.Fatal("zero-value registry should be empty")
	}
}

func TestFactoriesReturnsIndependentSnapshot(t *testing.T) {
	var r Registry[int]
	r.MustRegister("a", 0, func() int { return 1 })
	fs := r.Factories()
	fs[0] = func() int { return 999 } // 修改快照不影响注册表
	if r.Factories()[0]() != 1 {
		t.Fatal("Factories() snapshot should be independent")
	}
}

func TestFactoriesAreConstructorsNotInstances(t *testing.T) {
	// 构造器语义：每次调用返回独立实例，注册一个 "实例序号" 计数器。
	counter := 0
	var r Registry[*int]
	r.MustRegister("counter", 0, func() *int {
		counter++
		v := counter
		return &v
	})
	fs := r.Factories()
	a, b := fs[0](), fs[0]()
	if a == b || *a != 1 || *b != 2 {
		t.Fatalf("factories should produce fresh instances, got %v %v", *a, *b)
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	var r Registry[int]
	var wg sync.WaitGroup
	// 写：不同 name 并发注册。
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("tool-%02d", i)
			_ = r.Register(name, i, func() int { return i })
		}(i)
	}
	// 读：并发遍历快照。
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Names()
			_ = r.Factories()
			_ = r.Len()
		}()
	}
	wg.Wait()
	if r.Len() != 50 {
		t.Fatalf("Len() = %d, want 50", r.Len())
	}
	names := r.Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("Names() not sorted: %v", names)
		}
	}
}
