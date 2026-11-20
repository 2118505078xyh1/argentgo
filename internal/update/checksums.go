package update

import (
	"errors"
	"fmt"
	"strings"
)

// ChecksumEntry is a single line from a checksums manifest. Hash is normalised
// to lowercase hex; Name is the file name as it appeared in the manifest,
// without a leading "./".
type ChecksumEntry struct {
	Hash string
	Name string
}

// ParseChecksums parses a checksums.txt / sha256sums.txt manifest of the kind
// produced by GNU coreutils and most release pipelines. Every non-blank,
// non-comment line must use one of the two canonical forms:
//
//	<64-hex-digest>  <file name>   (text mode: two spaces)
//	<64-hex-digest> *<file name>   (binary mode: one space, one asterisk)
//
// A leading "./" on the file name is removed and the digest is lowercased.
// On the first malformed line parsing stops and an error naming the 1-based
// line number is returned. This is a pure function: it performs no I/O and
// never mutates its input.
func ParseChecksums(text string) ([]ChecksumEntry, error) {
	entries := make([]ChecksumEntry, 0)
	for number, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, err := parseChecksumLine(trimmed)
		if err != nil {
			return nil, fmt.Errorf("update: checksums line %d: %w", number+1, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseChecksumLine validates and decodes one canonical checksums line.
func parseChecksumLine(line string) (ChecksumEntry, error) {
	// Prefer the text-mode separator; fall back to the binary-mode one. Either
	// way the digest is the prefix before the separator, so a short or invalid
	// digest is rejected by normaliseChecksum rather than silently accepted.
	index := strings.Index(line, "  ")
	if index < 0 {
		index = strings.Index(line, " *")
	}
	if index < 0 {
		return ChecksumEntry{}, errors.New("expected two spaces or a space and asterisk after the digest")
	}
	digest, err := normaliseChecksum(line[:index])
	if err != nil {
		return ChecksumEntry{}, fmt.Errorf("invalid digest: %w", err)
	}
	name := strings.TrimSpace(line[index+2:])
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSpace(name)
	if name == "" {
		return ChecksumEntry{}, errors.New("empty file name")
	}
	return ChecksumEntry{Hash: digest, Name: name}, nil
}

// MatchedChecksum associates a release asset with the manifest hash that
// covers it.
type MatchedChecksum struct {
	Name string
	Hash string
}

// ChecksumDiagnosis is the read-only result of matching a checksums manifest
// against a release's assets. All slices preserve first-seen input order so
// identical inputs always produce identical output.
type ChecksumDiagnosis struct {
	Matched   []MatchedChecksum // assets with exactly one covering hash
	Missing   []string          // asset names with no covering hash
	Orphans   []string          // manifest names that match no asset
	Conflicts []string          // manifest names carrying two or more distinct hashes
}

// DiagnoseChecksums matches parsed manifest entries against release assets.
// Names are compared case-insensitively and the first asset with a given name
// wins. An entry whose name carries more than one distinct hash is a conflict:
// it is reported in Conflicts and treated as Missing for the assets it names,
// because no single hash can be trusted to verify that download.
//
// The function is pure: it neither downloads anything nor mutates its inputs,
// so it can be used to decide whether a published checksums file is complete
// and trustworthy before any asset is fetched.
func DiagnoseChecksums(entries []ChecksumEntry, assets []Asset) ChecksumDiagnosis {
	var out ChecksumDiagnosis

	assetNames := make(map[string]string, len(assets)) // lowercase -> original
	assetOrder := make([]string, 0, len(assets))
	for _, asset := range assets {
		key := strings.ToLower(strings.TrimSpace(asset.Name))
		if _, seen := assetNames[key]; seen {
			continue
		}
		assetNames[key] = asset.Name
		assetOrder = append(assetOrder, key)
	}

	entryNames := make(map[string]string, len(entries)) // lowercase -> first original
	hashes := make(map[string][]string, len(entries))
	entryOrder := make([]string, 0, len(entries))
	for _, entry := range entries {
		key := strings.ToLower(strings.TrimSpace(entry.Name))
		if _, seen := entryNames[key]; !seen {
			entryNames[key] = entry.Name
			entryOrder = append(entryOrder, key)
		}
		if !containsString(hashes[key], entry.Hash) {
			hashes[key] = append(hashes[key], entry.Hash)
		}
	}

	conflict := make(map[string]bool)
	for _, key := range entryOrder {
		if len(hashes[key]) > 1 {
			conflict[key] = true
			out.Conflicts = append(out.Conflicts, entryNames[key])
		}
	}
	for _, key := range assetOrder {
		name := assetNames[key]
		if conflict[key] || len(hashes[key]) == 0 {
			out.Missing = append(out.Missing, name)
			continue
		}
		out.Matched = append(out.Matched, MatchedChecksum{Name: name, Hash: hashes[key][0]})
	}
	for _, key := range entryOrder {
		if _, exists := assetNames[key]; !exists {
			out.Orphans = append(out.Orphans, entryNames[key])
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
