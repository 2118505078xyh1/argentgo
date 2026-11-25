package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"example.com/argentgo/internal/commands"
	"example.com/argentgo/internal/skills"
)

// fingerprintHexLen 是从 SHA-256 摘要中保留的十六进制字符数。16 个字符（64 位）
// 足以区分不同的定义树，同时保持缓存键较小。
const fingerprintHexLen = 16

// DefinitionCache 依据源目录的指纹缓存命令和技能的发现结果。
//
// commands.Discover 和 skills.Discover 每次调用都会遍历文件系统；在重建系统提示的地方，
// 这会在一个进程内反复发生。调用方在会话生命周期内保留一个 DefinitionCache，
// 改为向它请求当前快照。指纹计算很廉价（仅 stat，不读文件），
// 因此除非磁盘上确实有变化，Get 都很快。
//
// DefinitionCache 在首次使用后不得被复制；请使用指针。
type DefinitionCache struct {
	mu          sync.Mutex
	fingerprint string
	commands    *commands.Set
	skills      *skills.Registry

	loaded bool // 至少完成过一次发现
	dirty  bool // 调用过 Refresh；强制在下一次 Get 时重新发现

	hits      int64
	misses    int64
	refreshes int64
}

// Get 返回给定根目录的当前命令集和技能注册表。
//
// 根目录是 commands.Discover / skills.Discover 会扫描的目录；命令根目录会被转换为
// 无名的 commands.Source 值（带插件前缀的来源必须由调用方处理）。
// 当任一根目录列表的指纹自上次发现以来未变化时，会返回完全相同的指针，
// 且不会执行 stat 之外的任何文件系统扫描。
//
// Get 可安全并发使用。
func (c *DefinitionCache) Get(commandRoots, skillRoots []string) (*commands.Set, *skills.Registry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fingerprint := c.fingerprintOf(commandRoots) + "#" + c.fingerprintOf(skillRoots)
	if c.loaded && !c.dirty && fingerprint == c.fingerprint {
		c.hits++
		return c.commands, c.skills
	}

	// 替换先前加载的快照就是一次刷新，无论它是由指纹变化触发，
	// 还是由显式的 Refresh 调用触发。
	if c.loaded {
		c.refreshes++
	}
	c.misses++

	c.commands = commands.Discover(commandSources(commandRoots))
	c.skills = skills.Discover(skillRoots)
	c.fingerprint = fingerprint
	c.loaded = true
	c.dirty = false
	return c.commands, c.skills
}

// Refresh 使缓存的快照失效。下一次 Get 会丢弃它们并重新执行发现，即使指纹未变。
// 从配置更新/变更事件处理器中调用它是安全的，且它本身不会阻塞。
func (c *DefinitionCache) Refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		c.dirty = true
	}
}

// Stats 报告自构造以来的缓存统计：
//   - hits：由缓存快照服务的 Get 调用；
//   - misses：执行了发现的 Get 调用；
//   - refreshes：替换了已加载快照的 miss。
func (c *DefinitionCache) Stats() (hits, misses, refreshes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.refreshes
}

// fingerprintOf 遍历每个根目录，并对其下每个文件的稳定描述做哈希：
// 相对根目录的路径、修改时间（纳秒）和大小。缺失或不可读的根目录与条目会被跳过，
// 使暂时的权限错误不会屏蔽有效的定义。摘要会被截断为十六进制前缀，以保持缓存键紧凑。
//
// 只记录文件：每当添加一个无关条目时目录的 mtime 都会改变，而文件条目已经捕获了
// 所有影响发现的变更（markdown 文件的增加、删除、重命名或编辑）。
func (c *DefinitionCache) fingerprintOf(roots []string) string {
	var b strings.Builder
	for _, root := range roots {
		// 根目录本身也是键的一部分，因此即使两棵树其余完全相同，
		// 交换根目录列表仍会使缓存失效。
		fmt.Fprintf(&b, "root:%s\n", filepath.ToSlash(root))
		fingerprintWalk(&b, root, root)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:fingerprintHexLen]
}

// fingerprintWalk 递归记录 dir 下的文件。os.ReadDir 返回按名称排序的条目，
// 因此生成的描述是确定的。
func fingerprintWalk(b *strings.Builder, root, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // 根目录不存在或权限被拒：优雅跳过
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			continue // 该条目发生竞态删除或权限错误
		}
		if info.IsDir() {
			fingerprintWalk(b, root, path)
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		fmt.Fprintf(b, "%s|%d|%d\n", filepath.ToSlash(rel), info.ModTime().UnixNano(), info.Size())
	}
}

// commandSources 把普通根目录路径适配为 Discover 所期望的 Source 列表。
func commandSources(roots []string) []commands.Source {
	sources := make([]commands.Source, 0, len(roots))
	for _, root := range roots {
		sources = append(sources, commands.Source{Root: root})
	}
	return sources
}
