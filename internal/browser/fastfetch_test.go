package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFastFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>T</title><style>.x{color:red}</style><script>alert(1)</script></head>
<body><h1>Hello</h1><p>第一段  文字</p><p>第二段 &amp; 更多</p></body></html>`)
	}))
	defer srv.Close()

	res, err := FastFetch(context.Background(), srv.URL, FastFetchOpts{})
	if err != nil {
		t.Fatalf("FastFetch: %v", err)
	}
	if res.Status != 200 {
		t.Fatalf("status = %d", res.Status)
	}
	for _, want := range []string{"Hello", "第一段 文字", "第二段 & 更多"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text missing %q: %q", want, res.Text)
		}
	}
	for _, unwanted := range []string{"alert", "color:red", "<h1>"} {
		if strings.Contains(res.Text, unwanted) {
			t.Errorf("text should not contain %q: %q", unwanted, res.Text)
		}
	}
}

func TestFastFetchRejectsNonHTTP(t *testing.T) {
	if _, err := FastFetch(context.Background(), "ftp://x/y", FastFetchOpts{}); err == nil {
		t.Fatal("expected error for non-http URL")
	}
}
