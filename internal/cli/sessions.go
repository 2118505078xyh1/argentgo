package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// runSessions 实现 `sessions list|show|checkout|fork-at|list-messages|health|fidelity|rm|find|--grep|tree|undo|retry|discard-attempt|name|export|import`。
func runSessions(app App, _ context.Context, args []string) int {
	if len(args) == 0 {
		sessionsUsage(app.IO.Err)
		return 2
	}
	store := app.sessionStore()
	switch args[0] {
	case "-h", "--help":
		sessionsUsage(app.IO.Out)
		return 0
	case "list":
		return sessionsListWithJSON(app, store, args[1:])
	case "show":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsShow(app, store, args[1])
	case "checkout":
		if len(args) < 3 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsCheckout(app, args[1], args[2])
	case "fork-at":
		if len(args) < 3 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsForkAt(app, args[1], args[2])
	case "health":
		return sessionsHealth(app, args[1:])
	case "list-messages":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsListMessages(app, args[1])
	case "fidelity":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsFidelity(app, args[1], args[2:])
	case "rm":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		if err := store.Delete(args[1]); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "removed %s\n", args[1])
		return 0
	case "find":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsFind(app, store, strings.Join(args[1:], " "))
	case "--grep":
		query, limit, filter, err := parseGrepArgs(args[1:])
		if err != nil {
			fmt.Fprintf(app.IO.Err, "argentgo: %v\n", err)
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsGrep(app, store, query, limit, filter)
	case "tree":
		// tree 支持省略 id（渲染最近会话）以及可选的 --depth 上界，
		// 因此把剩余参数整体交给 sessionsTree 解析。
		return sessionsTree(app, args[1:])
	case "undo":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsUndo(app, args[1], args[2:])
	case "retry":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsRetry(app, args[1], args[2:])
	case "discard-attempt":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsDiscardAttempt(app, args[1], args[2:])
	case "name":
		if len(args) < 3 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsName(app, args[1], strings.Join(args[2:], " "))
	case "export":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsExport(app, store, args[1], args[2:])
	case "import":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsImport(app, args[1], args[2:])
	default:
		fmt.Fprintf(app.IO.Err, "argentgo: unknown sessions command %q\n", args[0])
		sessionsUsage(app.IO.Err)
		return 2
	}
}

func (a App) sessionStore() SessionStore {
	if a.Sessions != nil {
		return a.Sessions
	}
	return &fsStore{dir: a.sessionDir()}
}

func sessionsList(app App, store SessionStore) int {
	infos, err := store.List()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sortSessions(infos)
	for _, info := range infos {
		fmt.Fprintf(app.IO.Out, "%s\t%s\t%s\n", info.ID, formatTime(info.Updated), info.Title)
	}
	return 0
}

// checkpointGuardEnv 是 checkout 还原目标守卫的 opt-in 开关。只有值为 "1"
// （忽略首尾空白）时才开启，默认关闭且 checkout 行为逐字节不变。
const checkpointGuardEnv = "ARGENT_CHECKPOINT_GUARD"

// checkoutGuardEnabled 读取可选的 checkout 守卫开关。刻意只认 "1"，
// 与会话包内其它 opt-in 读点保持一致；显式关闭（如 "0"）走原路径。
func checkoutGuardEnabled(app App) bool {
	return strings.TrimSpace(app.env(checkpointGuardEnv)) == "1"
}

// sessionsCheckout 打开会话并把活动叶节点切换到指定记录。活动指针仅存在于
// 内存中，因此命令本身只报告切换结果；后续使用该句柄（例如 --at 恢复流程中的
// OpenSessionAt）追加的消息会挂到这条分支，而不复制既有历史。
// opt-in 开启时，先让 store 对转录目标做只读校验，拒绝逃逸或已消失的目标。
func sessionsCheckout(app App, id, entryID string) int {
	store, err := session.Open(app.sessionDir())
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if checkoutGuardEnabled(app) {
		store = store.WithCheckpointGuard(session.NewCheckoutGuard())
	}
	sess, err := store.OpenSessionAt(id, entryID)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	leaf, ok := sess.ActiveLeafID()
	if !ok {
		fmt.Fprintln(app.IO.Err, "error: session has no active record")
		return 1
	}
	fmt.Fprintf(app.IO.Out, "checked out %s at %s\n", sess.ID(), leaf)
	return 0
}

