package security

import (
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The seed corpora are shared by the fuzz targets and by the deterministic
// tests at the bottom of this file. `go test ./internal/security` therefore
// exercises exactly the inputs a `go test -fuzz` run starts from, and the
// invariants hold on every seed without a fuzzing engine installed.
//
// Credentials in the seeds are assembled at runtime so the repository never
// contains a literal token that the privacy scan would reject. They are all
// synthetic strings of the letter 'a'.
func secret(prefix string, n int) string { return prefix + strings.Repeat("a", n) }

var (
	redactSeeds = []string{
		"",
		"plain text without any credential",
		secret("sk-", 32),
		secret("sk-proj-", 28),
		secret("ghp_", 36),
		secret("AKIA", 20),
		"Authorization: Bearer " + secret("sk-", 24),
		"api_key=" + secret("sk-", 24),
		"https://user:" + secret("p", 16) + "@example.com/repo",
		"line1\n" + secret("sk-", 24) + "\nline2",
		"重复 " + secret("sk-", 20) + " 🚀",
		secret("sk-", 20) + "\x00\xff" + secret("ghp_", 24),
	}

	invisibleSeeds = []string{
		"",
		"echo hello",
		"日本語 🚀",
		"\U000e0000", // lowest Unicode Tag Block code point
		"\U000e007f", // highest Unicode Tag Block code point
		"echo \U000e0041hi",
		"a\u202eb", // bidi override
		"\u061c\u200e\u200f\ufeff",
		"\u202a\u202b\u202c\u202d\u202e",
		"\u2066\u2067\u2068\u2069",
		"\U000e0041\u202e\xff\x00", // mixed tags, bidi and invalid UTF-8
	}

	globSeeds = [][2]string{
		{"", ""},
		{"*.env", "project/src/.env"},
		{"src/**", "src/a/b.rs"},
		{"~/.ssh", "~/.ssh/id_rsa"},
		{"**/*", "a/b/c"},
		{"read", "read/file.txt"},
		{"a?c", "abc"},
		{"a[bc]d", "abd"},
		{"**\\*", "a\\b\\c"},
		{"[", "abc"}, // malformed pattern must not panic
		{"a*a*a*a*a", "aaaaaaaaaaaa"},
	}

	commandGuardSeeds = []struct{ command, suffix string }{
		{"", ""},
		{"rm -rf x", "safe args"},
		{"git status", ""},
		{"echo \"rm -rf\"", "x"},
		{"cat <<EOF\nrm -rf x\nEOF\n", "y"},
		{"Remove-Item -Force x", ""},
		{"echo hi > /etc/x", ""},
		{"echo hi > /tmp/x", ""},
		{"go test ./...", ""},
		{"ls; rm x", ""},
		{"ls # rm x", ""},
		{"\x00\xffrm\t-rf\nx", "z"},
	}

	wildcardSeeds = [][2]string{
		{"", ""},
		{"read", "read"},
		{"read*", "read/file.txt"},
		{"*.go", "x.go"},
		{"[", "x"}, // malformed pattern must not panic
		{"a?c", "abc"},
		{"**", "a/b"},
	}

	// canonicalSecretPattern recognises a high-confidence credential: a known
	// provider prefix followed by at least twelve token characters. tokenEnd in
	// redact.go consumes exactly this character set, so a match here must be
	// gone from Redact's output.
	canonicalSecretPattern = regexp.MustCompile(`(?i)(?:sk-proj-|sk-ant-|sk-or-|sk-|gh[pousr]_|github_pat_|AKIA|ASIA|AIza|xox[abps]-|r8_|hf_|glpat-|npm_|pypi-)[A-Za-z0-9_.~+/=:@%-]{12,}`)
)

// FuzzRedact checks the core redaction promise: no high-confidence credential
// survives, and the scrubber neither panics nor corrupts UTF-8 nor expands
// without bound on arbitrary bytes.
func FuzzRedact(f *testing.F) {
	for _, seed := range redactSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkRedact(t, s)
	})
}

func checkRedact(t *testing.T, s string) {
	t.Helper()
	out := Redact(s)
	if again := Redact(s); again != out {
		t.Fatalf("Redact is not deterministic for %q", s)
	}
	if utf8.ValidString(s) && !utf8.ValidString(out) {
		t.Fatalf("Redact produced invalid UTF-8: in %q out %q", s, out)
	}
	// Every accepted match consumes at least a three-byte token prefix and is
	// replaced by a placeholder shorter than 32 bytes, so 16x plus slack is a
	// generous bound that still catches runaway replacement loops.
	if len(out) > 16*len(s)+128 {
		t.Fatalf("Redact output length %d is implausible for input length %d", len(out), len(s))
	}
	for _, credential := range canonicalSecretPattern.FindAllString(s, -1) {
		if strings.Contains(out, credential) {
			t.Fatalf("credential %q survived redaction: %q", credential, out)
		}
	}
}

