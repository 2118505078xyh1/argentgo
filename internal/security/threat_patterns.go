package security

import (
	"regexp"
	"strings"
)

// ThreatCategory names a class of dangerous command that the token-aware
// file-editing classifier in FindFileEditingCommand does not cover (piped
// shells, reverse shells, encoded execution and wrapper-launched deletion).
// The constants are additive: the existing classifier and its semantics stay
// exactly as they were.
type ThreatCategory string

const (
	ThreatNone         ThreatCategory = ""
	ThreatForcedRM     ThreatCategory = "forced-rm"
	ThreatWrapper      ThreatCategory = "wrapper"
	ThreatDownloadExec ThreatCategory = "download-exec"
	ThreatReverseShell ThreatCategory = "reverse-shell"
	ThreatEncodedExec  ThreatCategory = "encoded-exec"
)

// ThreatConfidence is an ordinal confidence level. Higher values are more
// certain; only ConfidenceHigh threats are acted on by the guardrail.
type ThreatConfidence int

const (
	ConfidenceNone ThreatConfidence = iota
	ConfidenceLow
	ConfidenceMedium
	ConfidenceHigh
)

// ThreatMatch is the pure result of inspecting a command for known dangerous
// patterns. Evidence is always a substring of the inspected command, so a
// caller may use it directly as the offending token in a block decision.
type ThreatMatch struct {
	Category   ThreatCategory
	Confidence ThreatConfidence
	Evidence   string
}

// Detected reports whether any threat was recognised.
func (m ThreatMatch) Detected() bool { return m.Category != ThreatNone }

// IsHigh reports whether the match is confident enough to act on.
func (m ThreatMatch) IsHigh() bool { return m.Confidence >= ConfidenceHigh }

type threatPattern struct {
	category   ThreatCategory
	confidence ThreatConfidence
	re         *regexp.Regexp
}

// All patterns are compiled once at package init. They are intentionally
// conservative and RE2-safe (no backreferences or look-around).
var (
	// Wrapper prefixes are stripped before the core patterns run: a dangerous
	// command hidden behind sudo/env/nohup/command is still dangerous. This is
	// the "dimensionality reduction" step; group 1 keeps the preceding
	// separator so commands joined with ; | & are still split correctly.
	wrapperPrefixRe = regexp.MustCompile(`(?i)(^|[\s;|&(])(sudo|env|nohup|command|builtin|exec)\s+`)

	// A trap payload normally lives inside quotes, which sanitizeCommand blanks.
	// Recover quoted payloads from trap invocations and classify them separately.
	// RE2 has no backreferences, so single and double quotes use distinct regexes.
	trapSingleRe = regexp.MustCompile(`(?i)\btrap\s+'([^']*)'`)
	trapDoubleRe = regexp.MustCompile(`(?i)\btrap\s+"([^"]*)"`)

	// A forced rm aimed at a root/wide target is catastrophic and confidence is
	// raised to HIGH. Specific paths stay MEDIUM unless a wrapper bumps them.
	wideTargetRe = regexp.MustCompile(`(^|\s)(/(\s|$)|/\*|/(etc|usr|var|bin|boot|home|opt|root|srv)(/|\s|$)|~(/|\s|$)|\$home(/|\s|$)|\.\.(/|\s|$))`)
	forcedRMRe   = regexp.MustCompile(`\brm\b[^\n]*\s(-[a-zA-Z]*f[a-zA-Z]*|--force)\b`)

	corePatterns = []threatPattern{
		// Reverse shells.
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`/dev/tcp/`)},
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`\b(nc|netcat|ncat)\b[^\n]*\s-(e|exec)\b`)},
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`\b(nc|netcat|ncat)\b[^\n]*\s--(sh-)?exec\b`)},
		{ThreatReverseShell, ConfidenceHigh, regexp.MustCompile(`\bsocat\b[^\n]*exec:`)},
		// Download-and-execute (curl/wget piped into a shell).
		{ThreatDownloadExec, ConfidenceHigh, regexp.MustCompile(`\b(curl|wget)\b[^\n]*\|\s*(sudo\s+)?(ba)?sh\b`)},
		// Encoded execution (base64/hex decoded into a shell).
		{ThreatEncodedExec, ConfidenceHigh, regexp.MustCompile(`\bbase64\s+(-d|--decode|-di)\b[^\n]*\|\s*(sudo\s+)?(ba)?sh\b`)},
		{ThreatEncodedExec, ConfidenceHigh, regexp.MustCompile(`\bxxd\s+-r\b[^\n]*\|\s*(sudo\s+)?(ba)?sh\b`)},
	}
)

