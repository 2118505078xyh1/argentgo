package app

// 编辑后主动通知语言服务器文件变更的接线层。MutationPaths 报告的路径是工作区
// 相对路径，这里先转成绝对路径与 file:// URI，再按绝对路径经进程级 mux 找到
// *已经启动* 的客户端，发送 workspace/didChangeWatchedFiles。
//
// 这是纯优化：通知阶段绝不懒启动语言服务，没有客户端、路径转换失败或通知出错
// 都静默跳过，绝不影响工具本身的输出与成功/失败判定。

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
)

// lspWatchOptOutEnv 是关闭编辑后 LSP 文件变更通知的显式开关。
// 默认开启（因为只通知已运行的客户端，无客户端时零副作用），仅值为 "0" 时关闭。
const lspWatchOptOutEnv = "ARGENT_LSP_WATCH"

// lspWatchNotifyTimeout 限制单次通知的时长，避免慢服务器拖住工具返回。
const lspWatchNotifyTimeout = 2 * time.Second

// muxClientForFile 是可替换的客户端查找接缝：按工作区根与文件绝对路径从进程级
// mux 中取已注册的客户端。只读取已有绑定，绝不触发派生（通知阶段不懒启动）。
// root 由接缝保留以便将来按工作区过滤，当前实现按文件扩展名路由即可。
var muxClientForFile = func(root, abs string) (*lsp.Client, bool) {
	_ = root
	mgr := getSharedManager()
	if mgr == nil {
		return nil, false
	}
	// 与 lspManager.Close 的 mux 重建同步：先短暂持锁取出当前 mux 指针，
	// 之后的所有路由都交给 mux 自身的读写锁保护。
	mgr.mu.Lock()
	mux := mgr.mux
	mgr.mu.Unlock()
	if mux == nil {
		return nil, false
	}
	return mux.ClientForFile(abs)
}

// didChangeWatchedFiles 是可替换的通知接缝，生产实现直接委托给 lsp.Client。
// 测试可替换它以离线观察发送的 FileChange 列表，无需真实语言服务进程。
var didChangeWatchedFiles = func(ctx context.Context, client *lsp.Client, changes []lsp.FileChange) error {
	return client.DidChangeWatchedFiles(ctx, changes)
}

// notifyLSPFileChanges 把变更文件通知给已运行的语言服务器。路径是工作区相对路径，
// 先转绝对路径再转 URI；同一客户端的多条变更合并成一次通知。没有客户端或通知
// 出错时静默跳过——这是纯优化，绝不改变工具结果。
func notifyLSPFileChanges(root string, paths []string) {
	if len(paths) == 0 {
		return
	}
	// 根路径先转绝对路径，使相对路径的拼接基于工作区而非进程当前目录。
	if root != "" {
		if absolute, err := filepath.Abs(root); err == nil && absolute != "" {
			root = absolute
		}
	}

	// 按客户端指针分组，order 保留首次命中顺序，使通知顺序确定且可测试。
	changesByClient := make(map[*lsp.Client][]lsp.FileChange)
	order := make([]*lsp.Client, 0)
	for _, path := range paths {
		if path == "" {
			continue
		}
		abs := path
		if !filepath.IsAbs(abs) && root != "" {
			abs = filepath.Join(root, path)
		}
		absolute, err := filepath.Abs(abs)
		if err != nil {
			continue
		}
		client, ok := muxClientForFile(root, absolute)
		if !ok || client == nil {
			continue
		}
		if _, seen := changesByClient[client]; !seen {
			order = append(order, client)
		}
		changesByClient[client] = append(changesByClient[client], lsp.FileChange{
			URI:  lsp.PathToURI(absolute),
			Type: lsp.FileChangeChanged, // MutationPaths 不区分变更类型，统一按内容修改通知。
		})
	}

	for _, client := range order {
		changes := changesByClient[client]
		if len(changes) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), lspWatchNotifyTimeout)
		// 通知结果被刻意丢弃：发送失败只意味着这次优化没生效，不应反馈给模型。
		_ = didChangeWatchedFiles(ctx, client, changes)
		cancel()
	}
}

// notifyLSPFileChanged 在工具成功的调用点按 mutationPaths 收集路径后触发通知。
// notifier 或 mutationPaths 为 nil 时为空操作；失败分支不会走到这里。
func (h *hookedTools) notifyLSPFileChanged(name, args string) {
	if h.lspFileNotifier == nil || h.mutationPaths == nil {
		return
	}
	root := ""
	if h.bridge != nil {
		root = h.bridge.cwd
	}
	h.lspFileNotifier(root, h.mutationPaths(name, args))
}

// lspWatchNotifier 按 ARGENT_LSP_WATCH 返回要注入的 notifier：默认启用（只通知
// 已运行的客户端，无客户端时零副作用），显式设为 "0" 时返回 nil 完全关闭。
func lspWatchNotifier(env func(string) string) func(string, []string) {
	if env != nil && strings.TrimSpace(env(lspWatchOptOutEnv)) == "0" {
		return nil
	}
	return notifyLSPFileChanges
}