// FuzzSanitizeInvisible checks that the prompt sanitizer removes every tag,
// makes every bidi/format rune visible, and reports tags accurately.
func FuzzSanitizeInvisible(f *testing.F) {
	for _, seed := range invisibleSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkSanitizeInvisible(t, s)
	})
}

func checkSanitizeInvisible(t *testing.T, s string) {
	t.Helper()
	cleaned, hadTags := SanitizeInvisible(s)
	if again, againTags := SanitizeInvisible(s); again != cleaned || againTags != hadTags {
		t.Fatalf("SanitizeInvisible is not deterministic for %q", s)
	}
	if want := ContainsInvisibleTags(s); hadTags != want {
		t.Fatalf("SanitizeInvisible(%q) hadTags=%v, want %v", s, hadTags, want)
	}
	if utf8.ValidString(s) {
		if !utf8.ValidString(cleaned) {
			t.Fatalf("SanitizeInvisible produced invalid UTF-8: in %q out %q", s, cleaned)
		}
		// With valid input, UTF-8 encodings are self-delimiting, so skipping tag
		// runes cannot accidentally reassemble a tag from neighbouring bytes.
		// (Invalid input can: stray continuation bytes may concatenate into a
		// tag encoding after the real tag is removed.)
		if ContainsInvisibleTags(cleaned) {
			t.Fatalf("a Unicode Tag Block character survived sanitizing %q: %q", s, cleaned)
		}
	}
	for _, r := range s {
		if isBidiOrFormat(r) && strings.ContainsRune(cleaned, r) {
			t.Fatalf("bidi rune U+%04X was not made visible: %q -> %q", r, s, cleaned)
		}
	}
	// An escape is six bytes for a rune of at most four bytes.
	if len(cleaned) > 6*len(s)+16 {
		t.Fatalf("SanitizeInvisible output length %d is implausible for input length %d", len(cleaned), len(s))
	}
}

// FuzzContainsInvisibleTags compares the detector against an independent
// reference: probing every code point of the tag block with strings.ContainsRune.
func FuzzContainsInvisibleTags(f *testing.F) {
	for _, seed := range invisibleSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkContainsInvisibleTags(t, s)
	})
}

func checkContainsInvisibleTags(t *testing.T, s string) {
	t.Helper()
	want := false
	for r := rune(tagBlockStart); r <= tagBlockEnd; r++ {
		if strings.ContainsRune(s, r) {
			want = true
			break
		}
	}
	if got := ContainsInvisibleTags(s); got != want {
		t.Fatalf("ContainsInvisibleTags(%q) = %v, want %v", s, got, want)
	}
}

// FuzzEscapeBidi checks the visible-escape helper used by approval prompts.
func FuzzEscapeBidi(f *testing.F) {
	for _, seed := range invisibleSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkEscapeBidi(t, s)
	})
}

func checkEscapeBidi(t *testing.T, s string) {
	t.Helper()
	out := EscapeBidi(s)
	if utf8.ValidString(s) && !utf8.ValidString(out) {
		t.Fatalf("EscapeBidi produced invalid UTF-8: in %q out %q", s, out)
	}
	for _, r := range s {
		if isBidiOrFormat(r) && strings.ContainsRune(out, r) {
			t.Fatalf("bidi rune U+%04X was not escaped: %q -> %q", r, s, out)
		}
	}
	if len(out) > 6*len(s)+16 {
		t.Fatalf("EscapeBidi output length %d is implausible for input length %d", len(out), len(s))
	}
}

// FuzzGlobMatch checks the policy path matcher for panics, nondeterminism and
// regressions in its literal-pattern (prefix) semantics. The Go fuzz engine's
// own per-input timeout catches backtracking blowups on adversarial patterns.
func FuzzGlobMatch(f *testing.F) {
	for _, pair := range globSeeds {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		checkGlobMatch(t, pattern, path)
	})
}

