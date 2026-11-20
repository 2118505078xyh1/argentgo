// Package skills discovers SKILL.md procedures on disk and loads them on demand.
//
// A skill is a directory containing `SKILL.md` (a bare `<name>.md` is accepted
// too). The file starts with a `---` frontmatter block carrying at least `name`
// and `description`; the rest is the procedure itself.
//
// Only the compact index (one line per skill) belongs in the system prompt; the
// body is fetched through the `use_skill` tool. Twenty installed skills then
// cost twenty lines instead of twenty documents.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// DefaultProjectSubdir is where a repository keeps its own skills.
const DefaultProjectSubdir = ".argent/skills"

// DefaultHomeSubdir is where the user keeps skills shared by every project.
const DefaultHomeSubdir = ".argentgo/skills"

// ErrNotFound is returned by Load when no skill carries the requested name.
var ErrNotFound = errors.New("skill not found")

// Skill is one procedure on disk.
type Skill struct {
	// Name is what the model passes to the tool.
	Name string
	// Description is the one-line trigger summary shown in the index.
	Description string
	// WhenToUse is the longer "load me when…" hint, also shown in the index.
	WhenToUse string
	// ArgumentHint is the usage hint from the frontmatter `argument-hint` field.
	// It carries optional enumerated candidates used by SkillArgumentHints.
	ArgumentHint string
	// Body is the procedure without frontmatter.
	Body string
	// Path is the SKILL.md file the procedure came from.
	Path string
	// Tools names script tools the skill carries (frontmatter `tools`).
	Tools []string
	// AllowedTools is the tool allow-list advertised by the skill.
	AllowedTools []string
	// Warnings reports invisible characters found in the declarative content.
	// It is metadata only: parsing never changes and never blocks on it.
	Warnings []string
	// Triggers holds optional automatic-activation rules. It is metadata only:
	// nothing here changes how the skill loads by name through use_skill.
	Triggers Triggers
}

// Entry is the compact form of a skill used by the index and by Match.
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	WhenToUse   string `json:"when_to_use,omitempty"`
	// Score is only set by Match; it is never serialised.
	Score int `json:"-"`
}

// Entry returns the compact index form of a skill.
func (s Skill) Entry() Entry {
	return Entry{Name: s.Name, Description: s.Description, WhenToUse: s.WhenToUse}
}

// Parse reads one skill document. path is used for diagnostics and as the
// fallback name (the directory name for `.../foo/SKILL.md`).
func Parse(text, path string) (Skill, error) {
	frontmatter, body, ok := SplitFrontmatter(text)
	if !ok {
		return Skill{}, fmt.Errorf("%s: skill file must start with a '---' frontmatter block", path)
	}
	fields := ParseFields(frontmatter)

	name := fields.Get("name")
	if name == "" {
		name = skillNameFromPath(path)
	}
	if name == "" {
		name = "unnamed"
	}
	return Skill{
		Name:         name,
		Description:  fields.Get("description"),
		WhenToUse:    fields.Get("when_to_use"),
		ArgumentHint: fields.Get("argument-hint"),
		Body:         strings.TrimSpace(body),
		Path:         path,
		Tools:        fields.List("tools"),
		AllowedTools: fields.List("allowed-tools"),
		Warnings:     security.ScanArtifact(text).Warnings(),
		Triggers: Triggers{
			Paths:    fields.List("paths"),
			Keywords: fields.List("keywords"),
			Models:   fields.List("models"),
		},
	}, nil
}

