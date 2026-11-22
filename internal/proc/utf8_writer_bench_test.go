package proc

import (
	"bytes"
	"math/rand"
	"testing"
	"unicode/utf8"
)

// noopWriter 丢弃所有写入，用于分配计数与基准测试。
type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// naiveOutput 直接拼接所有分块，代表"朴素参考实现"的期望输出。
// utf8CarryWriter 无论怎样分块、暂存多久，Flush 之后的输出都应与之逐字节相等。
func naiveOutput(chunks [][]byte) []byte {
	var out []byte
	for _, p := range chunks {
		out = append(out, p...)
	}
	return out
}

// referenceOutput 复刻优化前的 utf8CarryWriter 核心算法：
// 携带暂存字节时用 make 分配新切片，并用 FullRune + DecodeRune 双解码定位尾部。
// 作为差分基准，确保优化后的输出与优化前逐字节一致。
func referenceOutput(chunks [][]byte, limit int) []byte {
	var out, carry []byte
	for _, p := range chunks {
		n := len(p)
		if len(carry) == 0 && n == 0 {
			continue
		}
		data := p
		if len(carry) > 0 {
			data = make([]byte, 0, len(carry)+n)
			data = append(data, carry...)
			data = append(data, p...)
			carry = carry[:0]
		}
		split := 0
		for split < len(data) {
			if !utf8.FullRune(data[split:]) {
				break
			}
			_, size := utf8.DecodeRune(data[split:])
			split += size
		}
		tail := data[split:]
		if len(tail) > limit {
			excess := len(tail) - limit
			out = append(out, data[:split+excess]...)
			carry = append(carry[:0], tail[excess:]...)
		} else {
			out = append(out, data[:split]...)
			carry = append(carry[:0], tail...)
		}
	}
	return append(out, carry...)
}

// optimizedOutput 用优化后的 utf8CarryWriter 处理分块并在末尾 Flush。
func optimizedOutput(chunks [][]byte) []byte {
	var buf bytes.Buffer
	cw := newUTF8CarryWriter(&buf)
	for _, p := range chunks {
		_, _ = cw.Write(p)
	}
	cw.Flush()
	return buf.Bytes()
}

