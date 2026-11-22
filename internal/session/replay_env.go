package session

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"sort"
	"sync"
	"time"
)

// replay_env.go 提供可重放执行的"环境"原语：版本化环境指纹、确定性虚拟时钟、
// 确定性伪随机源，以及把乱序到达的结果按序号严格重排的收集器。
//
// 它们与 replay_verify.go 的区别：后者校验"会话重放后的状态"，这里约束的是
// "驱动重放的环境"——时间、随机数、结果顺序。只要这三者确定，同一段输入就能
// 得到同一段输出，eval 才能做可信的 A/B 对照与回归。全部为 stdlib-only，
// 不读写磁盘、不调用模型、不依赖真实时钟。

// replayEnvVersion 是环境指纹的格式版本。结构变化时抬版本，旧指纹显式失配，
// 避免把不同语义的指纹拿去比较。
const replayEnvVersion = 1

// ErrSeqOutOfOrder 表示序号收集器收到了重复或过旧的序号。
var ErrSeqOutOfOrder = errors.New("session: replay sequence out of order")

// ReplayEnv 描述一次可重放执行所处的环境。所有字段都参与指纹。
type ReplayEnv struct {
	Version     int       // 环境格式版本
	Seed        uint64    // 伪随机源种子
	Start       time.Time // 虚拟时钟起点，按 UTC 参与哈希
	ImplVersion string    // 被重放实现的版本标识，例如 "v0.8.1"
}

// EnvFingerprint 返回环境的版本化 sha256 指纹。
//
// 每个变长字符串都带 8 字节大端长度前缀，定长数值占固定宽度，时间统一取
// Unix 纳秒并转成 UTC，因此相邻字段不会串味，机器时区也不影响结果。
func EnvFingerprint(env ReplayEnv) [32]byte {
	h := sha256.New()
	writeEnvFingerprintUint64(h, uint64(env.Version))
	writeEnvFingerprintUint64(h, env.Seed)
	writeEnvFingerprintUint64(h, uint64(env.Start.UTC().UnixNano()))
	writeFingerprintString(h, env.ImplVersion)

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// EnvFingerprintText 返回带版本前缀的十六进制指纹，便于写进报告与日志。
func EnvFingerprintText(env ReplayEnv) string {
	sum := EnvFingerprint(env)
	return fmt.Sprintf("replay-env-v%d:%x", replayEnvVersion, sum)
}

// writeEnvFingerprintUint64 以 8 字节大端写入无符号整数。
// 字符串的写入复用 replay_verify.go 的 writeFingerprintString（长度前缀规则一致）。
func writeEnvFingerprintUint64(h hash.Hash, n uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	_, _ = h.Write(buf[:])
}

// VirtualClock 是一个只按 Advance 前进的确定性时钟。它不读真实时间，
// 因此同一脚本在两个进程里会得到完全相同的时间序列。
type VirtualClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewVirtualClock 以 start 为起点创建虚拟时钟。传入的位置信息被去掉，统一按 UTC 使用。
func NewVirtualClock(start time.Time) *VirtualClock {
	return &VirtualClock{now: start.UTC()}
}

// Now 返回当前虚拟时间。
func (c *VirtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance 把虚拟时间向前推进 d；负增量被忽略，避免脚本把时钟拨回过去。
func (c *VirtualClock) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// SeededRandom 是 mulberry32 的并发安全封装。标准库 math/rand 的具体算法
// 会随 Go 版本调整，不利于跨版本重放；这里固定算法，保证同种子同序列。
type SeededRandom struct {
	mu    sync.Mutex
	state uint32
}

// NewSeededRandom 以 seed 创建确定性伪随机源。
func NewSeededRandom(seed uint32) *SeededRandom {
	return &SeededRandom{state: seed}
}

// Next 返回下一个 32 位伪随机整数。
func (r *SeededRandom) Next() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state += 0x6d2b79f5
	t := r.state
	t = (t ^ (t >> 15)) * (t | 1)
	t ^= t + ((t ^ (t >> 7)) * (t | 61))
	return t ^ (t >> 14)
}

// Float64 返回 [0,1) 区间内的确定性伪随机浮点数。
func (r *SeededRandom) Float64() float64 {
	return float64(r.Next()>>8) / (1 << 24)
}

// SeqCollector 把乱序到达的结果按序号严格重排。
//
// 调用方从 next 开始提交序号；乱序（更大的序号）先缓存，重复的或小于 next 的
// 序号被拒绝。Drain 只返回从 next 起连续可达的那一段，缺口之后的条目继续
// 等待，因此重放端永远不会观察到跳号。
type SeqCollector struct {
	mu      sync.Mutex
	next    uint64
	pending map[uint64][]byte
	limit   int
}

// NewSeqCollector 创建从序号 0 开始、最多缓存 limit 个乱序条目的收集器。
// limit <= 0 时使用 1024，避免无界缓存。
func NewSeqCollector(limit int) *SeqCollector {
	if limit <= 0 {
		limit = 1024
	}
	return &SeqCollector{
		next:    0,
		pending: make(map[uint64][]byte),
		limit:   limit,
	}
}

// Add 提交一个带序号的结果。序号必须 >= next，且不能与已缓存的重复。
func (c *SeqCollector) Add(seq uint64, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq < c.next {
		return fmt.Errorf("%w: seq %d is older than next %d", ErrSeqOutOfOrder, seq, c.next)
	}
	if _, exists := c.pending[seq]; exists {
		return fmt.Errorf("%w: seq %d submitted twice", ErrSeqOutOfOrder, seq)
	}
	if len(c.pending) >= c.limit {
		return fmt.Errorf("%w: pending limit %d reached at seq %d", ErrSeqOutOfOrder, c.limit, seq)
	}
	c.pending[seq] = append([]byte(nil), value...)
	return nil
}

// Drain 返回从当前 next 起连续序号对应的值，并推进 next。返回值按序号升序，
// 且与传入切片不共享底层数组。
func (c *SeqCollector) Drain() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := make([]uint64, 0, len(c.pending))
	for seq := range c.pending {
		if seq >= c.next {
			keys = append(keys, seq)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	out := make([][]byte, 0, len(keys))
	for _, seq := range keys {
		if seq != c.next {
			break // 遇到缺口即停止，后续条目继续等待
		}
		value := c.pending[seq]
		delete(c.pending, seq)
		c.next++
		out = append(out, append([]byte(nil), value...))
	}
	return out
}