// sessionsForkAt 在指定用户消息处分叉出新会话。ForkBefore 会复制从根到该消息的
// 路径并保留原始记录 id，不改动原会话；同时返回该消息当时的 prompt 文本，CLI
// 把它打印出来供用户复制到 composer。
func sessionsForkAt(app App, id, messageID string) int {
	store, err := session.Open(app.sessionDir())
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sess, err := store.OpenSession(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	newID, restoredPrompt, err := sess.ForkBefore(messageID)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "forked session %s\n", newID)
	fmt.Fprintf(app.IO.Out, "restored prompt: %s\n", restoredPrompt)
	return 0
}

// sessionsHealth 对指定会话（或全部会话）做只读健康检查。它复用 session 包的
// ValidateJSONL 与 MeasureDiskUsage 原语，输出两部分：转录问题的 kind/count 汇总
// 与会话目录的磁盘占用。
//
// 脱敏：ValidateJSONL 的每条问题 Detail 会内嵌原始记录 id / prompt 文本，因此这里
// 只累计 issue.Kind 的数量并排序输出，绝不打印 Detail、原始 id 或文本内容。
// 目录不存在时给出可读的中文错误；全程不做按字节截断，CJK id / 文本保持完整。
// 无参数时检查全部会话；带一个参数时检查该 id（支持精确 id 与唯一前缀）。
func sessionsHealth(app App, args []string) int {
	if len(args) > 1 {
		sessionsUsage(app.IO.Err)
		return 2
	}

	dir := app.sessionDir()
	// 先显式检查目录，避免把底层的英文 stat 错误直接暴露给用户。
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(app.IO.Err, "错误: 会话目录不存在或不可访问: %s\n", dir)
		return 1
	}

	var files []string
	if len(args) == 0 {
		// 全部会话：只收集 .jsonl 转录，按文件名排序保证结果确定。
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "错误: 读取会话目录失败: %v\n", err)
			return 1
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
				continue
			}
			files = append(files, filepath.Join(dir, entry.Name()))
		}
		sort.Strings(files)
	} else {
		// 指定会话：ResolvePath 会拒绝逃逸路径并解析精确 id / 唯一前缀。
		path, err := session.ResolvePath(dir, args[0])
		if err != nil {
			fmt.Fprintf(app.IO.Err, "错误: %v\n", err)
			return 1
		}
		files = []string{path}
	}

	// 只保留 issue.Kind -> count 的汇总，刻意丢弃 Detail（内嵌原始 id / 文本）。
	counts := make(map[string]int)
	total := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "错误: 读取会话转录失败: %v\n", err)
			return 1
		}
		for _, issue := range session.ValidateJSONL(data) {
			counts[issue.Kind]++
			total++
		}
	}

	if len(args) == 0 {
		fmt.Fprintln(app.IO.Out, "会话健康检查: 全部会话")
	} else {
		fmt.Fprintln(app.IO.Out, "会话健康检查: 指定会话")
	}
	fmt.Fprintf(app.IO.Out, "转录问题: 共 %d 处\n", total)
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		fmt.Fprintf(app.IO.Out, "  %s: %d\n", kind, counts[kind])
	}

	// 磁盘占用按整目录统计（与 doctor 一致）；kind 顺序固定为 session/archive/other。
	usage, err := session.MeasureDiskUsage(dir)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "错误: 统计磁盘占用失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "磁盘占用: %d 个文件, %d 字节\n", usage.Files, usage.Bytes)
	for _, kind := range []string{"session", "archive", "other"} {
		fmt.Fprintf(app.IO.Out, "  %s: %d 字节\n", kind, usage.ByKind[kind])
	}
	return 0
}

