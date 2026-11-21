package llm

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
)

// resetExtraHeaders 在每个测试前后清空全局注入，避免测试间互相污染。
// 这些测试不能 t.Parallel：它们共享包级注入状态。
func resetExtraHeaders(t *testing.T) {
	t.Helper()
	ClearExtraHeaders()
	t.Cleanup(ClearExtraHeaders)
}

func newTestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// TestSetAndClearExtraHeaders 验证注入可被读取与清除，默认状态为空。
func TestSetAndClearExtraHeaders(t *testing.T) {
	resetExtraHeaders(t)

	if got := currentExtraHeaders(); len(got) != 0 {
		t.Fatalf("初始状态应无注入头，got %#v", got)
	}

	want := map[string]string{"api-key": "sk-azure", "X-Route": "east"}
	SetExtraHeaders(want)
	if got := currentExtraHeaders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("注入后读取不符\n got: %#v\nwant: %#v", got, want)
	}

	ClearExtraHeaders()
	if got := currentExtraHeaders(); len(got) != 0 {
		t.Fatalf("清除后应无注入头，got %#v", got)
	}
}

// TestSetExtraHeadersEmptyClears 验证传入 nil 或空 map 等价于清除。
func TestSetExtraHeadersEmptyClears(t *testing.T) {
	resetExtraHeaders(t)

	SetExtraHeaders(map[string]string{"api-key": "sk"})
	SetExtraHeaders(nil)
	if got := currentExtraHeaders(); len(got) != 0 {
		t.Fatalf("nil 应清除注入，got %#v", got)
	}

	SetExtraHeaders(map[string]string{"api-key": "sk"})
	SetExtraHeaders(map[string]string{})
	if got := currentExtraHeaders(); len(got) != 0 {
		t.Fatalf("空 map 应清除注入，got %#v", got)
	}
}

// TestApplyExtraHeaders 验证注入头被写入请求，且请求既有头不受影响。
func TestApplyExtraHeaders(t *testing.T) {
	resetExtraHeaders(t)

	req := newTestRequest(t)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer original")

	SetExtraHeaders(map[string]string{
		"api-key":  "sk-azure",
		"X-Trace":  "trace-123",
		"X-Region": "east-asia",
	})
	ApplyExtraHeaders(req)

	for k, v := range map[string]string{
		"Api-Key":       "sk-azure", // Header.Set 会规范化头名
		"X-Trace":       "trace-123",
		"X-Region":      "east-asia",
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Authorization": "Bearer original",
	} {
		if got := req.Header.Get(k); got != v {
			t.Errorf("头 %q 不符：got %q, want %q", k, got, v)
		}
	}
}

// TestApplyExtraHeadersOverwrite 验证覆盖语义：注入头与既有头同名时注入值胜出。
func TestApplyExtraHeadersOverwrite(t *testing.T) {
	resetExtraHeaders(t)

	req := newTestRequest(t)
	req.Header.Set("Authorization", "Bearer original")
	req.Header.Set("X-Trace", "old")

	SetExtraHeaders(map[string]string{
		"Authorization": "api-key-replacement",
		"X-Trace":       "new",
	})
	ApplyExtraHeaders(req)

	if got := req.Header.Get("Authorization"); got != "api-key-replacement" {
		t.Errorf("Authorization 应被覆盖，got %q", got)
	}
	if got := req.Header.Get("X-Trace"); got != "new" {
		t.Errorf("X-Trace 应被覆盖，got %q", got)
	}
	if values := req.Header.Values("Authorization"); len(values) != 1 {
		t.Errorf("覆盖后应只有一个 Authorization 值，got %#v", values)
	}
}

// TestApplyExtraHeadersEmptyNoop 验证未注入时 Apply 完全不改请求头（默认逐字节不变）。
func TestApplyExtraHeadersEmptyNoop(t *testing.T) {
	resetExtraHeaders(t)

	req := newTestRequest(t)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer original")
	before := req.Header.Clone()

	ApplyExtraHeaders(req)
	if !reflect.DeepEqual(req.Header, before) {
		t.Fatalf("空注入应 no-op\n got: %#v\nwant: %#v", req.Header, before)
	}
}

