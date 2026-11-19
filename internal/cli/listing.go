package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// workspaceRoot resolves -C/--cwd (or the process directory) to an absolute
// path for a subcommand.
func workspaceRoot(app App, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Abs(cwd)
}

// homeFromEnv finds the user's home directory for skill/command discovery.
func homeFromEnv(env func(string) string) string {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// splitRoot removes a leading -C/--cwd flag from a subcommand's arguments.
func splitRoot(args []string) (cwd string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-C" || args[i] == "--cwd":
			if i+1 >= len(args) {
				return "", nil, &usageError{msg: args[i] + " needs a value"}
			}
			cwd = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--cwd="):
			cwd = strings.TrimPrefix(args[i], "--cwd=")
		case strings.HasPrefix(args[i], "-C="):
			cwd = strings.TrimPrefix(args[i], "-C=")
		default:
			rest = append(rest, args[i])
		}
	}
	return cwd, rest, nil
}