// sessionsListMessages 按文件顺序列出会话树中的用户消息（id 与 prompt 文本），
// 供用户挑选 fork-at 的分叉点。它只读，不修改活动指针也不追加记录。
func sessionsListMessages(app App, id string) int {
	store, err := session.Open(app.sessionDir())
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sess, err := store.OpenSession(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	for _, msg := range sess.UserMessages() {
		fmt.Fprintf(app.IO.Out, "%s\t%s\n", msg.ID, msg.Text)
	}
	return 0
}

func sessionsFind(app App, store SessionStore, keyword string) int {
	infos, err := store.List()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sortSessions(infos)
	// 把 ID 和标题拼成一个可搜索文本，既保留按 ID 查找的能力，又让模糊
	// 匹配和相关性排序作用于同一个字段。
	searchables := make([]string, len(infos))
	for i, info := range infos {
		searchables[i] = info.ID + " " + info.Title
	}
	indices := fuzzyFindSessions(keyword, searchables)
	for _, index := range indices {
		info := infos[index]
		fmt.Fprintf(app.IO.Out, "%s\t%s\t%s\n", info.ID, formatTime(info.Updated), info.Title)
	}
	if len(indices) == 0 {
		fmt.Fprintf(app.IO.Out, "no sessions matching %q\n", keyword)
	}
	return 0
}

func sessionsShow(app App, store SessionStore, id string) int {
	events, err := store.Load(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	var content strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		content.Write(line)
		content.WriteByte('\n')
	}
	output := content.String()
	// 头尾预览是可选能力，默认关闭；显式开启后才折叠中间的行，
	// 避免改变默认输出而破坏既有的逐字节行为。
	if sessionsHeadTailEnabled(app) {
		output = applyHeadTailToOutput(output)
	}
	return reportLongOut(app, output)
}

// sessionGrepper 是注入的 store 可以提供的可选读取端。回退方案会直接读取
// 存储目录中的 JSONL 文件。
type sessionGrepper interface {
	Search(query string, limit int) ([]session.Hit, error)
}

// sessionTreeStore 是注入的 store 可选的分支/导出端。
type sessionTreeStore interface {
	Branches(id string) ([]session.Branch, error)
	RenderHTML(id, branchID string, w io.Writer) error
}

func parseGrepArgs(args []string) (string, int, SessionFilter, error) {
	limit := 20
	var filter SessionFilter
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// 可选的多条件过滤 flag；未提供时 filter 保持零值。
		if arg == "--title" || arg == "--since" || arg == "--until" ||
			arg == "--in" || arg == "--text" ||
			strings.HasPrefix(arg, "--title=") || strings.HasPrefix(arg, "--since=") ||
			strings.HasPrefix(arg, "--until=") || strings.HasPrefix(arg, "--in=") ||
			strings.HasPrefix(arg, "--text=") {
			name := arg
			if idx := strings.IndexByte(arg, '='); idx >= 0 {
				name = arg[:idx]
			}
			value, next, errMsg := takeAuditStringValue(args, i, name)
			if errMsg != "" {
				return "", 0, SessionFilter{}, errors.New(errMsg)
			}
			switch name {
			case "--title":
				filter.Title = value
			case "--since":
				filter.Since = value
			case "--until":
				filter.Until = value
			case "--in":
				filter.In = value
			case "--text":
				filter.Text = value
			}
			i = next
			continue
		}
		switch {
		case arg == "--limit":
			if i+1 >= len(args) {
				return "", 0, SessionFilter{}, errors.New("--grep needs a value for --limit")
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return "", 0, SessionFilter{}, fmt.Errorf("invalid --limit %q", args[i])
			}
			limit = value
		case strings.HasPrefix(arg, "--limit="):
			value, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil || value <= 0 {
				return "", 0, SessionFilter{}, fmt.Errorf("invalid --limit %q", args[i])
			}
			limit = value
		case strings.HasPrefix(arg, "-"):
			return "", 0, SessionFilter{}, fmt.Errorf("unknown --grep option %q", arg)
		default:
			words = append(words, arg)
		}
	}
	query := strings.Join(words, " ")
	if strings.TrimSpace(query) == "" {
		return "", 0, SessionFilter{}, errors.New("--grep needs search text")
	}
	return query, limit, filter, nil
}

