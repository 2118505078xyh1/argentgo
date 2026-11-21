package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// fpExpected 用字面拼接重新计算指纹，独立于 SpeculativeFingerprint 的实现，
// 用于验证其公式确为 sha256(name + "\x00" + NormalizeArgs(args))。
func fpExpected(t *testing.T, name, normalizedArgs string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(name + "\x00" + normalizedArgs))
	return hex.EncodeToString(sum[:])
}

func TestSpeculativeFingerprintFormula(t *testing.T) {
	tests := []struct {
		name           string
		toolName       string
		args           string
		normalizedArgs string
	}{
		{name: "empty args", toolName: "read", args: "", normalizedArgs: "{}"},
		{name: "blank args", toolName: "read", args: "   ", normalizedArgs: "{}"},
		{name: "plain json", toolName: "grep", args: `{"pattern":"x"}`, normalizedArgs: `{"pattern":"x"}`},
		{name: "non json fold", toolName: "read", args: "  foo   bar  ", normalizedArgs: "foo bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SpeculativeFingerprint(tt.toolName, tt.args)
			want := fpExpected(t, tt.toolName, tt.normalizedArgs)
			if got != want {
				t.Fatalf("SpeculativeFingerprint(%q,%q) = %s, want %s", tt.toolName, tt.args, got, want)
			}
			if len(got) != 64 {
				t.Fatalf("fingerprint length = %d, want 64 hex chars", len(got))
			}
		})
	}
}

func TestSpeculativeFingerprintNormalization(t *testing.T) {
	tests := []struct {
		name  string
		left  [2]string // {name, args}
		right [2]string
		equal bool
	}{
		{
			name:  "json key order",
			left:  [2]string{"grep", `{"b":2,"a":1}`},
			right: [2]string{"grep", `{"a":1,"b":2}`},
			equal: true,
		},
		{
			name:  "json whitespace",
			left:  [2]string{"grep", `{ "pattern" : "x" , "n" : 3 }`},
			right: [2]string{"grep", `{"n":3,"pattern":"x"}`},
			equal: true,
		},
		{
			name:  "non json whitespace folding",
			left:  [2]string{"read", "  a\t b\n c  "},
			right: [2]string{"read", "a b c"},
			equal: true,
		},
		{
			name:  "different args",
			left:  [2]string{"grep", `{"pattern":"x"}`},
			right: [2]string{"grep", `{"pattern":"y"}`},
			equal: false,
		},
		{
			name:  "different name",
			left:  [2]string{"grep", `{}`},
			right: [2]string{"read", `{}`},
			equal: false,
		},
		{
			name:  "nul delimiter disambiguation",
			left:  [2]string{"a", "b"},
			right: [2]string{"ab", ""},
			equal: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := SpeculativeFingerprint(tt.left[0], tt.left[1])
			r := SpeculativeFingerprint(tt.right[0], tt.right[1])
			if (l == r) != tt.equal {
				t.Fatalf("equal = %v, want %v (left=%s right=%s)", l == r, tt.equal, l, r)
			}
		})
	}
}

func TestSpeculativeFingerprintCJK(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		args  string
		want  string
		other string // 应与 want 不同的另一个 CJK 指纹
	}{
		{
			name: "cjk name and args",
			tool: "读取",
			args: `{"路径":"中文"}`,
			want: "ac8089099e18024d9e890a45fbc687cd31e5cd9e5a4eea681e4fe2837c3a0351",
		},
		{
			name: "cjk value",
			tool: "grep",
			args: `{"pattern":"你好"}`,
			want: "6108104a2c359de79340afbfe7fe612bab44524dcf17b514dba77b6cf4ef5ddf",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SpeculativeFingerprint(tt.tool, tt.args)
			if got != tt.want {
				t.Fatalf("fingerprint = %s, want %s", got, tt.want)
			}
			// 同一 CJK 调用重复计算必须稳定。
			if again := SpeculativeFingerprint(tt.tool, tt.args); again != got {
				t.Fatalf("fingerprint not stable: %s != %s", again, got)
			}
			// 不同 CJK 文本必须得到不同指纹。
			other := SpeculativeFingerprint(tt.tool, tt.args+"不同")
			if other == got {
				t.Fatalf("different CJK args collided: %s", got)
			}
		})
	}
}

