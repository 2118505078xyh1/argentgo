package lsp

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestRegistryNamesAndServersSortedByOrderThenName(t *testing.T) {
	var registry Registry
	// 故意乱序注册，验证输出按 (order, name) 稳定排序。
	entries := []struct {
		name  string
		order int
	}{
		{name: "charlie", order: 2},
		{name: "alpha", order: 1},
		{name: "bravo", order: 1},
		{name: "delta", order: 0},
	}
	for _, entry := range entries {
		server := Server{Name: entry.name, Command: "bin-" + entry.name, LanguageIDs: []string{entry.name}}
		if err := registry.RegisterOrdered(server, entry.order); err != nil {
			t.Fatalf("RegisterOrdered(%q, %d): %v", entry.name, entry.order, err)
		}
	}

	want := []string{"delta", "alpha", "bravo", "charlie"}
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	servers := registry.Servers()
	if len(servers) != len(want) {
		t.Fatalf("Servers() length = %d, want %d", len(servers), len(want))
	}
	for i, name := range want {
		if servers[i].Name != name {
			t.Fatalf("Servers()[%d].Name = %q, want %q", i, servers[i].Name, name)
		}
		if servers[i].Command != "bin-"+name {
			t.Fatalf("Servers()[%d].Command = %q, want %q", i, servers[i].Command, "bin-"+name)
		}
	}
}

func TestRegistryRejectsDuplicateAndEmptyNames(t *testing.T) {
	var registry Registry
	if err := registry.RegisterOrdered(Server{Name: "x", Command: "first", LanguageIDs: []string{"foo"}}, 0); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Server{Name: "x", Command: "second", LanguageIDs: []string{"bar"}}, 5); !errors.Is(err, ErrDuplicateServer) {
		t.Fatalf("duplicate registration error = %v, want ErrDuplicateServer", err)
	}
	if err := registry.Register(Server{Name: "   ", Command: "blank"}); !errors.Is(err, ErrEmptyServerName) {
		t.Fatalf("empty name error = %v, want ErrEmptyServerName", err)
	}
	if registry.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", registry.Len())
	}
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("Names() = %v, want [x]", got)
	}
	// 失败的重名注册不能污染语言映射。
	if _, ok := registry.Lookup("bar"); ok {
		t.Fatal("duplicate registration must not modify the language mapping")
	}
	if server, ok := registry.Lookup("foo"); !ok || server.Command != "first" {
		t.Fatalf("Lookup(foo) = %+v ok=%v, want first", server, ok)
	}
}

func TestRegistryConcurrentReads(t *testing.T) {
	var registry Registry
	const writers = 50
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("tool-%02d", i)
			_ = registry.RegisterOrdered(Server{Name: name, Command: name, LanguageIDs: []string{name}}, i)
		}(i)
	}
	// 写的同时并发读快照，验证 RWMutex 保护下的读不会 panic 或读到半更新状态。
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = registry.Names()
			_ = registry.Servers()
			_ = registry.Languages()
			_ = registry.Len()
			_, _ = registry.Lookup("tool-01")
		}()
	}
	wg.Wait()

	if registry.Len() != writers {
		t.Fatalf("Len() = %d, want %d", registry.Len(), writers)
	}
	names := registry.Names()
	if len(names) != writers {
		t.Fatalf("Names() length = %d, want %d", len(names), writers)
	}
	for i, name := range names {
		if want := fmt.Sprintf("tool-%02d", i); name != want {
			t.Fatalf("Names()[%d] = %q, want %q", i, name, want)
		}
	}
}

func TestZeroValueRegistryIsUsable(t *testing.T) {
	var registry Registry
	if registry.Len() != 0 || registry.Names() != nil || registry.Servers() != nil {
		t.Fatal("zero-value registry should be empty")
	}
	if len(registry.Languages()) != 0 {
		t.Fatalf("zero-value Languages() = %v, want empty", registry.Languages())
	}
	if _, ok := registry.Lookup("go"); ok {
		t.Fatal("zero-value registry should not resolve any language")
	}
	if err := registry.Register(Server{Name: "solo", Command: "solo-ls", LanguageIDs: []string{"solo"}}); err != nil {
		t.Fatal(err)
	}
	if server, ok := registry.ForPath("a.solo"); !ok || server.Command != "solo-ls" {
		t.Fatalf("ForPath after register = %+v ok=%v", server, ok)
	}
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"solo"}) {
		t.Fatalf("Names() = %v, want [solo]", got)
	}
}

func TestRegistrySnapshotsAreIndependent(t *testing.T) {
	registry := NewRegistry()
	servers := registry.Servers()
	names := registry.Names()
	if len(servers) == 0 {
		t.Fatal("Servers() should not be empty for the default registry")
	}
	servers[0] = Server{Name: "tampered"}
	names[0] = "tampered"
	if got := registry.Servers()[0].Name; got == "tampered" {
		t.Fatal("mutating Servers() snapshot must not affect the registry")
	}
	if got := registry.Names()[0]; got == "tampered" {
		t.Fatal("mutating Names() snapshot must not affect the registry")
	}
}

func TestDefaultRegistryOrderAndLegacyRegister(t *testing.T) {
	registry := NewRegistry()
	want := []string{"gopls", "rust-analyzer", "typescript", "pyright"}
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default Names() = %v, want %v", got, want)
	}
	// 旧式调用（不带 order）按注册先后自动排在末尾。
	registry.Register(Server{Name: "mine", Command: "my-ls", LanguageIDs: []string{"mine"}})
	want = append(want, "mine")
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() after legacy Register = %v, want %v", got, want)
	}
	if server, ok := registry.ForPath("a.mine"); !ok || server.Command != "my-ls" {
		t.Fatalf("custom server = %+v ok=%v", server, ok)
	}
}
