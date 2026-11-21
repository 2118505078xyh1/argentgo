package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// scopeTestErr 是释放函数返回的哨兵错误，供 errors.Is 验证错误聚合。
var scopeTestErr = errors.New("scope test failure")

// scopeReleaseKind 描述一个释放函数的行为，用于表驱动的错误/panic 隔离测试。
type scopeReleaseKind int

const (
	scopeReleaseOK scopeReleaseKind = iota
	scopeReleaseErr
	scopeReleasePanic
)

// reversed 返回切片的逆序副本，不修改原切片。
func reversed(in []string) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

func TestScopeDisposeLIFO(t *testing.T) {
	tests := []struct {
		name  string
		items []string
	}{
		{name: "empty", items: nil},
		{name: "single", items: []string{"a"}},
		{name: "three", items: []string{"a", "b", "c"}},
		{name: "five", items: []string{"a", "b", "c", "d", "e"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScope(nil)
			var got []string
			for _, item := range tt.items {
				item := item
				s.Track(item, func() error {
					got = append(got, item)
					return nil
				})
			}
			if err := s.Dispose(); err != nil {
				t.Fatalf("Dispose() = %v, want nil", err)
			}
			want := reversed(tt.items)
			if tt.items == nil {
				want = nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("release order = %v, want %v", got, want)
			}
		})
	}
}

func TestScopeDisposeIdempotent(t *testing.T) {
	s := NewScope(nil)
	calls := 0
	s.Track("once", func() error {
		calls++
		return nil
	})

	for i := 0; i < 3; i++ {
		if err := s.Dispose(); err != nil {
			t.Fatalf("Dispose #%d = %v, want nil", i+1, err)
		}
	}
	if calls != 1 {
		t.Fatalf("release calls = %d, want 1", calls)
	}

	// 已释放作用域无法登记；Track 立即执行释放，不进入已作废的 LIFO 列表。
	s.Track("late", func() error {
		calls++
		return nil
	})
	if calls != 2 {
		t.Fatalf("calls after late Track = %d, want 2", calls)
	}
	if err := s.Dispose(); err != nil {
		t.Fatalf("Dispose after late Track = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("calls after repeated Dispose = %d, want 2", calls)
	}
}

func TestScopeDisposeIsolatesFailures(t *testing.T) {
	tests := []struct {
		name         string
		kinds        []scopeReleaseKind
		wantSentinel bool
	}{
		{name: "ok-error-panic", kinds: []scopeReleaseKind{scopeReleaseOK, scopeReleaseErr, scopeReleasePanic}, wantSentinel: true},
		{name: "panic-first", kinds: []scopeReleaseKind{scopeReleasePanic, scopeReleaseOK, scopeReleaseErr}, wantSentinel: true},
		{name: "all-panic", kinds: []scopeReleaseKind{scopeReleasePanic, scopeReleasePanic}},
		{name: "error-only", kinds: []scopeReleaseKind{scopeReleaseErr}, wantSentinel: true},
		{name: "all-ok", kinds: []scopeReleaseKind{scopeReleaseOK, scopeReleaseOK}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScope(nil)
			var got []int
			wantErr := false
			wantPanic := false
			for i, kind := range tt.kinds {
				i, kind := i, kind
				name := "release" + string(rune('a'+i))
				s.Track(name, func() error {
					got = append(got, i)
					switch kind {
					case scopeReleaseErr:
						return scopeTestErr
					case scopeReleasePanic:
						panic("release exploded")
					default:
						return nil
					}
				})
				if kind != scopeReleaseOK {
					wantErr = true
				}
				if kind == scopeReleasePanic {
					wantPanic = true
				}
			}

			err := s.Dispose()
			// 所有释放函数都执行了，且仍按 LIFO 顺序；单个失败不阻断其余。
			wantOrder := make([]int, len(tt.kinds))
			for i := range tt.kinds {
				wantOrder[i] = len(tt.kinds) - 1 - i
			}
			if !reflect.DeepEqual(got, wantOrder) {
				t.Fatalf("release order = %v, want %v", got, wantOrder)
			}
			if wantErr {
				if err == nil {
					t.Fatal("Dispose() = nil, want aggregated error")
				}
				if tt.wantSentinel && !errors.Is(err, scopeTestErr) {
					t.Fatalf("Dispose() = %v, want errors.Is(scopeTestErr)", err)
				}
				if wantPanic && !strings.Contains(err.Error(), "panicked") {
					t.Fatalf("Dispose() = %v, want panic error", err)
				}
			} else if err != nil {
				t.Fatalf("Dispose() = %v, want nil", err)
			}
		})
	}
}

