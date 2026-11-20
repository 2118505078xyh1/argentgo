package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// fingerprintHexLen is the number of hex characters kept from the SHA-256
// digest. Sixteen chars (64 bits) are plenty to distinguish definition trees
// while keeping the cached key small.
const fingerprintHexLen = 16

// DefinitionCache memoises command and skill discovery behind a fingerprint of
// the source directories.
//
// commands.Discover and skills.Discover walk the filesystem on every call; at
// the sites that rebuild the system prompt this happens repeatedly within one
// process. Callers keep one DefinitionCache for the lifetime of the session and
// ask it for the current snapshots instead. The fingerprint is cheap (stat only,
// no file reads), so a Get is fast unless something on disk actually changed.
//
// A DefinitionCache must not be copied after first use; use a pointer.
type DefinitionCache struct {
	mu          sync.Mutex
	fingerprint string
	commands    *commands.Set
	skills      *skills.Registry

	loaded bool // a discovery has completed at least once
	dirty  bool // Refresh was called; force rediscovery on the next Get

	hits      int64
	misses    int64
	refreshes int64
}

// Get returns the current command set and skill registry for the given roots.
//
// The roots are the directories commands.Discover / skills.Discover would scan;
// command roots are converted to unnamed commands.Source values (plugin-prefixed
// sources must be handled by the caller). When the fingerprint of either root
// list has not changed since the last discovery, the exact same pointers are
// returned and no filesystem scan beyond stat is performed.
//
// Get is safe for concurrent use.
func (c *DefinitionCache) Get(commandRoots, skillRoots []string) (*commands.Set, *skills.Registry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fingerprint := c.fingerprintOf(commandRoots) + "#" + c.fingerprintOf(skillRoots)
	if c.loaded && !c.dirty && fingerprint == c.fingerprint {
		c.hits++
		return c.commands, c.skills
	}

	// Replacing a previously loaded snapshot is a refresh, whether it was
	// triggered by a fingerprint change or by an explicit Refresh call.
	if c.loaded {
		c.refreshes++
	}
	c.misses++

	c.commands = commands.Discover(commandSources(commandRoots))
	c.skills = skills.Discover(skillRoots)
	c.fingerprint = fingerprint
	c.loaded = true
	c.dirty = false
	return c.commands, c.skills
}

// Refresh invalidates the cached snapshots. The next Get discards them and
// re-runs discovery even if the fingerprint is unchanged. It is safe to call
// from a config-updated / change event handler and is itself non-blocking.
func (c *DefinitionCache) Refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		c.dirty = true
	}
}

// Stats reports cache accounting since construction:
//   - hits: Get calls served from the cached snapshots;
//   - misses: Get calls that ran discovery;
//   - refreshes: misses that replaced an already-loaded snapshot.
func (c *DefinitionCache) Stats() (hits, misses, refreshes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.refreshes
}

// fingerprintOf walks each root and hashes a stable description of every file
// under it: its path relative to the root, modification time (nanoseconds) and
// size. Missing or unreadable roots and entries are skipped so a transient
// permission error cannot suppress valid definitions. The digest is truncated
// to a hex prefix to keep the cache key compact.
//
// Only files are recorded: directory mtimes change whenever an unrelated entry
// is added, whereas the file entries already capture every change that affects
// discovery (add, remove, rename or edit of a markdown file).
func (c *DefinitionCache) fingerprintOf(roots []string) string {
	var b strings.Builder
	for _, root := range roots {
		// The root itself is part of the key so swapping root lists with
		// otherwise identical trees still invalidates the cache.
		fmt.Fprintf(&b, "root:%s\n", filepath.ToSlash(root))
		fingerprintWalk(&b, root, root)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:fingerprintHexLen]
}

// fingerprintWalk recursively records files under dir. os.ReadDir returns
// entries sorted by name, so the produced description is deterministic.
func fingerprintWalk(b *strings.Builder, root, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // nonexistent root or permission denied: skip gracefully
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			continue // raced deletion or permission error on this entry
		}
		if info.IsDir() {
			fingerprintWalk(b, root, path)
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		fmt.Fprintf(b, "%s|%d|%d\n", filepath.ToSlash(rel), info.ModTime().UnixNano(), info.Size())
	}
}

// commandSources adapts plain root paths to the Source list Discover expects.
func commandSources(roots []string) []commands.Source {
	sources := make([]commands.Source, 0, len(roots))
	for _, root := range roots {
		sources = append(sources, commands.Source{Root: root})
	}
	return sources
}
