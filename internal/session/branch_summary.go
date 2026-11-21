package session

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// ErrNoAbandonedBranch 表示转录中没有可供摘要的非活动分支。
var ErrNoAbandonedBranch = errors.New("session: no abandoned branch")

// AbandonedBranchOption 配置 SummarizeAbandonedBranch 的分支选择与摘要长度。
type AbandonedBranchOption func(*abandonedBranchConfig)

type abandonedBranchConfig struct {
	target string // 要摘要的分支尖端 id；为空时取文件中最后出现的非活动分支
	limit  int    // 摘要文本的符文上限；非正数表示不额外截断
}

// WithAbandonedBranchTarget 指定要摘要的分支尖端记录 id。
// 传入分叉点或该分支上任意记录的 id 都会选中对应的分支；未指定时取最近分叉出的那条。
func WithAbandonedBranchTarget(id string) AbandonedBranchOption {
	return func(cfg *abandonedBranchConfig) { cfg.target = id }
}

// WithAbandonedBranchLimit 把摘要文本限制在 limit 个符文内；非正数表示不额外截断。
// Summarize 自身的段落上限始终生效，这里只追加一个确定的上下文边界。
func WithAbandonedBranchLimit(limit int) AbandonedBranchOption {
	return func(cfg *abandonedBranchConfig) { cfg.limit = limit }
}

// branchChain 是一条非活动分支：从分叉点之后的第一条记录往下，连同其分叉点 id。
type branchChain struct {
	root      Record
	forkPoint string
	records   []Record
}

// SummarizeAbandonedBranch 为 path 处转录里离开的非活动分支生成一条确定性的 KindSummary 记录。
//
// 它复用 Summarize，不调用 LLM，也不写回文件：调用方决定是否以及如何追加这条记录，
// 因此默认的 Fork 字节输出不受影响。分支内容先经 security.RedactJSON 脱敏，再投影为
// 对话条目；生成的记录带 RetainInThread，能熬过压缩，却不会被 SelectForChildren
// 植入子会话。
func SummarizeAbandonedBranch(path string, opts ...AbandonedBranchOption) (Record, error) {
	var cfg abandonedBranchConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	branches, err := Branches(path)
	if err != nil {
		return Record{}, err
	}
	if len(branches) == 0 {
		return Record{}, fmt.Errorf("%w: %s", ErrNoAbandonedBranch, path)
	}
	records, err := readRecords(path)
	if err != nil {
		return Record{}, err
	}
	byID := indexByID(records)

	// 活动叶子与 Branches 的 Active 标记一致；拿不到标记时退回文件最后一条。
	activeID := ""
	for _, branch := range branches {
		if branch.Active {
			activeID = branch.ID
			break
		}
	}
	if activeID == "" {
		activeID = records[len(records)-1].ID
	}
	activeIDs := make(map[string]bool, len(records))
	for current := activeID; current != ""; {
		if activeIDs[current] {
			break // 环保护的防御性检查。
		}
		activeIDs[current] = true
		record, ok := byID[current]
		if !ok {
			break
		}
		current = record.Parent
	}

	// 按文件顺序收集每个从活动路径分叉出去的分支：分叉点是其 parent，
	// 分支本身是从该点往下、不在活动路径上的全部记录。
	children := make(map[string][]Record, len(records))
	for _, record := range records {
		if record.Parent != "" {
			children[record.Parent] = append(children[record.Parent], record)
		}
	}
	chains := make([]branchChain, 0, 2)
	for _, record := range records {
		if activeIDs[record.ID] || !activeIDs[record.Parent] {
			continue
		}
		chain := branchChain{root: record, forkPoint: record.Parent}
		seen := map[string]bool{record.ID: true}
		var collect func(Record)
		collect = func(current Record) {
			chain.records = append(chain.records, current)
			for _, child := range children[current.ID] {
				if activeIDs[child.ID] || seen[child.ID] {
					continue
				}
				seen[child.ID] = true
				collect(child)
			}
		}
		collect(record)
		chains = append(chains, chain)
	}
	if len(chains) == 0 {
		return Record{}, fmt.Errorf("%w: %s", ErrNoAbandonedBranch, path)
	}

	chain, err := chooseAbandonedChain(chains, cfg.target)
	if err != nil {
		return Record{}, err
	}

	// 先脱敏再投影：RedactJSON 既清洗凭据命名键的整个子树，也清洗字符串里的凭据形状片段。
	entries := make([]Entry, 0, len(chain.records))
	for _, record := range chain.records {
		cleaned := record
		cleaned.Data = json.RawMessage(security.RedactJSON(record.Data))
		if entry, ok := messageEntry(cleaned); ok {
			entries = append(entries, entry)
		}
	}
	summary := Summarize(entries, 0).Summary
	if cfg.limit > 0 {
		runes := []rune(summary)
		if len(runes) > cfg.limit {
			summary = string(runes[:cfg.limit])
		}
	}
	payload, err := json.Marshal(map[string]string{"text": summary})
	if err != nil {
		return Record{}, fmt.Errorf("session: encode branch summary: %w", err)
	}
	// 必须显式标记：KindSummary 的自动默认值是 RetainAlways，会把摘要泄漏进子会话。
	data, err := SetRetention(payload, RetainInThread)
	if err != nil {
		return Record{}, err
	}
	return Record{
		ID:     "branch-summary-" + chain.root.ID,
		Parent: chain.forkPoint,
		Kind:   KindSummary,
		Data:   data,
	}, nil
}

// chooseAbandonedChain 按调用方给定的 id 选择分支；未指定时返回文件中最后出现的那条。
func chooseAbandonedChain(chains []branchChain, target string) (branchChain, error) {
	if target == "" {
		return chains[len(chains)-1], nil
	}
	for _, chain := range chains {
		if chain.root.ID == target || chain.forkPoint == target {
			return chain, nil
		}
		for _, record := range chain.records {
			if record.ID == target {
				return chain, nil
			}
		}
	}
	return branchChain{}, fmt.Errorf("session: unknown abandoned branch target %q", target)
}