func TestScopeDisposeChildrenBeforeParent(t *testing.T) {
	tests := []struct {
		name      string
		childName []string
		want      []string
	}{
		{name: "chain", childName: []string{"root", "child", "grandchild"}, want: []string{"grandchild", "child", "root"}},
		{name: "siblings", childName: []string{"root", "child1", "child2"}, want: []string{"child2", "child1", "root"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := NewScope(nil)
			var got []string
			root.Track("root", func() error { got = append(got, "root"); return nil })

			parent := root
			for _, name := range tt.childName[1:] {
				child := parent.Child()
				child.Track(name, func() error { got = append(got, name); return nil })
				parent = child
			}

			if err := root.Dispose(); err != nil {
				t.Fatalf("Dispose() = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("release order = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScopeDisposeChildThenParent(t *testing.T) {
	root := NewScope(nil)
	var got []string
	root.Track("root", func() error { got = append(got, "root"); return nil })

	child := root.Child()
	child.Track("child", func() error { got = append(got, "child"); return nil })
	grandchild := child.Child()
	grandchild.Track("grandchild", func() error { got = append(got, "grandchild"); return nil })

	if err := child.Dispose(); err != nil {
		t.Fatalf("child.Dispose() = %v, want nil", err)
	}
	if want := []string{"grandchild", "child"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after child.Dispose() = %v, want %v", got, want)
	}
	if err := root.Dispose(); err != nil {
		t.Fatalf("root.Dispose() = %v, want nil", err)
	}
	if want := []string{"grandchild", "child", "root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after root.Dispose() = %v, want %v", got, want)
	}
}

func TestScopeOwnRollback(t *testing.T) {
	tests := []struct {
		name         string
		buildErr     error
		partial      bool
		wantOwnErr   error
		wantCalls    []string
		wantDisposed bool
	}{
		{
			name:      "success tracks release",
			buildErr:  nil,
			wantCalls: []string{"own", "second", "first"},
		},
		{
			name:         "failure rolls back",
			buildErr:     scopeTestErr,
			wantOwnErr:   scopeTestErr,
			wantCalls:    []string{"second", "first"},
			wantDisposed: true,
		},
		{
			name:         "failure with partial release",
			buildErr:     scopeTestErr,
			partial:      true,
			wantOwnErr:   scopeTestErr,
			wantCalls:    []string{"partial", "second", "first"},
			wantDisposed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScope(nil)
			var got []string
			s.Track("first", func() error { got = append(got, "first"); return nil })
			s.Track("second", func() error { got = append(got, "second"); return nil })

			err := s.Own(func() (func() error, error) {
				if tt.buildErr != nil {
					if tt.partial {
						return func() error { got = append(got, "partial"); return nil }, tt.buildErr
					}
					return nil, tt.buildErr
				}
				return func() error { got = append(got, "own"); return nil }, nil
			})
			if !errors.Is(err, tt.wantOwnErr) {
				t.Fatalf("Own() = %v, want errors.Is(%v)", err, tt.wantOwnErr)
			}

			if tt.wantDisposed {
				// 回滚已执行，再次 Dispose 不应重复释放。
				if err := s.Dispose(); err != nil {
					t.Fatalf("Dispose after rollback = %v, want nil", err)
				}
			} else {
				if err := s.Dispose(); err != nil {
					t.Fatalf("Dispose() = %v, want nil", err)
				}
			}
			if !reflect.DeepEqual(got, tt.wantCalls) {
				t.Fatalf("release order = %v, want %v", got, tt.wantCalls)
			}
		})
	}
}

func TestScopeOwnNilBuilder(t *testing.T) {
	s := NewScope(nil)
	if err := s.Own(nil); err != nil {
		t.Fatalf("Own(nil) = %v, want nil", err)
	}
	if err := s.Dispose(); err != nil {
		t.Fatalf("Dispose() = %v, want nil", err)
	}
}

func TestScopeConcurrentSafety(t *testing.T) {
	tests := []struct {
		name              string
		concurrentDispose bool
	}{
		{name: "concurrent track then dispose"},
		{name: "concurrent dispose", concurrentDispose: true},
	}
	const n = 64
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScope(nil)
			var mu sync.Mutex
			counts := make(map[int]int)

			if tt.concurrentDispose {
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					i := i
					s.Track(string(rune('a'+i%26)), func() error {
						mu.Lock()
						counts[i]++
						mu.Unlock()
						return nil
					})
					wg.Add(1)
					go func() {
						defer wg.Done()
						if err := s.Dispose(); err != nil {
							t.Errorf("Dispose() = %v, want nil", err)
						}
					}()
				}
				wg.Wait()
			} else {
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					i := i
					wg.Add(1)
					go func() {
						defer wg.Done()
						s.Track("concurrent", func() error {
							mu.Lock()
							counts[i]++
							mu.Unlock()
							return nil
						})
					}()
				}
				wg.Wait()
				if err := s.Dispose(); err != nil {
					t.Fatalf("Dispose() = %v, want nil", err)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if len(counts) != n {
				t.Fatalf("released items = %d, want %d", len(counts), n)
			}
			for i := 0; i < n; i++ {
				if counts[i] != 1 {
					t.Fatalf("item %d released %d times, want 1", i, counts[i])
				}
			}
		})
	}
}

func TestScopeContextRoundTrip(t *testing.T) {
	s := NewScope(nil)
	child := s.Child()
	tests := []struct {
		name string
		ctx  context.Context
		want *Scope
	}{
		{name: "attached", ctx: WithScope(context.Background(), s), want: s},
		{name: "nested overrides", ctx: WithScope(WithScope(context.Background(), s), child), want: child},
		{name: "missing", ctx: context.Background(), want: nil},
		{name: "nil scope", ctx: WithScope(context.Background(), nil), want: nil},
		{name: "nil ctx", ctx: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScopeOf(tt.ctx); got != tt.want {
				t.Fatalf("ScopeOf() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScopeIsolation(t *testing.T) {
	a := NewScope(nil)
	b := NewScope(nil)
	var gotA, gotB []string
	a.Track("a", func() error { gotA = append(gotA, "a"); return nil })
	b.Track("b", func() error { gotB = append(gotB, "b"); return nil })

	if err := a.Dispose(); err != nil {
		t.Fatalf("a.Dispose() = %v, want nil", err)
	}
	if !reflect.DeepEqual(gotA, []string{"a"}) {
		t.Fatalf("gotA = %v, want [a]", gotA)
	}
	if len(gotB) != 0 {
		t.Fatalf("gotB = %v, want empty", gotB)
	}

	if err := b.Dispose(); err != nil {
		t.Fatalf("b.Dispose() = %v, want nil", err)
	}
	if !reflect.DeepEqual(gotB, []string{"b"}) {
		t.Fatalf("gotB = %v, want [b]", gotB)
	}
}
