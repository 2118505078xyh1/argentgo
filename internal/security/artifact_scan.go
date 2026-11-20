package security

import "strings"

// ArtifactInvisibleWarning is the stable, user-facing warning emitted when a
// declarative artifact (workflow, skill, slash command or automation task)
// carries invisible characters. It is a warning, not an error: the parser keeps
// the original bytes, so parsing and execution semantics are unchanged.
const ArtifactInvisibleWarning = "⚠ artifact contains invisible characters (Unicode tags or bidi controls) that may hide instructions"

// maxArtifactScanBytes bounds the work done for one artifact. Artifacts are
// hand-written declarative files, so a megabyte is far beyond a realistic one.
// Only the first MiB is examined; a payload after the cap is not flagged.
const maxArtifactScanBytes = 1 << 20

// ArtifactScanResult is the outcome of scanning a declarative artifact for
// characters that can hide a prompt-injection payload.
type ArtifactScanResult struct {
	HasTags bool   // Unicode Tag Block characters were found.
	HasBidi bool   // bidi/format controls were found.
	Warning string // ArtifactInvisibleWarning when either flag is set, else empty.
}

// Warnings returns Warning as a one-element slice, or nil for clean content.
// It lets parsers add a warning with a single call and no empty-string check.
func (r ArtifactScanResult) Warnings() []string {
	if r.Warning == "" {
		return nil
	}
	return []string{r.Warning}
}

// ScanArtifact reports invisible characters in content. It reuses the detector
// from invisible.go: ContainsInvisibleTags for the Unicode Tag Block and
// isBidiOrFormat (the exact rune set EscapeBidi escapes) for bidi controls.
// Nothing is modified and no error is produced: callers decide how to warn.
//
// Scanning is bounded to the first maxArtifactScanBytes bytes. A leading UTF-8
// BOM is ignored because it is a byte-order mark, not a hidden instruction;
// otherwise every BOM-prefixed command/skill file would warn. A BOM or any
// other bidi control elsewhere in the text is still detected.
func ScanArtifact(content string) ArtifactScanResult {
	if len(content) > maxArtifactScanBytes {
		content = content[:maxArtifactScanBytes]
	}
	content = strings.TrimPrefix(content, "\ufeff")
	res := ArtifactScanResult{
		HasTags: ContainsInvisibleTags(content),
		HasBidi: ContainsBidi(content),
	}
	if res.HasTags || res.HasBidi {
		res.Warning = ArtifactInvisibleWarning
	}
	return res
}

// ContainsBidi reports whether s contains any bidi/format control character.
// It calls isBidiOrFormat from invisible.go rather than duplicating its rune
// ranges, so detection can never drift from the escapes shown in an approval
// prompt.
func ContainsBidi(s string) bool {
	for _, r := range s {
		if isBidiOrFormat(r) {
			return true
		}
	}
	return false
}

// RedactArtifactContent returns a display-safe copy of content: Unicode Tag
// Block characters are removed and bidi/format controls become visible
// \uXXXX escapes. It reuses SanitizeInvisible and EscapeBidi from invisible.go
// rather than reimplementing either transform; SanitizeInvisible already
// escapes bidi, so the extra EscapeBidi pass is a harmless no-op that keeps the
// display contract explicit.
//
// This is for display only. It never changes what an artifact executes:
// parsers keep the original content and use ScanArtifact to warn. As with
// ScanArtifact, only the first maxArtifactScanBytes bytes are returned, so a
// caller cannot accidentally render an unbounded document.
func RedactArtifactContent(content string) string {
	if len(content) > maxArtifactScanBytes {
		content = content[:maxArtifactScanBytes]
	}
	cleaned, _ := SanitizeInvisible(content)
	return EscapeBidi(cleaned)
}
