package tools

import (
	"path/filepath"
	"strings"
	"sync"
)

// fileMutationLocks 把规范的文件键映射到序列化该文件
// 整个读-改-写过程的互斥锁。不同的键使用独立的互斥锁，
// 因此写入无关文件仍可并行进行。
var fileMutationLocks sync.Map // map[string]*sync.Mutex

// fileMutationKey 为 path 构建队列键。它遵循 omp 的 realpath
// 规则：解析最近的存在祖先的符号链接，再追加
// 缺失的相对后缀。因此尚不存在的路径会与落在同一文件的
// 任何其他拼写（或符号链接路径）共享一个键。
// Windows 路径会折叠大小写；EvalSymlinks 失败时回退到 Abs+Clean。
func fileMutationKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(abs)
	}

	candidate := abs
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			key := filepath.Clean(resolved)
			// 后缀是从最深的段向上收集的。
			for i := len(suffix) - 1; i >= 0; i-- {
				key = filepath.Join(key, suffix[i])
			}
			return strings.ToLower(filepath.Clean(key))
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			// 到达卷根目录仍未找到可解析的祖先。
			return strings.ToLower(abs)
		}
		suffix = append(suffix, filepath.Base(candidate))
		candidate = parent
	}
}

func fileMutationMutex(path string) *sync.Mutex {
	key := fileMutationKey(path)
	mu, _ := fileMutationLocks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// lockFileMutation 获取每文件锁并返回其解锁函数。
// 调用方必须 defer 解锁，以免提前出错时把锁卡住。
func lockFileMutation(path string) func() {
	mu := fileMutationMutex(path)
	mu.Lock()
	return mu.Unlock
}

// withFileMutation 在文件的变更锁下运行 fn。该锁覆盖
// 整个读-改-写过程，而不仅是最后那次写系统调用。
func withFileMutation(path string, fn func()) {
	unlock := lockFileMutation(path)
	defer unlock()
	fn()
}
