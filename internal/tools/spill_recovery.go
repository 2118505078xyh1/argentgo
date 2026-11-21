package tools

import (
	"os"
	"strings"
)

// SpillRecovery 是"受控溢出仓库按需取回"的 opt-in 恢复原语。
// 它把一个溢出产出目录绑定为只读视图，让调用方按行范围取回
// 此前被 SpillLargeOutput 省略的内容。
//
// 本类型是纯本地的辅助工具：不注册到 Registry.Specs() 的默认
// 列表，零调用方，因此不会改变任何现有工具的行为，也不会扰动
// 工具清单 golden。接线由未来的调用方自行完成（例如 TUI 或
// session 层），tools 层本身不依赖它们。
type SpillRecovery struct {
	dir string
}

// NewSpillRecovery 绑定一个溢出产出目录。dir 传空时沿用现有
// readSpillRangeInDir 的约定（按当前目录 "." 处理）。
func NewSpillRecovery(dir string) *SpillRecovery {
	return &SpillRecovery{dir: dir}
}

// Read 从绑定的产出目录中按含端点的、基于 1 的行范围
// [start, end] 读取 path。它直接复用 readSpillRangeInDir 的
// 全部路径校验：拒绝 ".." 遍历、非规范产出文件名、目录、
// 以及符号链接（Lstat 拒绝），因此任意文件（密钥、配置等）
// 都无法借此接口被读取。totalLines 对恢复调用方无用，在此丢弃。
func (s *SpillRecovery) Read(path string, start, end int) (string, error) {
	content, _, err := readSpillRangeInDir(s.dir, path, start, end)
	return content, err
}

// IsSpillPath 报告 path 是否为本恢复实例可读取的溢出产出。
// 它与 Read 共用同一套校验链（遍历段、规范文件名、目录边界、
// 符号链接），但不读取文件内容，适合在发出恢复提示前做廉价判断。
//
// 校验逻辑与 readSpillRangeInDir 保持一致；由于文件隔离约束
// 不改动后者，这里本地复刻其前置校验步骤。
func (s *SpillRecovery) IsSpillPath(path string) bool {
	_, ok := s.verifiedSpillAbs(path)
	return ok
}

// verifiedSpillAbs 依次执行 readSpillRangeInDir 的前置校验，
// 成功时返回产出文件的绝对路径。它与 Read 实际走的是同一套
// 校验来源（spillArtifactAbsInDir / rejectSpillTraversal），
// 仅省略了读取文件内容这一步。
func (s *SpillRecovery) verifiedSpillAbs(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	if rejectSpillTraversal(path) != nil {
		return "", false
	}
	abs, ok := spillArtifactAbsInDir(s.dir, path)
	if !ok {
		return "", false
	}
	// spillArtifactAbsInDir 使用 os.Stat（跟随符号链接）。溢出
	// 文件始终以 O_EXCL 创建为普通文件，因此必须拒绝指向目录外
	// 任意目标的符号链接，与 readSpillRangeInDir 的 Lstat 一致。
	info, err := os.Lstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return abs, true
}
