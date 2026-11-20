package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestStartInSessionScopesJobsAndFiltersThem 覆盖隔离保证：
// TestStartInSessionScopesJobsAndFiltersThem 覆盖隔离保证：共享一个 registry 的两个会话的作业相互独立，ListForSession 按会话过滤，普通 Start 保持其旧版全局序列。会话记录在作业上；普通 Start 仍没有。
// 按会话过滤，普通 Start 保持其旧版全局序列。
func TestStartInSessionScopesJobsAndFiltersThem(t *testing.T) {
	registry := newRegistry(t)
	defer registry.waitIdle()

	a1, err := registry.StartInSession("session-A", shellCommand("echo a1"))
	if err != nil {
		t.Fatalf("start a1: %v", err)
	}
	a2, err := registry.StartInSession("session-A", shellCommand("echo a2"))
	if err != nil {
		t.Fatalf("start a2: %v", err)
	}
	b1, err := registry.StartInSession("session-B", shellCommand("echo b1"))
	if err != nil {
		t.Fatalf("start b1: %v", err)
	}
	plain, err := registry.Start(shellCommand("echo plain"))
	if err != nil {
		t.Fatalf("start plain: %v", err)
	}
	for _, job := range []Job{a1, a2, b1, plain} {
		waitForTerminal(t, registry, job.ID)
	}

	// 会话记录在作业上；普通 Start 仍没有。
	if a1.Session != "session-A" || a2.Session != "session-A" || b1.Session != "session-B" {
		t.Fatalf("sessions = %q, %q, %q", a1.Session, a2.Session, b1.Session)
	}
	if plain.Session != "" {
		t.Fatalf("plain session = %q, want empty", plain.Session)
	}

	// Id 按会话命名空间化，永不冲突，且启动会话作业
	// 并未消耗旧计数器：普通作业仍是 job1。
	seen := map[string]bool{}
	for _, job := range []Job{a1, a2, b1, plain} {
		if seen[job.ID] {
			t.Fatalf("duplicate id %q", job.ID)
		}
		seen[job.ID] = true
	}
	if plain.ID != "job1" {
		t.Fatalf("plain id = %q, want job1", plain.ID)
	}
	for _, job := range []Job{a1, a2} {
		if !strings.Contains(job.ID, "session-A") {
			t.Fatalf("id %q is not namespaced to session-A", job.ID)
		}
	}
	if !strings.Contains(b1.ID, "session-B") {
		t.Fatalf("id %q is not namespaced to session-B", b1.ID)
	}

	// 过滤只返回请求会话的作业。
	listA, err := registry.ListForSession("session-A")
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(listA) != 2 || (listA[0].ID != a1.ID && listA[1].ID != a1.ID) ||
		(listA[0].ID != a2.ID && listA[1].ID != a2.ID) {
		t.Fatalf("list A = %+v", listA)
	}
	listB, err := registry.ListForSession("session-B")
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(listB) != 1 || listB[0].ID != b1.ID {
		t.Fatalf("list B = %+v", listB)
	}
	listPlain, err := registry.ListForSession("")
	if err != nil {
		t.Fatalf("list plain: %v", err)
	}
	if len(listPlain) != 1 || listPlain[0].ID != plain.ID {
		t.Fatalf("list plain = %+v", listPlain)
	}
	empty, err := registry.ListForSession("no-such-session")
	if err != nil {
		t.Fatalf("list unknown: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("list unknown = %+v", empty)
	}

	// Id 按会话命名空间化，永不冲突，且启动会话作业并未消耗旧计数器：普通作业仍是 job1。过滤只返回请求会话的作业。未改动的 List 仍看到所有作业。
	all, err := registry.List()
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("list all = %d jobs, want 4", len(all))
	}
}

