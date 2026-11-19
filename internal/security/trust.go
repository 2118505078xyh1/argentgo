package security

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrIntegrity means a trusted file no longer matches its recorded digest.
var ErrIntegrity = errors.New("security: file integrity check failed")

// DigestFile returns the lowercase hex SHA-256 of a file's bytes.
func DigestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("security: reading %q for digest: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyFile checks path against a recorded hex SHA-256 digest. Any tampering,
// truncation or missing file is reported as ErrIntegrity so a hook loader can
// refuse to execute an untrusted document.
func VerifyFile(path, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("%w: malformed digest for %q", ErrIntegrity, path)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("%w: malformed digest for %q: %v", ErrIntegrity, path, err)
	}
	got, err := DigestFile(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: %q changed (want %s, got %s)", ErrIntegrity, path, want, got)
	}
	return nil
}
