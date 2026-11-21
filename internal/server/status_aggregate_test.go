package server

import (
	"bytes"
	"reflect"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// setOp 描述一次 Set 调用，供表驱动测试按序重放。
type setOp struct {
	section string
	data    map[string]string
}

// replay 把一组 Set 操作应用到聚合器上。
func replay(a *StatusAggregator, ops []setOp) {
	for _, op := range ops {
		a.Set(op.section, op.data)
	}
}

func TestStatusAggregatorSnapshot(t *testing.T) {
	tests := []struct {
		name string
		ops  []setOp
		want map[string]map[string]string
	}{
		{
			name: "zero value is empty non-nil map",
			want: map[string]map[string]string{},
		},
		{
			name: "set nil data creates empty section",
			ops:  []setOp{{section: "turn"}},
			want: map[string]map[string]string{"turn": {}},
		},
		{
			name: "single section with values",
			ops:  []setOp{{section: "turn", data: map[string]string{"state": "running", "步数": "三"}}},
			want: map[string]map[string]string{"turn": {"state": "running", "步数": "三"}},
		},
		{
			name: "multiple sections",
			ops: []setOp{
				{section: "turn", data: map[string]string{"state": "idle"}},
				{section: "readiness", data: map[string]string{"ready": "true"}},
			},
			want: map[string]map[string]string{
				"turn":      {"state": "idle"},
				"readiness": {"ready": "true"},
			},
		},
		{
			name: "later set replaces earlier section",
			ops: []setOp{
				{section: "turn", data: map[string]string{"state": "old", "only": "old"}},
				{section: "turn", data: map[string]string{"state": "new"}},
			},
			want: map[string]map[string]string{"turn": {"state": "new"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a StatusAggregator
			replay(&a, tt.ops)
			got := a.Snapshot()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Snapshot() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestStatusAggregatorRemove(t *testing.T) {
	base := []setOp{
		{section: "turn", data: map[string]string{"state": "running"}},
		{section: "readiness", data: map[string]string{"ready": "true"}},
		{section: "integrity", data: map[string]string{"ok": "yes"}},
	}

	tests := []struct {
		name     string
		remove   []string
		want     map[string]map[string]string
		wantJSON string
	}{
		{
			name:   "remove existing section",
			remove: []string{"readiness"},
			want: map[string]map[string]string{
				"turn":      {"state": "running"},
				"integrity": {"ok": "yes"},
			},
			wantJSON: `{"integrity":{"ok":"yes"},"turn":{"state":"running"}}`,
		},
		{
			name:     "remove all sections yields empty",
			remove:   []string{"turn", "readiness", "integrity"},
			want:     map[string]map[string]string{},
			wantJSON: `{}`,
		},
		{
			name:     "remove missing section is no-op",
			remove:   []string{"missing"},
			want:     map[string]map[string]string{"turn": {"state": "running"}, "readiness": {"ready": "true"}, "integrity": {"ok": "yes"}},
			wantJSON: `{"integrity":{"ok":"yes"},"readiness":{"ready":"true"},"turn":{"state":"running"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a StatusAggregator
			replay(&a, base)
			for _, name := range tt.remove {
				a.Remove(name)
			}
			if got := a.Snapshot(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Snapshot() = %#v, want %#v", got, tt.want)
			}
			if got := string(a.JSON()); got != tt.wantJSON {
				t.Fatalf("JSON() = %s, want %s", got, tt.wantJSON)
			}
		})
	}

	// 零值聚合器上 Remove 必须安全。
	var zero StatusAggregator
	zero.Remove("anything")
	if got := string(zero.JSON()); got != "{}" {
		t.Fatalf("zero aggregator JSON() = %s, want {}", got)
	}
}

func TestStatusAggregatorJSON(t *testing.T) {
	tests := []struct {
		name string
		ops  []setOp
		want string
	}{
		{
			name: "zero value",
			want: `{}`,
		},
		{
			name: "single section",
			ops:  []setOp{{section: "turn", data: map[string]string{"state": "running"}}},
			want: `{"turn":{"state":"running"}}`,
		},
		{
			name: "keys and sections sorted",
			ops: []setOp{{
				section: "turn",
				data:    map[string]string{"zeta": "z", "alpha": "a", "mid": "m"},
			}},
			want: `{"turn":{"alpha":"a","mid":"m","zeta":"z"}}`,
		},
		{
			name: "multiple sections sorted by name",
			ops: []setOp{
				{section: "integrity", data: map[string]string{"ok": "yes"}},
				{section: "turn", data: map[string]string{"state": "idle"}},
				{section: "readiness", data: map[string]string{"ready": "true"}},
			},
			want: `{"integrity":{"ok":"yes"},"readiness":{"ready":"true"},"turn":{"state":"idle"}}`,
		},
		{
			name: "empty data section serializes as empty object",
			ops:  []setOp{{section: "turn"}},
			want: `{"turn":{}}`,
		},
		{
			name: "CJK is not escaped",
			ops: []setOp{{
				section: "状态",
				data:    map[string]string{"消息": "服务器运行良好，欢迎访问", "状态": "正常"},
			}},
			want: `{"状态":{"消息":"服务器运行良好，欢迎访问","状态":"正常"}}`,
		},
		{
			name: "HTML characters are not escaped",
			ops: []setOp{{
				section: "view",
				data:    map[string]string{"lt": "<tag>", "amp": "a&b"},
			}},
			want: `{"view":{"amp":"a&b","lt":"<tag>"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a StatusAggregator
			replay(&a, tt.ops)
			got := string(a.JSON())
			if got != tt.want {
				t.Fatalf("JSON() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestStatusAggregatorJSONStableBytes(t *testing.T) {
	data := map[string]string{"zeta": "z", "alpha": "a", "消息": "你好", "mid": "m"}

	var a, b StatusAggregator
	// 两个不同插入顺序的等价映射。
	a.Set("turn", data)
	b.Set("turn", map[string]string{"mid": "m", "消息": "你好", "alpha": "a", "zeta": "z"})

	first := a.JSON()
	second := a.JSON()
	other := b.JSON()

	if !bytes.Equal(first, second) {
		t.Fatalf("JSON() 不稳定: 第一次 %s, 第二次 %s", first, second)
	}
	if !bytes.Equal(first, other) {
		t.Fatalf("不同插入顺序产生不同字节: %s vs %s", first, other)
	}
	if bytes.HasSuffix(first, []byte("\n")) {
		t.Fatalf("JSON() 不应带尾换行: %q", first)
	}
}

func TestStatusAggregatorJSONRedaction(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwxyz"
	var a StatusAggregator
	a.Set("turn", map[string]string{"tokenValue": secret, "state": "running"})

	want := `{"turn":{"state":"running","tokenValue":"` + security.Redact(secret) + `"}}`
	if got := string(a.JSON()); got != want {
		t.Fatalf("JSON() = %s, want %s", got, want)
	}
}

func TestStatusAggregatorDeepCopyIsolation(t *testing.T) {
	input := map[string]string{"state": "original"}
	var a StatusAggregator
	a.Set("turn", input)

	// 修改入参不得影响内部状态。
	input["state"] = "mutated"
	input["extra"] = "leaked"
	want := `{"turn":{"state":"original"}}`
	if got := string(a.JSON()); got != want {
		t.Fatalf("修改 Set 入参影响了内部状态: JSON() = %s, want %s", got, want)
	}

	// 修改 Snapshot 返回值不得影响内部状态，包括外层与内层映射。
	snap := a.Snapshot()
	snap["turn"]["state"] = "mutated"
	snap["turn"]["extra"] = "leaked"
	snap["injected"] = map[string]string{"x": "y"}
	if got := string(a.JSON()); got != want {
		t.Fatalf("修改 Snapshot 影响了内部状态: JSON() = %s, want %s", got, want)
	}

	// 两次 Snapshot 之间也必须互不影响。
	again := a.Snapshot()
	if !reflect.DeepEqual(again, map[string]map[string]string{"turn": {"state": "original"}}) {
		t.Fatalf("后续 Snapshot() = %#v", again)
	}
}

func TestStatusAggregatorConcurrent(t *testing.T) {
	var a StatusAggregator
	const (
		workers    = 8
		iterations = 300
	)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				name := "turn"
				if id%2 == 1 {
					name = "readiness"
				}
				a.Set(name, map[string]string{"i": "value", "worker": "x"})
				a.Remove("missing")
				_ = a.Snapshot()
				if body := a.JSON(); len(body) == 0 {
					t.Errorf("JSON() 返回空字节")
				}
			}
		}(w)
	}
	wg.Wait()

	// 并发结束后，每个写入的分段都应存在且结构完好。
	snap := a.Snapshot()
	for _, name := range []string{"turn", "readiness"} {
		if _, ok := snap[name]; !ok {
			t.Errorf("并发结束后缺少分段 %q: %#v", name, snap)
		}
	}
}
