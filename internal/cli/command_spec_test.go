package cli

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// assertSpecEqual 比较查找结果是否与期望规格逐字段一致。
func assertSpecEqual(t *testing.T, got CommandSpec, want CommandSpec) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spec = %+v, want %+v", got, want)
	}
}

func TestCommandRegistryRegisterAndLookup(t *testing.T) {
	// 表驱动：主名与别名都作为查找键，未注册的名称查不到。
	registered := []CommandSpec{
		{Name: "serve", Aliases: []string{"server", "run"}, Order: 1, Summary: "启动服务"},
		{Name: "tui", Aliases: []string{"ui"}, Order: 2, Summary: "启动界面"},
		{Name: "config", Order: 3, Summary: "查看配置"}, // 无别名
	}
	var r CommandRegistry
	for _, spec := range registered {
		if err := r.Register(spec); err != nil {
			t.Fatalf("Register(%q) error: %v", spec.Name, err)
		}
	}

	cases := []struct {
		query string
		want  CommandSpec
		ok    bool
	}{
		{"serve", registered[0], true},
		{"server", registered[0], true}, // 别名
		{"run", registered[0], true},    // 别名
		{"tui", registered[1], true},
		{"ui", registered[1], true},
		{"config", registered[2], true},
		{"unknown", CommandSpec{}, false},
		{"", CommandSpec{}, false}, // 空名称未注册
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got, ok := r.Lookup(tc.query)
			if ok != tc.ok {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tc.query, ok, tc.ok)
			}
			assertSpecEqual(t, got, tc.want)
		})
	}
}

func TestCommandRegistryDuplicateErrors(t *testing.T) {
	// 表驱动：主名/别名的各种冲突路径，以及同一条规格内部的重名；
	// 每次冲突都必须返回哨兵错误，且不改动注册表。
	initial := CommandSpec{Name: "tui", Aliases: []string{"dashboard"}}
	cases := []struct {
		name       string
		incoming   CommandSpec
		wantDupKey string
	}{
		{
			name:       "duplicate primary name",
			incoming:   CommandSpec{Name: "tui"},
			wantDupKey: "tui",
		},
		{
			name:       "name collides with existing alias",
			incoming:   CommandSpec{Name: "dashboard"},
			wantDupKey: "dashboard",
		},
		{
			name:       "alias collides with existing name",
			incoming:   CommandSpec{Name: "new", Aliases: []string{"tui"}},
			wantDupKey: "tui",
		},
		{
			name:       "alias collides with existing alias",
			incoming:   CommandSpec{Name: "new", Aliases: []string{"dashboard"}},
			wantDupKey: "dashboard",
		},
		{
			name:       "name equals own alias",
			incoming:   CommandSpec{Name: "self", Aliases: []string{"self"}},
			wantDupKey: "self",
		},
		{
			name:       "duplicate alias within spec",
			incoming:   CommandSpec{Name: "new", Aliases: []string{"dup", "dup"}},
			wantDupKey: "dup",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r CommandRegistry
			if err := r.Register(initial); err != nil {
				t.Fatalf("Register initial error: %v", err)
			}
			before := r.List()

			err := r.Register(tc.incoming)
			if err == nil {
				t.Fatal("Register conflict error = nil, want non-nil")
			}
			if !errors.Is(err, ErrDuplicateCommand) {
				t.Fatalf("Register conflict error = %v, want errors.Is(err, ErrDuplicateCommand)", err)
			}
			if !strings.Contains(err.Error(), tc.wantDupKey) {
				t.Fatalf("Register conflict error %q does not mention %q", err.Error(), tc.wantDupKey)
			}
			if after := r.List(); !reflect.DeepEqual(after, before) {
				t.Fatalf("conflicting Register mutated registry: got %+v, want %+v", after, before)
			}
		})
	}
}

func TestCommandRegistryListOrdering(t *testing.T) {
	// 表驱动：先按 Order 升序，Order 相同再按 Name 字节序；多次调用顺序一致。
	input := []CommandSpec{
		{Name: "zeta", Order: 2},
		{Name: "alpha", Order: 2},
		{Name: "middle", Order: 1},
		{Name: "beta", Order: 0},
		{Name: "aaa", Order: 0},
	}
	wantNames := []string{"aaa", "beta", "middle", "alpha", "zeta"}

	var r CommandRegistry
	for _, spec := range input {
		if err := r.Register(spec); err != nil {
			t.Fatalf("Register(%q) error: %v", spec.Name, err)
		}
	}

	first := r.List()
	second := r.List()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("List order not stable across calls: %+v vs %+v", first, second)
	}
	gotNames := make([]string, 0, len(first))
	for _, spec := range first {
		gotNames = append(gotNames, spec.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("List names = %v, want %v", gotNames, wantNames)
	}
}

