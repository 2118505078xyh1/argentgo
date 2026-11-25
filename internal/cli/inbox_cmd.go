package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/inbox"
)

// runInbox 实现 `argentgo inbox`：持久提示收件箱的只读/管理入口。
// 子命令：list（列出待投递）、add <text>（入队）、cancel <id>（取消）。
func runInbox(app App, _ context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		inboxUsage(app.IO.Out)
		return 0
	}
	box, err := inbox.New(inbox.Options{Path: filepath.Join(app.storageDir(), "inbox.jsonl")})
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if len(args) == 0 || args[0] == "list" {
		items := box.Pending()
		if len(items) == 0 {
			fmt.Fprintln(app.IO.Out, "inbox: empty")
			return 0
		}
		for _, it := range items {
			fmt.Fprintf(app.IO.Out, "%s\t%s\t%s\n", it.ID, it.Delivery, it.Text)
		}
		return 0
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(app.IO.Err, "argentgo: inbox add needs text")
			inboxUsage(app.IO.Err)
			return 2
		}
		delivery := inbox.DeliveryQueue
		textParts := args[1:]
		if args[1] == "--steer" {
			delivery = inbox.DeliverySteer
			textParts = args[2:]
		} else if args[1] == "--queue" {
			textParts = args[2:]
		}
		text := strings.Join(textParts, " ")
		// 入队前应用容量/TTL 治理，防止收件箱无限增长。
		item, err := box.EnqueueWithCleanup("", "", text, delivery, inbox.CleanupPolicy{})
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "enqueued %s (%s)\n", item.ID, item.Delivery)
		return 0
	case "cleanup":
		removed, err := box.Cleanup(inbox.CleanupPolicy{})
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "cleaned %d expired/overflow items\n", removed)
		return 0
	case "steer":
		// 把已有项的投递模式改为 steer（下一个安全步边界即可抢占）。
		if len(args) != 2 {
			fmt.Fprintln(app.IO.Err, "argentgo: inbox steer needs an id")
			return 2
		}
		if err := box.ChangeDelivery(args[1], inbox.DeliverySteer); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "steered %s\n", args[1])
		return 0
	case "cancel":
		if len(args) != 2 {
			fmt.Fprintln(app.IO.Err, "argentgo: inbox cancel needs an id")
			return 2
		}
		if err := box.Cancel("", args[1]); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "cancelled %s\n", args[1])
		return 0
	default:
		fmt.Fprintf(app.IO.Err, "argentgo: unknown inbox command %q\n", args[0])
		inboxUsage(app.IO.Err)
		return 2
	}
}

func inboxUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo inbox [list | add [--steer|--queue] <text> | steer <id> | cancel <id>]

Manages the durable prompt inbox. Items survive process restarts; "steer" can be
delivered at the next safe step boundary, "queue" preserves strict order.
"steer <id>" changes an existing item's delivery to steer.
`)
}
