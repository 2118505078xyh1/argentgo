package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var (
	defTimeV1 = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	defTimeV2 = time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	defTimeV3 = time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
)

// writeDefFile 用确定的 mtime 创建（或覆盖）一个定义文件，使指纹变化来自显式的测试动作，而非时钟跳动。
// （接上行）
func writeDefFile(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// newDefFixture 放置一个命令和一个技能（后者位于子目录中，以覆盖 SKILL.md 位于目录中的发现路径）。
// （接上行）
func newDefFixture(t *testing.T) (commandRoot, skillRoot, commandFile, skillFile string) {
	t.Helper()
	base := t.TempDir()
	commandRoot = filepath.Join(base, "commands")
	skillRoot = filepath.Join(base, "skills")
	commandFile = filepath.Join(commandRoot, "hello.md")
	skillFile = filepath.Join(skillRoot, "deploy", "SKILL.md")

	writeDefFile(t, commandFile, "---\ndescription: say hello\n---\nHello $ARGUMENTS\n", defTimeV1)
	writeDefFile(t, skillFile, "---\nname: deploy\ndescription: ship it\n---\nStep one.\n", defTimeV1)
	return commandRoot, skillRoot, commandFile, skillFile
}

func assertDefStats(t *testing.T, c *DefinitionCache, hits, misses, refreshes int64) {
	t.Helper()
	gotHits, gotMisses, gotRefreshes := c.Stats()
	if gotHits != hits || gotMisses != misses || gotRefreshes != refreshes {
		t.Fatalf("Stats() = (%d, %d, %d), want (%d, %d, %d) [hits, misses, refreshes]",
			gotHits, gotMisses, gotRefreshes, hits, misses, refreshes)
	}
}

func TestDefinitionCacheGetReusesSnapshotsOnHit(t *testing.T) {
	commandRoot, skillRoot, _, _ := newDefFixture(t)
	c := &DefinitionCache{}

	commands1, skills1 := c.Get([]string{commandRoot}, []string{skillRoot})
	if commands1.Len() != 1 || skills1.Len() != 1 {
		t.Fatalf("first Get: got %d commands, %d skills; want 1, 1", commands1.Len(), skills1.Len())
	}

	commands2, skills2 := c.Get([]string{commandRoot}, []string{skillRoot})
	if commands2 != commands1 {
		t.Fatal("command set pointer changed on an unchanged fingerprint")
	}
	if skills2 != skills1 {
		t.Fatal("skill registry pointer changed on an unchanged fingerprint")
	}

	assertDefStats(t, c, 1, 1, 0)
}

func TestDefinitionCacheRediscoversWhenFileChanges(t *testing.T) {
	commandRoot, skillRoot, commandFile, _ := newDefFixture(t)
	c := &DefinitionCache{}

	commands1, skills1 := c.Get([]string{commandRoot}, []string{skillRoot})
	writeDefFile(t, commandFile, "---\ndescription: say hi\n---\nHi $ARGUMENTS\n", defTimeV2)
	commands2, skills2 := c.Get([]string{commandRoot}, []string{skillRoot})

	if commands2 == commands1 {
		t.Fatal("command set was reused after the file changed")
	}
	if skills2 == skills1 {
		t.Fatal("skill registry was reused after roots changed on disk")
	}
	cmd, ok := commands2.Get("hello")
	if !ok || cmd.Body != "Hi $ARGUMENTS" {
		t.Fatalf("rediscovery did not pick up the edited body: %+v (found=%v)", cmd, ok)
	}
	assertDefStats(t, c, 0, 2, 1)
}

func TestDefinitionCachePicksUpAddedFile(t *testing.T) {
	commandRoot, skillRoot, _, _ := newDefFixture(t)
	c := &DefinitionCache{}

	_, skills1 := c.Get([]string{commandRoot}, []string{skillRoot})
	if skills1.Len() != 1 {
		t.Fatalf("initial skill count = %d, want 1", skills1.Len())
	}

	writeDefFile(t, filepath.Join(skillRoot, "review", "SKILL.md"),
		"---\nname: review\ndescription: review code\n---\nRead the diff.\n", defTimeV2)
	_, skills2 := c.Get([]string{commandRoot}, []string{skillRoot})

	if skills2 == skills1 {
		t.Fatal("skill registry was reused after a new skill file was added")
	}
	if skills2.Len() != 2 {
		t.Fatalf("skill count after add = %d, want 2", skills2.Len())
	}
	if _, ok := skills2.Get("review"); !ok {
		t.Fatal("new skill was not discovered")
	}
	assertDefStats(t, c, 0, 2, 1)
}

func TestDefinitionCacheRefreshForcesRediscover(t *testing.T) {
	commandRoot, skillRoot, _, _ := newDefFixture(t)
	c := &DefinitionCache{}

	commands1, skills1 := c.Get([]string{commandRoot}, []string{skillRoot})
	c.Refresh()
	commands2, skills2 := c.Get([]string{commandRoot}, []string{skillRoot})

	if commands2 == commands1 || skills2 == skills1 {
		t.Fatal("Refresh did not force rediscovery")
	}
	assertDefStats(t, c, 0, 2, 1)
}

func TestDefinitionCacheNonexistentRoots(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "does-not-exist")
	c := &DefinitionCache{}

	commands1, skills1 := c.Get([]string{missing}, []string{missing})
	if commands1 == nil || skills1 == nil {
		t.Fatal("Get returned nil snapshots for missing roots")
	}
	if commands1.Len() != 0 || skills1.Len() != 0 {
		t.Fatalf("missing roots produced %d commands, %d skills; want 0, 0",
			commands1.Len(), skills1.Len())
	}

	commands2, skills2 := c.Get([]string{missing}, []string{missing})
	if commands2 != commands1 || skills2 != skills1 {
		t.Fatal("snapshots were not reused for unchanged missing roots")
	}
	assertDefStats(t, c, 1, 1, 0)
}

