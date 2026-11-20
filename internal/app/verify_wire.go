package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// verifyOutputCap bounds the captured stdout+stderr tail. Only the last
	// cap bytes survive, cut on a rune boundary so UTF-8 is never split.
	verifyOutputCap   = 32 * 1024
	verifyTruncMarker = "...[output truncated]..."
	verifyLabelMax    = 120

	// verifyShellMeta lists separators, redirections, substitutions and control
	// characters. No shell is used here; rejecting them is defence in depth so
	// a future refactor cannot accidentally make them live.
	verifyShellMeta = ";|&<>()$`'\";\n\r\x00^"

	defaultVerifyTimeoutMS = 30000
	maxVerifyTimeoutMS     = 120000

	// verifyAllLanguagesKey mirrors commands.VerifyAllLanguages: the map key for
	// a command that applies to every edited file without a language-specific
	// command. Kept local so app need not import commands for one literal.
	verifyAllLanguagesKey = "*"
)

// Sentinel errors classify a rejected argv. They mirror the sentinels in
// internal/eval/execcheck.go so the two allowlists stay behaviourally aligned.
var (
	errVerifyEmptyCommand    = errors.New("app: verify: empty command")
	errVerifyCommandDenied   = errors.New("app: verify: command is not allowlisted")
	errVerifyGoSubcommand    = errors.New("app: verify: go subcommand must be test or build")
	errVerifyBadPath         = errors.New("app: verify: executable path must be relative and stay inside the workspace")
	errVerifyShellMeta       = errors.New("app: verify: shell metacharacters are not allowed")
	errVerifyDangerousGoFlag = errors.New("app: verify: go flag can run arbitrary binaries")
)

// Local, opt-in, security-bounded verification runner.
//
// This is NOT a sandbox: there is no OS isolation, no network restriction and
// no user switching. The operator explicitly opts in with a config key or
// --verify-cmd, and safety comes from boundedness rather than isolation:
//
//  1. The argv allowlist accepts only `go test|build ...` or a workspace-relative
//     .exe. Shells, curl, absolute paths, ".." traversal, shell metacharacters
//     and -exec/-toolexec are rejected (validateVerifyArgv).
//  2. argv is passed straight to os/exec; no shell is ever involved.
//  3. Dir is the workspace root, and GOFLAGS is stripped from the child env so
//     a smuggled -toolexec cannot reappear through the environment.
//  4. A short, capped context deadline kills a runaway process.
//  5. Captured output is a 32KB rune-safe tail, redacted before it is returned.
//
// Treat a configured command as trusted code, exactly like the Go tests a
// repository already runs. Nothing calls it unless Verify is set on the runner.

// validateVerifyArgv mirrors internal/eval/execcheck.go's ExecCheck.Validate.
// It is a local copy because that file belongs to another workstream; keep the
// two policies in sync when either changes.
//
// Allowed:
//   - "go" with argv[1] == "test" or "build"
//   - a relative path ending in .exe, with no ".." component
//
// Rejected: absolute paths (including Windows drive prefixes), bare commands
// such as sh/bash/curl, path traversal, shell metacharacters in any argument,
// and -exec/-toolexec variants that let go test/build launch arbitrary binaries.
func validateVerifyArgv(argv []string) error {
	if len(argv) == 0 {
		return errVerifyEmptyCommand
	}
	command := strings.TrimSpace(argv[0])
	if command == "" {
		return errVerifyEmptyCommand
	}
	if strings.ContainsAny(command, verifyShellMeta) {
		return fmt.Errorf("%w: command %q", errVerifyShellMeta, command)
	}
	args := argv[1:]

	if command == "go" {
		if len(args) == 0 {
			return fmt.Errorf("%w: missing subcommand", errVerifyGoSubcommand)
		}
		switch args[0] {
		case "test", "build":
		default:
			return fmt.Errorf("%w: %q", errVerifyGoSubcommand, args[0])
		}
		return validateVerifyArgs(args)
	}

	if filepath.IsAbs(command) || strings.Contains(command, ":") {
		return fmt.Errorf("%w: %q", errVerifyBadPath, command)
	}
	for _, part := range strings.FieldsFunc(command, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return fmt.Errorf("%w: %q", errVerifyBadPath, command)
		}
	}
	if !strings.EqualFold(filepath.Ext(command), ".exe") {
		return fmt.Errorf("%w: %q (expected \"go\" or a relative .exe path)", errVerifyCommandDenied, command)
	}
	return validateVerifyArgs(args)
}

func validateVerifyArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, verifyShellMeta) {
			return fmt.Errorf("%w: argument %q", errVerifyShellMeta, arg)
		}
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "-exec", "--exec", "-toolexec", "--toolexec":
			return fmt.Errorf("%w: %q", errVerifyDangerousGoFlag, arg)
		}
	}
	return nil
}

// verifyEnvironment drops GOFLAGS so a configured `go test` cannot be given
// -toolexec (or another arbitrary-binary flag) through the environment.
func verifyEnvironment() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// NewVerifyFunc builds the bounded verification callback consumed by the agent
// loop. cmd is the operator-configured argv; it is validated once here and the
// callback reuses the accepted copy. A misconfigured (empty or disallowed) cmd
// yields a callback that always reports ok=false, so no feedback is injected.
//
// changedPaths is never interpolated verbatim. For allowlisted `go test|build`
// commands carrying a full-repo `./...` target, the callback narrows that target
// to the changed Go packages using verifySafeTarget (clean + workspace check +
// character allowlist). Every surviving token is whitelist-generated and the
// final argv is re-validated before exec, so a model-chosen path cannot inject
// arguments. Paths that cannot be made safe are skipped; if none survive, the
// configured full command runs unchanged. Non-go /.exe commands are never
// narrowed — their argument contract is unknown.
//
// On a completed process the callback returns (label, exit==0, redacted output,
// true). On spawn failure or timeout it returns (label, false, redacted error,
// true) so the loop can feed the failure back to the model.
func NewVerifyFunc(cmd []string, timeoutMS int, root string) func(context.Context, []string) (string, bool, string, bool) {
	argv := append([]string(nil), cmd...)
	label := verifyLabel(argv)

	if err := validateVerifyArgv(argv); err != nil {
		reason := llm.RedactText("verify command rejected: " + err.Error())
		return func(context.Context, []string) (string, bool, string, bool) {
			return label, false, reason, false
		}
	}

	if timeoutMS <= 0 {
		timeoutMS = defaultVerifyTimeoutMS
	}
	if timeoutMS > maxVerifyTimeoutMS {
		timeoutMS = maxVerifyTimeoutMS
	}
	deadline := time.Duration(timeoutMS) * time.Millisecond
	if strings.TrimSpace(root) == "" {
		root = "."
	}

	return func(ctx context.Context, changedPaths []string) (string, bool, string, bool) {
		runArgv, _, _ := narrowVerifyArgv(argv, changedPaths, root)
		// The narrowed argv is rebuilt only from whitelist-generated tokens;
		// re-validate it anyway so a future narrowing change cannot smuggle an
		// illegal argument past the construction-time allowlist check.
		if err := validateVerifyArgv(runArgv); err != nil {
			reason := llm.RedactText("verify command rejected: " + err.Error())
			return label, false, reason, false
		}

		name := runArgv[0]
		if name == "go" {
			// Resolve "go" before Dir is applied so a go.exe planted in the
			// workspace cannot be picked up through a relative PATH entry.
			if found, err := exec.LookPath("go"); err == nil {
				if abs, err := filepath.Abs(found); err == nil {
					name = abs
				}
			}
		} else {
			// Validation guaranteed a relative, traversal-free .exe; anchoring
			// it to root keeps it inside the workspace even if Dir changes.
			name = filepath.Join(root, filepath.FromSlash(name))
		}

		runCtx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()

		command := exec.CommandContext(runCtx, name, runArgv[1:]...)
		command.Dir = root
		command.Env = verifyEnvironment()

		var out verifyTailBuffer
		command.Stdout = &out
		command.Stderr = &out
		err := command.Run()
		output := llm.RedactText(out.String())

		if err == nil {
			return label, true, output, true
		}

		// CommandContext kills a timed-out process, which surfaces as an
		// *exec.ExitError. Check the context first so a kill is reported as a
		// timeout rather than an ordinary non-zero exit.
		timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
		ctxErr := ctx.Err()
		if !timedOut && ctxErr == nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				// A normal non-zero exit is a failed check, still a valid verdict.
				return label, false, output, true
			}
		}

		// Spawn failure, deadline or cancellation: report a redacted error with
		// any partial output attached. ok stays true so the loop can react.
		detail := err.Error()
		if timedOut {
			detail = fmt.Sprintf("verify command timed out after %s", deadline)
		} else if ctxErr != nil {
			detail = "verify command cancelled: " + ctxErr.Error()
		}
		if tail := out.String(); tail != "" {
			detail += "\n" + tail
		}
		return label, false, llm.RedactText(detail), true
	}
}

