package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/proc"
)

// ---- bash ----

// toolProgressKey mirrors agent.toolProgressKey. The two packages cannot share
// a typed context key without tools importing agent, which the layering rules
// forbid, so they agree on this literal and on a plain func(string) value.
const toolProgressKey = "argent.tool.progress"

// emitProgress publishes one intermediate progress row through the emitter the
// agent loop installed in ctx. It is a silent no-op when no emitter is present,
// and the loop's gate drops the row once the tool call has returned. A shell
// job that outlives bashTool.Run therefore cannot append a stale row.
func emitProgress(ctx context.Context, text string) {
	emitter, _ := ctx.Value(toolProgressKey).(func(string))
	if emitter != nil {
		emitter(text)
	}
}

// bashCharsPerToken mirrors the agent loop's estimate so the volume hint uses a
// familiar unit. internal/tools must not import internal/agent, so the estimate
// is kept local to this package.
const bashCharsPerToken = 4

// isWideBashRune reports whether r belongs to a script that tokenizers treat as
// roughly one token per rune: Han, Kana, Hangul, fullwidth forms, CJK
// punctuation and common emoji. The ranges match agent.EstimateTokens.
func isWideBashRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0xA4CF) || // CJK radicals, Kangxi, Han
		(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
		(r >= 0xFE10 && r <= 0xFE19) || // CJK punctuation
		(r >= 0xFE30 && r <= 0xFE6F) || // CJK compatibility forms
		(r >= 0xFF00 && r <= 0xFF60) || // fullwidth forms
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1FAFF) || // symbols/emoji
		(r >= 0x20000 && r <= 0x3FFFD) // CJK extension planes
}

// bashOutputTokens estimates the token cost of raw command output. Wide CJK
// runes cost roughly one token each; other runes follow the chars-per-token
// rule. Counting them separately avoids badly under-reporting CJK output.
func bashOutputTokens(s string) int {
	wide, narrow := 0, 0
	for _, r := range s {
		if isWideBashRune(r) {
			wide++
		} else {
			narrow++
		}
	}
	return wide + (narrow+bashCharsPerToken-1)/bashCharsPerToken
}

// clipBashOutput sanitizes and clips raw command output and, only when content
// was actually dropped, prefixes a short volume hint so the user and the model
// can see the context cost of what the command produced. It is a pure text
// change: it never touches the exit status, error path or clipping threshold.
//
// It takes the raw output on purpose. Sanitizing first collapses runs of
// identical lines, and deciding on the collapsed text both under-reported the
// volume and skipped the hint for exactly the repetitive output that needs it
// most (3000 identical lines compress to one line, so the budget check no
// longer fired). Size and estimate therefore come from raw; only the body that
// reaches the model is sanitized.
func clipBashOutput(ctx context.Context, raw string) string {
	limit := outputLimit(ctx)
	if limit <= 0 {
		limit = maxToolOutput
	}
	clipped := clip(ctx, sanitizeShellOutput(raw))
	if len(raw) <= limit {
		return clipped
	}
	return fmt.Sprintf("[output: ~%.1fk tokens, clipped]\n%s",
		float64(bashOutputTokens(raw))/1000, clipped)
}

type bashTool struct{}

func (bashTool) Name() string        { return "bash" }
func (bashTool) Description() string { return "Run a shell command in the workspace root." }
func (bashTool) Mutating() bool      { return true }
func (bashTool) Schema() string {
	return `{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"},"background_after_ms":{"type":"integer","description":"If the command is still running after this many milliseconds, promote it to a background job instead of killing it on timeout."}},"required":["command"]}`
}

func (bashTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Command           string `json:"command"`
		TimeoutSeconds    int    `json:"timeout_seconds"`
		BackgroundAfterMS int    `json:"background_after_ms"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("bash: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("bash: command must not be empty")
	}
	var requested time.Duration
	if args.TimeoutSeconds > 0 {
		requested = time.Duration(args.TimeoutSeconds) * time.Second
	}
	timeout := clampTimeout("bash", requested, maxTimeout(ctx))
	path, argv := shellInvocation(args.Command)

	// Promotion is opt-in and needs somewhere to record the job. Without a
	// registry (or the argument), keep the original hard-kill behaviour.
	if args.BackgroundAfterMS > 0 {
		if registry := jobRegistry(ctx); registry != nil {
			return runPromotableBash(ctx, root, registry, args.Command, path, argv,
				timeout, time.Duration(args.BackgroundAfterMS)*time.Millisecond)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, argv...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	text := clipBashOutput(ctx, string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("bash: timed out after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("bash: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return "(no output)", nil
	}
	return text, nil
}

// shellInvocation returns the program and leading arguments that execute a
// command line. It is shared by the hard-timeout and promotable paths so a
// command is spelled the same way in both.
func shellInvocation(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err == nil {
			return "bash", []string{"-lc", command}
		}
		return "cmd", []string{"/c", command}
	}
	return "sh", []string{"-lc", command}
}

// runPromotableBash waits at most backgroundAfter for the command. On that
// deadline the still-running child is handed to the jobs registry; the model
// gets a normal tool result with the job id, so the turn is never blocked.
func runPromotableBash(ctx context.Context, root string, registry *jobs.Registry,
	command, path string, argv []string, hardTimeout, backgroundAfter time.Duration) (string, error) {
	// The caller's context and the hard timeout still bound the whole call; the
	// soft deadline is what turns a slow child into a job.
	runCtx, cancel := context.WithTimeout(ctx, hardTimeout)
	defer cancel()

	res, live, err := proc.RunWithTimeout(runCtx, proc.Command{
		Path:          path,
		Args:          argv,
		Dir:           root,
		Timeout:       backgroundAfter,
		KeepOnTimeout: true,
		NoWindow:      runtime.GOOS == "windows",
	})

	var timeoutErr *proc.TimeoutError
	if errors.As(err, &timeoutErr) && live != nil {
		logFile, fileErr := os.CreateTemp("", "argent-job-*.log")
		if fileErr != nil {
			live.Kill()
			return "", fmt.Errorf("bash: create job log: %w", fileErr)
		}
		logPath := logFile.Name()
		// Flush what was captured before the timeout and send the rest of the
		// child's output straight to the log the job will tail.
		live.SetOutput(logFile)
		// The job watcher reads the same Done channel; once it closes, every
		// write to the log is finished and the file can be released. This
		// goroutine outlives bashTool.Run: its progress row is deliberately
		// routed through the gated emitter so a late update cannot reach the
		// transcript after the tool result has been recorded.
		go func() {
			<-live.Done()
			emitProgress(ctx, "bash job finished: "+command)
			_ = logFile.Close()
		}()

		job, jobErr := registry.PromoteFromTimeout(command, live, logPath)
		if jobErr != nil {
			live.Kill()
			return "", fmt.Errorf("bash: promote job: %w", jobErr)
		}
		out := fmt.Sprintf("started background job %s (pid %d): %s\nstill running; check with `argent jobs` (output: `argent jobs tail %s`).",
			job.ID, job.PID, command, job.ID)
		if partial := strings.TrimSpace(res.Combined()); partial != "" {
			out += "\nOutput so far:\n" + clipBashOutput(ctx, partial)
		}
		return out, nil
	}

	if err != nil {
		return clipBashOutput(ctx, res.Combined()), fmt.Errorf("bash: %w", err)
	}
	text := clipBashOutput(ctx, res.Combined())
	if strings.TrimSpace(text) == "" {
		return "(no output)", nil
	}
	return text, nil
}
