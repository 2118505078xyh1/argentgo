package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FileSystem 是工作区面板使用的 workspace 只读视图。
// 默认读取真实文件系统；测试注入假树，且
// 任何渲染路径都不会自行调用 os.Getenv 或 os.ReadFile。
type FileSystem interface {
	ReadDir(path string) ([]FileInfo, error)
	ReadFile(path string) ([]byte, error)
}

// FileInfo 是 tree 所需的 os.DirEntry 的可移植子集。
type FileInfo struct {
	Name  string
	IsDir bool
}

type osFileSystem struct{}

func (osFileSystem) ReadDir(path string) ([]FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		out = append(out, FileInfo{Name: entry.Name(), IsDir: entry.IsDir()})
	}
	return out, nil
}

func (osFileSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

const (
	maxWorkspaceEntries = 300
	maxWorkspaceDepth   = 4
	maxPreviewBytes     = 64 << 10
	maxPreviewLines     = 80
)

// workspaceIgnoredDirs 是构建输出和依赖树：遍历它们
// 会把源码埋没在噪声中。
var workspaceIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"target":       true,
	"dist":         true,
}

type workspaceEntry struct {
	Path  string // slash-separated relative path
	Name  string
	Depth int
	IsDir bool
}

func (m *Model) buildWorkspaceLocked() {
	m.workspace = nil
	m.workspaceScroll = 0
	m.previewScroll = 0
	m.previewLines = nil
	m.previewPath = ""
	if m.root == "" || m.fs == nil {
		return
	}
	var walk func(dir, relative string, depth int)
	walk = func(dir, relative string, depth int) {
		if depth > maxWorkspaceDepth || len(m.workspace) >= maxWorkspaceEntries {
			return
		}
		entries, err := m.fs.ReadDir(dir)
		if err != nil {
			return
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Name < entries[j].Name
		})
		for _, entry := range entries {
			if len(m.workspace) >= maxWorkspaceEntries {
				return
			}
			if entry.IsDir && workspaceIgnoredDirs[entry.Name] {
				continue
			}
			path := entry.Name
			if relative != "" {
				path = relative + "/" + entry.Name
			}
			m.workspace = append(m.workspace, workspaceEntry{
				Path: path, Name: entry.Name, Depth: depth, IsDir: entry.IsDir,
			})
			if entry.IsDir {
				walk(filepath.Join(dir, entry.Name), path, depth+1)
			}
		}
	}
	walk(m.root, "", 0)
	m.clampWorkspaceCursorLocked()
	m.refreshPreviewLocked()
}

func (m *Model) clampWorkspaceCursorLocked() {
	if len(m.workspace) == 0 {
		m.workspaceCursor = 0
		return
	}
	if m.workspaceCursor >= len(m.workspace) {
		m.workspaceCursor = len(m.workspace) - 1
	}
	if m.workspaceCursor < 0 {
		m.workspaceCursor = 0
	}
	m.workspaceScroll = clampScroll(m.workspaceScroll, len(m.workspace)-1)
}

func (m *Model) moveWorkspaceCursorLocked(delta int) {
	if len(m.workspace) == 0 {
		return
	}
	m.workspaceCursor += delta
	m.clampWorkspaceCursorLocked()
	// 靠近底部的光标让最新行保持可见，而无需在
	// 靠近顶部时每次按键都滚动。
	treeRows := maxInt((m.bodyH-3)/2, 1)
	if m.workspaceCursor >= m.workspaceScroll+treeRows {
		m.workspaceScroll = m.workspaceCursor - treeRows + 1
	} else if m.workspaceCursor < m.workspaceScroll {
		m.workspaceScroll = m.workspaceCursor
	}
	m.workspaceScroll = clampScroll(m.workspaceScroll, len(m.workspace)-1)
	m.refreshPreviewLocked()
}

func (m *Model) refreshPreviewLocked() {
	m.previewLines = nil
	m.previewPath = ""
	if m.workspaceCursor < 0 || m.workspaceCursor >= len(m.workspace) || m.fs == nil {
		return
	}
	entry := m.workspace[m.workspaceCursor]
	if entry.IsDir {
		return
	}
	m.previewPath = entry.Path
	target := filepath.Join(m.root, filepath.FromSlash(entry.Path))
	data, err := m.fs.ReadFile(target)
	if err != nil {
		m.previewLines = []string{"error: " + err.Error()}
		return
	}
	if len(data) > maxPreviewBytes {
		data = data[:maxPreviewBytes]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > maxPreviewLines {
		lines = lines[:maxPreviewLines]
	}
	m.previewLines = lines
	m.previewScroll = clampScroll(m.previewScroll, len(lines))
}

// workspaceLines 在光标文件的只读预览上方渲染树。
// 结果恰好是 height 行，使面板各节对齐。
func (m *Model) workspaceLines(width, height int) []Line {
	title := NewLine()
	title.Push(text(m.locale, "workspace_title"), m.palette.accent())
	if m.root != "" {
		title.Push(" "+smartShortenPath(m.root, maxInt(width-12, 1)), m.palette.muted())
	}
	out := []Line{title}
	if height <= 1 {
		return out[:height]
	}
	avail := height - 1
	treeH := maxInt(avail/2, 1)
	previewH := avail - treeH - 1 // one separator row
	if previewH < 0 {
		previewH = 0
	}
	for i := m.workspaceScroll; i < len(m.workspace) && len(out) < 1+treeH; i++ {
		entry := m.workspace[i]
		line := NewLine()
		mark := " "
		if i == m.workspaceCursor {
			mark = "›"
		}
		line.Push(mark, m.palette.accent())
		if entry.IsDir {
			line.Push(strings.Repeat("  ", entry.Depth)+"▾ "+entry.Name, m.palette.user())
		} else {
			line.Push(strings.Repeat("  ", entry.Depth)+"  "+entry.Name, Style{})
		}
		out = append(out, line)
	}
	out = append(out, plainPanelRule(width))

	if previewH > 0 {
		head := NewLine()
		head.Push(Clip("↳ "+m.previewPath, width), m.palette.muted())
		out = append(out, head)
		previewH--
	}
	for i := m.previewScroll; i < len(m.previewLines) && previewH > 0; i++ {
		line := NewLine()
		line.Push(strconv.Itoa(i+1)+" │ ", m.palette.dim())
		line.Push(Clip(m.previewLines[i], maxInt(width-8, 1)), Style{})
		out = append(out, line)
		previewH--
	}
	for len(out) < height {
		out = append(out, NewLine())
	}
	return out[:height]
}