func TestSpecCacheSetGet(t *testing.T) {
	tests := []struct {
		name   string
		fp     string
		result SpecResult
	}{
		{name: "plain output", fp: "fp-a", result: SpecResult{Output: "ok"}},
		{name: "with error", fp: "fp-b", result: SpecResult{Output: "partial", Err: errors.New("boom")}},
		{name: "empty result", fp: "fp-c", result: SpecResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c SpecCache
			if _, ok := c.Get(tt.fp); ok {
				t.Fatal("Get on empty cache unexpectedly hit")
			}
			c.Set(tt.fp, tt.result)
			got, ok := c.Get(tt.fp)
			if !ok {
				t.Fatal("Get after Set missed")
			}
			if got.Output != tt.result.Output || !errors.Is(got.Err, tt.result.Err) {
				t.Fatalf("Get = %+v, want %+v", got, tt.result)
			}
			// 覆盖写入以最后一次为准。
			c.Set(tt.fp, SpecResult{Output: "replaced"})
			got, _ = c.Get(tt.fp)
			if got.Output != "replaced" {
				t.Fatalf("overwrite failed: got %+v", got)
			}
		})
	}
}

func TestSpecCacheInvalidateRound(t *testing.T) {
	var c SpecCache
	c.Set("fp-a", SpecResult{Output: "a"})
	c.Set("fp-b", SpecResult{Output: "b"})
	if _, ok := c.Get("fp-a"); !ok {
		t.Fatal("precondition: fp-a should hit")
	}

	c.InvalidateRound()

	for _, fp := range []string{"fp-a", "fp-b", "unknown"} {
		if _, ok := c.Get(fp); ok {
			t.Fatalf("fp %q still hits after InvalidateRound", fp)
		}
	}

	// 新一轮写入应重新命中，且多轮累积作废同样有效。
	c.Set("fp-a", SpecResult{Output: "a2"})
	if got, ok := c.Get("fp-a"); !ok || got.Output != "a2" {
		t.Fatalf("new round write missed: got %+v ok=%v", got, ok)
	}
	c.InvalidateRound()
	c.InvalidateRound()
	if _, ok := c.Get("fp-a"); ok {
		t.Fatal("fp-a still hits after two more invalidations")
	}
}

func TestSpecCacheConcurrent(t *testing.T) {
	var c SpecCache
	const (
		writers    = 8
		readers    = 8
		perG       = 200
		invalidate = 40
	)

	var wg sync.WaitGroup
	// 写入者：各自写入自己的指纹，互不干扰。
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				c.Set(fmt.Sprintf("fp-%d-%d", id, i), SpecResult{Output: "v"})
			}
		}(w)
	}
	// 读取者：只做 Get，值是什么都接受，关键是 -race 下不崩溃。
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_, _ = c.Get(fmt.Sprintf("fp-%d-%d", id%writers, i))
			}
		}(r)
	}
	// 作废者：并发推进轮次，制造新旧轮交替。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < invalidate; i++ {
			c.InvalidateRound()
		}
	}()
	wg.Wait()

	// 并发结束后缓存仍可用：写入并立即读取应命中。
	c.Set("final", SpecResult{Output: "done"})
	if got, ok := c.Get("final"); !ok || got.Output != "done" {
		t.Fatalf("cache unusable after concurrency: got %+v ok=%v", got, ok)
	}
}

