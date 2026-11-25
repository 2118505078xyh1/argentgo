// Package automation 调度 agent 提示：基于 cron 或间隔的任务列表、持久 JSON store，以及通过注入的 executor 触发到期任务的 daemon。
// 列表、持久 JSON store，以及通过注入的 executor 触发到期任务的
// daemon。
//
// 一个任务是一个小的 JSON 文档：
//
//	{"id":"nightly","name":"nightly review",
//	 "schedule":{"kind":"cron","expr":"0 3 * * *"},
//	 "prompt":"review yesterday's commits","cwd":"/repo",
//	 "enabled":true,"missed":"run_once","timeout":"30m"}
//
// 这里的一切都是顺序、无依赖的 Go：daemon 从不自行启动
// 这里的一切都是顺序、无依赖的 Go：daemon 从不自行启动 agent，而是调用 Executor，因此 CLI（或测试）在之后再接入真实的
// agent 循环。
package automation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"example.com/argentgo/internal/security"
)

// Missed 策略决定 daemon 在错过时隙后醒来时该怎么做。
// 已安排的时隙已过去。
const (
	// MissedRunOnce 立即运行任务一次，然后恢复调度。
	MissedRunOnce = "run_once"
	// MissedSkip 丢弃错过的时隙并等待下一个。
	MissedSkip = "skip"
	// MissedRunLatest 是 opt-in 的补偿策略：多个时隙被错过时，
	// 按最近的一个已过期时隙触发一次，而非最早的那个。默认策略保持为 run_once。
	MissedRunLatest = "run_latest"
)

// Duration 是 JSON 友好的 time.Duration。它编码为时长字符串（"30m"），解码时接受时长字符串或秒数，使手写配置文件保持可读。
// （"30m"），解码时接受时长字符串或秒数，
// 使手写配置文件保持可读。
type Duration time.Duration

// Std 以 time.Duration 形式返回值。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String 按 time.Duration 的方式渲染时长。
func (d Duration) String() string { return time.Duration(d).String() }

// Or 返回该时长，非正时返回 def。
func (d Duration) Or(def time.Duration) time.Duration {
	if v := time.Duration(d); v > 0 {
		return v
	}
	return def
}

// MarshalJSON 将时长写为字符串。
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON 接受 "45s"、90（秒）、"" 和 null。
func (d *Duration) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		*d = 0
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			*d = 0
			return nil
		}
		v, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("automation: duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("automation: duration must be a string like \"30m\" or a number of seconds: %w", err)
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// Schedule 说明任务何时触发。
type Schedule struct {
	// Kind 是 "cron" 或 "interval"。空 kind 从 Expr 推断。
	Kind string `json:"kind"`
	// Expr 是五字段 cron 表达式或 Go 时长（"30m"、"1d"）。
	Expr string `json:"expr"`
}

// 规范化后的调度种类。
const (
	KindCron     = "cron"
	KindInterval = "interval"
)

// kind 从表达式形态解析空或拼错的 kind。
func (s Schedule) kind() string {
	switch strings.ToLower(strings.TrimSpace(s.Kind)) {
	case KindCron:
		return KindCron
	case KindInterval, "every":
		return KindInterval
	case "":
		if len(strings.Fields(strings.TrimSpace(s.Expr))) == 5 {
			return KindCron
		}
		return KindInterval
	}
	return strings.ToLower(strings.TrimSpace(s.Kind))
}

// Validate 报告调度是否可解析。
func (s Schedule) Validate() error {
	switch s.kind() {
	case KindCron:
		_, err := ParseCron(s.Expr)
		return err
	case KindInterval:
		_, err := ParseInterval(s.Expr)
		return err
	}
	return fmt.Errorf("automation: unknown schedule kind %q (use cron or interval)", s.Kind)
}

