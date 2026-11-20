package session

import "testing"

// TestForkInheritsNewestTitleWhenOlderTitleIsOnPath 覆盖一个真实的分叉 bug：
// 分叉点可能位于较旧标题记录之后、较新标题覆盖之前。
// 只检查复制路径上是否*存在*标题记录，
// 会让分叉继承过时的旧标题，而不是源会话在分叉时
// 实际显示的标题。
func TestForkInheritsNewestTitleWhenOlderTitleIsOnPath(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("First"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "second prompt"); err != nil {
		t.Fatal(err)
	}
	// 在较新覆盖生效之前固定分叉点。复制的路径
	// 仍包含 "First" 标题记录。
	forkPoint := sess.activeSnapshot()
	if err := sess.SetTitle("Second"); err != nil {
		t.Fatal(err)
	}
	if got := sess.Title(); got != "Second" {
		t.Fatalf("source title = %q, want %q", got, "Second")
	}

	fork, err := sess.Fork(forkPoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "Second" {
		t.Fatalf("fork title = %q, want %q (source's effective title)", got, "Second")
	}
	// 分叉中最新的标题记录必须是带过来的标题，而不是
	// 复制路径上已有的过时标题。
	records, err := fork.Records()
	if err != nil {
		t.Fatal(err)
	}
	var newest string
	for _, record := range records {
		if record.Kind == KindTitle {
			newest = fieldString(record.Data, "title")
		}
	}
	if newest != "Second" {
		t.Fatalf("newest title record in fork = %q, want %q", newest, "Second")
	}
}

// TestForkCarriesDerivedTitleWhenOverrideClearedAfterForkPoint 是过时标题情形的
// 镜像情形：在分叉点之后清除覆盖，
// 使源回退到从 prompt 派生的标题，而分叉必须
// 显示该派生标题，而不是已被清除的显式标题。
func TestForkCarriesDerivedTitleWhenOverrideClearedAfterForkPoint(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "the prompt"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("Named"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "later prompt"); err != nil {
		t.Fatal(err)
	}
	forkPoint := sess.activeSnapshot()
	if err := sess.SetTitle(""); err != nil {
		t.Fatal(err)
	}
	if got := sess.Title(); got != "the prompt" {
		t.Fatalf("source title = %q, want derived %q", got, "the prompt")
	}

	fork, err := sess.Fork(forkPoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "the prompt" {
		t.Fatalf("fork title = %q, want derived %q", got, "the prompt")
	}
}

// TestForkDoesNotDuplicateTitleWhenSourceTitleUnchanged 守护常见路径：
// 当复制路径已显示源的有效标题时，不会
// 追加额外的标题记录，分叉是忠实的副本。
func TestForkDoesNotDuplicateTitleWhenSourceTitleUnchanged(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("Only"); err != nil {
		t.Fatal(err)
	}
	forkPoint := sess.activeSnapshot()
	if err := sess.Append(KindUser, "second prompt"); err != nil {
		t.Fatal(err)
	}

	fork, err := sess.Fork(forkPoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "Only" {
		t.Fatalf("fork title = %q, want %q", got, "Only")
	}
	records, err := fork.Records()
	if err != nil {
		t.Fatal(err)
	}
	titleRecords := 0
	for _, record := range records {
		if record.Kind == KindTitle {
			titleRecords++
		}
	}
	if titleRecords != 1 {
		t.Fatalf("fork has %d title records, want 1", titleRecords)
	}
}
