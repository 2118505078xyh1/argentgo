package session

import "sort"

// LineageLink 描述血缘链中的一跳："来源会话 SessionID 的第 RecordID 条记录"。
// 它对应 CLI 树视图里 link=session/record 的显示值（见 internal/cli/sessions.go）。
type LineageLink struct {
	SessionID string
	RecordID  string
}

// defaultLineageDepth 是 maxDepth <= 0 时采用的默认追溯层数。
const defaultLineageDepth = 16

// TraceLineage 沿 Attempt 的 SourceSession/SourceRecord 追溯分叉血缘链，返回去重后
// 按深度（离链起点的跳数）从小到大排列的来源链接。每个链接表示"从会话 SessionID
// 的 RecordID 条记录分叉而来"；没有指定 RecordID 时为空字符串（CLI 只显示会话 id）。
//
// 链式规则：一条链接 (S, R) 表示分叉来源位于会话 S 的记录 R；若 R 同时也是另一条
// 链接的来源会话 id，则后者是再往上一层的来源，由此串起多层血缘。输出采用广度优先
// 遍历，因此第 1 层是链起点的直接来源，第 2 层是它的上游，以此类推。
//
// 行为保证：
//   - 去重：重复的 (SessionID, RecordID) 只输出一次；
//   - 防环：已访问过的会话 id 不再展开，链成环或自环都会终止，不会死循环；
//   - maxDepth <= 0 时使用默认值 16；遍历层数不会超过 limit；
//   - 确定性：根按字典序、每条来源会话的链接按 (RecordID, SessionID) 字典序排列，
//     因此相同输入（无论 attempt 顺序）总得到相同输出；
//   - CJK 安全：会话 id 与记录 id 作为普通 Go 字符串处理，不按字节/符文切片，
//     中文 id 原样保留，排序使用 UTF-8 字节序（稳定且可复现）；
//   - 纯函数：只读入参，不修改 attempts 及其元素，每次调用返回全新的切片。
//
// SourceSession 为空的 Attempt 不携带来源信息，会被跳过。没有可输出的链接时返回 nil。
func TraceLineage(attempts []Attempt, maxDepth int) []LineageLink {
	limit := maxDepth
	if limit <= 0 {
		limit = defaultLineageDepth
	}

	// 收集去重后的来源边：SourceSession -> LineageLink。
	// recordIDs 记录所有作为 RecordID 出现的 id，用于识别链起点（根）。
	edges := make(map[string][]LineageLink)
	recordIDs := make(map[string]bool)
	for _, attempt := range attempts {
		if attempt.SourceSession == "" {
			continue
		}
		link := LineageLink{SessionID: attempt.SourceSession, RecordID: attempt.SourceRecord}
		links := edges[attempt.SourceSession]
		duplicate := false
		for _, existing := range links {
			if existing == link {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		edges[attempt.SourceSession] = append(links, link)
		if attempt.SourceRecord != "" {
			recordIDs[attempt.SourceRecord] = true
		}
	}
	if len(edges) == 0 {
		return nil
	}

	// 确定性：每条来源会话内部的链接按 (RecordID, SessionID) 排序。
	for source := range edges {
		sort.Slice(edges[source], func(i, j int) bool {
			a, b := edges[source][i], edges[source][j]
			if a.RecordID != b.RecordID {
				return a.RecordID < b.RecordID
			}
			return a.SessionID < b.SessionID
		})
	}

	// 根 = 作为来源会话出现、却从不作为记录 id 出现的节点（没有上游的来源）。
	// 若所有节点都带上游（纯环），退化为把所有来源会话当作根，保证仍能输出链接。
	roots := make([]string, 0, len(edges))
	for source := range edges {
		if !recordIDs[source] {
			roots = append(roots, source)
		}
	}
	if len(roots) == 0 {
		for source := range edges {
			roots = append(roots, source)
		}
	}
	sort.Strings(roots)

	// 广度优先逐层展开：visited 防环，emitted 去重，frontier 为下一层的会话 id。
	visited := make(map[string]bool, len(edges))
	emitted := make(map[LineageLink]bool)
	out := make([]LineageLink, 0)
	frontier := roots
	for depth := 1; depth <= limit && len(frontier) > 0; depth++ {
		next := make([]string, 0)
		for _, source := range frontier {
			if visited[source] {
				continue
			}
			visited[source] = true
			for _, link := range edges[source] {
				if emitted[link] {
					continue
				}
				emitted[link] = true
				out = append(out, link)
				if link.RecordID != "" && !visited[link.RecordID] {
					next = append(next, link.RecordID)
				}
			}
		}
		frontier = next
	}
	return out
}