// Next 返回严格晚于 t 的第一个触发时间。零时间表示
// Next 返回严格晚于 t 的第一个触发时间。零时间表示表达式永不触发（例如 "0 0 31 2 *"）或无法解析。
func (s Schedule) Next(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	switch s.kind() {
	case KindCron:
		c, err := ParseCron(s.Expr)
		if err != nil {
			return time.Time{}
		}
		return c.Next(t)
	case KindInterval:
		// "1d" 表示"明天的同一时间"，因此推进日历而非
		// "1d" 表示"明天的同一时间"，因此推进日历而非加上固定 24h，后者会在 DST 过渡中漂移。
		if days, ok := parseDays(s.Expr); ok {
			return addCalendarDays(t, days)
		}
		d, err := ParseInterval(s.Expr)
		if err != nil {
			return time.Time{}
		}
		return t.Add(d)
	}
	return time.Time{}
}

// NextTrigger 根据错过补偿策略计算在观测时刻 now 的触发时隙与下一次到期时间。
// slot 是进程认领的（可能已错过的）时隙，now 是当前观测时间。返回的 trigger 为本次
// 应当运行的时隙（零时间表示本次不运行），next 为运行后 NextDue 应推进到的时间。
//
//   - MissedSkip：丢弃已过期的时隙，本次不运行，next 为严格晚于 now 的时隙。
//   - MissedRunOnce（默认）：运行认领的 slot；next 取 Next(slot)，若仍不晚于 now 则
//     回退到 Next(now)，避免长时间停机后每个 tick 都重复触发同一时隙。保持原有行为不变。
//   - MissedRunLatest：补偿到不晚于 now 的最近一个时隙并运行一次，next 为 Next(该时隙)。
func (s Schedule) NextTrigger(slot, now time.Time, policy string) (trigger, next time.Time) {
	switch normalizeMissed(policy) {
	case MissedSkip:
		return time.Time{}, s.Next(now)
	case MissedRunLatest:
		latest := s.latestSlotAtOrBefore(slot, now)
		if latest.IsZero() {
			// slot 晚于 now：没有错过的时隙，退化为跳到 now 之后的下一个时隙。
			return time.Time{}, s.Next(now)
		}
		return latest, s.Next(latest)
	default: // MissedRunOnce，原有行为不变。
		next = s.Next(slot)
		if !next.IsZero() && !next.After(now) {
			next = s.Next(now)
		}
		return slot, next
	}
}

// latestSlotAtOrBefore 返回从 slot 出发、不晚于 now 的最后一个时隙。
// slot 为空或晚于 now 时返回零时间。它只沿 Next 前进，因而复用 Next 已处理的
// DST 与边界逻辑；若 Next 停滞或不再前进则停在 latest，避免无限循环。
func (s Schedule) latestSlotAtOrBefore(slot, now time.Time) time.Time {
	if slot.IsZero() || slot.After(now) {
		return time.Time{}
	}
	latest := slot
	for {
		next := s.Next(latest)
		if next.IsZero() || !next.After(latest) || next.After(now) {
			return latest
		}
		latest = next
	}
}

// String 为日志和只读任务列表渲染调度。规范的
// String 为日志和只读任务列表渲染调度。规范的 "kind expr" 始终保留为前缀；有效的 cron 表达式额外在括号中给出易读摘要，使 CLI/TUI 列表
// 额外在括号中给出易读摘要，使 CLI/TUI 列表
// 无需单独查找即可显示调度的实际含义。
func (s Schedule) String() string {
	kind := s.kind()
	if kind == "" {
		kind = s.Kind
	}
	base := kind + " " + strings.TrimSpace(s.Expr)
	if kind == KindCron {
		if d, err := DescribeCron(s.Expr, time.Time{}, 0); err == nil && d.Summary != "" {
			return base + " (" + d.Summary + ")"
		}
	}
	return base
}

// Spec 是一个计划任务及其持久化的运行状态。
type Spec struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Schedule Schedule `json:"schedule"`
	Prompt   string   `json:"prompt"`
	Cwd      string   `json:"cwd,omitempty"`
	Enabled  bool     `json:"enabled"`
	// Missed 是 "run_once"（默认）或 "skip"。
	Missed  string   `json:"missed,omitempty"`
	Timeout Duration `json:"timeout,omitzero"`
	// NextDue 是 daemon 等待的时隙。它由 store 种入并在每次运行后推进，使任务不会因一个时隙触发两次。
	// 并在每次运行后推进，使任务不会因一个时隙触发两次。
	NextDue    time.Time  `json:"nextDue,omitzero"`
	LastRun    *time.Time `json:"lastRun,omitempty"`
	LastStatus string     `json:"lastStatus,omitempty"`
	LastLog    string     `json:"lastLog,omitempty"`
	// Warnings 报告在声明性内容中发现的不可见字符。
	// 它仅是元数据：从不被序列化且永不阻塞任务。
	Warnings []string `json:"-"`
}

