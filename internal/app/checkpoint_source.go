package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// checkpointSource 把会话检查点存储适配为消费侧的 tools.CheckpointSource 接口。
// 它位于 app 中，使 internal/tools 无需导入 internal/session
// （该分层规则由 internal/archguard 强制检查）。
type checkpointSource struct {
	sess *session.Session
	root string
}

// Diff 返回最近检查点的逐文件摘要。空的 id 表示 "latest"；正整数 id 会被解析为回退步数。
func (c *checkpointSource) Diff(id string) ([]tools.FileChange, error) {
	if c == nil || c.sess == nil {
		return nil, fmt.Errorf("no session available")
	}
	// session.CheckpointBack 的步数从 1 起（1 表示最近）。空 id/"latest" 必须
	// 默认取 1；传 0 会被 CheckpointBack 当作非法步数恒定报错，使"最近差异"失效。
	steps := 1
	if id != "" && id != "latest" {
		if _, err := fmt.Sscanf(id, "%d", &steps); err != nil || steps < 1 {
			return nil, fmt.Errorf("bad checkpoint id %q", id)
		}
	}
	cp, _, err := c.sess.CheckpointBack(steps)
	if err != nil {
		return nil, err
	}
	out := make([]tools.FileChange, 0, len(cp.Files))
	for _, f := range cp.Files {
		before := f.Before
		after, err := os.ReadFile(filepath.Join(c.root, f.Path))
		if err != nil {
			after = nil
		}
		out = append(out, tools.FileChangeFromBytes(f.Path, before, after))
	}
	return out, nil
}

// wireCheckpointDiff 在已知会话和工作区根目录之后，把 checkpoint_diff 工具挂接到注册表。
func wireCheckpointDiff(registry *tools.Registry, sess *session.Session, root string) {
	if registry == nil || sess == nil {
		return
	}
	registry.SetCheckpointSource(&checkpointSource{sess: sess, root: root})
}