func skillNameFromPath(path string) string {
	base := filepath.Base(path)
	if strings.EqualFold(base, "SKILL.md") || strings.EqualFold(base, "SKILL.markdown") {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// DefaultRoots returns the skill directories searched for one session, in
// priority order: the project first, then the user's home directory.
func DefaultRoots(cwd, home string) []string {
	var roots []string
	if cwd != "" {
		roots = append(roots, filepath.Join(cwd, filepath.FromSlash(DefaultProjectSubdir)))
	}
	if home != "" {
		roots = append(roots, filepath.Join(home, filepath.FromSlash(DefaultHomeSubdir)))
	}
	return roots
}

// Registry is the set of skills available to one session.
type Registry struct {
	skills []Skill
}

// New returns an empty registry.
func New() *Registry { return &Registry{} }

// Discover loads every skill under roots.
//
// Roots are searched in order and a later root never replaces an earlier one,
// so a project skill shadows the user's skill with the same name. A directory
// or file that fails to parse is skipped: one broken skill must not hide the
// rest.
func Discover(roots []string) *Registry {
	r := New()
	for _, root := range roots {
		for _, candidate := range skillFiles(root) {
			text, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			skill, err := Parse(string(text), candidate)
			if err != nil {
				continue
			}
			r.addIfAbsent(skill)
		}
	}
	sort.Slice(r.skills, func(i, j int) bool { return r.skills[i].Name < r.skills[j].Name })
	return r
}

func skillFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			file := filepath.Join(path, "SKILL.md")
			if info, err := os.Stat(file); err == nil && !info.IsDir() {
				files = append(files, file)
			}
			continue
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

// Add registers a skill, replacing any existing skill with the same name.
func (r *Registry) Add(skill Skill) {
	for i := range r.skills {
		if r.skills[i].Name == skill.Name {
			r.skills[i] = skill
			return
		}
	}
	r.skills = append(r.skills, skill)
}

func (r *Registry) addIfAbsent(skill Skill) {
	for _, existing := range r.skills {
		if existing.Name == skill.Name {
			return
		}
	}
	r.skills = append(r.skills, skill)
}

// All returns every skill, sorted by name.
func (r *Registry) All() []Skill {
	out := make([]Skill, len(r.skills))
	copy(out, r.skills)
	return out
}

// Len returns the number of installed skills.
func (r *Registry) Len() int { return len(r.skills) }

// Get finds one skill by exact name.
func (r *Registry) Get(name string) (Skill, bool) {
	name = strings.TrimSpace(name)
	for _, skill := range r.skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

// Index returns the compact entries for the system prompt.
func (r *Registry) Index() []Entry {
	entries := make([]Entry, 0, len(r.skills))
	for _, skill := range r.skills {
		entries = append(entries, skill.Entry())
	}
	return entries
}

// IndexText renders the index as the block appended to the system prompt.
// It is empty when no skill is installed.
func (r *Registry) IndexText() string {
	if len(r.skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills: procedures you can load by name with the use_skill tool. ")
	b.WriteString("Load one when the task matches its description; do not load all of them.\n")
	for _, skill := range r.skills {
		// Declarative metadata is untrusted and may come from a block scalar or
		// a quoted \n escape. Fold it so each skill still occupies exactly one
		// index line and cannot inject entries into the system prompt.
		desc, when := oneLine(skill.Description), oneLine(skill.WhenToUse)
		switch {
		case desc == "" && when == "":
			fmt.Fprintf(&b, "- %s\n", skill.Name)
		case when == "":
			fmt.Fprintf(&b, "- %s: %s\n", skill.Name, desc)
		case desc == "":
			fmt.Fprintf(&b, "- %s (when: %s)\n", skill.Name, when)
		default:
			fmt.Fprintf(&b, "- %s: %s (when: %s)\n", skill.Name, desc, when)
		}
	}
	return b.String()
}

// oneLine collapses embedded whitespace so a single skill cannot emit more
// than one line in a plain-text rendering. Parse intentionally preserves
// newlines in block scalars, so the folding belongs at the rendering boundary.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Load returns the full skill, body included.
func (r *Registry) Load(name string) (Skill, error) {
	if skill, ok := r.Get(name); ok {
		return skill, nil
	}
	wanted := strings.TrimSpace(name)
	if suggestions := r.Suggest(wanted); len(suggestions) > 0 {
		return Skill{}, fmt.Errorf("%w: %q (did you mean: %s)", ErrNotFound, wanted, strings.Join(suggestions, ", "))
	}
	return Skill{}, fmt.Errorf("%w: %q", ErrNotFound, wanted)
}

// Match ranks skills by keyword overlap with prompt.
//
// The score counts distinct prompt words that also appear in the name (weight
// 4), the when_to_use hint (weight 3) or the description (weight 2). Only
// skills with a positive score are returned, best first, then by name.
func (r *Registry) Match(prompt string) []Entry {
	words := keywords(prompt)
	if len(words) == 0 {
		return nil
	}
	var matched []Entry
	for _, skill := range r.skills {
		score := overlap(words, keywords(skill.Name))*4 +
			overlap(words, keywords(skill.WhenToUse))*3 +
			overlap(words, keywords(skill.Description))*2
		if score == 0 {
			continue
		}
		entry := skill.Entry()
		entry.Score = score
		matched = append(matched, entry)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Score != matched[j].Score {
			return matched[i].Score > matched[j].Score
		}
		return matched[i].Name < matched[j].Name
	})
	return matched
}

// Suggest lists skill names loosely related to name, for a miss hint.
func (r *Registry) Suggest(name string) []string {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return nil
	}
	var names []string
	for _, skill := range r.skills {
		lower := strings.ToLower(skill.Name)
		haystack := lower + " " + strings.ToLower(skill.Description)
		if strings.Contains(haystack, needle) || strings.Contains(needle, lower) {
			names = append(names, skill.Name)
		}
	}
	sort.Strings(names)
	return names
}

// stopwords keeps filler words from creating matches on their own.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "do": true, "for": true, "from": true,
	"how": true, "i": true, "if": true, "in": true, "into": true, "is": true,
	"it": true, "me": true, "my": true, "of": true, "on": true, "or": true,
	"so": true, "that": true, "the": true, "then": true, "this": true, "to": true,
	"use": true, "when": true, "with": true, "you": true,
}

// keywords lower-cases s and splits it into distinct word tokens, dropping
// filler words and single characters.
func keywords(s string) map[string]bool {
	words := make(map[string]bool)
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		word := strings.ToLower(current.String())
		current.Reset()
		if len(word) < 2 || stopwords[word] {
			return
		}
		words[word] = true
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return words
}

func overlap(a, b map[string]bool) int {
	count := 0
	for word := range a {
		if b[word] {
			count++
		}
	}
	return count
}
