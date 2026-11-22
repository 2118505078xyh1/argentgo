package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestMemoryConsolidateEnabled(t *testing.T) {
	if memoryConsolidateEnabled(nil) {
		t.Fatal("nil env should be off (default closed)")
	}
	if !memoryConsolidateEnabled(func(string) string { return "1" }) {
		t.Fatal("=1 should enable")
	}
	if memoryConsolidateEnabled(func(string) string { return "0" }) {
		t.Fatal("=0 should disable")
	}
}

func TestWireMemoryConsolidateOptIn(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}
	// 默认关：不注册。
	wireMemoryConsolidate(cli.IO{}, set)
	for _, name := range r.Names() {
		if name == "memory_consolidate" {
			t.Fatal("should not register by default")
		}
	}
	// =1：注册且幂等。
	env := cli.IO{Env: func(string) string { return "1" }}
	wireMemoryConsolidate(env, set)
	wireMemoryConsolidate(env, set)
	count := 0
	for _, name := range r.Names() {
		if name == "memory_consolidate" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("registered %d times", count)
	}
}
