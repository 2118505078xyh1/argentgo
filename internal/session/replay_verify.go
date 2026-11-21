package session

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strconv"
)

// replay_verify.go 提供重放状态的可导出校验原语。
//
// 这里的函数全部是 stdlib-only 的纯函数：只读入参、不写 JSONL、不调用模型，
// 也不 import llm/agent/app。它们目前没有调用方，存在意义是给上层提供一个
// 稳定、可单测的"重放 → 指纹/摘要"接缝。
//
// 指纹只用于内存比较（例如对比两段记录流重放后的规范化状态），绝不写入
// JSONL，也不作为持久化校验和；它是对 ReplayState 规范字段的一次性投影。

var (
	// ErrReplayMismatch 表示重放得到的指纹与预期不一致。
	ErrReplayMismatch = errors.New("session: replay fingerprint mismatch")
)

// ReplayFingerprint 对 ReplayState 的规范化字段计算 sha256 指纹。
//
// 参与哈希的字段依次为：ActiveLeaf、RecordCount、按字典序排序后的
// UnpairedToolCalls（先写元素个数，再逐项写）、LastKind、Complete。
// 每个字符串都以 8 字节大端长度前缀开头，定长整数与布尔各占固定宽度，
// 因此相邻字段的拼接不会串味（"a"+"bc" 与 "ab"+"c" 指纹不同）。
//
// 函数只读入参：会先复制 UnpairedToolCalls 再排序，绝不改动 st 及其切片；
// nil 与空切片视为同一规范状态。
func ReplayFingerprint(st ReplayState) [32]byte {
	h := sha256.New()
	writeFingerprintString(h, st.ActiveLeaf)
	writeFingerprintInt(h, st.RecordCount)

	calls := make([]string, len(st.UnpairedToolCalls))
	copy(calls, st.UnpairedToolCalls)
	sort.Strings(calls)
	writeFingerprintInt(h, len(calls))
	for _, id := range calls {
		writeFingerprintString(h, id)
	}

	writeFingerprintString(h, st.LastKind)
	if st.Complete {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// writeFingerprintString 写入 8 字节大端长度前缀，再写入 UTF-8 字节。
// 长度前缀消除了相邻字符串字段拼接后的歧义。
func writeFingerprintString(h hash.Hash, s string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(s)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(s))
}

// writeFingerprintInt 以 8 字节大端写入定长整数。
func writeFingerprintInt(h hash.Hash, n int) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(n))
	_, _ = h.Write(buf[:])
}

// VerifyReplayFingerprint 重放 records 并把指纹与 want 比对。
//
// 不一致时返回用 %w 包装 ErrReplayMismatch 的错误，调用方可用 errors.Is
// 分类；一致时返回 nil。入参 records 及其 Data 均不被修改。
func VerifyReplayFingerprint(records []Record, want [32]byte) error {
	got := ReplayFingerprint(Replay(records))
	if got != want {
		return fmt.Errorf("%w: got %x, want %x", ErrReplayMismatch, got, want)
	}
	return nil
}

// ReplaySummary 返回 ReplayState 的稳定中文摘要，形如
// "记录 N 条，未配对工具调用 M 个，会话完整/不完整"。
// 摘要只取数量与完整状态，不暴露 id 或调用内容；纯函数，不改入参。
func ReplaySummary(st ReplayState) string {
	status := "会话不完整"
	if st.Complete {
		status = "会话完整"
	}
	return "记录 " + strconv.Itoa(st.RecordCount) + " 条，未配对工具调用 " +
		strconv.Itoa(len(st.UnpairedToolCalls)) + " 个，" + status
}