// TestUTF8CarryWriterDifferential 把优化后的 Write 与朴素参考实现（直接拼接）
// 以及优化前的算法逐字节对比，覆盖中文/emoji 被切开、畸形字节、空写入等场景。
func TestUTF8CarryWriterDifferential(t *testing.T) {
	check := func(name string, chunks [][]byte) {
		t.Helper()
		got := optimizedOutput(chunks)
		wantNaive := naiveOutput(chunks)
		wantRef := referenceOutput(chunks, maxCarryBytes)
		if !bytes.Equal(got, wantNaive) {
			t.Fatalf("%s: output != 直接拼接: got %x, want %x", name, got, wantNaive)
		}
		if !bytes.Equal(got, wantRef) {
			t.Fatalf("%s: output != 优化前参考实现: got %x, want %x", name, got, wantRef)
		}
	}
	// checkAllSplits 检查整个序列作为一个分块，以及在每个字节边界切开的两分块。
	checkAllSplits := func(name string, seq []byte) {
		t.Helper()
		check(name+"/whole", [][]byte{seq})
		for j := 0; j <= len(seq); j++ {
			check(name+"/split", [][]byte{seq[:j], seq[j:]})
		}
	}

	// 中文、emoji 与 ASCII 混合文本在每个字节边界被切开，以及逐字节写入。
	const mixed = "hello中文world Go语言🌍end"
	checkAllSplits("mixed", []byte(mixed))
	{
		chunks := make([][]byte, 0, len(mixed))
		for i := 0; i < len(mixed); i++ {
			chunks = append(chunks, []byte(mixed[i:i+1]))
		}
		check("mixed/byte-at-a-time", chunks)
	}

	// 畸形字节：非法首字节、过长编码、孤立续接字节、越界续接字节，以及空写入。
	malformed := []byte{
		0xFF, 0xFE, 0xC0, 0xC1, 0x80, 0xBF, 0xF5,
		0xE0, 0x80, 0x80, // 0xE0 后接低于 0xA0 的字节：非法
		0xED, 0xA0, 0x80, // 代理区编码：非法
		0xF0, 0x90, 0x80, 0x80, // 合法 4 字节
		0xF4, 0x90, 0x80, 0x80, // 超出 U+10FFFF：非法
		0xC2, 0x41, // 续接字节缺失/非法
		0x00, 0x7F, 0x41,
	}
	checkAllSplits("malformed", malformed)
	{
		chunks := make([][]byte, 0, len(malformed)+4)
		for i := range malformed {
			chunks = append(chunks, malformed[i:i+1])
		}
		// 在头尾穿插空写入，确认空写入不改变语义。
		withEmpty := append([][]byte{{}}, chunks...)
		withEmpty = append(withEmpty, []byte{}, []byte{})
		check("malformed/byte-at-a-time+empty", withEmpty)
	}

	// 所有单字节值逐个写入。
	{
		chunks := make([][]byte, 0, 256)
		for b := 0; b < 256; b++ {
			chunks = append(chunks, []byte{byte(b)})
		}
		check("all-single-bytes", chunks)
	}

	// 穷举所有两字节序列：整块写入与逐字节写入。
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			checkAllSplits("two-byte", []byte{byte(a), byte(b)})
		}
	}

	// 从覆盖所有首字节类别的字母表穷举长度 3..4 的尾部。
	alphabet := []byte{
		0x41, 0x80, 0xA0, 0xBF, // ASCII、续接、下界、上界
		0xC0, 0xC2, 0xDF, // 非法两字节首字节、合法两字节首字节
		0xE0, 0xED, 0xEF, // 三字节首字节（含受限的 E0/ED）
		0xF0, 0xF4, 0xF5, 0xFF, // 四字节首字节（含受限的 F0/F4、非法 F5/FF）
	}
	var walk func(prefix []byte, depth int)
	walk = func(prefix []byte, depth int) {
		for _, b := range alphabet {
			seq := append(append([]byte(nil), prefix...), b)
			checkAllSplits("tail", seq)
			if depth > 1 {
				walk(seq, depth-1)
			}
		}
	}
	walk(nil, 4)

	// 随机数据 + 随机分块，进一步覆盖组合边界。
	rng := rand.New(rand.NewSource(42))
	sources := [][]byte{
		[]byte(mixed),
		malformed,
		bytes.Repeat([]byte("中a🌍x"), 4),
	}
	for iter := 0; iter < 1000; iter++ {
		src := sources[rng.Intn(len(sources))]
		start := rng.Intn(len(src) + 1)
		end := start + rng.Intn(len(src)-start+1)
		data := src[start:end]
		chunks := [][]byte{}
		for pos := 0; pos < len(data); {
			step := 1 + rng.Intn(4)
			if pos+step > len(data) {
				step = len(data) - pos
			}
			chunks = append(chunks, data[pos:pos+step])
			pos += step
		}
		check("random", chunks)
	}
}

// TestUTF8CarryWriterNoPerWriteAlloc 断言稳态下携带续字节写入不再每次分配。
func TestUTF8CarryWriterNoPerWriteAlloc(t *testing.T) {
	cw := newUTF8CarryWriter(noopWriter{})
	// 先建立暂存尾部，使后续每次 Write 都进入"携带分块"路径。
	_, _ = cw.Write([]byte{0xE4, 0xB8})
	// 补齐"中"，同时留下新的暂存尾部，每次迭代结构相同。
	chunk := []byte{0xAD, 0xE4, 0xB8}
	allocs := testing.AllocsPerRun(200, func() {
		_, _ = cw.Write(chunk)
	})
	if allocs > 0 {
		t.Fatalf("每次携带写入仍产生 %.1f 次分配，期望 0（scratch 应被复用）", allocs)
	}
}

// BenchmarkUTF8CarryWriterChunked 测量稳态下携带续字节分块写入的分配情况。
// 配合 TestUTF8CarryWriterNoPerWriteAlloc 的断言，证明不再每次 make。
func BenchmarkUTF8CarryWriterChunked(b *testing.B) {
	cw := newUTF8CarryWriter(noopWriter{})
	_, _ = cw.Write([]byte{0xE4, 0xB8}) // 建立初始暂存
	chunk := []byte{0xAD, 0xE4, 0xB8}   // 补齐上一字符并留下新的暂存
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cw.Write(chunk)
	}
}
