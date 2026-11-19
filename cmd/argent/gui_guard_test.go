package main

import (
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/tools"
)

func TestGUIToolRegistryIsMarkedGuarded(t *testing.T) {
	registry := tools.NewWithLimit(t.TempDir(), true, 0)
	runner := guardedToolRunner{registry}
	marker, ok := any(runner).(interface{ Guarded() bool })
	if !ok || !marker.Guarded() {
		t.Fatal("GUI tool runner must report Guarded so the loop does not ask for approval twice")
	}
	if len(runner.Specs()) == 0 {
		t.Fatal("guarded wrapper must still expose the registry's tools")
	}
}
