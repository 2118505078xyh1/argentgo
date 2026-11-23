package tools

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
)

// searchSkipDirs 是遍历时跳过的嘈杂目录。与原 walkFiles 保持一致，
// 只是搬到并行实现里，避免每次调用都重建 map。
var searchSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "dist": true,
}

// dirPathScratch 在目录遍历期间复用路径拼接用的 scratch，降低
// 10 万文件场景下的小对象分配。
var dirPathScratch = sync.Pool{
	New: func() any {
		s := make([]byte, 0, 256)
		return &s
	},
}

// walkFiles 并行遍历 base 下的普通文件，跳过嘈杂目录。
// visit 会在多个 worker 中并发调用，调用方必须自行保证线程安全；
// 返回 false 会置位全局停止标记，其它 worker 在下一个检查点退出。
// ctx 取消时停止派发新目录，已在途的 visit 会自然结束。
func walkFiles(ctx context.Context, base string, visit func(string) bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}

	// 单文件根：与 filepath.WalkDir 的行为一致，直接投递该路径。
	if info, err := os.Stat(base); err == nil && info.Mode().IsRegular() {
		visit(base)
		return
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	// semaphore 限制同时展开目录的 goroutine 数，等价于固定 worker 池；
	// 相比共享 channel 的 worker 池，它不会在目录扇出时因 channel 写满死锁。
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var stopped atomic.Bool

	var walk func(dir string)
	walk = func(dir string) {
		sem <- struct{}{}
		defer func() { <-sem }()

		entries, err := readDirCached(dir)
		if err != nil {
			return
		}
		bufp := dirPathScratch.Get().(*[]byte)
		buf := *bufp
		defer func() {
			*bufp = buf[:0]
			dirPathScratch.Put(bufp)
		}()

		for _, entry := range entries {
			if stopped.Load() || ctx.Err() != nil {
				return
			}
			full := joinPath(buf, dir, entry.name)
			buf = full
			path := string(full)
			if entry.isDir {
				// 与原 filepath.WalkDir 实现一致：任何名为 node_modules
				// 等的子目录都跳过（base 自身作为根直接传入，不受此检查影响）。
				if searchSkipDirs[entry.name] {
					continue
				}
				wg.Add(1)
				go func(p string) {
					defer wg.Done()
					walk(p)
				}(path)
				continue
			}
			if entry.kind.IsRegular() || entry.kind == 0 {
				if !visit(path) {
					stopped.Store(true)
					return
				}
			}
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		walk(base)
	}()
	wg.Wait()
}

// joinPath 把 scratch 当作可复用缓冲区拼出 dir/name，避免 filepath.Join
// 为每个文件分配一个字符串；返回切片在下次调用前有效。
func joinPath(scratch []byte, dir, name string) []byte {
	need := len(dir) + 1 + len(name)
	if cap(scratch) < need {
		scratch = make([]byte, need, need*2)
	}
	scratch = scratch[:need]
	copy(scratch, dir)
	scratch[len(dir)] = os.PathSeparator
	copy(scratch[len(dir)+1:], name)
	return scratch
}
