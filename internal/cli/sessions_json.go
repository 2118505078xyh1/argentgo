package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/server"
)

// sessionListMeta 是会话列表 JSON 输出的稳定字段投影。字段顺序固定，
// 让脚本不必依赖旧版制表符输出的列位置即可稳定消费。
type sessionListMeta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	Turns   int       `json:"turns"`
}

// newSessionListMeta 把存储层的会话元数据投影成稳定的 JSON 字段集合。
// 时间统一以 UTC RFC3339 纳秒精度输出，避免不同时区的展示差异。
func newSessionListMeta(info server.SessionInfo, turns int) sessionListMeta {
	return sessionListMeta{
		ID:      info.ID,
		Title:   info.Title,
		Updated: info.Updated.UTC(),
		Turns:   turns,
	}
}

// sessionListJSON 把会话元数据序列化为稳定的缩进 JSON。
// 关闭 HTML 转义后，中文标题与 <、& 等字符都以原始 UTF-8 输出，
// 不会变成 \uXXXX；nil 与空切片统一输出 []，避免脚本端处理 null。
// 所有字段都是标量，编码不会失败；防御性地返回 nil 仅用于不应发生的情况。
func sessionListJSON(metas []sessionListMeta) []byte {
	if metas == nil {
		metas = []sessionListMeta{}
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(metas); err != nil {
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// sessionTurnCount 统计会话事件流中的轮次数。规则与 usage 命令一致：
// turn 记录优先取 data.turns，缺失或为零时按一轮计。
func sessionTurnCount(events []server.SessionEvent) int {
	turns := 0
	for _, event := range events {
		if event.Kind != "turn" {
			continue
		}
		var payload struct {
			Turns int `json:"turns"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil && payload.Turns > 0 {
			turns += payload.Turns
		} else {
			turns++
		}
	}
	return turns
}

// sessionsListWithJSON 处理 `sessions list [--json]`。不带 --json 时
// 直接委托原有的 sessionsList，输出逐字节保持不变；带 --json 时只向
// stdout 写结构化数组，加载单条会话失败仅向 stderr 提示，不污染管道。
func sessionsListWithJSON(app App, store SessionStore, args []string) int {
	asJSON := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			sessionsUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected sessions list argument %q\n", arg)
			sessionsUsage(app.IO.Err)
			return 2
		}
	}
	if !asJSON {
		return sessionsList(app, store)
	}

	infos, err := store.List()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sortSessions(infos)
	metas := make([]sessionListMeta, 0, len(infos))
	for _, info := range infos {
		turns := 0
		if events, loadErr := store.Load(info.ID); loadErr == nil {
			turns = sessionTurnCount(events)
		} else {
			fmt.Fprintf(app.IO.Err, "warning: %s: %v\n", info.ID, loadErr)
		}
		metas = append(metas, newSessionListMeta(info, turns))
	}
	fmt.Fprintln(app.IO.Out, string(sessionListJSON(metas)))
	return 0
}
