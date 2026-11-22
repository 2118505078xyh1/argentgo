package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestIntegrationBrowser 连本机 CDP（端口 9333，测试前需启动 Chrome），
// 验证 导航→提取 refs→输入→点击→eval→截图 的完整闭环。
// 没有 CDP 端点时跳过，不影响常规测试。
func TestIntegrationBrowser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>测试页</title></head><body>
<h1>Hello Browser</h1>
<input id="q" type="text" placeholder="输入名字">
<button id="go" onclick="document.getElementById('out').textContent='Hi '+document.getElementById('q').value">提交</button>
<div id="out">结果区</div>
<a href="/next">下一页</a>
</body></html>`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := Open(ctx, OpenOpts{Connect: "http://127.0.0.1:9333"})
	if err != nil {
		t.Skipf("no CDP endpoint at 9333: %v", err)
	}
	defer b.Close()

	status, title, err := b.Navigate(ctx, srv.URL)
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if status != 200 || title != "测试页" {
		t.Fatalf("Navigate status/title = %d/%q", status, title)
	}

	snap, err := b.Extract(ctx, LevelContent)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	t.Logf("snapshot refs: %d\n%s", len(snap.Elements), snap.Text)
	if len(snap.Elements) < 3 {
		t.Fatalf("expected >=3 interactive elements, got %d", len(snap.Elements))
	}

	// 找到 input 与 button 的 ref。
	var inputRef, btnRef string
	for _, e := range snap.Elements {
		if e.Tag == "input" {
			inputRef = e.Ref
		}
		if e.Tag == "button" {
			btnRef = e.Ref
		}
	}
	if inputRef == "" || btnRef == "" {
		t.Fatalf("missing refs: input=%q button=%q", inputRef, btnRef)
	}

	if err := b.Type(ctx, inputRef, "张伟"); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if err := b.Click(ctx, btnRef); err != nil {
		t.Fatalf("Click: %v", err)
	}
	out, err := b.Eval(ctx, "document.getElementById('out').textContent")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if !strings.Contains(out, "张伟") {
		t.Fatalf("expected out to contain 张伟, got %q", out)
	}
	t.Logf("after click: %s", out)

	png, err := b.Screenshot(ctx, false)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(png) < 100 {
		t.Fatalf("screenshot too small: %d bytes", len(png))
	}
	t.Logf("screenshot: %d base64 bytes", len(png))
}
