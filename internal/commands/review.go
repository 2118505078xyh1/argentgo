package commands

// Built-in /review: a read-only inspection of a dirty git tree. The only
// commands it may issue are `git status --porcelain` and
// `git diff --unified=3`, both through an injected runner so callers can
// enforce the read-only boundary and tests need no real repository.

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/security"
)

// reviewLargeDiffLines flags a file whose diff adds an unusually large number
// of lines. git status alone cannot report file sizes, so diff volume is the
// read-only proxy used here.
const reviewLargeDiffLines = 200

// CommandRunner runs a single read-only process and returns its stdout.
// Implementations should reject any command other than the two git reads
// Review asks for.
type CommandRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

// RunnerFunc adapts a function to CommandRunner.
type RunnerFunc func(name string, args ...string) ([]byte, error)

// Run implements CommandRunner.
func (f RunnerFunc) Run(name string, args ...string) ([]byte, error) { return f(name, args...) }

// Finding is one compact review observation. Detail is always redacted.
type Finding struct {
	Kind   string // todo, debug, large-file, secret
	File   string
	Line   int
	Detail string
}

// ReviewReport is the result of a read-only diff review.
type ReviewReport struct {
	Clean    bool
	Files    []string
	Findings []Finding
}

// String renders the compact, human-readable findings report.
func (r ReviewReport) String() string {
	if r.Clean {
		return "no changes"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d changed file(s):\n", len(r.Files))
	for _, file := range r.Files {
		fmt.Fprintf(&out, "  %s\n", file)
	}
	if len(r.Findings) == 0 {
		out.WriteString("findings: none\n")
		return out.String()
	}
	fmt.Fprintf(&out, "findings (%d):\n", len(r.Findings))
	for _, finding := range r.Findings {
		location := finding.File
		if finding.Line > 0 {
			location += ":" + strconv.Itoa(finding.Line)
		}
		fmt.Fprintf(&out, "  [%s] %s %s\n", finding.Kind, location, finding.Detail)
	}
	return out.String()
}

// Review inspects the working tree with two read-only git commands. It never
// writes files, stages changes or shells out to anything else.
func Review(runner CommandRunner) (ReviewReport, error) {
	if runner == nil {
		return ReviewReport{}, errors.New("commands: nil git command runner")
	}
	status, err := runner.Run("git", "status", "--porcelain")
	if err != nil {
		return ReviewReport{}, fmt.Errorf("commands: git status: %w", err)
	}
	files := changedFiles(string(status))
	if len(files) == 0 {
		return ReviewReport{Clean: true}, nil
	}
	diff, err := runner.Run("git", "diff", "--unified=3")
	if err != nil {
		return ReviewReport{}, fmt.Errorf("commands: git diff: %w", err)
	}
	return ReviewReport{Files: files, Findings: diffFindings(string(diff))}, nil
}

// ReviewCommand is the built-in slash-command descriptor. Registration lives
// in internal/app; call Set.Add(ReviewCommand()) after Discover and route
// `/review` to ReviewHook.Run.
func ReviewCommand() Command {
	return Command{
		Name:        "review",
		Description: "Read-only review of the dirty git tree without modifying it",
		Body:        "Review the working tree diff: report TODO/FIXME markers, debug prints, large diffs and secret-looking literals. Do not modify files.",
		Path:        "<builtin:review>",
	}
}

// ReviewHook is the registration seam for the /review slash command. A caller
// supplies a read-only CommandRunner (typically backed by internal/proc) and
// invokes Run with the user's arguments.
type ReviewHook struct {
	Runner CommandRunner
}

// Run executes the review and returns a prompt-ready findings report.
func (h ReviewHook) Run(_ string) (string, error) {
	report, err := Review(h.Runner)
	if err != nil {
		return "", err
	}
	return report.String(), nil
}

// Add registers built-in commands into a discovered set. Explicit commands
// from markdown files win; Add never overwrites an existing name.
func (s *Set) Add(cmds ...Command) {
	if s == nil {
		return
	}
	for _, cmd := range cmds {
		if cmd.Name == "" {
			continue
		}
		if _, exists := s.Get(cmd.Name); exists {
			continue
		}
		s.commands = append(s.commands, cmd)
	}
	sort.Slice(s.commands, func(i, j int) bool { return s.commands[i].Name < s.commands[j].Name })
}

func changedFiles(status string) []string {
	seen := make(map[string]bool)
	var files []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if index := strings.Index(path, " -> "); index >= 0 {
			path = path[index+4:]
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

type addedLine struct {
	file string
	line int
	text string
}

var todoPattern = regexp.MustCompile(`\b(?:TODO|FIXME)\b`)

// quotedSecretPattern covers the `apiKey := "value"` shape that
// security.Redact's simple key/value scan can miss. The local pass runs first
// so the quoted value is gone before the general redactor rewrites the line.
var quotedSecretPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|passwd|token|access[_-]?key|private[_-]?key)\s*(?::=|=|:)\s*["']([^"']{6,})["']`)

func redactFinding(text string) string {
	return security.Redact(quotedSecretPattern.ReplaceAllString(text, `${1}="[redacted:secret]"`))
}

var debugMarkers = []string{
	"fmt.Print(", "fmt.Println(", "console.log(", "console.debug(",
	"println(", "print(", "debugger;", "dbg!", "pprint(",
}

func diffFindings(diff string) []Finding {
	var added []addedLine
	counts := make(map[string]int)
	file, lineNo := "", 0
	for _, raw := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(raw, "+++ "):
			file = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(raw, "+++ ")), "b/")
			if file == "/dev/null" {
				file = ""
			}
		case strings.HasPrefix(raw, "@@ "):
			lineNo = hunkStart(raw)
		case strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "+++"):
			// Context lines occupy a line in the new file too.
			lineNo++
		case strings.HasPrefix(raw, "+") && !strings.HasPrefix(raw, "+++"):
			if file == "" {
				continue
			}
			counts[file]++
			added = append(added, addedLine{file: file, line: lineNo, text: strings.TrimPrefix(raw, "+")})
			lineNo++
		}
	}

	findings := make([]Finding, 0, len(added))
	for _, item := range added {
		if redacted := redactFinding(item.text); redacted != item.text {
			findings = append(findings, Finding{
				Kind: "secret", File: item.file, Line: item.line, Detail: clipFinding(redacted, 120),
			})
		}
		if todoPattern.MatchString(item.text) {
			findings = append(findings, Finding{
				Kind: "todo", File: item.file, Line: item.line, Detail: clipFinding(item.text, 120),
			})
		}
		if marker := debugMarker(item.text); marker != "" {
			findings = append(findings, Finding{
				Kind: "debug", File: item.file, Line: item.line, Detail: marker + ": " + clipFinding(item.text, 120),
			})
		}
	}
	for file, count := range counts {
		if count > reviewLargeDiffLines {
			findings = append(findings, Finding{
				Kind: "large-file", File: file,
				Detail: fmt.Sprintf("diff adds %d lines", count),
			})
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Kind < findings[j].Kind
	})
	return findings
}

func hunkStart(header string) int {
	index := strings.Index(header, " +")
	if index < 0 {
		return 0
	}
	part := strings.TrimSpace(header[index+2:])
	if end := strings.IndexAny(part, " ,"); end >= 0 {
		part = part[:end]
	}
	value, _ := strconv.Atoi(part)
	return value
}

func debugMarker(text string) string {
	for _, marker := range debugMarkers {
		if strings.Contains(text, marker) {
			return marker
		}
	}
	return ""
}

func clipFinding(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return text[:limit-1] + "…"
}
