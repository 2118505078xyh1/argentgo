package gui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// NewFileUsage returns the stdlib default Usage backend. It scans every
// <storage>/sessions/*.jsonl transcript and folds turn/user/assistant/tool
// records plus their token fields into one cumulative report. An empty
// storageDir means ~/.argent.
func NewFileUsage(storageDir string) Usage {
	return &fileUsage{root: storageRoot(storageDir)}
}

type fileUsage struct {
	root string
}

func (u *fileUsage) dir() string {
	return filepath.Join(u.root, "sessions")
}

type usageRecord struct {
	TS   time.Time       `json:"ts"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// turnKinds are the record kinds that represent one completed agent turn.
// Older transcripts may use any of these spellings.
var turnKinds = map[string]bool{
	"turn": true, "turn_end": true, "turn_done": true, "turn_end_done": true,
}

func (u *fileUsage) Summary() (UsageSummary, error) {
	summary := UsageSummary{Models: []ModelUsage{}, Daily: []DailyUsage{}}
	entries, err := os.ReadDir(u.dir())
	if err != nil {
		if os.IsNotExist(err) {
			return summary, nil
		}
		return summary, fmt.Errorf("usage: read %s: %w", u.dir(), err)
	}

	models := make(map[string]*ModelUsage)
	daily := make(map[string]*DailyUsage)
	userRecords, assistantRecords := 0, 0
	turnsFromKinds := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			continue
		}
		path := filepath.Join(u.dir(), entry.Name())
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var record usageRecord
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			var data map[string]any
			if len(record.Data) > 0 && string(record.Data) != "null" {
				_ = json.Unmarshal(record.Data, &data)
			}
			fields := readUsageFields(data)
			kind := strings.ToLower(strings.TrimSpace(record.Kind))
			isTurn := turnKinds[kind]
			if isTurn {
				turnsFromKinds++
			}
			switch kind {
			case "user", "prompt":
				userRecords++
			case "assistant", "model", "reply":
				assistantRecords++
			}

			summary.PromptTokens += fields.prompt
			summary.CompletionTokens += fields.completion
			summary.CachedTokens += fields.cached
			summary.Requests += fields.requests
			summary.WarmRequests += fields.warm
			summary.Cost += fields.cost
			if fields.missed >= 0 {
				summary.MissedTokens += fields.missed
			}

			model := redactSecrets(fields.model)
			if model != "" {
				item := models[model]
				if item == nil {
					item = &ModelUsage{Name: model}
					models[model] = item
				}
				if isTurn {
					item.Turns++
				}
				item.PromptTokens += fields.prompt
				item.Cost += fields.cost
			}

			day := record.TS.UTC().Format("2006-01-02")
			if !record.TS.IsZero() {
				item := daily[day]
				if item == nil {
					item = &DailyUsage{Day: day}
					daily[day] = item
				}
				if isTurn {
					item.Turns++
				}
				item.Tokens += fields.prompt + fields.completion
				item.Cost += fields.cost
			}
		}
		_ = file.Close() // a torn transcript must not fail the whole report
	}

	// Older transcripts have no turn record: one user message is one turn.
	if turnsFromKinds == 0 {
		summary.Turns = userRecords
	} else {
		summary.Turns = turnsFromKinds
	}
	if summary.Requests == 0 {
		summary.Requests = assistantRecords
	}
	if summary.MissedTokens == 0 && summary.PromptTokens > summary.CachedTokens {
		summary.MissedTokens = summary.PromptTokens - summary.CachedTokens
	}
	if summary.PromptTokens > 0 {
		summary.CacheHitPercent = math.Round(float64(summary.CachedTokens) /
			float64(summary.PromptTokens) * 100)
	}
	for _, model := range models {
		summary.Models = append(summary.Models, *model)
	}
	for _, day := range daily {
		summary.Daily = append(summary.Daily, *day)
	}
	sort.Slice(summary.Models, func(i, j int) bool { return summary.Models[i].Name < summary.Models[j].Name })
	sort.Slice(summary.Daily, func(i, j int) bool { return summary.Daily[i].Day < summary.Daily[j].Day })
	return summary, nil
}

type usageFields struct {
	prompt, completion, cached, requests, warm, missed int
	cost                                               float64
	model                                              string
}

func readUsageFields(data map[string]any) usageFields {
	var out usageFields
	out.prompt = int(numberField(data, "promptTokens", "prompt_tokens", "inputTokens", "input_tokens"))
	out.completion = int(numberField(data, "completionTokens", "completion_tokens", "outputTokens", "output_tokens"))
	out.cached = int(numberField(data, "cachedTokens", "cached_tokens", "cacheReadTokens", "cache_read_tokens"))
	out.requests = int(numberField(data, "requests", "requestCount"))
	out.warm = int(numberField(data, "warmRequests", "warm_requests"))
	out.cost = numberField(data, "cost", "costUSD", "costUsd")
	out.model = textField(data, "model", "modelName", "model_name", "modelId", "model_id")
	if raw, ok := data["missedTokens"]; ok {
		out.missed = int(numberValue(raw))
	}
	return out
}

func numberField(data map[string]any, keys ...string) float64 {
	if data == nil {
		return 0
	}
	for _, key := range keys {
		if value, ok := data[key]; ok {
			return numberValue(value)
		}
	}
	return 0
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		number, _ := typed.Float64()
		return number
	case string:
		var number float64
		_, _ = fmt.Sscanf(typed, "%f", &number)
		return number
	}
	return 0
}

func textField(data map[string]any, keys ...string) string {
	if data == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := data[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