// TestLegacyJobsWithoutSessionStayVisible 锁定 jobs.json 兼容性：缺少 "session" 键解码为空会话，因此旧作业仍留在全局列表和 ListForSession("") 中，且不能与新 id 冲突。没有 "session" 也没有 "sessionNext" 键：正是较旧构建写出的内容。新 id 不得复用持久化的旧 id 或加载的计数器。旧作业仍与新作业一起可见。
// 缺少 "session" 键解码为空会话，因此旧作业仍留在
// 全局列表和 ListForSession("") 中，且不能与新 id 冲突。
func TestLegacyJobsWithoutSessionStayVisible(t *testing.T) {
	dir := t.TempDir()
	// 没有 "session" 也没有 "sessionNext" 键：正是较旧构建写出的内容。
	raw := `{"next":1,"jobs":[{"id":"job1","command":"legacy","startedAt":"2020-01-01T00:00:00Z","status":"exited","exitCode":0,"logPath":"","pid":0}]}`
	if err := os.WriteFile(filepath.Join(dir, storeFile), []byte(raw), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	registry, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer registry.waitIdle()

	all, err := registry.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || all[0].ID != "job1" || all[0].Session != "" {
		t.Fatalf("legacy job = %+v", all)
	}
	legacy, err := registry.ListForSession("")
	if err != nil {
		t.Fatalf("list empty session: %v", err)
	}
	if len(legacy) != 1 || legacy[0].ID != "job1" {
		t.Fatalf("legacy view = %+v", legacy)
	}
	other, err := registry.ListForSession("session-A")
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("a legacy job leaked into another session: %+v", other)
	}

	// 新 id 不得复用持久化的旧 id 或加载的计数器。
	plain, err := registry.Start(shellCommand("echo new-plain"))
	if err != nil {
		t.Fatalf("start plain: %v", err)
	}
	scoped, err := registry.StartInSession("session-A", shellCommand("echo new-scoped"))
	if err != nil {
		t.Fatalf("start scoped: %v", err)
	}
	waitForTerminal(t, registry, plain.ID)
	waitForTerminal(t, registry, scoped.ID)
	if plain.ID != "job2" {
		t.Fatalf("plain id = %q, want job2", plain.ID)
	}
	if plain.ID == scoped.ID || scoped.ID == "job1" {
		t.Fatalf("id collision: plain=%q scoped=%q", plain.ID, scoped.ID)
	}
	// 旧作业仍与新作业一起可见。
	all, err = registry.List()
	if err != nil {
		t.Fatalf("list after starts: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list after starts = %+v", all)
	}
}

// TestSessionIDsSurviveReopen 验证会话计数器被持久化，使
// TestSessionIDsSurviveReopen 验证会话计数器被持久化，使重启的 registry 永不复用其日志可能仍在磁盘上的 id。
func TestSessionIDsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	before, err := first.StartInSession("session-A", shellCommand("echo before"))
	if err != nil {
		t.Fatalf("start before: %v", err)
	}
	waitForTerminal(t, first, before.ID)
	first.waitIdle()

	second, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.waitIdle()
	after, err := second.StartInSession("session-A", shellCommand("echo after"))
	if err != nil {
		t.Fatalf("start after: %v", err)
	}
	waitForTerminal(t, second, after.ID)
	if after.ID == before.ID {
		t.Fatalf("session id %q was reused after reopen", after.ID)
	}
	listed, err := second.ListForSession("session-A")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("list = %+v", listed)
	}
}

// TestConcurrentSessionStartsGetUniqueIDs 在竞态检测器下检查会话计数器
// TestConcurrentSessionStartsGetUniqueIDs 在多个会话并发共享一个 registry 时在竞态检测器下检查会话计数器。
func TestConcurrentSessionStartsGetUniqueIDs(t *testing.T) {
	registry := newRegistry(t)
	defer registry.waitIdle()

	const count = 4
	sessions := []string{"s1", "s2", "s3", "s4"}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ids  []string
		errs []error
	)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(session string) {
			defer wg.Done()
			job, err := registry.StartInSession(session, shellCommand("echo concurrent"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids = append(ids, job.ID)
		}(sessions[i])
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("start errors: %v", errs)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	for _, id := range ids {
		waitForTerminal(t, registry, id)
	}
}

// TestSessionIDIsSafeAsAFileName 确保奇怪的会话名不会在 id 变成日志名时把路径逃逸出 registry 目录。已完成的作业必须被置之不理：StopAll 只触及运行中的作业。已经退出的作业保持其终态。
// 变成逃逸 registry 目录的路径。
func TestSessionIDIsSafeAsAFileName(t *testing.T) {
	registry := newRegistry(t)
	defer registry.waitIdle()

	job, err := registry.StartInSession("../odd\\name/../x", shellCommand("echo safe"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, registry, job.ID)
	if strings.ContainsAny(job.ID, `/\`) {
		t.Fatalf("id %q contains a path separator", job.ID)
	}
	wantDir := registry.Dir() + string(os.PathSeparator)
	if !strings.HasPrefix(job.LogPath, wantDir) {
		t.Fatalf("log path %q escapes %q", job.LogPath, wantDir)
	}
}
