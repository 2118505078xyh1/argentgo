package agent

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestPartialBatchReceiptAllSuccess(t *testing.T) {
	got := partialBatchReceipt([]CallRecord{
		{Name: "read", Status: CallSucceeded},
		{Name: "grep", Status: CallSucceeded},
		{Name: "ls", Status: CallSucceeded},
	})
	for _, want := range []string{"已完成 3 个：", "read(成功)", "grep(成功)", "ls(成功)", "不撤销", "请勿盲目重发"} {
		if !strings.Contains(got, want) {
			t.Fatalf("receipt = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "(失败") {
		t.Fatalf("all-success receipt mentions a failed call: %q", got)
	}
}

func TestPartialBatchReceiptPartialFailure(t *testing.T) {
	got := partialBatchReceipt([]CallRecord{
		{Name: "read", Status: CallSucceeded, Duration: time.Millisecond},
		{Name: "write", Status: CallFailed, Err: "disk full"},
		{Name: "grep", Status: CallSucceeded},
	})
	for _, want := range []string{"已完成 3 个：", "read(成功)", "write(失败: disk full)", "grep(成功)", "不撤销"} {
		if !strings.Contains(got, want) {
			t.Fatalf("receipt = %q, want it to contain %q", got, want)
		}
	}
}

func TestPartialBatchReceiptAllFailed(t *testing.T) {
	got := partialBatchReceipt([]CallRecord{
		{Name: "a", Status: CallFailed, Err: "boom-a"},
		{Name: "b", Status: CallFailed, Err: "boom-b"},
	})
	for _, want := range []string{"已完成 2 个：", "a(失败: boom-a)", "b(失败: boom-b)", "不撤销"} {
		if !strings.Contains(got, want) {
			t.Fatalf("receipt = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "(成功)") {
		t.Fatalf("all-failed receipt claims a success: %q", got)
	}
}

func TestPartialBatchReceiptMultipleCallsKeepOrder(t *testing.T) {
	calls := make([]CallRecord, 8)
	for i := range calls {
		calls[i] = CallRecord{Name: "tool" + string(rune('A'+i)), Status: CallSucceeded}
	}
	got := partialBatchReceipt(calls)
	if !strings.Contains(got, "已完成 8 个：") {
		t.Fatalf("receipt = %q, want count 8", got)
	}
	last := -1
	for i := range calls {
		at := strings.Index(got, calls[i].Name+"(")
		if at <= last {
			t.Fatalf("call %d (%s) is out of order at %d (last %d): %q", i, calls[i].Name, at, last, got)
		}
		last = at
	}
}

func TestPartialBatchReceiptCJKIsRuneSafe(t *testing.T) {
	got := partialBatchReceipt([]CallRecord{
		{Name: "读取文件", Status: CallSucceeded},
		{Name: "写入配置", Status: CallFailed, Err: "参数无效：路径不存在"},
	})
	if !utf8.ValidString(got) {
		t.Fatalf("CJK receipt is not valid UTF-8: %q", got)
	}
	for _, want := range []string{"读取文件(成功)", "写入配置(失败: 参数无效：路径不存在)", "不撤销"} {
		if !strings.Contains(got, want) {
			t.Fatalf("receipt = %q, want it to contain %q", got, want)
		}
	}
}

func TestPartialBatchReceiptBounded(t *testing.T) {
	longName := strings.Repeat("工", receiptNameMaxRunes*3)
	longErr := strings.Repeat("错", receiptErrMaxRunes*3)
	calls := make([]CallRecord, 40)
	for i := range calls {
		calls[i] = CallRecord{Name: longName, Status: CallFailed, Err: longErr}
	}
	got := partialBatchReceipt(calls)
	if !utf8.ValidString(got) {
		t.Fatalf("bounded receipt is not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > receiptMaxRunes {
		t.Fatalf("receipt rune count = %d, want <= %d", n, receiptMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("overlong receipt was not truncated with a marker: %q", got)
	}
}

func TestPartialBatchReceiptIsPure(t *testing.T) {
	calls := []CallRecord{
		{Name: "a", Status: CallSucceeded},
		{Name: "b", Status: CallFailed, Err: "x"},
	}
	first := partialBatchReceipt(calls)
	second := partialBatchReceipt(calls)
	if first != second {
		t.Fatalf("not deterministic:\n%q\n%q", first, second)
	}
	if calls[0].Name != "a" || calls[1].Err != "x" {
		t.Fatalf("input was mutated: %+v", calls)
	}
}

func TestPartialBatchReceiptEmptyStatusMeansSkipped(t *testing.T) {
	got := partialBatchReceipt([]CallRecord{{Name: "blocked"}})
	if !strings.Contains(got, "blocked(未执行)") {
		t.Fatalf("receipt = %q, want empty status to render as 未执行", got)
	}
}

// TestBatchReceiptOptInWiring 验证 opt-in 接线：默认路径不产生回执，
// 只有显式安装发出器时才会收到一份描述本批次的回执，且不改动工具结果字节。
func TestBatchReceiptOptInWiring(t *testing.T) {
	newRunner := func() *Runner {
		tools := &fakeTools{
			specs: []llm.ToolSpec{toolSpec("read"), toolSpec("grep")},
			run: func(_ context.Context, name, _ string) (string, error) {
				if name == "grep" {
					return "", context.DeadlineExceeded
				}
				return "out-" + name, nil
			},
		}
		chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			if n == 1 {
				return toolReply(toolCall("c1", "read", `{}`), toolCall("c2", "grep", `{}`)), nil
			}
			return textReply("done"), nil
		}}
		return &Runner{Opts: Options{
			Model: "m", Chatter: chat, Tools: tools,
			MaxTurns: 2, MaxParallel: 2,
		}}
	}

	// 默认路径：工具结果中不得混入回执文本。
	plain, err := newRunner().Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("plain run: %v", err)
	}
	for _, msg := range plain.Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "不撤销") {
			t.Fatalf("default path leaked a receipt into tool output: %q", msg.Content)
		}
	}

	// opt-in 路径：发出器收到恰好一份回执，且内容描述了成功的 read 与失败的 grep。
	var receipts []string
	ctx := WithBatchReceipt(context.Background(), func(text string) {
		receipts = append(receipts, text)
	})
	if _, err := newRunner().Run(ctx, "inspect"); err != nil {
		t.Fatalf("opt-in run: %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("receipt emissions = %d, want 1: %v", len(receipts), receipts)
	}
	for _, want := range []string{"已完成 2 个：", "read(成功)", "grep(失败", "不撤销"} {
		if !strings.Contains(receipts[0], want) {
			t.Fatalf("receipt = %q, want it to contain %q", receipts[0], want)
		}
	}
}