// verifyLabel is a bounded, redacted name for the configured check.
func verifyLabel(argv []string) string {
	label := strings.Join(argv, " ")
	if runes := []rune(label); len(runes) > verifyLabelMax {
		label = string(runes[:verifyLabelMax]) + "..."
	}
	return llm.RedactText(label)
}

// verifySafeTarget validates one checkpoint path and maps it to a single,
// conservative Go package pattern ("./dir"). It never returns model-controlled
// bytes verbatim. The path arrives from the MutationPaths seam, which is fed by
// model-chosen tool arguments, so every one of these checks is defence in depth:
//
//   - no shell metacharacters, control bytes, NUL or whitespace;
//   - no absolute path, drive prefix ("C:"), UNC root or leading separator;
//   - an allowlist of runes: Unicode letters/digits (so CJK paths work) plus
//     / \ _ - . — anything else rejects the path;
//   - lexical Clean plus a filepath.Rel confinement check against root;
//   - only .go files and extension-less directories become targets. A .go file
//     narrows to its containing package; other file types cannot narrow a Go
//     package set and are skipped.
//
// ok=false means "do not trust this path" — the caller skips it.
func verifySafeTarget(rootAbs, changed string) (string, bool) {
	p := strings.TrimSpace(changed)
	if p == "" || strings.ContainsAny(p, verifyShellMeta) {
		return "", false
	}
	// Normalise Windows separators before cleaning so the allowlist and the
	// confinement check behave identically on every platform.
	p = strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return "", false
	}
	// Reject traversal components before Clean can fold them into an
	// innocuous-looking in-root path (e.g. "a/../secret.go").
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", false
		}
	}
	for _, r := range p {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '/', r == '_', r == '-', r == '.':
			// allowed
		default:
			return "", false
		}
	}

	cleaned := filepath.Clean(filepath.FromSlash(p))
	slash := filepath.ToSlash(cleaned)
	if slash == "." || slash == ".." || strings.HasPrefix(slash, "../") ||
		strings.Contains(slash, "/../") || strings.HasSuffix(slash, "/..") {
		return "", false
	}
	abs, err := filepath.Abs(filepath.Join(rootAbs, cleaned))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	relSlash := filepath.ToSlash(rel)

	dir := relSlash
	switch {
	case strings.HasSuffix(strings.ToLower(relSlash), ".go"):
		dir = filepath.ToSlash(filepath.Dir(filepath.FromSlash(relSlash)))
	case filepath.Ext(relSlash) != "":
		return "", false // non-Go file: cannot narrow a Go build
	}
	if dir == "." {
		dir = ""
	}
	return "./" + dir, true
}