// DetectThreat inspects command for dangerous non-file-editing patterns and
// returns the highest-confidence match. It is a pure function: quoted strings,
// heredoc bodies and comments are masked with the same token-aware sanitiser
// used by FindFileEditingCommand, so literal text never produces a false
// positive. An empty command yields the zero ThreatMatch.
func DetectThreat(command string) ThreatMatch {
	return detectThreatDepth(command, 0)
}

// detectThreatDepth is DetectThreat with an explicit recursion budget for
// literal payloads recovered from wrappers (trap and shell -c). Each level
// consumes at least the wrapper text, so the budget is a defensive bound rather
// than a termination requirement.
func detectThreatDepth(command string, depth int) ThreatMatch {
	if strings.TrimSpace(command) == "" {
		return ThreatMatch{}
	}

	best := ThreatMatch{}

	// Mask quoted strings, heredocs and comments, then reduce wrappers before
	// matching the core patterns.
	sanitized := strings.ToLower(sanitizeCommand(command))
	reduced, hadWrapper := reduceWrappers(sanitized)
	if m := detectCore(reduced, hadWrapper); m.Confidence > best.Confidence {
		best = m
	}

	if depth < maxShellLiteralDepth {
		// trap payloads were blanked by the sanitiser; inspect them from the raw
		// command. The trap keyword itself is a wrapper, so a medium core threat
		// found inside a trap is promoted to HIGH.
		for _, payload := range trapPayloads(command) {
			if m := detectCore(strings.ToLower(payload), true); m.Confidence > best.Confidence {
				best = m
			}
			// A trap payload can itself launch `bash -c ...`; recurse so nested
			// literals are not hidden behind two layers of quoting.
			if m := detectThreatDepth(payload, depth+1); m.Confidence > best.Confidence {
				best = m
			}
		}

		// P0 fix: `bash -c`/`sh -c`/`bash -lc` (and friends) wrap a quoted command
		// that sanitizeCommand blanks before the classifiers run. Recover the
		// literal payload from the raw command and run both the wrapper-aware
		// core patterns (promoting medium threats to HIGH) and a full recursive
		// classification. Extraction is conservative: only real quoted payloads
		// at a command position are returned, so benign strings never match.
		for _, payload := range shellLiteralPayloads(command) {
			if m := detectCore(strings.ToLower(payload), true); m.Confidence > best.Confidence {
				best = m
			}
			if m := detectThreatDepth(payload, depth+1); m.Confidence > best.Confidence {
				best = m
			}
		}
	}

	return best
}

// detectCore runs the compiled patterns against a single normalised command.
func detectCore(s string, hadWrapper bool) ThreatMatch {
	best := ThreatMatch{}

	for _, p := range corePatterns {
		if loc := p.re.FindStringIndex(s); loc != nil {
			m := ThreatMatch{
				Category:   p.category,
				Confidence: p.confidence,
				Evidence:   s[loc[0]:loc[1]],
			}
			if m.Confidence > best.Confidence {
				best = m
			}
		}
	}

	if loc := forcedRMRe.FindStringIndex(s); loc != nil {
		confidence := ConfidenceMedium
		if wideTargetRe.MatchString(s) {
			confidence = ConfidenceHigh
		}
		// Launching a forced delete through a wrapper is itself suspicious, even
		// when the target path is specific.
		if hadWrapper && confidence < ConfidenceHigh {
			confidence = ConfidenceHigh
		}
		m := ThreatMatch{
			Category:   ThreatForcedRM,
			Confidence: confidence,
			Evidence:   s[loc[0]:loc[1]],
		}
		if m.Confidence > best.Confidence {
			best = m
		}
	}

	return best
}

// reduceWrappers strips leading sudo/env/nohup/command/builtin/exec prefixes,
// repeating until stable so stacked wrappers (sudo env rm ...) are handled. It
// reports whether any wrapper was removed.
func reduceWrappers(s string) (string, bool) {
	reduced := s
	found := false
	for {
		next := wrapperPrefixRe.ReplaceAllString(reduced, "$1")
		if next == reduced {
			return reduced, found
		}
		found = true
		reduced = next
	}
}

// trapPayloads returns the quoted command payload of each trap invocation.
func trapPayloads(command string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{trapSingleRe, trapDoubleRe} {
		for _, m := range re.FindAllStringSubmatch(command, -1) {
			if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
				out = append(out, m[1])
			}
		}
	}
	return out
}