func TestDefinitionCacheFingerprintOfStableAndSensitive(t *testing.T) {
	commandRoot, skillRoot, commandFile, _ := newDefFixture(t)
	c := &DefinitionCache{}
	roots := []string{commandRoot, skillRoot}

	first := c.fingerprintOf(roots)
	if len(first) != fingerprintHexLen {
		t.Fatalf("fingerprint length = %d, want %d (%q)", len(first), fingerprintHexLen, first)
	}
	if again := c.fingerprintOf(roots); again != first {
		t.Fatalf("fingerprint is not stable: %q != %q", again, first)
	}

	writeDefFile(t, commandFile, "---\ndescription: say hi\n---\nHi $ARGUMENTS\n", defTimeV3)
	if changed := c.fingerprintOf(roots); changed == first {
		t.Fatal("fingerprint did not change after editing a file")
	}

	writeDefFile(t, filepath.Join(commandRoot, "new.md"), "---\ndescription: new\n---\nNew.\n", defTimeV1)
	beforeAdd := c.fingerprintOf(roots)
	if beforeAdd == first {
		t.Fatal("fingerprint did not change after adding a file")
	}
	if afterAdd := c.fingerprintOf(roots); afterAdd != beforeAdd {
		t.Fatal("fingerprint changed between two identical walks")
	}

	missingPath := filepath.Join(t.TempDir(), "nope")
	missing := c.fingerprintOf([]string{missingPath})
	if missing == "" || len(missing) != fingerprintHexLen {
		t.Fatalf("missing roots produced an invalid fingerprint %q", missing)
	}
	if again := c.fingerprintOf([]string{missingPath}); again != missing {
		t.Fatalf("fingerprint is not stable for a missing root: %q != %q", again, missing)
	}
}

func TestDefinitionCacheConcurrentGetIsSafe(t *testing.T) {
	commandRoot, skillRoot, _, _ := newDefFixture(t)
	c := &DefinitionCache{}

	const goroutines = 32
	var wg sync.WaitGroup

	// 阶段 1：没有文件系统变化，因此每个调用方都必须看到相同的快照。（占位切片只计数结果，不区分类型。）
	// （接上行）
	type pair struct {
		commands interface{}
		skills   interface{}
	}
	seen := make([]pair, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			cmd, sk := c.Get([]string{commandRoot}, []string{skillRoot})
			seen[i] = pair{commands: cmd, skills: sk}
		}(i)
	}
	wg.Wait()

	first := seen[0]
	for i, got := range seen {
		if got != first {
			t.Fatalf("goroutine %d saw different pointers on an unchanged tree", i)
		}
	}
	hits, misses, _ := c.Stats()
	if hits+misses != goroutines {
		t.Fatalf("hits+misses = %d, want %d", hits+misses, goroutines)
	}

	// 阶段 2：在编辑目录树的同时并发 Refresh/Get。确切的重新发现次数取决于时序，但不得有竞态、panic 或计数丢失。
	// （接上行）
	// （接上行）
	writeDefFile(t, filepath.Join(commandRoot, "hello.md"),
		"---\ndescription: updated\n---\nUpdated.\n", defTimeV2)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			if i%4 == 0 {
				c.Refresh()
			}
			c.Get([]string{commandRoot}, []string{skillRoot})
		}(i)
	}
	wg.Wait()

	hits, misses, _ = c.Stats()
	if hits+misses != 2*goroutines {
		t.Fatalf("hits+misses = %d, want %d", hits+misses, 2*goroutines)
	}
	finalCommands, _ := c.Get([]string{commandRoot}, []string{skillRoot})
	if cmd, ok := finalCommands.Get("hello"); !ok || cmd.Body != "Updated." {
		t.Fatalf("final snapshots are stale after concurrent access: %+v (found=%v)", cmd, ok)
	}
}
