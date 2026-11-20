package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---- current_time ----

// currentTimeNow 是一个变量，便于测试固定时钟。
var currentTimeNow = time.Now

type currentTimeTool struct{}

func (currentTimeTool) Name() string { return "current_time" }
func (currentTimeTool) Description() string {
	return "Return the current UTC and zone time (rfc3339|date|time|unix); timezone accepts UTC, Local or an IANA name."
}
func (currentTimeTool) Mutating() bool { return false }
func (currentTimeTool) Schema() string {
	return `{"type":"object","properties":{"timezone":{"type":"string","description":"UTC, Local, or an IANA name such as Asia/Shanghai; defaults to Local"},"format":{"type":"string","enum":["rfc3339","date","time","unix"],"description":"output format; defaults to rfc3339"}}}`
}

func (currentTimeTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		Timezone string `json:"timezone"`
		Format   string `json:"format"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("current_time: bad arguments: %w", err)
	}

	format := strings.ToLower(strings.TrimSpace(args.Format))
	if format == "" {
		format = "rfc3339"
	}
	switch format {
	case "rfc3339", "date", "time", "unix":
	default:
		return "", fmt.Errorf("current_time: invalid format %q (want rfc3339, date, time or unix)", args.Format)
	}

	zone := strings.TrimSpace(args.Timezone)
	if zone == "" {
		zone = "Local"
	}
	var loc *time.Location
	switch zone {
	case "UTC":
		loc = time.UTC
	case "Local":
		loc = time.Local
	default:
		loaded, err := time.LoadLocation(zone)
		if err != nil {
			return "", fmt.Errorf("current_time: unknown timezone %q: %w", zone, err)
		}
		loc = loaded
	}

	now := currentTimeNow()
	return fmt.Sprintf("utc: %s\n%s: %s", formatTime(now.UTC(), format), zone, formatTime(now.In(loc), format)), nil
}

// formatTime 按请求的、对模型友好的形式渲染某个时刻。
func formatTime(t time.Time, format string) string {
	switch format {
	case "date":
		return t.Format("2006-01-02")
	case "time":
		return t.Format("15:04:05")
	case "unix":
		return strconv.FormatInt(t.Unix(), 10)
	default:
		return t.Format(time.RFC3339)
	}
}