func TestShouldSpeculate(t *testing.T) {
	ro := func(name string) bool { return name == "read" || name == "ls" }

	tests := []struct {
		name     string
		delta    llm.ToolCallDelta
		readOnly func(string) bool
		enabled  bool
		want     bool
	}{
		{
			name:     "disabled always false",
			delta:    llm.ToolCallDelta{Name: "read"},
			readOnly: ro, enabled: false, want: false,
		},
		{
			name:     "nil predicate fail closed",
			delta:    llm.ToolCallDelta{Name: "read"},
			readOnly: nil, enabled: true, want: false,
		},
		{
			name:     "empty name false",
			delta:    llm.ToolCallDelta{Name: ""},
			readOnly: ro, enabled: true, want: false,
		},
		{
			name:     "read only name true",
			delta:    llm.ToolCallDelta{Name: "read"},
			readOnly: ro, enabled: true, want: true,
		},
		{
			name:     "another read only name true",
			delta:    llm.ToolCallDelta{Name: "ls", Arguments: `{"path":"."}`},
			readOnly: ro, enabled: true, want: true,
		},
		{
			name:     "non read only name false",
			delta:    llm.ToolCallDelta{Name: "bash", Arguments: `{"cmd":"rm"}`},
			readOnly: ro, enabled: true, want: false,
		},
		{
			name:     "cjk read only name via injected predicate",
			delta:    llm.ToolCallDelta{Name: "读取"},
			readOnly: func(string) bool { return true }, enabled: true, want: true,
		},
		{
			name:     "disabled wins over permissive predicate",
			delta:    llm.ToolCallDelta{Name: "bash"},
			readOnly: func(string) bool { return true }, enabled: false, want: false,
		},
		{
			name:     "arguments and id do not affect decision",
			delta:    llm.ToolCallDelta{ID: "x", Name: "read", Arguments: `{"path":"中文"}`},
			readOnly: ro, enabled: true, want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldSpeculate(tt.delta, tt.readOnly, tt.enabled); got != tt.want {
				t.Fatalf("ShouldSpeculate() = %v, want %v", got, tt.want)
			}
		})
	}
	// 默认关闭：不启用时，即便任意调用也一律不推测。
	if ShouldSpeculate(llm.ToolCallDelta{Name: "read"}, ro, false) {
		t.Fatal("default-off violated: disabled speculation returned true")
	}
}

func TestSpecCacheLookupOutcome(t *testing.T) {
	delta := llm.ToolCallDelta{Name: "read", Arguments: `{"path":"."}`}
	ro := func(string) bool { return true }
	fp := SpeculativeFingerprint(delta.Name, delta.Arguments)

	t.Run("skip when disabled", func(t *testing.T) {
		var c SpecCache
		out := c.Lookup(delta, ro, false)
		if out.String() != specOutcomeSkip || out.Eligible || out.Fingerprint != "" {
			t.Fatalf("out = %+v (%s), want skip zero value", out, out.String())
		}
	})

	t.Run("miss when eligible but empty", func(t *testing.T) {
		var c SpecCache
		out := c.Lookup(delta, ro, true)
		if out.String() != specOutcomeMiss || !out.Eligible || out.Fingerprint != fp {
			t.Fatalf("out = %+v (%s), want miss", out, out.String())
		}
	})

	t.Run("hit with result", func(t *testing.T) {
		var c SpecCache
		c.Set(fp, SpecResult{Output: "payload"})
		out := c.Lookup(delta, ro, true)
		if out.String() != specOutcomeHit || !out.Hit || out.Result.Output != "payload" {
			t.Fatalf("out = %+v (%s), want hit with result", out, out.String())
		}
		if out.Invalidated {
			t.Fatal("hit outcome must not be invalidated")
		}
	})

	t.Run("invalidated after round advance", func(t *testing.T) {
		var c SpecCache
		c.Set(fp, SpecResult{Output: "stale"})
		c.InvalidateRound()
		out := c.Lookup(delta, ro, true)
		if out.String() != specOutcomeInvalidated || !out.Invalidated || out.Hit {
			t.Fatalf("out = %+v (%s), want invalidated", out, out.String())
		}
	})
}

func TestSpecCacheNilReceiver(t *testing.T) {
	var c *SpecCache
	if _, ok := c.Get("fp"); ok {
		t.Fatal("nil cache Get unexpectedly hit")
	}
	c.Set("fp", SpecResult{Output: "x"})
	c.InvalidateRound()
	if out := c.Lookup(llm.ToolCallDelta{Name: "read"}, func(string) bool { return true }, true); out.String() != specOutcomeMiss {
		t.Fatalf("nil cache Lookup = %s, want miss", out.String())
	}
}