// TestApplyExtraHeadersNilRequest 验证 nil 请求安全返回。
func TestApplyExtraHeadersNilRequest(t *testing.T) {
	resetExtraHeaders(t)
	SetExtraHeaders(map[string]string{"api-key": "sk"})
	ApplyExtraHeaders(nil) // 不应 panic
}

// TestSetExtraHeadersCopiesInput 验证注入时复制入参 map，调用方后续修改不生效。
func TestSetExtraHeadersCopiesInput(t *testing.T) {
	resetExtraHeaders(t)

	input := map[string]string{"api-key": "original"}
	SetExtraHeaders(input)
	input["api-key"] = "mutated"
	input["X-New"] = "injected-later"

	want := map[string]string{"api-key": "original"}
	if got := currentExtraHeaders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("入参 map 的后续修改不应影响已注入头\n got: %#v\nwant: %#v", got, want)
	}
}

// TestCurrentExtraHeadersReturnsCopy 验证读取返回副本，改动副本不影响内部状态。
func TestCurrentExtraHeadersReturnsCopy(t *testing.T) {
	resetExtraHeaders(t)

	want := map[string]string{"api-key": "sk"}
	SetExtraHeaders(want)

	got := currentExtraHeaders()
	got["api-key"] = "mutated"
	delete(got, "api-key")
	got["X-New"] = "x"

	if again := currentExtraHeaders(); !reflect.DeepEqual(again, want) {
		t.Fatalf("改动读取副本不应影响内部状态\n got: %#v\nwant: %#v", again, want)
	}
}

// TestApplyExtraHeadersCJKValue 验证 CJK 头值被完整写入（UTF-8 不被截断）。
func TestApplyExtraHeadersCJKValue(t *testing.T) {
	resetExtraHeaders(t)

	const cjk = "中文-值-你好世界"
	SetExtraHeaders(map[string]string{"X-Display-Name": cjk})

	req := newTestRequest(t)
	ApplyExtraHeaders(req)
	if got := req.Header.Get("X-Display-Name"); got != cjk {
		t.Fatalf("CJK 头值不符：got %q, want %q", got, cjk)
	}
}

// TestExtraHeadersConcurrentAccess 验证注入、读取与应用可并发调用而不损坏状态。
// 在 -race 下运行可捕获数据竞争。
func TestExtraHeadersConcurrentAccess(t *testing.T) {
	resetExtraHeaders(t)

	const goroutines = 32
	var wg sync.WaitGroup
	start := make(chan struct{})

	// 先注入：Apply goroutine 与 Set goroutine 无序，需保证任意时刻都有头可读。
	SetExtraHeaders(map[string]string{"api-key": "sk", "X-Concurrent": "yes"})
	for i := 0; i < goroutines; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			// Set 写入相同值，使预期结果不依赖 goroutine 执行顺序。
			SetExtraHeaders(map[string]string{"api-key": "sk", "X-Concurrent": "yes"})
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = currentExtraHeaders()
		}()
		go func() {
			defer wg.Done()
			<-start
			// 不在此处调用 newTestRequest：它内含 t.Fatalf，不能从 goroutine 调用。
			req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
			if err != nil {
				t.Errorf("new request: %v", err)
				return
			}
			ApplyExtraHeaders(req)
			if got := req.Header.Get("api-key"); got != "sk" {
				t.Errorf("并发 Apply 后头不符：got %q", got)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := currentExtraHeaders(); len(got) == 0 {
		t.Fatal("并发结束后应保留注入头")
	}
}

// TestSetExtraHeadersReplaces 验证再次注入会整体替换先前集合，而非追加。
func TestSetExtraHeadersReplaces(t *testing.T) {
	resetExtraHeaders(t)

	SetExtraHeaders(map[string]string{"api-key": "sk", "X-Old": "old"})
	SetExtraHeaders(map[string]string{"X-New": "new"})

	want := map[string]string{"X-New": "new"}
	if got := currentExtraHeaders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("再次注入应整体替换\n got: %#v\nwant: %#v", got, want)
	}
}
