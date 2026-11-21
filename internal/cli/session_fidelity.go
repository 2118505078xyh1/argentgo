package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// sessionsFidelity 对指定会话中最近一条压缩摘要做只读保真度诊断。
// 与 sessions undo/retry 不同，这里既不打开可写的 Session，也不复制转录：
// ValidateAndRepair(path, false) 只校验，随后直接读取原始字节，
// 因此源会话文件永远不会被修补或改写。
func sessionsFidelity(app App, id string, args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(app.IO.Err, "argentgo: unexpected fidelity argument %q\n", args[0])
		sessionsUsage(app.IO.Err)
		return 2
	}
	path, err := session.ResolvePath(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if _, err := session.ValidateAndRepair(path, false); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	summary, found, err := latestCompactionSummary(path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if !found || strings.TrimSpace(summary) == "" {
		fmt.Fprintln(app.IO.Out, "该会话尚无压缩摘要")
		return 0
	}
	writeCompactionFidelityReport(app.IO.Out, session.MeasureCompactionFidelity(summary))
	return 0
}

// latestCompactionSummary 返回文件中最后一条 KindCompaction 记录的摘要文本。
// 解析沿用 session.readRecords 的宽松策略：格式错误的行会被跳过；
// 最后一条载荷无法解析的压缩记录不会被误当作有效摘要。
func latestCompactionSummary(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	var summary string
	found := false
	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var record session.Record
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if record.Kind != session.KindCompaction {
			continue
		}
		var compaction session.Compaction
		if err := json.Unmarshal(record.Data, &compaction); err != nil {
			continue
		}
		summary, found = compaction.Summary, true
	}
	return summary, found, nil
}

// writeCompactionFidelityReport 输出稳定的中文诊断报告。字段顺序与文案固定，
// 不包含摘要原文或时间戳，因此同一份摘要总是得到逐字节相同的报告。
func writeCompactionFidelityReport(w io.Writer, fidelity session.CompactionFidelity) {
	var b strings.Builder
	b.WriteString("压缩摘要保真度诊断\n")
	fmt.Fprintf(&b, "目标（Goal）：%s\n", fidelityPresence(fidelity.HasGoal))
	fmt.Fprintf(&b, "改动文件（ChangedFiles）：%s（%d 项）\n",
		fidelityPresence(fidelity.ChangedFiles > 0), fidelity.ChangedFiles)
	fmt.Fprintf(&b, "失败（FailureCount）：%s（%d 项）\n",
		fidelityPresence(fidelity.FailureCount > 0), fidelity.FailureCount)
	fmt.Fprintf(&b, "下一步（NextSteps）：%s（%d 项）\n",
		fidelityPresence(fidelity.NextSteps > 0), fidelity.NextSteps)
	fmt.Fprintf(&b, "Score：%s\n", strconv.FormatFloat(fidelity.Score, 'f', -1, 64))
	fmt.Fprintf(&b, "Complete：%s\n", fidelityBool(fidelity.Complete))
	_, _ = io.WriteString(w, b.String())
}

func fidelityPresence(present bool) string {
	if present {
		return "有"
	}
	return "无"
}

func fidelityBool(value bool) string {
	if value {
		return "是"
	}
	return "否"
}
