package session

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// seqKey 是 SetSeq 写入记录 JSON 数据包络的保留字段。与 retentionKey 一样，
// 它位于现有的 Data 块中，因此无需改动 Record schema，也无需增加 JSONL 列，
// 且 Data 仍逐字到达对话视图。
//
// 记录的序号是单调递增的追加顺序计数器。与 TS 不同，当多条记录在同一时钟周期内写入时，
// 它不会冲突，从而使"最新"和分叉点具有确定性。
const seqKey = "_seq"

// GetSeq 从记录的 JSON 数据包络中读取显式序号。只有当 JSON 对象的 seqKey 持有
// 一个能放进 int64 的非负整数时，才返回 (seq, true)。缺失、格式错误、非对象、null、
// 小数、字符串和负值都返回 (0, false)，因此序号出现之前写入的记录（以及手写的记录）
// 会回退到派生的顺序。
func GetSeq(data []byte) (int64, bool) {
	var fields map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &fields) != nil || fields == nil {
		return 0, false
	}
	raw, ok := fields[seqKey]
	if !ok {
		return 0, false
	}
	// json.Unmarshal(null, &int64) 会成功并保留零值，因此在这里拒绝它。
	if string(bytes.TrimSpace(raw)) == "null" {
		return 0, false
	}
	var seq int64
	if err := json.Unmarshal(raw, &seq); err != nil || seq < 0 {
		return 0, false
	}
	return seq, true
}

// SetSeq 将 seq 注入记录 JSON 数据包络中 seqKey 对应的位置，保留所有其他字段。
// 空的 data 切片视为空对象。非空但不是 JSON 对象（或格式错误）的切片会报错：
// 替换一个无法保全其他字段的载荷，比拒绝打标记更糟糕。负序号会被拒绝，
// 因为序号必须可用作单调的重放点。
func SetSeq(data []byte, seq int64) ([]byte, error) {
	if seq < 0 {
		return nil, fmt.Errorf("session: set seq: negative ordinal %d", seq)
	}
	fields := make(map[string]json.RawMessage)
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, fmt.Errorf("session: set seq: parse data: %w", err)
		}
	}
	if fields == nil { // JSON "null" 会反序列化为 nil map。
		fields = make(map[string]json.RawMessage)
	}
	value, err := json.Marshal(seq)
	if err != nil {
		return nil, fmt.Errorf("session: set seq: encode value: %w", err)
	}
	fields[seqKey] = value

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("session: set seq: encode data: %w", err)
	}
	return out, nil
}

// SeqOf 报告 r 携带的显式序号（如果有）。
func SeqOf(r Record) (int64, bool) { return GetSeq(r.Data) }

// RecordSeqs 按相同顺序为每条记录返回一个有效序号。带有显式 _seq 的记录使用它；
// 没有的记录从其在数组中的零基索引派生序号。派生的回退让分叉和重放辅助函数
// 能处理序号出现之前的旧转录。
//
// 只有当显式序号按文件顺序递增时（仅追加会话就是如此），混用显式和派生的序号才有意义。
// 写入方应为每条新记录调用 SetSeq 打标记，而不是依赖回退。
func RecordSeqs(records []Record) []int64 {
	seqs := make([]int64, len(records))
	for i := range records {
		if seq, ok := GetSeq(records[i].Data); ok {
			seqs[i] = seq
		} else {
			seqs[i] = int64(i)
		}
	}
	return seqs
}

// NewestBySeq 返回序号最高的记录。显式序号具有权威性：任何携带 _seq 的记录都比
// 没有它的旧记录更新，而在已打标记的记录中，seq 更大者胜出。
//
// 当两个候选都没有 seq（或 seq 相同）时，用 TS 再以 ID 决胜——与 Store.List 使用的
// "时间戳较晚者优先，再以 id 较大者优先"规则相同。这使多条记录共享同一秒时"最新"仍然确定，
// 而正是这种模糊性让仅基于时间戳的恢复变得不稳定。空输入返回零值 Record。
func NewestBySeq(records []Record) Record {
	if len(records) == 0 {
		return Record{}
	}
	newest := records[0]
	newestSeq, newestHasSeq := SeqOf(newest)
	for _, record := range records[1:] {
		seq, hasSeq := SeqOf(record)
		switch {
		case hasSeq != newestHasSeq:
			// 已打标记的记录始终优先于未打标记的记录。
			if hasSeq {
				newest, newestSeq, newestHasSeq = record, seq, hasSeq
			}
		case hasSeq && seq != newestSeq:
			if seq > newestSeq {
				newest, newestSeq = record, seq
			}
		default:
			// 两者都没有 seq，或它们的 seq 相同。
			if newerByTimeAndID(record, newest) {
				newest, newestSeq, newestHasSeq = record, seq, hasSeq
			}
		}
	}
	return newest
}

// newerByTimeAndID 按 Store.List 的方式决胜：时间戳较晚者胜出，
// 然后是字典序较大的 id。
func newerByTimeAndID(a, b Record) bool {
	if a.TS.Equal(b.TS) {
		return a.ID > b.ID
	}
	return a.TS.After(b.TS)
}

// ForkBeforeSeq 返回有效序号 < seq 的前导记录，保持原始顺序。它是在某个序号处分叉的
// 重放前缀：严格位于目标之前的全部内容，不包含目标及其之后的任何内容。
// 有效序号来自 RecordSeqs，因此没有 _seq 的旧记录按数组索引排序，仍可分叉。
//
// 结果是副本，绝不是调用方底层数组的切片。空前缀（目标位于第一个序号处或之下）返回 nil；
// 高于所有序号的目标返回整个切片的副本。调用方应保持序号按文件顺序递增——
// 前缀在第一个 >= seq 的序号处停止。
func ForkBeforeSeq(records []Record, seq int64) []Record {
	seqs := RecordSeqs(records)
	i := 0
	for i < len(records) && seqs[i] < seq {
		i++
	}
	if i == 0 {
		return nil
	}
	out := make([]Record, i)
	copy(out, records[:i])
	return out
}