func sessionsGrep(app App, store SessionStore, query string, limit int, filter SessionFilter) int {
	var hits []session.Hit
	var err error
	if grepper, ok := store.(sessionGrepper); ok {
		hits, err = grepper.Search(query, limit)
	} else {
		hits, err = session.Search(app.sessionDir(), query, limit)
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	// 仅在提供了过滤 flag 时收窄命中；无过滤时命中原样输出，保持既有行为。
	hits = filterSessionHits(hits, filter)
	if len(hits) == 0 {
		fmt.Fprintf(app.IO.Out, "no matches for %q\n", query)
		return 0
	}
	for _, hit := range hits {
		fmt.Fprintf(app.IO.Out, "%s:%d\t%s\t%s\t%s\n", hit.ID, hit.Line, hit.Path, formatTime(hit.TS), hit.Snippet)
	}
	return 0
}

// 树渲染的可调上界：记录数限制建树规模，深度限制渲染规模。
// 二者共同保证异常大的会话不会让只读命令的输出或递归爆炸。
const (
	sessionsTreeMaxRecords   = 20000
	sessionsTreeDefaultDepth = 200
	sessionsTreeMaxDepth     = 1000
)

// sessionsTree 把指定会话（省略 id 时为最近会话）的 id/parent 记录投影成
// Unicode ASCII 树并标记活动叶。它全程只读：路径解析走 session.ResolvePath，
// 不经过可能回写转录的 OpenSession；节点标签按字节原样拼接，CJK 标题不会被截断。
func sessionsTree(app App, args []string) int {
	id, maxDepth, err := parseTreeArgs(args)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "错误: %v\n", err)
		sessionsUsage(app.IO.Err)
		return 2
	}
	dir := app.sessionDir()
	if id == "" {
		// 未指定 id 时选最近更新的会话；纯目录扫描，不触发索引旁车回写。
		id, err = latestSessionID(dir)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "错误: %v\n", err)
			return 1
		}
	}
	path, err := session.ResolvePath(dir, id)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "错误: %v\n", err)
		return 1
	}
	records, err := readTreeRecords(path)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "错误: 读取会话转录失败: %v\n", err)
		return 1
	}
	if len(records) == 0 {
		fmt.Fprintln(app.IO.Err, "错误: 会话没有可渲染的记录")
		return 1
	}

	// 记录数上界：只取前 N 条建树，省略量在输出末尾说明。
	skippedRecords := 0
	if len(records) > sessionsTreeMaxRecords {
		skippedRecords = len(records) - sessionsTreeMaxRecords
		records = records[:sessionsTreeMaxRecords]
	}
	// 活动叶与 session.OpenSession 的语义一致：取最后一条记录。
	// 分叉/检出产生的指针只存在于内存，重新打开时必然回落到文件末条。
	activeID := records[len(records)-1].ID
	nodes := session.ExportTree(records, activeID)

	// 深度上界：RenderTreeASCII 会跳过不在 nodes 中的子节点，
	// 因此按 Depth 裁掉深层节点即可，无需改动 session 包。
	bounded := make([]session.TreeNode, 0, len(nodes))
	skippedDepth := 0
	for _, node := range nodes {
		if node.Depth <= maxDepth {
			bounded = append(bounded, node)
		} else {
			skippedDepth++
		}
	}

	displayID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	fmt.Fprintf(app.IO.Out, "会话树: %s\n", displayID)
	if rendered := session.RenderTreeASCII(bounded); rendered != "" {
		fmt.Fprintln(app.IO.Out, rendered)
	}
	if skippedRecords > 0 {
		fmt.Fprintf(app.IO.Out, "（记录数超过 %d，已省略 %d 条）\n", sessionsTreeMaxRecords, skippedRecords)
	}
	if skippedDepth > 0 {
		fmt.Fprintf(app.IO.Out, "（深度超过 %d，已省略 %d 个节点）\n", maxDepth, skippedDepth)
	}
	return 0
}

// parseTreeArgs 解析 `sessions tree [id] [--depth N]`：id 至多一个，
// --depth 必须为 0 以外的正整数且不超过硬上限，避免调用方传入超大深度。
func parseTreeArgs(args []string) (string, int, error) {
	maxDepth := sessionsTreeDefaultDepth
	var ids []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--depth":
			if i+1 >= len(args) {
				return "", 0, errors.New("tree 需要 --depth 的值")
			}
			i++
			depth, err := parseTreeDepth(args[i])
			if err != nil {
				return "", 0, err
			}
			maxDepth = depth
		case strings.HasPrefix(arg, "--depth="):
			depth, err := parseTreeDepth(strings.TrimPrefix(arg, "--depth="))
			if err != nil {
				return "", 0, err
			}
			maxDepth = depth
		case strings.HasPrefix(arg, "-"):
			return "", 0, fmt.Errorf("tree 未知选项 %q", arg)
		default:
			ids = append(ids, arg)
		}
	}
	if len(ids) > 1 {
		return "", 0, errors.New("tree 最多接受一个会话 id")
	}
	if len(ids) == 1 {
		return ids[0], maxDepth, nil
	}
	return "", maxDepth, nil
}

func parseTreeDepth(value string) (int, error) {
	depth, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || depth <= 0 {
		return 0, fmt.Errorf("非法 --depth %q", value)
	}
	if depth > sessionsTreeMaxDepth {
		return 0, fmt.Errorf("--depth 不能超过 %d", sessionsTreeMaxDepth)
	}
	return depth, nil
}

