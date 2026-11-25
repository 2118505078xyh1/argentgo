// Package session 将对话存储为 JSONL 文件，其记录构成一棵树。
//
// 每个会话有一个目录，其中存放一个文件。每一行都是一条记录
// `{"id","parent","ts","kind","data"}`；记录的 parent 是它所跟随记录的 id，
// 因此分支永远不会重写历史。模型看到的对话是从根到活动叶节点的路径，
// 而压缩记录会用摘要替换该路径中较旧的部分。
package session

import (
	"encoding/json"
	"os"
	"runtime"
	"sync"
	"time"
)

// store 写入的记录 kind。kind 是开放的：未知 kind 会被逐字保留，并被对话视图忽略。
const (
	KindHeader     = "header"
	KindMessage    = "message"
	KindUser       = "user"
	KindAssistant  = "assistant"
	KindTool       = "tool"
	KindSummary    = "summary"
	KindCompaction = "compaction"
	KindCheckpoint = "checkpoint"
	KindAttempt    = "attempt"
	KindTitle      = "title"
)

const (
	fileExt    = ".jsonl"
	titleLimit = 60
	maxLineLen = 16 << 20

	// keepAppendFD 在 Windows 上为 false：那里的 os.OpenFile 不请求 FILE_SHARE_DELETE，
	// 因此持有的句柄会阻塞对会话文件的 os.Remove/os.Rename（并破坏 t.TempDir 清理）。
	// id 索引在所有平台上仍然消除了 O(n^2) 的重复读取；Windows 只是每次追加重复执行
	// 开销很小的 open/write/close 三步骤。
	keepAppendFD = runtime.GOOS != "windows"
)

// Record 是会话文件中的一行。
type Record struct {
	ID     string          `json:"id"`
	Parent string          `json:"parent"`
	TS     time.Time       `json:"ts"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`
	// Ignorable 标记未知 kind 可被旧客户端安全跳过（清单 #186）。
	// 未标记的未知 kind 在读取时 fail-closed 报错，不再静默丢弃。
	Ignorable bool `json:"ignorable,omitempty"`
}

// Entry 是线性对话中的一条消息。
type Entry struct {
	ID      string
	Parent  string
	Role    string // system | user | assistant | tool
	Content string
	// Reasoning 是 assistant 轮次中模型的思考文本。它是仅存在于转录中的元数据：
	// 在旧记录和非 assistant 轮次上为空，未设置时从归档中省略，以便旧字节保持一致。
	Reasoning string `json:"Reasoning,omitempty"`
	// ReasoningSignature 是 assistant 轮次中模型思考文本的签名。与 Reasoning 一样，
	// 它仅存在于转录中，未设置时从归档中省略，以便旧字节保持一致。
	ReasoningSignature string `json:"ReasoningSignature,omitempty"`
	Name               string // 记录携带时的工具名称
	ToolCallID         string
	TS                 time.Time
	Data               json.RawMessage
}

// Header 是每个会话的第一条记录。
type Header struct {
	Title   string    `json:"title,omitempty"`
	Created time.Time `json:"created"`
	// Provider 与 Model 记录会话使用的模型，供 resume 时沿用。
	// 它们是加法字段：omitempty 让旧会话写入的字节保持不变，
	// 缺少这两个键的旧 JSON 也能照常解码为空字符串。
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Session 是单个会话文件的仅追加句柄。
type Session struct {
	mu     sync.Mutex
	dir    string
	id     string
	active string
	// file 是会话打开或创建时解析出的精确磁盘路径。发现过程以不区分大小写的方式
	// 匹配 ".jsonl" 后缀，因此转录文件可能命名为 "Mixed.JSONL"；从 id+fileExt
	// 派生路径会指向 "Mixed.jsonl"，在区分大小写的文件系统上什么也读不到。
	// 仅对于测试中直接由 dir+id 构造的会话为空。
	file string

	// 追加状态在第一次 AppendRecord 时惰性构建，并在支撑文件未被改动时复用。
	// 以下所有字段都由 mu 保护。重建它意味着一次完整的 JSONL 读取；
	// 稳态追加只写一行并更新这些字段，避免 O(n^2) 的重复解析。
	ids      map[string]struct{} // 当前文件中记录的 id
	lastID   string              // 按文件顺序最后一条记录的 id
	nextSeq  int                 // 下一个待生成的 n%06d 候选 id
	fd       *os.File            // 长生命周期的 O_CREATE|O_WRONLY|O_APPEND 句柄（Windows 上为 nil）
	fdSize   int64               // 上次同步时的文件大小
	fdMTime  time.Time           // 上次同步时的修改时间
	lastInfo os.FileInfo         // 上次同步时的路径标识（Windows 上为 inode/卷）

	// bufferedWrite 是显式开启的批量写模式，默认关闭以保持逐行写路径的字节与 IO 行为不变。
	// 开启后 Append 只把编码后的 JSONL 行暂存到 pending，直到 Flush/Close 才一次性落盘，
	// 把大量小记录的多次系统调用合并为一次写入。
	bufferedWrite bool
	pending       []byte // 尚未落盘的 JSONL 行；仅在 bufferedWrite 开启时非空

	// readRecordsFn 由测试设置时，会覆盖用于构建或重建索引的全量读取。
	// Records() 刻意始终使用 readRecords。
	readRecordsFn func(string) ([]Record, error)

	// appendLockPath 是该会话跨进程锁 sidecar 的惰性缓存路径；由 mu 保护。
	// 同一句柄的每次追加都复用它，避免重复做字符串清理与拼接。
	appendLockPath string
}

// ID 是会话 id，也就是去掉扩展名后的文件名。
func (s *Session) ID() string { return s.id }

// Dir 报告存放会话文件的目录。
func (s *Session) Dir() string { return s.dir }