// verifyNarrowTargets maps checkpoint paths to a deterministic (deduplicated,
// sorted) list of safe Go package patterns. Unsafe or irrelevant paths are
// counted in skipped and omitted — never interpolated. An empty target list
// tells the caller to keep the full command.
func verifyNarrowTargets(root string, changedPaths []string) (targets []string, skipped int) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, len(changedPaths)
	}
	seen := make(map[string]bool)
	for _, changed := range changedPaths {
		target, ok := verifySafeTarget(rootAbs, changed)
		if !ok {
			skipped++
			continue
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets, skipped
}

// narrowVerifyArgv narrows an allowlisted `go test|build` command to the changed
// Go packages. Two modes:
//
//  1. a full-repo `./...` (or `...`) target is replaced with the safe targets —
//     this is the actual O(full repo) -> O(changed files) win;
//  2. with no target declared, the safe targets are appended (a bare
//     `go test` / `go build` otherwise means "the current package only").
//
// It is a no-op for non-go commands, for .exe verification commands (whose argv
// contract is unknown), when no path can be made safe, and when the operator
// already named an explicit package target. In every no-op case the configured
// argv is returned as a copy, byte-for-byte, so behaviour is unchanged.
func narrowVerifyArgv(argv []string, changedPaths []string, root string) ([]string, bool, int) {
	out := append([]string(nil), argv...)
	if len(out) < 2 || out[0] != "go" || (out[1] != "test" && out[1] != "build") {
		return out, false, 0
	}
	targets, skipped := verifyNarrowTargets(root, changedPaths)
	if len(targets) == 0 {
		return out, false, skipped
	}
	for i := 2; i < len(out); i++ {
		if out[i] == "./..." || out[i] == "..." {
			next := make([]string, 0, len(out)-1+len(targets))
			next = append(next, out[:i]...)
			next = append(next, targets...)
			next = append(next, out[i+1:]...)
			return next, true, skipped
		}
	}
	for _, arg := range out[2:] {
		if !strings.HasPrefix(arg, "-") {
			// A non-flag token is an explicit target chosen by the operator;
			// overriding it would silently change the configured check.
			return out, false, skipped
		}
	}
	return append(out, targets...), true, skipped
}

// verifyLangForms expands one language/extension key into the equivalent
// spellings used to match a changed file's extension. The same expansion is
// applied to config keys and to path extensions, so "go", ".go", "py",
// ".py" and "python" all meet in the middle. The first two forms are always
// the bare key and its dotted variant; the remainder are small, fixed aliases.
func verifyLangForms(key string) []string {
	base := strings.ToLower(strings.TrimSpace(key))
	base = strings.TrimPrefix(base, ".")
	if base == "" {
		return nil
	}
	forms := []string{base, "." + base}
	switch base {
	case "py", "python":
		forms = append(forms, "python", ".python", "py", ".py")
	case "js", "javascript", "jsx", "mjs", "cjs":
		forms = append(forms, "javascript", ".javascript", "js", ".js", "jsx", ".jsx", "mjs", ".mjs", "cjs", ".cjs")
	case "ts", "typescript", "tsx":
		forms = append(forms, "typescript", ".typescript", "ts", ".ts", "tsx", ".tsx")
	case "rs", "rust":
		forms = append(forms, "rust", ".rust", "rs", ".rs")
	case "go", "golang":
		forms = append(forms, "golang", ".golang", "go", ".go")
	}
	return forms
}

// verifyAllLanguagesCmd returns the catch-all command configured under the "*"
// key (commands.VerifyAllLanguages). The lookup trims both key and value so a
// hand-written " * " or a blank value behaves sanely. ok is false when no
// non-empty catch-all is configured, which lets the single VerifyCommand field
// remain the fallback.
func verifyAllLanguagesCmd(commands map[string]string) (string, bool) {
	for key, cmd := range commands {
		if strings.TrimSpace(key) == verifyAllLanguagesKey && strings.TrimSpace(cmd) != "" {
			return cmd, true
		}
	}
	return "", false
}

// canonicalVerifyCmd collapses a configured command string to its argv key so
// selection and execution use the same spelling for the same command.
func canonicalVerifyCmd(cmd string) string {
	return strings.Join(strings.Fields(cmd), " ")
}

// verifyCommandIndex normalizes the configured language map once: every
// spelling of a key points at the same command string.
func verifyCommandIndex(commands map[string]string) map[string]string {
	index := make(map[string]string, len(commands)*2)
	for key, cmd := range commands {
		for _, form := range verifyLangForms(key) {
			index[form] = cmd
		}
	}
	return index
}

// selectVerifyArgvs resolves the ordered, de-duplicated argv list to run for
// changedPaths. It is deliberately pure (no process spawn) so per-language
// dispatch can be unit-tested without executing commands. Selection priority,
// highest first:
//
//  1. a language-specific command matching the path's extension;
//  2. the "*" catch-all command (commands.VerifyAllLanguages), which aider
//     applies to every language without a specific entry;
//  3. the single VerifyCommand field, the legacy fallback.
//
// Only one catch-all runs: "*" wins over VerifyCommand when both are set. When
// nothing can be selected, the catch-all runs once so a missing checkpoint
// still verifies something. The allowlist is applied later by NewVerifyFunc,
// not here.
func selectVerifyArgvs(commands map[string]string, fallback []string, changedPaths []string) [][]string {
	index := verifyCommandIndex(commands)
	allCmd, hasAll := verifyAllLanguagesCmd(commands)
	seen := map[string]bool{}
	var out [][]string
	add := func(argv []string) {
		if len(argv) == 0 {
			return
		}
		key := strings.Join(argv, " ")
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, argv)
	}

	// catchAll is the single fallback for a path with no specific command:
	// the "*" command takes precedence over the single VerifyCommand field.
	catchAll := func() {
		if hasAll {
			add(strings.Fields(allCmd))
			return
		}
		add(append([]string(nil), fallback...))
	}

	for _, path := range changedPaths {
		base := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
		matched := false
		for _, form := range verifyLangForms(base) {
			if cmd, ok := index[form]; ok {
				add(strings.Fields(cmd))
				matched = true
				break
			}
		}
		if !matched {
			catchAll()
		}
	}
	if len(out) == 0 {
		// No checkpoint paths (or none selected anything): still verify once
		// with the catch-all.
		catchAll()
	}
	return out
}