// latestSessionID 返回目录中最后更新的会话 id。它只读目录项，
// 不调用可能回写索引旁车的 session.List，也不创建不存在的目录。
func latestSessionID(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("没有可用的会话，请指定会话 id")
		}
		return "", fmt.Errorf("读取会话目录失败: %w", err)
	}
	var latestID string
	var latestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(latestTime) {
			continue
		}
		latestID = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		latestTime = info.ModTime()
	}
	if latestID == "" {
		return "", errors.New("没有可用的会话，请指定会话 id")
	}
	return latestID, nil
}

// readTreeRecords 只读地解析转录里 id/parent 两列。它刻意走 os.ReadFile
// 而非 session.OpenSession，后者会在尾部残缺时回写文件；同时跳过坏行、
// 并为无 id 的旧记录按 session.nextID 的规则补 id，保持父引用可解析。
func readTreeRecords(path string) ([]session.Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	type rawRecord struct {
		ID     string `json:"id"`
		Parent string `json:"parent"`
	}
	parsed := make([]rawRecord, 0, 16)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec rawRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		parsed = append(parsed, rec)
	}
	used := make(map[string]bool, len(parsed))
	for _, rec := range parsed {
		if rec.ID != "" {
			used[rec.ID] = true
		}
	}
	records := make([]session.Record, 0, len(parsed))
	for _, rec := range parsed {
		id := rec.ID
		if id == "" {
			for n := 1; ; n++ {
				candidate := fmt.Sprintf("n%06d", n)
				if !used[candidate] {
					id = candidate
					used[candidate] = true
					break
				}
			}
		}
		records = append(records, session.Record{ID: id, Parent: rec.Parent})
	}
	return records, nil
}

func sessionsExport(app App, store SessionStore, id string, args []string) int {
	outPath, branchID, format := "", "", "html"
	sanitize := false
	var filter ExportFilter
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-o" || args[i] == "--output":
			if i+1 >= len(args) {
				sessionsUsage(app.IO.Err)
				return 2
			}
			i++
			outPath = args[i]
		case strings.HasPrefix(args[i], "-o="):
			outPath = strings.TrimPrefix(args[i], "-o=")
		case strings.HasPrefix(args[i], "--output="):
			outPath = strings.TrimPrefix(args[i], "--output=")
		case args[i] == "--branch":
			if i+1 >= len(args) {
				sessionsUsage(app.IO.Err)
				return 2
			}
			i++
			branchID = args[i]
		case args[i] == "--format":
			if i+1 >= len(args) {
				sessionsUsage(app.IO.Err)
				return 2
			}
			i++
			format = args[i]
		case strings.HasPrefix(args[i], "--format="):
			format = strings.TrimPrefix(args[i], "--format=")
		case args[i] == "--sanitize":
			sanitize = true
		case args[i] == "--since" || strings.HasPrefix(args[i], "--since="):
			value, next, errMsg := takeAuditStringValue(args, i, "--since")
			if errMsg != "" {
				fmt.Fprintln(app.IO.Err, errMsg)
				return 2
			}
			filter.Since = value
			i = next
		case args[i] == "--until" || strings.HasPrefix(args[i], "--until="):
			value, next, errMsg := takeAuditStringValue(args, i, "--until")
			if errMsg != "" {
				fmt.Fprintln(app.IO.Err, errMsg)
				return 2
			}
			filter.Until = value
			i = next
		case args[i] == "--type" || strings.HasPrefix(args[i], "--type="):
			value, next, errMsg := takeAuditStringValue(args, i, "--type")
			if errMsg != "" {
				fmt.Fprintln(app.IO.Err, errMsg)
				return 2
			}
			for _, typ := range strings.Split(value, ",") {
				if trimmed := strings.TrimSpace(typ); trimmed != "" {
					filter.Types = append(filter.Types, trimmed)
				}
			}
			i = next
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected export argument %q\n", args[i])
			return 2
		}
	}

	// 先渲染，再通过 no-follow 辅助函数写入。缓冲使文件创建成为一次性操作，
	// 从而符号链接检查覆盖整个写入过程，并避免在渲染失败时留下被截断的文件。
	var data []byte
	var err error
	if isYAMLExport(format) {
		// session 包只认识 html/json/markdown。在本层根据它的 JSON 转录构建
		// YAML：JSON 是合法的 YAML 1.2 文档，因此无需新依赖，也不会丢失任何导出字段。
		data, err = session.ExportProject(app.sessionDir(), id, branchID, session.ExportOptions{
			Branch: branchID, Sanitize: sanitize, Format: "json",
		})
		if err == nil {
			data, err = jsonExportToYAML(data)
		}
	} else if sanitize || !strings.EqualFold(format, "html") {
		// 结构化和脱敏导出完全在 session 包中生成；下面的纯 HTML 路径
		// 保证注入的 store 仍能正常工作。
		data, err = session.ExportProject(app.sessionDir(), id, branchID, session.ExportOptions{
			Branch: branchID, Sanitize: sanitize, Format: format,
		})
	} else if trees, ok := store.(sessionTreeStore); ok {
		var buf bytes.Buffer
		err = trees.RenderHTML(id, branchID, &buf)
		data = buf.Bytes()
	} else {
		var path string
		path, err = session.ResolvePath(app.sessionDir(), id)
		if err == nil {
			var buf bytes.Buffer
			err = session.RenderHTML(path, branchID, &buf)
			data = buf.Bytes()
		}
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	// 结构化导出可以在本层收窄到时间/类型窗口。文本导出保持原样，
	// 并向用户报告这一限制。
	if filter.active() {
		if !isFilterableExport(format) {
			fmt.Fprintf(app.IO.Err, "argentgo: --since/--until/--type only apply to json/yaml exports; %s export is unchanged\n", format)
		} else {
			data, err = filterExportArtifact(data, format, filter)
			if err != nil {
				fmt.Fprintln(app.IO.Err, "error:", err)
				return 1
			}
		}
	}
	if outPath != "" {
		if err := writeFileNoFollow(outPath, data, 0o666); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "wrote %s\n", outPath)
	} else if _, err := app.IO.Out.Write(data); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	return 0
}

