package mcp

import (
	"bytes"
	"math/rand"
	"sync"
	"testing"
)

// naiveRing 是"保留最后 limit 字节"的朴素模型：每次追加后裁掉最旧部分。
// 用它逐字节校验环形实现的输出与旧语义完全一致。
type naiveRing struct {
	limit int
	data  []byte
}

func (m *naiveRing) Write(p []byte) int {
	if len(p) >= m.limit {
		m.data = append([]byte(nil), p[len(p)-m.limit:]...)
		return len(p)
	}
	m.data = append(m.data, p...)
	if len(m.data) > m.limit {
		m.data = append([]byte(nil), m.data[len(m.data)-m.limit:]...)
	}
	return len(p)
}

func (m *naiveRing) String() string { return string(m.data) }

func TestRingBufferLargeWriteStaysBounded(t *testing.T) {
	const limit = 4096
	r := &ringBuffer{limit: limit}

	payload := bytes.Repeat([]byte("x"), 1<<20)
	tail := []byte("---end-of-stderr---")
	copy(payload[len(payload)-len(tail):], tail)

	n, err := r.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	if len(r.buf) > limit || cap(r.buf) > limit {
		t.Fatalf("buffer exceeded limit: len=%d cap=%d limit=%d", len(r.buf), cap(r.buf), limit)
	}
	want := string(payload[len(payload)-limit:])
	if got := r.String(); got != want {
		t.Fatalf("String() kept the wrong tail:\n got %q\nwant %q", got, want)
	}
}

func TestRingBufferByteByByteWriteStaysBounded(t *testing.T) {
	const limit = 1024
	const total = 100 * 1024
	r := &ringBuffer{limit: limit}

	for i := 0; i < total; i++ {
		if n, err := r.Write([]byte{byte('a' + i%26)}); err != nil || n != 1 {
			t.Fatalf("Write(byte %d) = (%d, %v)", i, n, err)
		}
	}
	if len(r.buf) > limit || cap(r.buf) > limit {
		t.Fatalf("buffer exceeded limit: len=%d cap=%d limit=%d", len(r.buf), cap(r.buf), limit)
	}

	want := make([]byte, limit)
	for i := range want {
		want[i] = byte('a' + (total-limit+i)%26)
	}
	if got := r.String(); got != string(want) {
		t.Fatalf("String() kept the wrong tail:\n got %q\nwant %q", got, want)
	}
}

func TestRingBufferDefaultLimit(t *testing.T) {
	r := &ringBuffer{}
	payload := bytes.Repeat([]byte("z"), stderrRingBytes+100)
	_, _ = r.Write(payload)

	if len(r.buf) != stderrRingBytes || cap(r.buf) != stderrRingBytes {
		t.Fatalf("buffer = (len %d, cap %d), want %d", len(r.buf), cap(r.buf), stderrRingBytes)
	}
	if got, want := r.String(), string(payload[len(payload)-stderrRingBytes:]); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestRingBufferKeepsLastBytesAcrossWrites(t *testing.T) {
	r := &ringBuffer{limit: 8}
	_, _ = r.Write([]byte("hello "))
	_, _ = r.Write([]byte("world"))

	if got, want := r.String(), "lo world"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// TestRingBufferMatchesNaiveModel 混合小写入与超大写入，逐步与朴素模型逐字节比对，
// 既验证"超大写入只留尾部"，也验证小写入后 String 与旧实现逐字节一致。
func TestRingBufferMatchesNaiveModel(t *testing.T) {
	const limit = 37
	rng := rand.New(rand.NewSource(20261122))
	r := &ringBuffer{limit: limit}
	model := &naiveRing{limit: limit}

	for step := 0; step < 2000; step++ {
		var payload []byte
		if rng.Intn(20) == 0 {
			// 偶发超大写入：长度超过上限，只应保留尾部。
			payload = make([]byte, limit+1+rng.Intn(limit*3))
		} else {
			// 多数为小写入，包含 0 字节边界。
			payload = make([]byte, rng.Intn(limit))
		}
		for i := range payload {
			payload[i] = byte(rng.Intn(256))
		}

		if n, err := r.Write(payload); err != nil || n != len(payload) {
			t.Fatalf("step %d: Write() = (%d, %v), want (%d, nil)", step, n, err, len(payload))
		}
		model.Write(payload)

		if got, want := r.String(), model.String(); got != want {
			t.Fatalf("step %d: String() mismatch:\n got %q\nwant %q", step, got, want)
		}
	}
}

// TestRingBufferConcurrentWriteString 在 -race 下确保 Write 与 String 并发无数据竞争。
func TestRingBufferConcurrentWriteString(t *testing.T) {
	const limit = 64
	const writers = 8
	const each = 500
	r := &ringBuffer{limit: limit}

	start := make(chan struct{})
	var workers sync.WaitGroup
	for w := 0; w < writers; w++ {
		workers.Add(1)
		go func(id byte) {
			defer workers.Done()
			<-start
			_, _ = r.Write(bytes.Repeat([]byte{id}, each))
		}(byte('a' + w))
	}

	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if s := r.String(); len(s) > limit {
					t.Errorf("String() length %d > limit %d", len(s), limit)
					return
				}
			}
		}
	}()

	close(start)
	workers.Wait()
	close(stop)
	reader.Wait()

	if got := r.String(); len(got) != limit {
		t.Fatalf("final String() length = %d, want %d", len(got), limit)
	}
}

// BenchmarkRingWrite 衡量稳态写入开销：预热后底层数组只分配一次，B/op 应为 0。
func BenchmarkRingWrite(b *testing.B) {
	const limit = stderrRingBytes
	const size = 512
	r := &ringBuffer{limit: limit}
	_, _ = r.Write(bytes.Repeat([]byte("x"), size)) // 预热，触发首次分配

	payload := bytes.Repeat([]byte("y"), size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Write(payload)
	}
}
