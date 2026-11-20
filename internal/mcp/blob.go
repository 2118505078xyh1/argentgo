package mcp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file closes the MCP "binary resource black hole": a tools/call result
// can embed a "resource" content block whose resource carries base64 `blob`
// data (binary, audio, images, PDFs, ...). The old CallTool parser only read
// `resource.text`, so blob-only resources contributed nothing.
//
// Design constraints (from the audit):
//   - Default behaviour is byte-for-byte unchanged. Blob artifacts are written
//     only when an artifact directory is explicitly configured via
//     ARGENT_MCP_BLOB_ARTIFACT_DIR. With the variable unset, CallTool takes the
//     exact same code path it always did.
//   - No new dependencies; stdlib only, mirroring the artifact conventions in
//     internal/tools/output_spill*.go (0700 dir, 0600 O_EXCL file, random hex
//     name, best-effort writes that never fail the originating tool call).
//   - Untrusted input is the server-supplied blob and mimeType. Both are
//     bounded/sanitized before they can affect memory or the filesystem.

const (
	// maxResourceBlobBytes caps the decoded size of one embedded resource blob.
	// The cap is checked BEFORE decoding so a hostile server cannot force a
	// large allocation via base64 amplification (4 encoded chars -> 3 bytes).
	maxResourceBlobBytes = 16 << 20 // 16 MiB

	// blobArtifactDirEnv opts the CallTool blob handling into writing artifacts.
	// Empty/unset keeps the legacy behaviour (blob resources are ignored).
	blobArtifactDirEnv = "ARGENT_MCP_BLOB_ARTIFACT_DIR"

	blobArtifactPrefix = "mcp-blob-"
	blobIDBytes        = 8  // -> 16 lowercase hex chars
	maxArtifactExtLen  = 12 // generous for ".svg", ".webp", ".jpeg", ...
)

// Sentinel errors let callers distinguish why a blob could not be decoded.
var (
	ErrBlobEmpty     = errors.New("mcp: resource blob is empty")
	ErrBlobTooLarge  = errors.New("mcp: resource blob exceeds 16 MiB limit")
	ErrBlobBadBase64 = errors.New("mcp: resource blob is not valid base64")
)