// newVerifyDispatchFunc wraps NewVerifyFunc with per-language dispatch. Every
// distinct configured command is validated once up front; on each call the
// changed paths pick which of those pre-validated runners to execute. Results
// are aggregated: a run passes only when every selected command passes, and
// ok is false only when a selected command could not run.
func newVerifyDispatchFunc(commands map[string]string, fallback []string, timeoutMS int, root string) func(context.Context, []string) (string, bool, string, bool) {
	runners := map[string]func(context.Context, []string) (string, bool, string, bool){}
	for _, cmd := range commands {
		key := canonicalVerifyCmd(cmd)
		if _, ok := runners[key]; !ok {
			runners[key] = NewVerifyFunc(strings.Fields(cmd), timeoutMS, root)
		}
	}
	if len(fallback) > 0 {
		runners[canonicalVerifyCmd(strings.Join(fallback, " "))] = NewVerifyFunc(fallback, timeoutMS, root)
	}

	return func(ctx context.Context, changedPaths []string) (string, bool, string, bool) {
		argvs := selectVerifyArgvs(commands, fallback, changedPaths)
		if len(argvs) == 0 {
			return "", false, "", false
		}
		labels := make([]string, 0, len(argvs))
		outputs := make([]string, 0, len(argvs))
		allPassed, allOK := true, true
		for _, argv := range argvs {
			key := strings.Join(argv, " ")
			run := runners[key]
			if run == nil {
				// Selection only draws from pre-registered commands; building a
				// fallback here keeps a future key-format change from panicking.
				run = NewVerifyFunc(argv, timeoutMS, root)
			}
			label, passed, output, ok := run(ctx, changedPaths)
			labels = append(labels, label)
			if output != "" {
				outputs = append(outputs, output)
			}
			allPassed = allPassed && passed
			allOK = allOK && ok
		}
		label := strings.Join(labels, "; ")
		if runes := []rune(label); len(runes) > verifyLabelMax {
			label = string(runes[:verifyLabelMax]) + "..."
		}
		return label, allPassed, strings.Join(outputs, "\n"), allOK
	}
}

// verifyConfigured reports whether either verification form is enabled.
func verifyConfigured(res Resolved) bool {
	return len(res.Cfg.VerifyCommand) > 0 || len(res.Cfg.VerifyCommands) > 0
}

// verifyFuncFor returns nil unless the operator explicitly configured a
// verification command, so an unconfigured run's Options stay byte-identical
// (agent.Options.Verify == nil disables the whole loop). With only the legacy
// VerifyCommand set the original fixed callback is returned unchanged; the
// per-language map adds a dispatcher that consults the changed paths supplied
// by the agent loop's checkpoint.
func verifyFuncFor(res Resolved) func(context.Context, []string) (string, bool, string, bool) {
	if len(res.Cfg.VerifyCommands) > 0 {
		return newVerifyDispatchFunc(res.Cfg.VerifyCommands, res.Cfg.VerifyCommand, res.Cfg.VerifyTimeoutMS, res.Root)
	}
	if len(res.Cfg.VerifyCommand) == 0 {
		return nil
	}
	return NewVerifyFunc(res.Cfg.VerifyCommand, res.Cfg.VerifyTimeoutMS, res.Root)
}

// verifyReflectionsFor resolves the per-run cap. Zero is returned when the
// feature is off, so even this field matches the previous Options exactly.
func verifyReflectionsFor(res Resolved) int {
	if !verifyConfigured(res) {
		return 0
	}
	if n := res.Cfg.VerifyMaxReflections; n > 0 {
		return n
	}
	return agent.DefaultMaxVerifyReflections
}

// verifyTailBuffer keeps only the last verifyOutputCap bytes written to it,
// advancing to a rune boundary so a tail never starts mid-UTF-8-rune.
type verifyTailBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *verifyTailBuffer) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.buf.Len() > verifyOutputCap {
		data := w.buf.Bytes()
		drop := w.buf.Len() - verifyOutputCap
		for drop < len(data) && !utf8.RuneStart(data[drop]) {
			drop++
		}
		copy(data, data[drop:])
		// Truncate to the bytes actually retained, not to verifyOutputCap.
		// Advancing drop past a split rune shortens the tail by that many
		// bytes; truncating to the full cap would expose stale bytes from
		// before the retained window at the end of the output.
		w.buf.Truncate(len(data) - drop)
		w.truncated = true
	}
	return n, err
}

func (w *verifyTailBuffer) String() string {
	if !w.truncated {
		return w.buf.String()
	}
	return verifyTruncMarker + w.buf.String()
}
