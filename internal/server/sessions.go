package server

import (
	"encoding/json"
	"time"
)

// SessionInfo 为 session.list 汇总一条已存储的会话。
// Status 是执行状态的可选投影（idle/planning/working/done/error）：未接入执行
// 状态注册表的存储留空并由 omitempty 省略，既有 wire 形态逐字节不变。
type SessionInfo struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Updated time.Time `json:"updated"`
	Status  string    `json:"status,omitempty"`
}

// SessionEvent 是会话树中的一条记录。其形态对应 JSONL
// 会话约定（id/parent/kind/ts/data），使集成方可以适配
// internal/session，而无需在这里重塑记录。
type SessionEvent struct {
	ID     string          `json:"id,omitempty"`
	Parent string          `json:"parent,omitempty"`
	Kind   string          `json:"kind"`
	TS     time.Time       `json:"ts"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// SessionStore 是 RPC 和 ACP 传输使用的会话存储的
// 读取侧。
type SessionStore interface {
	List() ([]SessionInfo, error)
	Load(id string) ([]SessionEvent, error)
}

// SessionBranch 描述会话 id/parent 树中的一条记录，供 session.tree RPC
// 使用。它刻意使用显式的 wire 形态，使服务器不必 import internal/session：
// 集成方（如 app 适配器）负责把内部记录映射到这里。
// 字段顺序同时决定 JSON 对象中的键顺序。
type SessionBranch struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Kind   string `json:"kind"`
	Depth  int    `json:"depth"`
	Active bool   `json:"active,omitempty"`
	Label  string `json:"label,omitempty"`
	Fork   bool   `json:"fork,omitempty"`
}

// SessionTreeStore 是注入的 SessionStore 可以提供的可选分支读取端。
// 没有实现该接口的存储仍可正常支撑 session.list/session.load，
// session.tree 会返回干净的 "not configured" 错误。
type SessionTreeStore interface {
	Branches(id string) ([]SessionBranch, error)
}

// SessionStatusStore 是注入的 SessionStore 可以提供的可选执行状态读取端。
// Snapshot 返回会话 id → 状态字符串（idle/planning/working/done/error）的快照；
// 状态值用字符串而非 agent 类型，使 server 保持对 agent 包的独立。
// 未实现该接口的存储仍可正常支撑 session.list/session.load，只是没有状态字段。
type SessionStatusStore interface {
	Snapshot() map[string]string
}
