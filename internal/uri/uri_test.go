package uri

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestParse(t *testing.T) {
	cases := []struct {
		raw    string
		scheme string
		path   string
		ok     bool
	}{
		{"argent://docs/USER_GUIDE.md", "docs", "USER_GUIDE.md", true},
		{"argent://skill/foo", "skill", "foo", true},
		{"argent://docs/", "docs", "/", true},
		{"argent://docs/a/b.md", "docs", "a/b.md", true},
		{"argent://docs/../secret", "", "", false},
		{"https://evil.com/x", "", "", false},
		{"argent://", "", "", false},
		{"  argent://docs/x.md  ", "docs", "x.md", true},
	}
	for _, tc := range cases {
		s, p, err := Parse(tc.raw)
		if (err == nil) != tc.ok {
			t.Errorf("Parse(%q) ok=%v err=%v", tc.raw, err == nil, err)
			continue
		}
		if tc.ok && (s != tc.scheme || p != tc.path) {
			t.Errorf("Parse(%q) = (%q,%q), want (%q,%q)", tc.raw, s, p, tc.scheme, tc.path)
		}
	}
}

func TestRegisterAndResolve(t *testing.T) {
	r := New()
	h := HandlerFunc(func(p string) (Resource, error) {
		if p == "missing" {
			return Resource{}, ErrNotFound
		}
		return Resource{Content: "content-of-" + p}, nil
	})
	if err := r.Register("test", h); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("test", h); err == nil {
		t.Fatal("duplicate registration must fail")
	}
	res, err := r.Resolve("argent://test/foo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "content-of-foo" || res.Scheme != "test" || res.Path != "foo" {
		t.Fatalf("res = %+v", res)
	}
	if _, err := r.Resolve("argent://test/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := r.Resolve("argent://unknown/x"); !errors.Is(err, ErrUnknownScheme) {
		t.Fatalf("unknown err = %v", err)
	}
}

func TestSchemes(t *testing.T) {
	r := New()
	r.Register("zeta", HandlerFunc(func(string) (Resource, error) { return Resource{}, nil }))
	r.Register("alpha", HandlerFunc(func(string) (Resource, error) { return Resource{}, nil }))
	got := r.Schemes()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("Schemes = %v", got)
	}
}

func TestDocsHandlerIndexRoundTrip(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md":        {Data: []byte("# Readme\nhello\n")},
		"docs/GUIDE.md":    {Data: []byte("# Guide\nworld\n")},
		"docs/nested/X.md": {Data: []byte("# X\n")},
		"docs/ignore.txt":  {Data: []byte("not markdown")},
	}
	h, err := NewDocsHandler(fsys)
	if err != nil {
		t.Fatal(err)
	}
	names := h.List()
	if len(names) != 3 {
		t.Fatalf("docs list = %v, want 3 md files", names)
	}
	res, err := h.Resolve("README.md")
	if err != nil || res.Content != "# Readme\nhello\n" {
		t.Fatalf("resolve README = %+v %v", res, err)
	}
	// 省略后缀
	res, err = h.Resolve("docs/GUIDE")
	if err != nil || res.Content != "# Guide\nworld\n" {
		t.Fatalf("resolve GUIDE = %+v %v", res, err)
	}
	// 惰性解压缓存：第二次命中缓存路径
	res2, err := h.Resolve("docs/GUIDE.md")
	if err != nil || res2.Content != res.Content {
		t.Fatalf("cached resolve differs: %+v %v", res2, err)
	}
	if _, err := h.Resolve("nope.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing doc err = %v", err)
	}
}

func TestDocsIndexEncodeDecode(t *testing.T) {
	fsys := fstest.MapFS{"A.md": {Data: []byte("alpha")}, "B.md": {Data: []byte("beta")}}
	h, err := NewDocsHandler(fsys)
	if err != nil {
		t.Fatal(err)
	}
	text, err := h.EncodeIndex()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := NewDocsHandlerFromIndex(text)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h2.Resolve("B.md")
	if err != nil || res.Content != "beta" {
		t.Fatalf("restored index resolve = %+v %v", res, err)
	}
}

func TestIsURI(t *testing.T) {
	if !IsURI("argent://docs/x") {
		t.Fatal("valid uri rejected")
	}
	if IsURI("plain text") || IsURI("http://x") {
		t.Fatal("non-uri accepted")
	}
}