func sortSessions(infos []server.SessionInfo) {
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Updated.After(infos[j].Updated)
	})
}

func formatTime(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	return ts.UTC().Format(time.RFC3339)
}

func sessionsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo sessions list
       argentgo sessions show <id>
       argentgo sessions checkout <id> <entry>
       argentgo sessions fork-at <id> <message-id>
       argentgo sessions health [id]
       argentgo sessions list-messages <id>
       argentgo sessions fidelity <id>
       argentgo sessions rm <id>
       argentgo sessions find <keyword>
        argentgo sessions --grep <text> [--limit N] [--title substr] [--since time] [--until time] [--in dir] [--text substr]
        argentgo sessions tree [id] [--depth N]   render the id/parent tree as ASCII, marking the active leaf
        argentgo sessions undo <id> [-C <workspace>]
        argentgo sessions retry <id> [checkpoint-id]
        argentgo sessions discard-attempt <id> [attempt-id]
        argentgo sessions name <id> <title>
        argentgo sessions export <id> [-o file] [--format html|json|markdown|yaml] [--sanitize] [--branch id] [--since time] [--until time] [--type kind[,kind...]]
        argentgo sessions import <file> [--format claude|codex|auto]
`)
}

// fsStore 直接从存储目录读取 JSONL 会话文件。在集成方注入
// internal/session 之前，它作为回退方案使用。
type fsStore struct {
	dir string
}

func (s *fsStore) List() ([]server.SessionInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []server.SessionInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		info, err := entry.Info()
		if err != nil {
			continue
		}
		events, err := s.Load(id)
		if err != nil {
			continue
		}
		infos = append(infos, server.SessionInfo{
			ID:      id,
			Title:   sessionTitle(events),
			Updated: info.ModTime(),
		})
	}
	return infos, nil
}

func (s *fsStore) Load(id string) ([]server.SessionEvent, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load session %q: %w", id, err)
	}
	var events []server.SessionEvent
	for number, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event server.SessionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, number+1, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *fsStore) Delete(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove session %q: %w", id, err)
	}
	return nil
}

func (s *fsStore) path(id string) (string, error) {
	if !safeSessionID(id) {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return filepath.Join(s.dir, id+".jsonl"), nil
}

func safeSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	return true
}

// sessionTitle 找出会话中第一段人类可读的文本。
func sessionTitle(events []server.SessionEvent) string {
	for i, event := range events {
		if i >= 40 {
			break
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		for _, key := range []string{"text", "prompt", "content", "message"} {
			if text, ok := payload[key].(string); ok {
				return session.TitleFromPrompt(text)
			}
		}
	}
	return ""
}
