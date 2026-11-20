package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockFilePath(t *testing.T) {
	sep := string(filepath.Separator)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "相对路径拼接 lock 后缀",
			input: "config.json",
			want:  "config.json.lock",
		},
		{
			name:  "带目录的相对路径",
			input: filepath.Join("a", "b", "config.json"),
			want:  filepath.Join("a", "b", "config.json.lock"),
		},
		{
			name:  "CJK 路径原样保留",
			input: filepath.Join("配置", "中文设置.json"),
			want:  filepath.Join("配置", "中文设置.json.lock"),
		},
		{
			name:  "消解点和上级目录",
			input: strings.Join([]string{"a", "..", "b", ".", "config.json"}, sep),
			want:  filepath.Join("b", "config.json.lock"),
		},
		{
			name:  "去除重复分隔符",
			input: "a" + sep + sep + "b" + sep + "config.json",
			want:  filepath.Join("a", "b", "config.json.lock"),
		},
		{
			name:  "去除首尾空白",
			input: "  config.json  ",
			want:  "config.json.lock",
		},
		{
			name:  "去除 CJK 全角空白",
			input: "　配置.json　",
			want:  "配置.json.lock",
		},
		{
			name:  "结尾斜杠先清理再加后缀",
			input: "a" + sep + "b" + sep,
			want:  filepath.Join("a", "b.lock"),
		},
		{
			name:  "已有 lock 后缀会再次追加",
			input: "config.json.lock",
			want:  "config.json.lock.lock",
		},
		{
			name:  "空字符串返回空",
			input: "",
			want:  "",
		},
		{
			name:  "纯空白返回空",
			input: "   ",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockFilePath(tt.input); got != tt.want {
				t.Fatalf("lockFilePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLockFilePathAbsoluteAndStable(t *testing.T) {
	// t.TempDir 返回绝对路径，覆盖绝对路径场景。
	configPath := filepath.Join(t.TempDir(), "配置.json")
	lock := lockFilePath(configPath)
	if want := configPath + ".lock"; lock != want {
		t.Fatalf("lockFilePath(%q) = %q, want %q", configPath, lock, want)
	}
	if !filepath.IsAbs(lock) {
		t.Fatalf("绝对路径输入应得到绝对路径锁文件: %q", lock)
	}
	// 同一输入重复调用结果一致。
	if again := lockFilePath(configPath); again != lock {
		t.Fatalf("结果不稳定: %q != %q", again, lock)
	}
}

func TestWithConfigLockRunsFnAndCreatesLockFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "配置.json")
	called := false
	err := withConfigLock(configPath, func() error {
		called = true
		// 锁文件在 fn 执行期间应已存在。
		if _, statErr := os.Stat(lockFilePath(configPath)); statErr != nil {
			t.Fatalf("锁文件未创建: %v", statErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withConfigLock: %v", err)
	}
	if !called {
		t.Fatal("fn 未被调用")
	}
	if _, err := os.Stat(lockFilePath(configPath)); err != nil {
		t.Fatalf("锁文件在创建目录后应仍存在: %v", err)
	}
}

func TestWithConfigLockPropagatesFnError(t *testing.T) {
	sentinel := errors.New("fn failed")
	configPath := filepath.Join(t.TempDir(), "config.json")
	err := withConfigLock(configPath, func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("withConfigLock 错误 = %v, want %v", err, sentinel)
	}
}

func TestWithConfigLockRejectsEmptyPath(t *testing.T) {
	called := false
	err := withConfigLock("   ", func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("空路径应返回错误")
	}
	if called {
		t.Fatal("空路径不应执行 fn")
	}
}

func TestWithConfigLockSequentialCalls(t *testing.T) {
	// 连续两次获取应能成功，验证锁被正确释放。
	configPath := filepath.Join(t.TempDir(), "config.json")
	for i := 0; i < 2; i++ {
		n := i
		if err := withConfigLock(configPath, func() error {
			if n < 0 {
				return errors.New("unreachable")
			}
			return nil
		}); err != nil {
			t.Fatalf("第 %d 次获取锁失败: %v", i, err)
		}
	}
}