func checkGlobMatch(t *testing.T, pattern, path string) {
	t.Helper()
	if got := GlobMatch(pattern, path); got != GlobMatch(pattern, path) {
		t.Fatalf("GlobMatch is not deterministic for (%q, %q)", pattern, path)
	}
	// A pattern with no metacharacter (and no home or trailing slash, which
	// normalize() rewrites) must match its own spelling and must not match an
	// empty path. Glob metacharacters such as '*' legitimately match empty
	// runs, so this rule only applies to literal patterns.
	if pattern != "" && !strings.ContainsAny(pattern, `*?[~\`) && !strings.HasSuffix(pattern, "/") {
		if GlobMatch(pattern, "") {
			t.Fatalf("literal pattern %q matched an empty path", pattern)
		}
		if !GlobMatch(pattern, pattern) {
			t.Fatalf("literal pattern %q does not match itself", pattern)
		}
	}
}

// FuzzFindFileEditingCommand checks that the plan-mode command guard never
// panics on arbitrary bytes and never flags a read-only command. The second
// fuzz argument is normalized into an inert argument list before being spliced
// into `git status`, so any mutation of it is guaranteed harmless.
func FuzzFindFileEditingCommand(f *testing.F) {
	for _, seed := range commandGuardSeeds {
		f.Add(seed.command, seed.suffix)
	}
	f.Fuzz(func(t *testing.T, command, suffix string) {
		checkCommandGuard(t, command, suffix)
	})
}

func checkCommandGuard(t *testing.T, command, suffix string) {
	t.Helper()
	token := FindFileEditingCommand(command)
	if again := FindFileEditingCommand(command); again != token {
		t.Fatalf("FindFileEditingCommand is not deterministic for %q", command)
	}
	if len(token) > len(command)+2 {
		t.Fatalf("guard returned token %q longer than command %q", token, command)
	}
	if got := FindFileEditingCommand("git status " + safeShellArg(suffix)); got != "" {
		t.Fatalf("read-only command was flagged as mutating: %q -> %q", suffix, got)
	}
}

// safeShellArg maps arbitrary bytes to a shell word made only of letters,
// digits and inert separators, so it cannot introduce a mutating token or a
// redirection into the command it is spliced into.
func safeShellArg(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == ' ', c == '_', c == '-', c == '.', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('x')
		}
	}
	return b.String()
}

// FuzzWildcardMatch checks the permission-rule matcher for panics on malformed
// patterns and for the reflexive property of literal patterns.
func FuzzWildcardMatch(f *testing.F) {
	for _, pair := range wildcardSeeds {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, pattern, subject string) {
		checkWildcardMatch(t, pattern, subject)
	})
}

func checkWildcardMatch(t *testing.T, pattern, subject string) {
	t.Helper()
	if got := WildcardMatch(pattern, subject); got != WildcardMatch(pattern, subject) {
		t.Fatalf("WildcardMatch is not deterministic for (%q, %q)", pattern, subject)
	}
	if pattern != "" && !strings.ContainsAny(pattern, `*?[\\`) && !WildcardMatch(pattern, pattern) {
		t.Fatalf("literal pattern %q does not match itself", pattern)
	}
}

// --- Deterministic seed tests ------------------------------------------------
//
// Fuzz targets only run their seeds during `go test` (without -fuzz). These
// tests call the same invariant helpers so a plain `go test ./internal/security`
// guards the seeds and the assertions without needing a fuzzing engine.

func TestRedactSeedCorpus(t *testing.T) {
	for _, seed := range redactSeeds {
		checkRedact(t, seed)
	}
}

func TestSanitizeInvisibleSeedCorpus(t *testing.T) {
	for _, seed := range invisibleSeeds {
		checkSanitizeInvisible(t, seed)
	}
}

func TestContainsInvisibleTagsSeedCorpus(t *testing.T) {
	for _, seed := range invisibleSeeds {
		checkContainsInvisibleTags(t, seed)
	}
}

func TestEscapeBidiSeedCorpus(t *testing.T) {
	for _, seed := range invisibleSeeds {
		checkEscapeBidi(t, seed)
	}
}

func TestGlobMatchSeedCorpus(t *testing.T) {
	for _, pair := range globSeeds {
		checkGlobMatch(t, pair[0], pair[1])
	}
}

func TestCommandGuardSeedCorpus(t *testing.T) {
	for _, seed := range commandGuardSeeds {
		checkCommandGuard(t, seed.command, seed.suffix)
	}
}

func TestWildcardMatchSeedCorpus(t *testing.T) {
	for _, pair := range wildcardSeeds {
		checkWildcardMatch(t, pair[0], pair[1])
	}
}

// TestGlobMatchTerminatesOnBacktrackingPattern bounds the matcher on a small
// adversarial input that exercises its recursive '*' backtracking. The generous
// timeout only catches a regression that turns the linear-in-practice walk into
// a hang; it is not a performance benchmark.
func TestGlobMatchTerminatesOnBacktrackingPattern(t *testing.T) {
	pattern := "a*a*a*a*a"
	path := strings.Repeat("a", 20) + "b"
	done := make(chan bool, 1)
	go func() { done <- GlobMatch(pattern, path) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GlobMatch did not terminate on a small adversarial pattern")
	}
}
