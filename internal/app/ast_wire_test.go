package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestAstEditEnabled(t *testing.T) {
	if !astEditEnabled(nil) || !astEditEnabled(func(string) string { return "1" }) {
		t.Fatal("default should be enabled")
	}
	if astEditEnabled(func(string) string { return "0" }) {
		t.Fatal("=0 should disable")
	}
}

func TestWireAstEdit(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}
	wireAstEdit(cli.IO{}, set)
	wireAstEdit(cli.IO{}, set) // 幂等
	count := 0
	for _, name := range r.Names() {
		if name == "ast_edit" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ast_edit registered %d times", count)
	}

	r2 := tools.New(t.TempDir(), true)
	set2 := &toolset{base: &multiRootTools{regs: []*tools.Registry{r2}}}
	wireAstEdit(cli.IO{Env: func(string) string { return "0" }}, set2)
	for _, name := range r2.Names() {
		if name == "ast_edit" {
			t.Fatal("ast_edit registered despite disable")
		}
	}
}
