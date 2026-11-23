package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
)

// 本文件实现 killi 决策 21 的"日志就是实际发送的字节"：把请求体原样存入会话。
// session 的 kind 是开放字符串，messageEntry 对未知 kind 返回 false，因此快照
// 记录不会进入 Messages()/对话视图，默认读写路径逐字节不变。

// KindRequestSnapshot 是一条实际请求字节快照。
const KindRequestSnapshot = "request_snapshot"

// RequestSnapshotPayload 是一次请求的线协议快照。
// Body 用 []byte 存储（JSON 编码为 base64），保证字节往返精确；
// 时间戳等元数据放在 Record.TS，不污染正文。
type RequestSnapshotPayload struct {
	Version    int      `json:"version"`
	Protocol   string   `json:"protocol"`
	Model      string   `json:"model"`
	Body       []byte   `json:"body"`
	BodySHA256 [32]byte `json:"body_sha256"`
}

// valid 校验正文未被篡改。快照是审计材料，读取端可据此判断记录完整性。
func (p RequestSnapshotPayload) valid() bool {
	return p.Version == 1 && sha256.Sum256(p.Body) == p.BodySHA256
}

// RequestSnapshots 从记录中提取所有请求快照，按记录顺序返回。
// 坏记录（无法解析或 hash 校验失败）被跳过；这是纯只读函数，不修改入参。
func RequestSnapshots(records []Record) []RequestSnapshotPayload {
	out := make([]RequestSnapshotPayload, 0)
	for _, rec := range records {
		if rec.Kind != KindRequestSnapshot {
			continue
		}
		var p RequestSnapshotPayload
		if err := json.Unmarshal(rec.Data, &p); err != nil || !p.valid() {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ReplaySnapshot 描述一次可用于 resume 字节回放的快照及其在活动路径上的位置。
type ReplaySnapshot struct {
	Payload       RequestSnapshotPayload
	SnapshotID    string
	HasLaterReply bool // 快照之后是否已有 assistant 回复（有则说明请求已被答复，无需回放）
	HasCompaction bool // 快照之后是否发生了压缩（有则快照字节对应的是已丢弃的历史，必须失效）
}

// ReplaySnapshotFromPath 从已按活动路径排列的记录中找出最后一条有效快照。
// 它只看活动分支，坏快照（hash 校验失败）被跳过。返回的 HasLaterReply /
// HasCompaction 供调用方判定快照是否仍可作回放基线。
func ReplaySnapshotFromPath(path []Record) *ReplaySnapshot {
	lastIdx := -1
	var payload RequestSnapshotPayload
	for i, rec := range path {
		if rec.Kind != KindRequestSnapshot {
			continue
		}
		var p RequestSnapshotPayload
		if err := json.Unmarshal(rec.Data, &p); err != nil || !p.valid() {
			continue
		}
		lastIdx = i
		payload = p
	}
	if lastIdx < 0 {
		return nil
	}
	out := &ReplaySnapshot{Payload: payload, SnapshotID: path[lastIdx].ID}
	for _, rec := range path[lastIdx+1:] {
		switch rec.Kind {
		case KindAssistant:
			out.HasLaterReply = true
		case KindCompaction:
			out.HasCompaction = true
		}
	}
	return out
}

// ReplaySnapshotOf 返回会话活动路径上的最新回放快照；无有效快照时返回 nil。
func (s *Session) ReplaySnapshotOf() *ReplaySnapshot {
	records, err := s.Records()
	if err != nil {
		return nil
	}
	return ReplaySnapshotFromPath(s.activePath(records))
}

// AppendRequestSnapshot 把一次请求字节追加为快照记录。
// 调用方负责确保 Body 不被后续复用修改（json.Marshal 会立即拷贝）。
func AppendRequestSnapshot(s *Session, p RequestSnapshotPayload) (Record, error) {
	if s == nil {
		return Record{}, errors.New("append request snapshot: nil session")
	}
	p.Version = 1
	p.BodySHA256 = sha256.Sum256(p.Body)
	encoded, err := json.Marshal(p)
	if err != nil {
		return Record{}, err
	}
	return s.AppendRecord(KindRequestSnapshot, json.RawMessage(encoded))
}
