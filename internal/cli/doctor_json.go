package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/2118505078xyh1/argentgo/internal/i18n"
)

// doctorJSONCheck 是 doctor --json 中单条检查的稳定投影。
// Detail 已经过 redactDoctorDetail 清洗，可安全粘贴到公开 issue。
type doctorJSONCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// doctorJSONProbe 是环境探测的稳定投影。有意只保留 name/ok/detail：
// ProbeResult.Value 保存的是原始观测值（TERM、shell 名、git 可执行文件路径、
// 已设置的关键环境变量名），把它们带进 JSON 会泄漏 env 值与路径，
// 因此这里不包含 Value 字段。Detail 均为固定文案，不含任何外部输入。
type doctorJSONProbe struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// doctorJSONReport 是 doctor --json 的顶层结构。字段顺序固定，
// 让脚本无需依赖文本输出的行格式即可稳定消费。
type doctorJSONReport struct {
	Checks      []doctorJSONCheck `json:"checks"`
	Failures    int               `json:"failures"`
	Environment []doctorJSONProbe `json:"environment"`
	Issues      []doctorIssue     `json:"issues"`
}

// runDoctorJSON 输出机器可读的 doctor 报告。
//
// 检查项由调用方计算并传入（与文本分支完全共享），因此两次运行的检查结果
// 一致。每条 detail 都经 redactDoctorDetail 脱敏；失败计数与退出码语义也与
// 文本分支一致（failures>0 → 1）。ctx 与 opts 保留在签名中，使两个分支的
// 调用形态对称，并便于后续扩展。
func runDoctorJSON(app App, ctx context.Context, opts doctorOptions, checks ...doctorCheck) int {
	jsonChecks := make([]doctorJSONCheck, 0, len(checks))
	failures := 0
	for _, check := range checks {
		if check.status == statusFail {
			failures++
		}
		jsonChecks = append(jsonChecks, doctorJSONCheck{
			Name:   check.name,
			Status: check.status,
			Detail: redactDoctorDetail(check.detail),
		})
	}

	// baseURL、配置注入、会话健康与 i18n 审计告警与文本分支共享同一份检测
	// 逻辑；它们都是 warn 级别，只作为独立字段输出，不计入 failures、也不
	// 改变退出码。i18n 审计通过时无 issue，golden 输出逐字节不变。
	issues := doctorBaseURLIssues(app)
	issues = append(issues, doctorConfigInjectionIssues(app)...)
	issues = append(issues, doctorSessionHealthIssues(app)...)
	issues = append(issues, doctorI18nIssues(i18n.FullAudit())...)
	jsonIssues := make([]doctorIssue, 0, len(issues))
	for _, issue := range issues {
		jsonIssues = append(jsonIssues, doctorIssue{
			Name:    issue.Name,
			Level:   issue.Level,
			Message: redactDoctorDetail(issue.Message),
		})
	}

	// 环境探测仅提供诊断上下文，和文本分支一样从不改变退出码。
	probes := probeEnvironment(app.env, exec.LookPath)
	jsonProbes := make([]doctorJSONProbe, 0, len(probes))
	for _, probe := range probes {
		jsonProbes = append(jsonProbes, doctorJSONProbe{
			Name:   probe.Name,
			OK:     probe.OK,
			Detail: probe.Detail,
		})
	}

	report := doctorJSONReport{
		Checks:      jsonChecks,
		Failures:    failures,
		Environment: jsonProbes,
		Issues:      jsonIssues,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		// 所有字段都是标量，正常不会走到这里；防御性地向 stderr 报错，
		// 保持 stdout 为干净的可解析流。
		fmt.Fprintln(app.IO.Err, "argentgo: doctor: encode JSON:", err)
		return 1
	}
	fmt.Fprintln(app.IO.Out, string(data))
	if failures > 0 {
		return 1
	}
	return 0
}
