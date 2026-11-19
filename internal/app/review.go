package app

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/commands"
)

// reviewRunner is deliberately narrower than a general shell: /review may run
// only the two read-only git commands the review report needs.
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

// reviewPrompt returns an injected instruction for slash-style /review input.
// Returning ok=false leaves normal prompts untouched.
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