func TestCommandRegistryListIsIndependentCopy(t *testing.T) {
	// List 与 Lookup 返回的规格与内部存储不共享底层数组；调用方修改返回值、
	// 或修改注册时传入的切片，都不能污染注册表。
	aliases := []string{"alias-a"}
	var r CommandRegistry
	if err := r.Register(CommandSpec{Name: "a", Aliases: aliases}); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	if err := r.Register(CommandSpec{Name: "b"}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 修改注册时传入的切片，不影响已注册内容。
	aliases[0] = "tampered"
	got, ok := r.Lookup("alias-a")
	if !ok || got.Aliases[0] != "alias-a" {
		t.Fatalf("input mutation leaked into registry: got %+v, ok=%v", got, ok)
	}

	// 修改 List 返回的切片元素与别名，不影响注册表。
	out := r.List()
	idx := -1
	for i, spec := range out {
		if len(spec.Aliases) > 0 && spec.Aliases[0] == "alias-a" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("could not locate spec with alias-a in List result")
	}
	out[idx].Aliases[0] = "tampered-list"
	out[idx].Name = "tampered-list"
	out[0] = CommandSpec{Name: "overwritten"} // 直接替换槽位同样不应影响注册表
	if got, ok := r.Lookup("a"); !ok || got.Name != "a" || got.Aliases[0] != "alias-a" {
		t.Fatalf("List result mutation leaked into registry: got %+v, ok=%v", got, ok)
	}

	// 修改 Lookup 结果也不影响注册表。
	got, _ = r.Lookup("a")
	got.Aliases[0] = "tampered-lookup"
	if again, _ := r.Lookup("a"); again.Aliases[0] != "alias-a" {
		t.Fatalf("Lookup result mutation leaked into registry: %+v", again)
	}
}

func TestCommandRegistryZeroValue(t *testing.T) {
	// 零值注册表：查不到、列表为空，且可直接 Register。
	var r CommandRegistry
	if got, ok := r.Lookup("anything"); ok || !reflect.DeepEqual(got, CommandSpec{}) {
		t.Fatalf("zero-value Lookup = %+v, %v; want zero, false", got, ok)
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("zero-value List = %+v, want empty", got)
	}
	spec := CommandSpec{Name: "first", Aliases: []string{"one"}}
	if err := r.Register(spec); err != nil {
		t.Fatalf("Register on zero value error: %v", err)
	}
	assertSpecEqual(t, mustLookup(t, &r, "one"), spec)
	if got := r.List(); len(got) != 1 {
		t.Fatalf("List after Register = %+v, want 1 element", got)
	}
}

func TestCommandRegistryCJK(t *testing.T) {
	// CJK 名称与别名：多字节字符不被切断，查找、排序与摘要逐字节稳定。
	input := []CommandSpec{
		{Name: "服务器", Aliases: []string{"服务端", "服务"}, Order: 2, Summary: "启动服务"},
		{Name: "配置", Aliases: []string{"设置"}, Order: 1, Summary: "查看配置"},
	}
	var r CommandRegistry
	for _, spec := range input {
		if err := r.Register(spec); err != nil {
			t.Fatalf("Register(%q) error: %v", spec.Name, err)
		}
	}

	lookups := []struct {
		query string
		want  CommandSpec
	}{
		{"服务端", input[0]},
		{"服务", input[0]},
		{"设置", input[1]},
	}
	for _, tc := range lookups {
		t.Run(tc.query, func(t *testing.T) {
			got, ok := r.Lookup(tc.query)
			if !ok {
				t.Fatalf("Lookup(%q) ok = false, want true", tc.query)
			}
			assertSpecEqual(t, got, tc.want)
			if !utf8.ValidString(got.Name) || !utf8.ValidString(got.Summary) {
				t.Fatalf("Lookup(%q) produced invalid UTF-8: %+v", tc.query, got)
			}
		})
	}

	got := r.List()
	if len(got) != 2 || got[0].Name != "配置" || got[1].Name != "服务器" {
		t.Fatalf("List = %+v, want 配置 then 服务器 (ordered by Order)", got)
	}
}

func TestCommandRegistryConcurrentAccess(t *testing.T) {
	// 并发注册与读取不应触发数据竞争（配合 -race 验证锁保护）。
	var r CommandRegistry
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Register(CommandSpec{Name: fmt.Sprintf("cmd-%03d", i)})
		}(i)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.List()
			_, _ = r.Lookup("cmd-001")
		}()
	}
	wg.Wait()
	if got := len(r.List()); got != n {
		t.Fatalf("List length = %d, want %d", got, n)
	}
}

// mustLookup 是测试辅助函数：要求查询必须命中并返回其规格。
func mustLookup(t *testing.T, r *CommandRegistry, name string) CommandSpec {
	t.Helper()
	got, ok := r.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false, want true", name)
	}
	return got
}
