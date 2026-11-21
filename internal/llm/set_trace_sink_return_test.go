package llm

import "testing"

// TestSetTraceSinkReturnsPrevious 验证 SetTraceSink 返回安装前的收集器，
// 从而串出 nil → sink → second 的安装/恢复链。
func TestSetTraceSinkReturnsPrevious(t *testing.T) {
	resetTraceSink(t)

	if got := currentTraceSink(); got != nil {
		t.Fatalf("初始状态应无收集器，got %#v", got)
	}

	sink := &captureTraceSink{}
	if prev := SetTraceSink(sink); prev != nil {
		t.Fatalf("首次安装应返回 nil，got %#v", prev)
	}
	if got := currentTraceSink(); got != sink {
		t.Fatalf("首次安装后读取不符：got %#v want %#v", got, sink)
	}

	second := &captureTraceSink{}
	if prev := SetTraceSink(second); prev != sink {
		t.Fatalf("再次安装应返回先前的 sink，got %#v want %#v", prev, sink)
	}
	if got := currentTraceSink(); got != second {
		t.Fatalf("再次安装后读取不符：got %#v want %#v", got, second)
	}

	// 用返回值恢复：把 sink 装回去，应返回被替换的 second。
	if prev := SetTraceSink(sink); prev != second {
		t.Fatalf("恢复安装应返回 second，got %#v want %#v", prev, second)
	}

	// 清除时同样返回先前的收集器。
	if prev := SetTraceSink(nil); prev != sink {
		t.Fatalf("清除应返回先前的 sink，got %#v want %#v", prev, sink)
	}
	if got := currentTraceSink(); got != nil {
		t.Fatalf("清除后应无收集器，got %#v", got)
	}
}
