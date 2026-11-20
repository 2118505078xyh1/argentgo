package agent

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Checkpoint 描述一批尚未运行的变更型工具调用。
//
// agent 包不负责会话清单；它只提供轮次、调用以及调用方应当快照的工作区路径。
// 会话层可以在工具改动工作区之前，为 Paths 记录轮次前的字节内容和 SHA-256 摘要，
// 然后把结果合并进它的检查点文件。
type Checkpoint struct {
	Turn  int              `json:"turn"`
	Calls []CheckpointCall `json:"calls"`
	Paths []string         `json:"paths"`
}

// CheckpointCall 是检查点中包含的一次工具调用。
type CheckpointCall struct {
	ID   string `json:"id,omitempty"`
	Tool string `json:"tool"`
	Args string `json:"args,omitempty"`
}

// MutationReporter 是 ToolRunner 可选实现的接口，用于声明一次调用可以改动哪些工作区路径。
// 只有工作区内的路径才有用；agent 会对它们做规范化和去重。
type MutationReporter interface {
	MutationPaths(name, args string) []string
}

// newCheckpoint 为一批调用构建检查点。当批次中不含变更型调用时返回 nil，
// 因此只读批次完全保持静默。
func (s *runState) newCheckpoint(turn int, calls []llm.ToolCall) *Checkpoint {
	cp := &Checkpoint{Turn: turn}
	seenPaths := make(map[string]bool)
	for _, call := range calls {
		name := call.Function.Name
		mutating := s.mutating(name)
		if !mutating {
			continue
		}
		cp.Calls = append(cp.Calls, CheckpointCall{
			ID:   call.ID,
			Tool: name,
			Args: call.Function.Arguments,
		})
		for _, path := range s.mutationPaths(name, call.Function.Arguments) {
			clean := normalizeMutationPath(path)
			if clean == "" || seenPaths[clean] {
				continue
			}
			seenPaths[clean] = true
			cp.Paths = append(cp.Paths, clean)
		}
	}
	if len(cp.Calls) == 0 {
		return nil
	}
	sort.Strings(cp.Paths)
	return cp
}

// mutationPaths 先查询显式钩子，再查询工具运行器的可选接口。
// 包装后的运行器可能会隐藏该接口，因此这里使用原始的 Options.Tools 而非 s.tools。
func (s *runState) mutationPaths(name, args string) []string {
	if s.opts.MutationPaths != nil {
		return s.opts.MutationPaths(name, args)
	}
	if reporter, ok := s.opts.Tools.(MutationReporter); ok {
		return reporter.MutationPaths(name, args)
	}
	return nil
}

// normalizeMutationPath 拒绝绝对路径和越界路径，返回稳定的、以斜杠分隔的相对路径。
// 返回空字符串表示"不是检查点路径"。
func normalizeMutationPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}
