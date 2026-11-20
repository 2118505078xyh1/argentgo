package security

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrIntegrity 表示受信任的文件不再与其记录的摘要相符。
var ErrIntegrity = errors.New("security: file integrity check failed")

// DigestFile 返回文件字节的小写十六进制 SHA-256。
func DigestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("security: reading %q for digest: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyFile 根据记录的小写十六进制 SHA-256 摘要检查 path。任何篡改、
// 截断或文件缺失都会报告为 ErrIntegrity，使 hook 加载器能够拒绝执行
// 不受信任的文档。
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
