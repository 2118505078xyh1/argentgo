package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrInvalidSkillName 在名称不是小写 kebab-case 或试图逃逸其 skills 根目录时返回。
// 或试图逃逸其 skills 根目录。
var ErrInvalidSkillName = errors.New("invalid skill name")

// ErrSkillExists 在目标路径已存在技能且调用方未选择覆盖时返回。
// 且调用方未选择覆盖它。
var ErrSkillExists = errors.New("skill already exists")

// maxNameLength 限制作者命名的长度，使模型无法创建荒谬的路径。
const maxNameLength = 64

// AuthorOptions 配置 AuthorWithOptions。
type AuthorOptions struct {
	// Overwrite 允许替换目标路径处已有的 SKILL.md。
	Overwrite bool
}

// AuthorResult 是成功创作操作的结果。
type AuthorResult struct {
	// Path 是被写入的 SKILL.md 文件。
	Path string
	// Name 是验证过的技能名。
	Name string
}

// authorMu 在一个进程内串行化创作，使多个调用方争相创建同一技能时无覆盖检查仍然可靠。本包中的发现无需该锁也安全，因为发布使用临时文件+重命名。
// 使多个调用方争相创建同一技能时无覆盖检查仍然可靠。本包中的发现
// 无需该锁也安全，因为发布使用临时文件+重命名。
var authorMu sync.Mutex

// Author 在 root 下为 name 写入一个格式良好的 SKILL.md 并返回其路径。root 为空时使用用户的技能目录。已有技能
// 并返回其路径。root 为空时使用用户的技能目录。已有技能
// 从不被替换；使用带 Overwrite 的 AuthorWithOptions 来允许替换。
//
// 该签名刻意只使用标准库类型，使 agent 包能声明一个相同的本地接口而无需导入本包。
// 可声明一个相同的本地接口而无需导入本包。
func Author(root, name, description, body string) (string, error) {
	res, err := AuthorWithOptions(root, name, description, body, AuthorOptions{})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// AuthorWithOptions 是带显式选项的 Author。
func AuthorWithOptions(root, name, description, body string, opts AuthorOptions) (AuthorResult, error) {
	if err := validateName(name); err != nil {
		return AuthorResult{}, err
	}
	target, err := authorTarget(root, name)
	if err != nil {
		return AuthorResult{}, err
	}
	content := renderSkill(name, description, body)

	authorMu.Lock()
	defer authorMu.Unlock()
	if err := publishSkill(target, content, opts.Overwrite); err != nil {
		return AuthorResult{}, err
	}
	return AuthorResult{Path: target, Name: name}, nil
}

// validateName 强制小写 kebab-case 且无路径遍历。字符规则本身已拒绝点、分隔符和大写，但显式的 ".." 检查给调用方更清晰的错误诊断。
// 字符规则本身已拒绝点、分隔符和大写，但
// 显式的 ".." 检查给调用方更清晰的错误诊断。
func validateName(name string) error {
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q contains '..'", ErrInvalidSkillName, name)
	}
	if name == "" || len(name) > maxNameLength {
		return fmt.Errorf("%w: %q must be 1-%d characters", ErrInvalidSkillName, name, maxNameLength)
	}
	dash := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			dash = false
		case c == '-':
			if i == 0 || dash {
				return fmt.Errorf("%w: %q has a misplaced '-'", ErrInvalidSkillName, name)
			}
			dash = true
		default:
			return fmt.Errorf("%w: %q must be lowercase kebab-case", ErrInvalidSkillName, name)
		}
	}
	if dash {
		return fmt.Errorf("%w: %q must not end with '-'", ErrInvalidSkillName, name)
	}
	return nil
}

// authorTarget 解析新技能的绝对位置。非空 root 是工作区（项目技能）；空 root 使用用户主目录的技能。
// 是工作区（项目技能）；空 root 使用用户主目录的技能。
func authorTarget(root, name string) (string, error) {
	base, subdir := root, DefaultProjectSubdir
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("skills: user home directory: %w", err)
		}
		base, subdir = home, DefaultHomeSubdir
	}
	return filepath.Join(base, filepath.FromSlash(subdir), name, "SKILL.md"), nil
}

// renderSkill 构建文档。name 在嵌入前已验证，
// renderSkill 构建文档。name 在嵌入前已验证，因此无需引用；description 写为单引号 YAML 标量，引号加倍且换行折叠为空格，这能
// 带引号的 YAML 标量，引号加倍且换行折叠为空格，这能
// 经由 ParseFields 精确往返且无法突破 frontmatter 块。
func renderSkill(name, description, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "description: %s\n", yamlScalar(description))
	b.WriteString("---\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func yamlScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// publishSkill 在同一目录写入临时文件并
// publishSkill 在同一目录写入临时文件并重命名到位。因此并发的 Discover 看到的是旧文件或完整的新文件，绝不是半写的文档。
// 完整的新文件，绝不是半写的文档。
func publishSkill(target string, content []byte, overwrite bool) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("skills: create %s: %w", dir, err)
	}
	if !overwrite {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("%w: %s", ErrSkillExists, target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("skills: stat %s: %w", target, err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".SKILL.md-*")
	if err != nil {
		return fmt.Errorf("skills: temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: chmod: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("skills: close: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("skills: publish %s: %w", target, err)
	}
	return nil
}
