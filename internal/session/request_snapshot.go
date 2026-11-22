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
