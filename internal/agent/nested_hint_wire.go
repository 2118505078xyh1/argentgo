package agent

// 工具触及路径的收集器与嵌套指令提醒的注入载荷。
//
// 工具派发完成后把 read/edit/write/glob/grep 的路径与 bash cwd 交给 Add，
// 请求前调用 Reminder 得到一条有界提醒，由调用方经 injectSteeringAs(extra...)
// 追加到下一轮用户消息。本文件不触碰 loop.go/loop_helpers.go：它只提供纯状态
// 容器与字符串载荷，接线点在循环的工具派发处与轮次边界（由上层完成）。

import (
	"path/filepath"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/memory"
)

// TouchedPathCollector 在一次运行内收集工具触及的路径，去重且有界。
// 线程安全：工具可能在子代理/后台路径上并发派发。
type TouchedPathCollector struct {
	mu    sync.Mutex
	root  string
	paths []string
	seen  map[string]bool
}

// NewTouchedPathCollector 返回绑定到工作区根目录的收集器。
func NewTouchedPathCollector(root string) *TouchedPathCollector {
	return &TouchedPathCollector{root: root, seen: map[string]bool{}}
}

// Add 记录一批触及路径。空白项被忽略；达到 TouchedPathLimit 后丢弃新项。
func (c *TouchedPathCollector) Add(paths ...string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range paths {
		if p == "" {
			continue
		}
		key := filepath.Clean(p)
		if c.root != "" && !filepath.IsAbs(key) {
			key = filepath.Join(c.root, key)
		}
		key = filepath.Clean(key)
		if c.seen[key] {
			continue
		}
		if len(c.paths) >= memory.TouchedPathLimit {
			break
		}
		c.seen[key] = true
		c.paths = append(c.paths, p)
	}
}

// Reminder 发现与已收集路径相关的嵌套指令文件，返回只含路径的有界提醒。
// 没有发现时返回空串，调用方应跳过注入。
func (c *TouchedPathCollector) Reminder() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	paths := append([]string(nil), c.paths...)
	root := c.root
	c.mu.Unlock()
	if len(paths) == 0 {
		return ""
	}
	return memory.NestedInstructionHint(root, paths)
}

// Reset 清空已收集的路径。新回合或手术式回退后调用，避免旧提醒重复注入。
func (c *TouchedPathCollector) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = nil
	c.seen = map[string]bool{}
}

// Count 返回当前已收集的去重路径数。
func (c *TouchedPathCollector) Count() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}
