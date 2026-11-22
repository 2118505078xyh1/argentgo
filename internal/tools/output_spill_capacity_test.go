package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSpillArtifactCapacityGuard(t *testing.T) {
	dir := t.TempDir()
	// 预置一个占用接近上限的文件。
	if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, had := os.LookupEnv(spillMaxBytesEnv)
	defer func() {
		if had {
			os.Setenv(spillMaxBytesEnv, old)
		} else {
			os.Unsetenv(spillMaxBytesEnv)
		}
	}()

	// 上限 10 字节，已有 10 字节 → 任何新写入都应被容量闸门拒绝。
	os.Setenv(spillMaxBytesEnv, "10")
	if _, err := writeSpillArtifact(dir, []byte("new data")); err != ErrSpillCapacity {
		t.Fatalf("expected ErrSpillCapacity, got %v", err)
	}

	// 放宽上限后允许写入。
	os.Setenv(spillMaxBytesEnv, "1024")
	path, err := writeSpillArtifact(dir, []byte("new data"))
	if err != nil || path == "" {
		t.Fatalf("expected artifact, got %q %v", path, err)
	}
}

func TestSpillMaxBytesParsing(t *testing.T) {
	old, had := os.LookupEnv(spillMaxBytesEnv)
	defer func() {
		if had {
			os.Setenv(spillMaxBytesEnv, old)
		} else {
			os.Unsetenv(spillMaxBytesEnv)
		}
	}()
	os.Setenv(spillMaxBytesEnv, "abc")
	if spillMaxBytes() != 0 {
		t.Fatal("invalid -> 0")
	}
	os.Setenv(spillMaxBytesEnv, "-5")
	if spillMaxBytes() != 0 {
		t.Fatal("negative -> 0")
	}
	os.Setenv(spillMaxBytesEnv, "1024")
	if spillMaxBytes() != 1024 {
		t.Fatal("1024")
	}
}
