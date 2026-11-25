package session

import (
	"testing"
)

func TestScanModelRoutesLastWins(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("mr")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendModelRoute("auto", "simple", "cheap"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendModelRoute("auto", "simple", "cheaper"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendModelRoute("auto", "complex", "strong"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendModelRoute("auto", "simple", ""); err != nil {
		t.Fatal(err)
	}
	routes, err := sess.ModelRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v", routes)
	}
	if routes[0].Model != "cheaper" || routes[1].Model != "strong" {
		t.Fatalf("last-wins failed: %+v", routes)
	}
	// 新 kind 必须已被登记，否则读取时 fail-closed 报错。
	if msgs := sess.Messages(); msgs == nil {
		t.Fatal("model_route records broke message projection")
	}
}
