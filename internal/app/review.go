package app

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"example.com/argentgo/internal/commands"
)

// reviewRunner 刻意比通用 shell 更窄：/review 只能运行审查报告所需的两个只读 git 命令。
type reviewRunner struct{ root string }

func (r reviewRunner) Run(name string, args ...string) ([]byte, error) {
	if name != "git" || len(args) == 0 {
		return nil, errors.New("review: only read-only git commands are allowed")
	}
	switch strings.Join(args, " ") {
	case "status --porcelain":
	case "diff --unified=3":
	default:
		return nil, fmt.Errorf("review: git %s is not allowed", strings.Join(args, " "))
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = r.root
	return cmd.Output()
}

// reviewPrompt 为斜杠式的 /review 输入返回一条注入的指令。返回 ok=false 时普通提示保持原样。
func reviewPrompt(root, prompt string) (string, bool) {
	text := strings.TrimSpace(prompt)
	if text != "/review" && !strings.HasPrefix(text, "/review ") {
		return "", false
	}
	report, err := commands.Review(reviewRunner{root: root})
	if err != nil {
		return "The /review command failed: " + err.Error() + ". Answer the user directly.", true
	}
	return "Read-only /review report:\n" + report.String() +
		"\nSummarise the findings; do not modify files.", true
}
