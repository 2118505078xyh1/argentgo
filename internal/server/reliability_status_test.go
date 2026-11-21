package server

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"
)

// stubReliabilityReg 是 NewReliabilityStatus 最小接口的测试替身。
type stubReliabilityReg struct {
	enabled    bool
	queriedKey string
}

func (r *stubReliabilityReg) Enabled(key string) bool {
	r.queriedKey = key
	return r.enabled
}

func TestNewReliabilityStatusOptIn(t *testing.T) {
	cases := []struct {
		name    string
		reg     interface{ Enabled(string) bool }
		enabled bool
	}{
		{"nil 注册表默认关闭", nil, false},
		{"显式关闭", &stubReliabilityReg{enabled: false}, false},
		{"显式开启", &stubReliabilityReg{enabled: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewReliabilityStatus(tc.reg)
			if got == nil {
				t.Fatal("NewReliabilityStatus 返回 nil")
			}
			if got.Enabled != tc.enabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tc.enabled)
			}
			if len(got.Sections) != 0 {
				t.Errorf("Sections 非空: %v", got.Sections)
			}
			if want := "{}"; string(got.JSON()) != want {
				t.Errorf("JSON() = %s, want %s", got.JSON(), want)
			}
		})
	}

	// 构造时必须用稳定的 opt-in 键查询注册表，便于主会话对齐开关名。
	reg := &stubReliabilityReg{enabled: true}
	NewReliabilityStatus(reg)
	if reg.queriedKey != ReliabilityStatusOptInKey {
		t.Errorf("查询键 = %q, want %q", reg.queriedKey, ReliabilityStatusOptInKey)
	}
}

func TestReliabilityStatusSetWhenDisabled(t *testing.T) {
	// 关闭实例：Set 必须是空操作。
	off := NewReliabilityStatus(&stubReliabilityReg{enabled: false})
	off.Set("approval", "有内容")
	off.Set("", "空段名")
	if len(off.Sections) != 0 {
		t.Errorf("关闭实例 Sections 非空: %v", off.Sections)
	}
	if want := "{}"; string(off.JSON()) != want {
		t.Errorf("关闭实例 JSON() = %s, want %s", off.JSON(), want)
	}

	// 零值实例同样关闭，Set 空操作、JSON 为 {}。
	var zero ReliabilityStatus
	zero.Set("jobs", "有内容")
	if zero.Enabled {
		t.Error("零值实例 Enabled 应为 false")
	}
	if len(zero.Sections) != 0 {
		t.Errorf("零值实例 Sections 非空: %v", zero.Sections)
	}
	if want := "{}"; string(zero.JSON()) != want {
		t.Errorf("零值实例 JSON() = %s, want %s", zero.JSON(), want)
	}
}

func TestReliabilityStatusSetWhenEnabled(t *testing.T) {
	s := NewReliabilityStatus(&stubReliabilityReg{enabled: true})
	if !s.Enabled {
		t.Fatal("实例应为开启")
	}

	s.Set("approval", "待审批=2")
	s.Set("jobs", "运行=1")
	if got := s.Sections["approval"]; got != "待审批=2" {
		t.Errorf("approval = %q", got)
	}
	if got := s.Sections["jobs"]; got != "运行=1" {
		t.Errorf("jobs = %q", got)
	}

	// 同段名重复 Set 以最后一次为准。
	s.Set("approval", "待审批=0")
	if got := s.Sections["approval"]; got != "待审批=0" {
		t.Errorf("覆盖后 approval = %q", got)
	}
}

func TestReliabilityStatusJSONStable(t *testing.T) {
	newFilled := func() *ReliabilityStatus {
		s := NewReliabilityStatus(&stubReliabilityReg{enabled: true})
		s.Set("批准板", "待审批=2")
		s.Set("作业", "运行=1")
		s.Set("进程", "健康")
		return s
	}

	a := newFilled()
	b := newFilled()
	if !bytes.Equal(a.JSON(), b.JSON()) {
		t.Errorf("相同内容不同实例字节不一致:\n a=%s\n b=%s", a.JSON(), b.JSON())
	}

	// 同一实例重复调用也必须逐字节一致。
	if !bytes.Equal(a.JSON(), a.JSON()) {
		t.Error("重复 JSON() 字节不一致")
	}

	// 键排序：按 UTF-8 字节序输出，且与插入顺序无关。
	keys := []string{"作业", "批准板", "进程"}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`"` + k + `":`)
		raw, _ := json.Marshal(a.Sections[k])
		buf.Write(raw)
	}
	buf.WriteByte('}')
	if got := string(a.JSON()); got != buf.String() {
		t.Errorf("JSON() = %s, want %s", got, buf.String())
	}
}

func TestReliabilityStatusJSONCJKAndEscape(t *testing.T) {
	s := NewReliabilityStatus(&stubRegWithKey{})
	s.Set("中文键", "<你好&世界>")
	got := string(s.JSON())

	// SetEscapeHTML(false)：CJK 与 <、>、& 都按 UTF-8 原样输出。
	want := `{"中文键":"<你好&世界>"}`
	if got != want {
		t.Errorf("JSON() = %s, want %s", got, want)
	}
	if bytes.Contains(s.JSON(), []byte(`\u`)) {
		t.Errorf("不应出现 Unicode 转义: %s", got)
	}
	// 去尾换行。
	if bytes.HasSuffix(s.JSON(), []byte("\n")) {
		t.Errorf("JSON() 不应带尾随换行: %q", got)
	}
	// 合法 JSON 且可还原。
	var decoded map[string]string
	if err := json.Unmarshal(s.JSON(), &decoded); err != nil {
		t.Fatalf("JSON() 不是合法 JSON: %v", err)
	}
	if decoded["中文键"] != "<你好&世界>" {
		t.Errorf("还原值 = %q", decoded["中文键"])
	}
}

// stubRegWithKey 是只读替身，供并发测试复用（不会写共享字段）。
type stubRegWithKey struct{}

func (stubRegWithKey) Enabled(string) bool { return true }

func TestReliabilityStatusConcurrent(t *testing.T) {
	s := NewReliabilityStatus(stubRegWithKey{})

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			key := "分段-" + string(rune('A'+n%26))
			s.Set(key, "值")
			_ = s.JSON()
		}(i)
	}
	wg.Wait()

	if len(s.Sections) == 0 {
		t.Error("并发写入后 Sections 不应为空")
	}
	var decoded map[string]string
	if err := json.Unmarshal(s.JSON(), &decoded); err != nil {
		t.Fatalf("并发后 JSON() 不是合法 JSON: %v", err)
	}
}
