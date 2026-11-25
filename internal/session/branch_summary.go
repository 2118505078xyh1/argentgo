package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/textutil"
)

// branchSummaryFilesKey 是遗弃分支摘要载荷中携带机械抽取文件清单的稳定键。
// 它与 text 并列：旧读取方只认 text，不受新增字段影响；新摘要读取同一键
// 实现跨嵌套累计。字段名固定为英文，保证转录跨语言、跨版本可读。
const branchSummaryFilesKey = "branch_files"

// branchSummaryPayload 是遗弃分支摘要的载荷。text 保持旧字段名与语义；
// branch_files 是新增的机械抽取清单（omitempty 让空清单不写入多余键）。
type branchSummaryPayload struct {
	Text  string       `json:"text"`
	Files *branchFiles `json:"branch_files,omitempty"`
}

// branchFiles 是遗弃分支上被读取与被修改的去重文件路径清单。
// 路径按字典序输出，因此相同输入总得到逐字节一致的摘要。
type branchFiles struct {
	Read     []string `json:"read,omitempty"`
	Modified []string `json:"modified,omitempty"`
}

// branchFilesMax 是每类清单的路径上限：保留最近出现的文件，防止超长分支把摘要撑爆。
const branchFilesMax = 64

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
	// 机械抽取被读/被改文件（无需 LLM）：先从本分支的全部记录提取，再并入链上
	// 已有的遗弃分支摘要清单，实现跨嵌套摘要累计。清单去重、排序且有界，
	// 以稳定的 [read: ...] / [modified: ...] 行附在叙事摘要之后。
	files := branchFilesIn(chain.records)
	if filesLine := files.renderLine(); filesLine != "" {
		if summary != "" && !strings.HasSuffix(summary, "\n") {
			summary += "\n"
		}
		summary += filesLine
	}
	if cfg.limit > 0 {
		// 先脱敏再裁剪；切点由 textutil.ClipRunes 吸附到字素簇边界，
		// 摘要进入上下文时不会留下悬空的组合标记或半个 emoji。
		summary = textutil.ClipRunes(summary, cfg.limit)
	}
	payload, err := json.Marshal(branchSummaryPayload{Text: summary, Files: &files})
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

// branchFilesIn 从分支记录中机械抽取被读/被改文件，并并入链上既有摘要携带的清单。
// 它遍历全部记录（不受摘要文本预算限制），只做 map 查找与 JSON 解码，不调用 LLM、
// 不触碰文件系统；返回的两类清单各自去重、保留最近出现项并按字典序排序。
func branchFilesIn(records []Record) branchFiles {
	readSeen, modifiedSeen := map[string]bool{}, map[string]bool{}
	var readOrder, modifiedOrder []string
	add := func(path string, modified bool) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if len([]rune(path)) > activityPathLimit {
			path = textutil.ClipRunes(path, activityPathLimit)
		}
		seen, order := readSeen, &readOrder
		if modified {
			seen, order = modifiedSeen, &modifiedOrder
		}
		if seen[path] {
			return
		}
		seen[path] = true
		*order = append(*order, path)
	}
	for _, record := range records {
		// 跨嵌套累计：上一条遗弃分支摘要的清单视为本分支的文件操作事实。
		if record.Kind == KindSummary {
			var payload branchSummaryPayload
			if json.Unmarshal(record.Data, &payload) == nil {
				for _, path := range payload.Files.Read {
					add(path, false)
				}
				for _, path := range payload.Files.Modified {
					add(path, true)
				}
			}
		}
		for _, call := range callsFromData(record.Data) {
			name := strings.ToLower(strings.TrimSpace(call.name))
			path := activityPath(call.args)
			if path == "" {
				path = topLevelFilePath(record.Data)
			}
			switch {
			case activityReadTools[name]:
				add(path, false)
			case activityEditTools[name]:
				add(path, true)
			}
		}
	}
	return branchFiles{
		Read:     sortedRecent(readOrder, readSeen, branchFilesMax),
		Modified: sortedRecent(modifiedOrder, modifiedSeen, branchFilesMax),
	}
}

// topLevelFilePath 兼容工具结果记录把路径直接放在顶层的旧布局
// （例如 {"name":"edit","path":"x.go"}），而 arguments/args/input 均为空。
func topLevelFilePath(raw json.RawMessage) string {
	return fieldString(raw, "path", "file_path", "file")
}

// sortedRecent 保留最近出现的 limit 项，再按字典序排序，输出确定且无重复。
func sortedRecent(order []string, seen map[string]bool, limit int) []string {
	if len(order) > limit {
		order = order[len(order)-limit:]
	}
	out := make([]string, 0, len(order))
	for _, path := range order {
		if seen[path] {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// renderLine 把非空清单渲染为稳定的单行文本（无清单时返回空串）。
// 只输出路径，不输出任何工具原文或参数值，因此不会把敏感参数带入摘要。
func (f branchFiles) renderLine() string {
	var parts []string
	if len(f.Read) > 0 {
		parts = append(parts, "[read: "+strings.Join(f.Read, ", ")+"]")
	}
	if len(f.Modified) > 0 {
		parts = append(parts, "[modified: "+strings.Join(f.Modified, ", ")+"]")
	}
	return strings.Join(parts, " ")
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
