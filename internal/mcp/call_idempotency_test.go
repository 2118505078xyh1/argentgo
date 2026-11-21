package mcp

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestCallIdempotencyKeyIsOrderedKeyIndependent(t *testing.T) {
	// 仅对象键顺序不同，应视为同一次调用。
	a := callIdempotencyKey("tools/call", json.RawMessage(`{"b":2,"a":1}`))
	b := callIdempotencyKey("tools/call", json.RawMessage(`{"a":1,"b":2}`))
	if a != b {
		t.Fatalf("键顺序不同导致幂等键不同：%q != %q", a, b)
	}
}

func TestCallIdempotencyKeySortsNestedObjectsRecursively(t *testing.T) {
	a := callIdempotencyKey("m", json.RawMessage(`{"outer":{"y":2,"x":1},"k":3}`))
	b := callIdempotencyKey("m", json.RawMessage(`{"k":3,"outer":{"x":1,"y":2}}`))
	if a != b {
		t.Fatalf("嵌套对象键顺序未被规范化：%q != %q", a, b)
	}
}

func TestCallIdempotencyKeyDistinguishesMethodAndParams(t *testing.T) {
	base := callIdempotencyKey("tools/call", json.RawMessage(`{"a":1}`))
	if got := callIdempotencyKey("tools/other", json.RawMessage(`{"a":1}`)); got == base {
		t.Fatalf("不同方法产生了相同幂等键：%q", got)
	}
	if got := callIdempotencyKey("tools/call", json.RawMessage(`{"a":2}`)); got == base {
		t.Fatalf("不同参数产生了相同幂等键：%q", got)
	}
}

func TestCallIdempotencyKeySeparatesMethodAndParams(t *testing.T) {
	// NUL 分隔符应阻止 ("ab", "c") 与 ("a", "bc") 这类边界拼接碰撞。
	a := callIdempotencyKey("ab", json.RawMessage(`"c"`))
	b := callIdempotencyKey("a", json.RawMessage(`"bc"`))
	if a == b {
		t.Fatalf("方法名与参数边界发生碰撞：%q", a)
	}
}

func TestCallIdempotencyKeyPreservesArrayOrder(t *testing.T) {
	a := callIdempotencyKey("m", json.RawMessage(`{"xs":[1,2]}`))
	b := callIdempotencyKey("m", json.RawMessage(`{"xs":[2,1]}`))
	if a == b {
		t.Fatalf("数组顺序被错误地规范化：%q", a)
	}
}

func TestCallIdempotencyKeyEmptyAndInvalidParamsAreStable(t *testing.T) {
	tests := []struct {
		name   string
		params json.RawMessage
	}{
		{"nil", nil},
		{"空切片", json.RawMessage{}},
		{"纯空白", json.RawMessage("  \n\t ")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := callIdempotencyKey("m", tc.params)
			second := callIdempotencyKey("m", tc.params)
			if first != second {
				t.Fatalf("空参数结果不稳定：%q != %q", first, second)
			}
		})
	}

	invalid := []json.RawMessage{
		json.RawMessage(`{`),
		json.RawMessage(`{"a":}`),
		json.RawMessage(`{} {}`),
	}
	for i, params := range invalid {
		first := callIdempotencyKey("m", params)
		second := callIdempotencyKey("m", params)
		if first != second {
			t.Fatalf("非法参数[%d]结果不稳定：%q != %q", i, first, second)
		}
	}

	// 空参数与非法参数都稳定，但不应互相混淆。
	empty := callIdempotencyKey("m", nil)
	bad := callIdempotencyKey("m", json.RawMessage(`{`))
	if empty == bad {
		t.Fatalf("空参数与非法参数发生了碰撞：%q", empty)
	}
	// 合法但空对象也不应与空参数碰撞。
	if empty == callIdempotencyKey("m", json.RawMessage(`{}`)) {
		t.Fatalf("空参数与空对象发生了碰撞：%q", empty)
	}
}

func TestCallIdempotencyKeyCJK(t *testing.T) {
	// 中文键名与取值：转义写法与字面写法应归一到同一个键。
	literal := callIdempotencyKey("工具_调用", json.RawMessage(`{"路径":"读取文件"}`))
	escaped := callIdempotencyKey("工具_调用", json.RawMessage(`{"\u8def\u5f84":"\u8bfb\u53d6\u6587\u4ef6"}`))
	if literal != escaped {
		t.Fatalf("CJK 转义未被归一化：%q != %q", literal, escaped)
	}
	// 不同中文取值应产生不同键。
	if literal == callIdempotencyKey("工具_调用", json.RawMessage(`{"路径":"写入文件"}`)) {
		t.Fatalf("不同 CJK 取值产生了相同幂等键：%q", literal)
	}
}

func TestCallIdempotencyKeyPreservesLargeNumbers(t *testing.T) {
	// UseNumber 保留了数字字面量，大的整数不应因浮点舍入而合流。
	a := callIdempotencyKey("m", json.RawMessage(`{"id":9007199254740993}`))
	b := callIdempotencyKey("m", json.RawMessage(`{"id":9007199254740992}`))
	if a == b {
		t.Fatalf("大整数因浮点舍入发生碰撞：%q", a)
	}
}

func TestCallIdempotencyKeyShapeAndDeterminism(t *testing.T) {
	key := callIdempotencyKey("m", json.RawMessage(`{"a":1}`))
	if len(key) != callIdempotencyKeyHexLen {
		t.Fatalf("幂等键长度 = %d, want %d", len(key), callIdempotencyKeyHexLen)
	}
	if _, err := hex.DecodeString(key); err != nil {
		t.Fatalf("幂等键不是合法的十六进制：%q (%v)", key, err)
	}
	if strings.ToLower(key) != key {
		t.Fatalf("幂等键应为小写十六进制：%q", key)
	}
	for i := 0; i < 100; i++ {
		if got := callIdempotencyKey("m", json.RawMessage(`{"a":1}`)); got != key {
			t.Fatalf("重复调用结果不一致：%q != %q", got, key)
		}
	}
}

func TestCallIdempotencyKeyConcurrent(t *testing.T) {
	// 纯函数应在并发调用下保持稳定，供 -race 检测。
	const goroutines = 16
	want := callIdempotencyKey("tools/call", json.RawMessage(`{"b":2,"a":1}`))
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := callIdempotencyKey("tools/call", json.RawMessage(`{"a":1,"b":2}`)); got != want {
				errs <- got
			}
		}()
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Fatalf("并发调用结果不一致：%q != %q", got, want)
	}
}