// ResourceContent is the embedded resource of a "resource" content block in a
// tools/call result. Per MCP the resource carries either `text` or a base64
// `blob`; both may be present but a well-formed block uses one.
type ResourceContent struct {
	URI      string `json:"uri,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// DecodeResourceBlob decodes the base64 `blob` of an embedded MCP resource.
// It is a pure function: no I/O, no global state. It returns the decoded bytes
// and the (trimmed) MIME type advertised by the server.
//
// The decoded payload is bounded at maxResourceBlobBytes. The encoded length
// is checked first, so an oversized string from an untrusted server is rejected
// before base64.StdEncoding allocates its output buffer. Padded std base64 and
// unpadded raw std base64 are both accepted.
func DecodeResourceBlob(resource ResourceContent) (data []byte, mime string, err error) {
	mime = strings.TrimSpace(resource.MIMEType)
	encoded := strings.TrimSpace(resource.Blob)
	if encoded == "" {
		return nil, mime, ErrBlobEmpty
	}

	// Upper bound on encoded characters for a 16 MiB payload, plus a small
	// cushion for padding / raw encodings. Bounding here prevents memory
	// amplification: we never call DecodeString on an arbitrarily large string.
	maxEncoded := base64.StdEncoding.EncodedLen(maxResourceBlobBytes) + 4
	if len(encoded) > maxEncoded {
		return nil, mime, fmt.Errorf("%w: encoded length %d", ErrBlobTooLarge, len(encoded))
	}

	data, err = base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Some servers omit padding; accept raw std base64 as a fallback.
		var rawErr error
		data, rawErr = base64.RawStdEncoding.DecodeString(encoded)
		if rawErr != nil {
			return nil, mime, fmt.Errorf("%w: %v", ErrBlobBadBase64, err)
		}
	}
	if len(data) > maxResourceBlobBytes {
		return nil, mime, fmt.Errorf("%w: decoded %d bytes", ErrBlobTooLarge, len(data))
	}
	return data, mime, nil
}

// blobArtifactDir reports the configured artifact directory. Empty means the
// blob feature is disabled and CallTool must keep its legacy behaviour.
func blobArtifactDir() string {
	return strings.TrimSpace(os.Getenv(blobArtifactDirEnv))
}

// saveBlobAsArtifact is the CallTool-side adapter: decode, persist under dir,
// and render the marker the model sees. It is deliberately best-effort — on
// any failure it returns an empty string, so a decoding or write problem never
// fails an otherwise successful tool call (the blob stays as invisible as it
// was before this change).
func saveBlobAsArtifact(dir string, resource ResourceContent) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	data, mime, err := DecodeResourceBlob(resource)
	if err != nil {
		return ""
	}
	path, err := SaveBlobArtifact(dir, data, blobExtFromMIME(mime))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("[Binary resource saved to %s]", path)
}

// SaveBlobArtifact writes data to a fresh, safely-named file inside dir and
// returns its path. It mirrors internal/tools' spill-artifact contract without
// importing or modifying that package (which is owned by another agent).
//
// Security: `ext` is server-influenced (derived from mimeType), so it is
// strictly sanitized — any path separator, ".." or non-alphanumeric character
// is rejected, which closes the path-escape vector. The final path is also
// verified to stay under dir. Files are created 0600 with O_EXCL inside a 0700
// directory and a random hex name, so existing files are never overwritten and
// the name holds no shell or path metacharacters.
func SaveBlobArtifact(dir string, data []byte, ext string) (path string, err error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("mcp: artifact directory is empty")
	}
	cleanExt, err := sanitizeArtifactExt(ext)
	if err != nil {
		return "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return "", err
	}

	const attempts = 8
	for i := 0; i < attempts; i++ {
		id, err := randomBlobID()
		if err != nil {
			return "", err
		}
		name := blobArtifactPrefix + id + cleanExt
		candidate := filepath.Join(absDir, name)
		// Defense in depth: even though name is generated, confirm the joined
		// path cannot escape the artifact directory.
		if !pathWithinDir(absDir, candidate) {
			return "", fmt.Errorf("mcp: artifact path escapes directory: %s", candidate)
		}

		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // astronomically unlikely; try a fresh id
		}
		if err != nil {
			return "", err
		}
		if _, werr := f.Write(data); werr != nil {
			f.Close()
			os.Remove(candidate) // never leave a partial artifact behind
			return "", werr
		}
		if cerr := f.Close(); cerr != nil {
			os.Remove(candidate)
			return "", cerr
		}
		return candidate, nil
	}
	return "", fmt.Errorf("mcp: could not create a unique blob artifact in %s", absDir)
}

// sanitizeArtifactExt normalizes an optional file extension. It accepts at
// most maxArtifactExtLen ASCII alphanumerics with an optional leading dot and
// returns a leading-dot lowercase form. Anything else (separators, "..",
// whitespace, punctuation) is an error so it cannot alter the target path.
func sanitizeArtifactExt(ext string) (string, error) {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return "", nil // an extensionless artifact is fine
	}
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" || len(ext) > maxArtifactExtLen {
		return "", fmt.Errorf("mcp: invalid artifact extension %q", ext)
	}
	for _, r := range ext {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z') {
			return "", fmt.Errorf("mcp: invalid artifact extension %q", ext)
		}
	}
	return "." + strings.ToLower(ext), nil
}

// pathWithinDir reports whether p resolves to a location inside dir. It is the
// same containment rule internal/tools uses for its spill artifacts.
func pathWithinDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// randomBlobID returns blobIDBytes*2 lowercase hex characters from crypto/rand.
func randomBlobID() (string, error) {
	var b [blobIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// blobExtFromMIME maps a (server-supplied) MIME type to a safe, fixed file
// extension. Unknown types fall back to ".bin"; the result is always accepted
// by sanitizeArtifactExt, so mimeType can never influence the path.
func blobExtFromMIME(mime string) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	switch base {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "application/pdf":
		return ".pdf"
	case "application/json":
		return ".json"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}
