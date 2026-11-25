package cli

import (
	"encoding/hex"
	"fmt"

	"example.com/argentgo/internal/session"
)

// sessionsSnapshots 只读列出会话中已记录的请求字节快照摘要。
// 不打印完整 body（可能很大且含敏感文本），只显示协议、模型、字节数与 hash 前 8 字节，
// 供确认快照存在与完整性；完整字节仍在会话文件的 request_snapshot 记录里。
func sessionsSnapshots(app App, id string) int {
	sess, err := session.OpenSession(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: open session: %v\n", err)
		return 1
	}
	records, err := sess.Records()
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: read records: %v\n", err)
		return 1
	}
	snapshots := session.RequestSnapshots(records)
	if len(snapshots) == 0 {
		fmt.Fprintln(app.IO.Out, "该会话没有请求字节快照（需 ARGENT_REQUEST_SNAPSHOT=1）")
		return 0
	}
	for i, s := range snapshots {
		sum := hex.EncodeToString(s.BodySHA256[:])
		if len(sum) > 16 {
			sum = sum[:16]
		}
		fmt.Fprintf(app.IO.Out, "%d. %s %s %d bytes sha256:%s\n",
			i+1, s.Protocol, s.Model, len(s.Body), sum)
	}
	return 0
}
