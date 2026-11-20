package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// ---- security_scan ----
//
// A local, read-only static check for the workspace: it walks text files and
// flags credential shapes, dangerous code and (on Unix) world-writable file
// modes. It never opens a network connection or spawns a subprocess. Every
// snippet is scrubbed with security.Redact, so the tool never echoes the
// secret it just found.

const (
	securityScanDefaultMaxFiles = 500
	securityScanMaxFileBytes    = 512 * 1024
	securityScanMaxFindings     = 2000
	securityScanSnippetRunes    = 200
)

// securityScanIgnoredDirs keeps vendored/generated trees out of the walk. The
// names mirror the other workspace-walking tools so their views agree.
var securityScanIgnoredDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "vendor": true,
	".argent": true, "dist": true, "build": true, "out": true,
	".idea": true, ".vscode": true,
}

// scanRule is one compiled detector. redactSpan is true for credential
// detectors: the matched span is replaced before security.Redact runs, so the
// raw token cannot survive even if Redact's pattern table changes later.
type scanRule struct {
	category   string
	re         *regexp.Regexp
	redactSpan bool
}

var securityScanRules = []scanRule{
	{"aws_access_key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), true},
	{"github_token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`), true},
	{"api_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`), true},
	{"bearer_token", regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]{16,}`), true},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), true},
	{"credential_assignment", regexp.MustCompile(`(?i)\b(?:password|passwd|api[_-]?key|secret|access[_-]?key|private[_-]?key)["']?\s*[:=]\s*["']?([A-Za-z0-9._~+/=-]{6,})`), true},
	{"eval", regexp.MustCompile(`\beval\s*\(`), false},
	{"shell_exec", regexp.MustCompile(`(?i)\bexec\.(?:Command|CommandContext)\s*\([^\n]*\+`), false},
	{"curl_pipe_shell", regexp.MustCompile(`(?i)\bcurl\b[^\n|]*\|\s*(?:sh|bash)\b`), false},
	{"chmod_777", regexp.MustCompile(`\bchmod\s+777\b`), false},
	{"private_key", regexp.MustCompile(`-{3,}BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-{3,}`), false},
}

type securityScanTool struct{}

func (securityScanTool) Name() string { return "security_scan" }

func (securityScanTool) Description() string {
	return "Locally scan workspace source for hardcoded credentials, dangerous code patterns and world-writable file modes. Read-only and offline; findings are redacted."
}

func (securityScanTool) Mutating() bool { return false }

func (securityScanTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string","description":"subdirectory or file to scan; defaults to the workspace root"},"max_files":{"type":"integer","description":"maximum number of files to scan (default 500)"},"include":{"type":"array","items":{"type":"string"},"description":"optional glob patterns limiting which files are scanned"}},"additionalProperties":false}`
}

type securityScanFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Category string `json:"category"`
	Snippet  string `json:"snippet"`
}

type securityScanResult struct {
	ScannedFiles int                   `json:"scanned_files"`
	Findings     []securityScanFinding `json:"findings"`
	Truncated    bool                  `json:"truncated"`
	Marker       string                `json:"marker,omitempty"`
}

func (securityScanTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path     string   `json:"path"`
		MaxFiles *int     `json:"max_files"`
		Include  []string `json:"include"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("security_scan: bad arguments: %w", err)
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	maxFiles := securityScanDefaultMaxFiles
	if args.MaxFiles != nil && *args.MaxFiles > 0 {
		maxFiles = *args.MaxFiles
	}
	globs, err := compileScanGlobs(args.Include)
	if err != nil {
		return "", err
	}

	findings := make([]securityScanFinding, 0)
	scanned := 0
	capped := false
	addFinding := func(file string, line int, category, snippet string) {
		if len(findings) >= securityScanMaxFindings {
			capped = true
			return
		}
		findings = append(findings, securityScanFinding{
			File: file, Line: line, Category: category, Snippet: snippet,
		})
	}

	scanOne := func(path, rel string) error {
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		if info.Size() > securityScanMaxFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil // binary, not source
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		scanned++
		scanFileLines(rel, data, addFinding)
		// File modes are not meaningful on Windows; skip this category there.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o002 != 0 {
			addFinding(rel, 0, "world_writable",
				fmt.Sprintf("mode %o is world-writable", info.Mode().Perm()))
		}
		return nil
	}

	info, err := os.Stat(base)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		err = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				if path != base && securityScanIgnoredDirs[entry.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			rel, relErr := filepath.Rel(base, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if !scanGlobMatch(globs, rel) {
				return nil
			}
			return scanOne(path, rel)
		})
		if err != nil {
			return "", err
		}
	} else if scanGlobMatch(globs, filepath.ToSlash(filepath.Base(base))) {
		if err := scanOne(base, filepath.ToSlash(filepath.Base(base))); err != nil && err != filepath.SkipAll {
			return "", err
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Category < findings[j].Category
	})
	return marshalScanResult(scanned, findings, capped, outputLimit(ctx)), nil
}

// scanFileLines applies every detector to each line. One finding per rule per
// line keeps the output bounded while still surfacing every category present.
func scanFileLines(rel string, data []byte, add func(string, int, string, string)) {
	for i, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		for _, rule := range securityScanRules {
			span := rule.re.FindStringIndex(line)
			if span == nil {
				continue
			}
			if rule.category == "credential_assignment" && scanIsEnvReference(line[span[0]:span[1]]) {
				continue
			}
			snippet := line
			if rule.redactSpan && span[1] <= len(snippet) {
				snippet = snippet[:span[0]] + "[redacted:" + rule.category + "]" + snippet[span[1]:]
			}
			snippet = security.Redact(clipScanSnippet(snippet, securityScanSnippetRunes))
			add(rel, i+1, rule.category, snippet)
		}
	}
}

// scanIsEnvReference filters out assignments whose "value" is just a variable
// name or a documented placeholder rather than a literal credential.
func scanIsEnvReference(span string) bool {
	lowered := strings.ToLower(span)
	for _, needle := range []string{
		"getenv", "environ", "process.env", "import.meta.env", "std::env", "env[",
		"vault", "keychain", "require(", "<", ">", "$", "{", "}",
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	for _, needle := range []string{
		"your", "example", "placeholder", "changeme", "change_me", "dummy",
		"todo", "fixme", "xxxx", "abc123",
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}

func clipScanSnippet(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

// compileScanGlobs turns simple globs into anchored regexps. In addition to
// single-segment '*' and '?', a "**" spans directories and "**/" spans zero or
// more leading directories.
func compileScanGlobs(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			continue
		}
		runes := []rune(pattern)
		var b strings.Builder
		b.WriteString("^")
		for i := 0; i < len(runes); i++ {
			switch runes[i] {
			case '*':
				if i+1 < len(runes) && runes[i+1] == '*' {
					b.WriteString(".*")
					i++
					if i+1 < len(runes) && runes[i+1] == '/' {
						i++
					}
				} else {
					b.WriteString("[^/]*")
				}
			case '?':
				b.WriteString("[^/]")
			default:
				b.WriteString(regexp.QuoteMeta(string(runes[i])))
			}
		}
		b.WriteString("$")
		re, err := regexp.Compile(b.String())
		if err != nil {
			return nil, fmt.Errorf("security_scan: invalid include glob %q: %w", pattern, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func scanGlobMatch(globs []*regexp.Regexp, rel string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, re := range globs {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// marshalScanResult keeps the JSON valid while clipping oversized results: it
// keeps as many findings as fit and flags the rest with truncated/marker.
func marshalScanResult(scanned int, findings []securityScanFinding, capped bool, limit int) string {
	build := func(subset []securityScanFinding, truncated bool) string {
		if subset == nil {
			subset = []securityScanFinding{}
		}
		result := securityScanResult{ScannedFiles: scanned, Findings: subset, Truncated: truncated}
		if truncated {
			result.Marker = "... [truncated] ..."
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return `{"scanned_files":0,"findings":[],"truncated":true,"marker":"... [truncated] ..."}`
		}
		return string(encoded)
	}
	if limit <= 0 {
		limit = maxToolOutput
	}
	full := build(findings, capped)
	if len(full) <= limit {
		return full
	}
	lo, hi := 0, len(findings)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(build(findings[:mid], true)) <= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return build(findings[:lo], true)
}
