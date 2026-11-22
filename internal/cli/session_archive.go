package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// sessionsArchive 实现 `sessions archive <id> [--format json|text]`：
// 读取会话最后一次压缩的无损归档，把被丢弃的原始消息读回。
// 这是只读出口：绝不改写会话或归档文件。
func sessionsArchive(app App, id string, args []string) int {
	format := "text"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argentgo: --format needs json or text")
				return 2
			}
			i++
			format = args[i]
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected archive argument %q\n", args[i])
			return 2
		}
	}
	if format != "json" && format != "text" {
		fmt.Fprintf(app.IO.Err, "argentgo: invalid archive format %q (want json or text)\n", format)
		return 2
	}

	path, err := session.ResolvePath(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	ref, found, err := latestArchiveRef(path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if !found || ref == nil {
		fmt.Fprintln(app.IO.Out, "该会话尚无压缩归档（写入方需启用归档）")
		return 0
	}
	entries, err := session.ReadCompactionArchive(app.sessionDir(), ref.Path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if format == "json" {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	writeArchiveText(app.IO.Out, entries)
	return 0
}

// latestArchiveRef 从会话文件最后一次 KindCompaction 记录中解出 ArchiveRef。
// 与 latestCompactionSummary 同范式，按文件顺序遍历使最后一条生效。
func latestArchiveRef(path string) (*session.ArchiveRef, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	var ref *session.ArchiveRef
	found := false
	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var record session.Record
		if json.Unmarshal(line, &record) != nil || record.Kind != session.KindCompaction {
			continue
		}
		var compaction session.Compaction
		if json.Unmarshal(record.Data, &compaction) != nil {
			continue
		}
		if compaction.Archive != nil {
			ref, found = compaction.Archive, true
		}
	}
	return ref, found, nil
}

// writeArchiveText 输出有界的可读文本：每条消息一行，长内容折叠为单行摘要。
func writeArchiveText(w io.Writer, entries []session.Entry) {
	const maxRunes = 300
	for _, e := range entries {
		text := strings.TrimSpace(e.Content)
		r := []rune(text)
		if len(r) > maxRunes {
			text = string(r[:maxRunes]) + "…"
		}
		text = strings.ReplaceAll(text, "\n", " ")
		fmt.Fprintf(w, "[%s] %s\n", e.Role, text)
	}
}
