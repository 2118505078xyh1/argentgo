package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResourceInfo describes one auxiliary file shipped alongside a skill's
// SKILL.md (for example a script under scripts/ or a document under
// references/). Path is a slash-separated path relative to the skill
// directory; Size is the file size in bytes. Callers use this manifest to tell
// a model which files it can read or run after the skill has been loaded.
type ResourceInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ListSkillResources lists the auxiliary files under skillDir. It is a
// read-only scan with no dependencies and no global state: the result depends
// only on the directory contents and maxBytes.
//
// Rules:
//
//   - the skill document itself (SKILL.md or SKILL.markdown, case-insensitive,
//     at any depth) is not a resource and is omitted;
//   - hidden files and directories (names starting with '.') are skipped, and
//     hidden directories are never descended into;
//   - symlinks that resolve outside skillDir are skipped, as are symlinks to
//     anything other than a regular file. Symlinks that stay inside are listed
//     under the link's own relative path with the size of their target;
//   - only regular files are listed;
//   - results are sorted by Path;
//   - maxBytes is the total byte budget. Files are accumulated in path order
//     and any file that would overflow the remaining budget is skipped, which
//     also excludes any single file larger than maxBytes. A non-positive
//     maxBytes yields no resources.
//
// Unreadable entries are skipped rather than failing the whole scan.
func ListSkillResources(skillDir string, maxBytes int64) []ResourceInfo {
	if maxBytes <= 0 {
		return nil
	}
	root, err := filepath.Abs(skillDir)
	if err != nil {
		return nil
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil
	}

	type candidate struct {
		path string
		size int64
	}
	var candidates []candidate

	filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isSkillDocument(name) {
			return nil
		}
		size, ok := resourceSize(p, d, root)
		if !ok {
			return nil
		}
		candidates = append(candidates, candidate{path: filepath.ToSlash(rel), size: size})
		return nil
	})

	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })

	var out []ResourceInfo
	var total int64
	for _, c := range candidates {
		if c.size < 0 || c.size > maxBytes-total {
			continue
		}
		total += c.size
		out = append(out, ResourceInfo{Path: c.path, Size: c.size})
	}
	return out
}

// isSkillDocument reports whether name is a skill document rather than a
// resource. Matching is case-insensitive so a differently-cased duplicate on
// Windows cannot leak into the manifest.
func isSkillDocument(name string) bool {
	lower := strings.ToLower(name)
	return lower == "skill.md" || lower == "skill.markdown"
}

// resourceSize returns the size of the regular file p represents. Symlinks are
// resolved; ones escaping root or pointing at a non-regular file are rejected,
// while ones that stay inside are reported with their target's size.
func resourceSize(p string, d fs.DirEntry, root string) (int64, bool) {
	if d.Type()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !pathWithin(root, resolved) {
			return 0, false
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return 0, false
		}
		return info.Size(), true
	}
	info, err := d.Info()
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Size(), true
}

// pathWithin reports whether target is root itself or lives beneath it. It is
// the containment check that rejects symlinks escaping the skill directory.
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}
