package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchSummaryOnCheckout(t *testing.T) {
	// 默认开启：不设置环境变量也应生成摘要。
	t.Setenv(branchSummaryEnv, "")
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("branch-summary-wire")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(KindUser, map[string]any{"content": "root user"})
	a, err := sess.AppendRecord(KindAssistant, map[string]any{"content": "main line A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := sess.AppendRecord(KindUser, map[string]any{"content": "main line B"})
	if err != nil {
		t.Fatal(err)
	}

	// 从 a 切出侧支，写入「abandoned side work」，再切回主线 b。
	if err := sess.Checkout(a.ID); err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(KindAssistant, map[string]any{"content": "abandoned side work"})
	if err := sess.Checkout(b.ID); err != nil {
		t.Fatal(err)
	}

	text := ""
	for _, e := range sess.Messages() {
		text += e.Content
	}
	if !strings.Contains(text, "abandoned side work") {
		t.Fatalf("abandoned branch summary not projected into messages: %s", text)
	}
}

func TestBranchSummaryOffLeavesCheckoutUnchanged(t *testing.T) {
	// 仅 =0 关闭。
	t.Setenv(branchSummaryEnv, "0")
	dir := t.TempDir()
	store, _ := Open(dir)
	sess, _ := store.NewSession("branch-summary-off")
	_ = sess.Append(KindUser, map[string]any{"content": "root"})
	a, _ := sess.AppendRecord(KindAssistant, map[string]any{"content": "A"})
	_ = sess.Append(KindUser, map[string]any{"content": "B"})
	if err := sess.Checkout(a.ID); err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(KindAssistant, map[string]any{"content": "side"})
	if err := sess.Checkout(a.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, sess.ID()+fileExt))
	for _, e := range sess.Messages() {
		if strings.Contains(e.Content, "branch summary") || strings.Contains(e.Content, "abandoned") {
			t.Fatal("no summary should be projected when =0 off")
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, sess.ID()+fileExt))
	if string(before) != string(after) {
		t.Fatal("checkout should not write when =0 off")
	}
}
