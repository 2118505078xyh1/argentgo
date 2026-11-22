package tools

import (
	"os"
	"strings"
	"testing"
)

func TestVerifyWriteDrift(t *testing.T) {
	old, had := os.LookupEnv(writeDriftEnv)
	defer func() {
		if had {
			os.Setenv(writeDriftEnv, old)
		} else {
			os.Unsetenv(writeDriftEnv)
		}
	}()

	path := t.TempDir() + "/f"
	if err := os.WriteFile(path, []byte("on-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 默认关闭：零读取、返回空。
	os.Unsetenv(writeDriftEnv)
	if w := verifyWriteDrift(path, []byte("expected")); w != "" {
		t.Fatalf("off should be no-op: %q", w)
	}
	// 开启且一致：空。
	os.Setenv(writeDriftEnv, "1")
	if w := verifyWriteDrift(path, []byte("on-disk")); w != "" {
		t.Fatalf("matching should be empty: %q", w)
	}
	// 漂移：带 hashline 锚点警告。
	w := verifyWriteDrift(path, []byte("expected"))
	if !strings.Contains(w, "hashline") || !strings.Contains(w, "changed after writing") {
		t.Fatalf("drift warning = %q", w)
	}
}
