package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- submit_plan ----

const planFileName = "plan.json"

const (
	planMaxSteps     = 100
	planMaxStepRunes = 500
	planMaxStatus    = 64
)

// planState 是 submit_plan 写入的会话级记录。计划只是
// 给模型的文档，绝不是执行队列。
type planState struct {
	Steps     []string `json:"steps"`
	Status    string   `json:"status"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type submitPlanTool struct{}

func (submitPlanTool) Name() string { return "submit_plan" }
func (submitPlanTool) Description() string {
	return "Record a plan in session state as plan.json; steps are stored, never executed."
}
func (submitPlanTool) Mutating() bool { return false }
func (submitPlanTool) Schema() string {
	return `{"type":"object","properties":{"steps":{"type":"array","description":"non-empty plan steps; omit to read the plan back","items":{"type":"string"}},"status":{"type":"string","description":"plan status; defaults to draft"}}}`
}

func (submitPlanTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Steps  []string `json:"steps"`
		Status string   `json:"status"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("submit_plan: bad arguments: %w", err)
	}
	path := filepath.Join(sessionDir(ctx, root), planFileName)

	if len(args.Steps) == 0 {
		plan, err := loadPlan(path)
		if err != nil {
			return "", err
		}
		return renderPlan(plan), nil
	}

	if len(args.Steps) > planMaxSteps {
		return "", fmt.Errorf("submit_plan: too many steps (%d, maximum %d)", len(args.Steps), planMaxSteps)
	}
	steps := make([]string, 0, len(args.Steps))
	seen := make(map[string]bool, len(args.Steps))
	for i, step := range args.Steps {
		step = strings.TrimSpace(step)
		if step == "" {
			return "", fmt.Errorf("submit_plan: step %d is not a non-empty string", i+1)
		}
		if utf8.RuneCountInString(step) > planMaxStepRunes {
			return "", fmt.Errorf("submit_plan: step %d is longer than %d characters", i+1, planMaxStepRunes)
		}
		if strings.ContainsAny(step, "\r\n\x00") {
			return "", fmt.Errorf("submit_plan: step %d contains a line break or control character", i+1)
		}
		key := strings.ToLower(step)
		if seen[key] {
			return "", fmt.Errorf("submit_plan: step %d duplicates step %q", i+1, step)
		}
		seen[key] = true
		steps = append(steps, step)
	}

	existing, err := loadPlan(path)
	if err != nil {
		return "", err
	}
	status := strings.TrimSpace(args.Status)
	if status == "" {
		status = "draft"
	}
	if utf8.RuneCountInString(status) > planMaxStatus || strings.ContainsAny(status, "\r\n\x00") {
		return "", fmt.Errorf("submit_plan: status must be at most %d characters with no line breaks", planMaxStatus)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	plan := planState{Steps: steps, Status: status, CreatedAt: now, UpdatedAt: now}
	if existing != nil && existing.CreatedAt != "" {
		plan.CreatedAt = existing.CreatedAt
	}

	stateDir := filepath.Dir(path)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return renderPlan(&plan), nil
}

func loadPlan(path string) (*planState, error) {
	data, err := readFileLimited(path, readMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var plan planState
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("submit_plan: corrupt state file %s: %w", path, err)
	}
	return &plan, nil
}

func renderPlan(plan *planState) string {
	if plan == nil || len(plan.Steps) == 0 {
		return "plan is empty"
	}
	lines := []string{fmt.Sprintf("plan (status: %s, updated: %s):", plan.Status, plan.UpdatedAt)}
	for i, step := range plan.Steps {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, step))
	}
	lines = append(lines, "plan recorded only; no steps were executed")
	return strings.Join(lines, "\n")
}
