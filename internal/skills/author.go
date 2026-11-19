package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrInvalidSkillName is returned when a name is not lowercase kebab-case or
// tries to escape its skills root.
var ErrInvalidSkillName = errors.New("invalid skill name")

// ErrSkillExists is returned when a skill already exists at the target path
// and the caller did not opt into overwriting it.
var ErrSkillExists = errors.New("skill already exists")

// maxNameLength bounds authored names so a model cannot create absurd paths.
const maxNameLength = 64

// AuthorOptions configures AuthorWithOptions.
type AuthorOptions struct {
	// Overwrite permits replacing an existing SKILL.md at the target path.
	Overwrite bool
}

// AuthorResult is the outcome of a successful authoring operation.
type AuthorResult struct {
	// Path is the SKILL.md file that was written.
	Path string
	// Name is the validated skill name.
	Name string
}

// authorMu serialises authors within one process so the no-overwrite check is
// reliable when several callers race to create the same skill. Discovery in
// this package is safe without the lock because publishing uses temp+rename.
var authorMu sync.Mutex

// Author writes a well-formed SKILL.md for name under root and returns its
// path. With an empty root the user's skill directory is used. Existing skills
// are never replaced; use AuthorWithOptions with Overwrite to allow that.
//
// The signature deliberately uses only stdlib types so an agent package can
// declare an identical local interface without importing this one.
func Author(root, name, description, body string) (string, error) {
	res, err := AuthorWithOptions(root, name, description, body, AuthorOptions{})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// AuthorWithOptions is Author with explicit options.
func AuthorWithOptions(root, name, description, body string, opts AuthorOptions) (AuthorResult, error) {
	if err := validateName(name); err != nil {
		return AuthorResult{}, err
	}
	target, err := authorTarget(root, name)
	if err != nil {
		return AuthorResult{}, err
	}
	content := renderSkill(name, description, body)

	authorMu.Lock()
	defer authorMu.Unlock()
	if err := publishSkill(target, content, opts.Overwrite); err != nil {
		return AuthorResult{}, err
	}
	return AuthorResult{Path: target, Name: name}, nil
}

// validateName enforces lowercase kebab-case with no path traversal. The
// character rules alone reject dots, separators and uppercase, but the
// explicit ".." check gives callers a clearer diagnostic.
func validateName(name string) error {
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q contains '..'", ErrInvalidSkillName, name)
	}
	if name == "" || len(name) > maxNameLength {
		return fmt.Errorf("%w: %q must be 1-%d characters", ErrInvalidSkillName, name, maxNameLength)
	}
	dash := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			dash = false
		case c == '-':
			if i == 0 || dash {
				return fmt.Errorf("%w: %q has a misplaced '-'", ErrInvalidSkillName, name)
			}
			dash = true
		default:
			return fmt.Errorf("%w: %q must be lowercase kebab-case", ErrInvalidSkillName, name)
		}
	}
	if dash {
		return fmt.Errorf("%w: %q must not end with '-'", ErrInvalidSkillName, name)
	}
	return nil
}

// authorTarget resolves the absolute location of a new skill. A non-empty root
// is a workspace (project skills); an empty root uses the user's home skills.
func authorTarget(root, name string) (string, error) {
	base, subdir := root, DefaultProjectSubdir
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("skills: user home directory: %w", err)
		}
		base, subdir = home, DefaultHomeSubdir
	}
	return filepath.Join(base, filepath.FromSlash(subdir), name, "SKILL.md"), nil
}

// renderSkill builds the document. name is validated before it is embedded,
// so it needs no quoting; description is written as a single-quoted YAML
// scalar with doubled quotes and newlines folded to spaces, which round-trips
// through ParseFields exactly and cannot break out of the frontmatter block.
func renderSkill(name, description, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "description: %s\n", yamlScalar(description))
	b.WriteString("---\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func yamlScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// publishSkill writes to a temp file in the same directory and renames it
// into place. A concurrent Discover therefore sees either the old file or the
// complete new one, never a half-written document.
func publishSkill(target string, content []byte, overwrite bool) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("skills: create %s: %w", dir, err)
	}
	if !overwrite {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("%w: %s", ErrSkillExists, target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("skills: stat %s: %w", target, err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".SKILL.md-*")
	if err != nil {
		return fmt.Errorf("skills: temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: chmod: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("skills: close: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("skills: publish %s: %w", target, err)
	}
	return nil
}