// UnmarshalJSON 应用手写任务文件可能省略的默认值：
// UnmarshalJSON 应用手写任务文件可能省略的默认值：enabled=true、missed="run_once"，以及从名称派生的 id。
func (s *Spec) UnmarshalJSON(data []byte) error {
	type specJSON struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		Schedule   Schedule   `json:"schedule"`
		Prompt     string     `json:"prompt"`
		Cwd        string     `json:"cwd"`
		Enabled    *bool      `json:"enabled"`
		Missed     string     `json:"missed"`
		Timeout    Duration   `json:"timeout"`
		NextDue    time.Time  `json:"nextDue"`
		LastRun    *time.Time `json:"lastRun"`
		LastStatus string     `json:"lastStatus"`
		LastLog    string     `json:"lastLog"`
	}
	var raw specJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = Spec{
		ID:         raw.ID,
		Name:       raw.Name,
		Schedule:   raw.Schedule,
		Prompt:     raw.Prompt,
		Cwd:        raw.Cwd,
		Enabled:    raw.Enabled == nil || *raw.Enabled,
		Missed:     raw.Missed,
		Timeout:    raw.Timeout,
		NextDue:    raw.NextDue,
		LastRun:    raw.LastRun,
		LastStatus: raw.LastStatus,
		LastLog:    raw.LastLog,
	}
	if s.Missed == "" {
		s.Missed = MissedRunOnce
	}
	if s.ID == "" {
		s.ID = slug(s.Name)
	}
	s.Warnings = security.ScanArtifact(string(data)).Warnings()
	return nil
}

// normalizeMissed 规范化 missed 策略字符串；未知值一律视为 run_once，
// 使拼写错误永不静默丢弃工作。
func normalizeMissed(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case MissedSkip:
		return MissedSkip
	case MissedRunLatest:
		return MissedRunLatest
	default:
		return MissedRunOnce
	}
}

// MissedPolicy 规范化 missed 字段；未知值表现为 run_once，使拼写错误永不静默丢弃工作。
func (s Spec) MissedPolicy() string {
	return normalizeMissed(s.Missed)
}

// Validate 检查 daemon 依赖的字段。
func (s Spec) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("automation: a task needs an id")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("automation: task %q needs a name", s.ID)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("automation: task %q needs a prompt", s.ID)
	}
	if err := s.Schedule.Validate(); err != nil {
		return fmt.Errorf("automation: task %q: %w", s.ID, err)
	}
	switch strings.ToLower(strings.TrimSpace(s.Missed)) {
	case "", MissedRunOnce, MissedSkip, MissedRunLatest:
	default:
		return fmt.Errorf("automation: task %q has an unknown missed policy %q", s.ID, s.Missed)
	}
	if s.Timeout.Std() < 0 {
		return fmt.Errorf("automation: task %q has a negative timeout", s.ID)
	}
	return nil
}

// IsDue 报告任务是否应在 now 运行。
func (s Spec) IsDue(now time.Time) bool {
	if !s.Enabled || s.NextDue.IsZero() {
		return false
	}
	return !s.NextDue.After(now)
}

// TimeoutOr 返回运行超时，否则回退到 def。
func (s Spec) TimeoutOr(def time.Duration) time.Duration { return s.Timeout.Or(def) }

// slug 把任务名转换为文件系统安全的 id。
func slug(text string) string {
	var out strings.Builder
	for _, ch := range strings.ToLower(text) {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			out.WriteRune(ch)
		case ch == ' ' || ch == '-' || ch == '_' || ch == '\t' || ch == '\n':
			out.WriteByte('-')
		}
	}
	id := strings.Trim(out.String(), "-")
	if id == "" {
		return "task"
	}
	return id
}
