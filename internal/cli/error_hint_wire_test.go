package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/i18n"
)

// constEnv 返回一个只认 cliHintsEnvVar 的注入式 env 桩。
func constEnv(hints string) func(string) string {
	return func(key string) string {
		if key == cliHintsEnvVar {
			return hints
		}
		return ""
	}
}

// envWith 返回一个同时回应两个门控变量的注入式 env 桩，用于覆盖两个开关的
// 组合场景：hints 对应 ARGENT_CLI_HINTS，optIns 对应 ARGENT_OPT_INS。
func envWith(hints, optIns string) func(string) string {
	return func(key string) string {
		switch key {
		case cliHintsEnvVar:
			return hints
		case optInEnvVar:
			return optIns
		}
		return ""
	}
}

// TestCliHintsEnabled 覆盖默认开启与 =0 关闭的全部取值与组合：nil、空值、
// 空白、真值以及任何 opt-in 组合默认都开启；只有 ARGENT_CLI_HINTS 修剪后为
// "0" 才关闭（即便 opt-in 列表里含 cli_hints 也不例外）。
func TestCliHintsEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{name: "nil env", env: nil, want: true},
		{name: "empty", env: constEnv(""), want: true},
		{name: "zero", env: constEnv("0"), want: false},
		{name: "zero with surrounding spaces", env: constEnv("  0  "), want: false},
		{name: "blank", env: constEnv("   "), want: true},
		{name: "one", env: constEnv("1"), want: true},
		{name: "one with surrounding spaces", env: constEnv("  1  "), want: true},
		{name: "both unset", env: envWith("", ""), want: true},
		{name: "cli hints only", env: envWith("1", ""), want: true},
		{name: "opt-in cli_hints only", env: envWith("", "cli_hints"), want: true},
		{name: "opt-in cli_hints among others", env: envWith("", "  apply_patch , cli_hints  "), want: true},
		{name: "opt-in unrelated key only", env: envWith("", "refusal_reason"), want: true},
		{name: "cli hints zero plus unrelated opt-in", env: envWith("0", "refusal_reason"), want: false},
		{name: "cli hints zero plus cli_hints opt-in", env: envWith("0", "cli_hints"), want: false},
		{name: "both set", env: envWith("1", "cli_hints"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cliHintsEnabled(tc.env); got != tc.want {
				t.Fatalf("cliHintsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWriteActionableErrorEnvGateOffMatchesBaseline 验证门控关闭（=0）时，
// writeActionableErrorEnv 的输出与 writeActionableError 逐字节相同。
func TestWriteActionableErrorEnvGateOffMatchesBaseline(t *testing.T) {
	messages := []string{
		"provider returned 401 Unauthorized: invalid api key",
		"open config.json: no such file or directory",
		"tls: handshake failed",
		"cannot prompt in non-interactive mode",
		"something unexpected happened",
	}
	envs := []struct {
		name string
		env  func(string) string
	}{
		{name: "zero", env: constEnv("0")},
		{name: "zero with surrounding spaces", env: constEnv("  0  ")},
		{name: "zero plus unrelated opt-in", env: envWith("0", "refusal_reason")},
		{name: "zero plus cli_hints opt-in", env: envWith("0", "cli_hints")},
	}
	for _, e := range envs {
		for _, msg := range messages {
			t.Run(e.name+"/"+msg, func(t *testing.T) {
				baseline := &bytes.Buffer{}
				writeActionableError(baseline, errors.New(msg))
				got := &bytes.Buffer{}
				writeActionableErrorEnv(got, errors.New(msg), e.env, "zh-CN")
				if got.String() != baseline.String() {
					t.Fatalf("gate off changed bytes:\nbaseline = %q\ngot      = %q", baseline.String(), got.String())
				}
			})
		}
	}
}

// TestWriteActionableErrorEnvAppendsLocalizedHint 验证门控开启时，既有两行
// 保持不变，且按 locale 追加与错误分支对应的本地化建议。
func TestWriteActionableErrorEnvAppendsLocalizedHint(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		key  string
	}{
		{"credentials", "provider returned 401 Unauthorized: invalid api key", i18n.ErrorHintCredentials},
		{"file", "open config.json: no such file or directory", i18n.ErrorHintFile},
		{"tls", "tls: handshake failed", i18n.ErrorHintTLS},
		{"interactive", "cannot prompt in non-interactive mode", i18n.ErrorHintInteractive},
	}
	for _, tc := range cases {
		for _, locale := range []string{"zh-CN", "en-US", ""} {
			t.Run(tc.name+"/"+locale, func(t *testing.T) {
				baseline := &bytes.Buffer{}
				writeActionableError(baseline, errors.New(tc.msg))
				got := &bytes.Buffer{}
				writeActionableErrorEnv(got, errors.New(tc.msg), constEnv("1"), locale)

				want := baseline.String() + i18n.ErrorHintMessage(locale, tc.key) + "\n"
				if got.String() != want {
					t.Fatalf("stderr =\n%q\nwant   =\n%q", got.String(), want)
				}
				if !strings.HasPrefix(got.String(), baseline.String()) {
					t.Fatalf("existing lines changed: baseline %q, got %q", baseline.String(), got.String())
				}
				// 中文场景追加行必须与 cliErrorHint 的原始中文逐字节一致。
				if locale == "zh-CN" {
					if wantZh := baseline.String() + cliErrorHint(tc.msg) + "\n"; got.String() != wantZh {
						t.Fatalf("zh hint drifted from cliErrorHint: got %q, want %q", got.String(), wantZh)
					}
				}
			})
		}
	}
}

// TestWriteActionableErrorEnvDefaultAppendsHint 验证策略变更后默认即追加建议：
// 不设任何门控变量（nil/空/任意非 "0" 值）时也会输出本地化建议，且既有两行不变。
func TestWriteActionableErrorEnvDefaultAppendsHint(t *testing.T) {
	const msg = "provider returned 401 Unauthorized: invalid api key"
	const key = i18n.ErrorHintCredentials
	envs := []struct {
		name string
		env  func(string) string
	}{
		{name: "nil", env: nil},
		{name: "empty", env: constEnv("")},
		{name: "blank", env: constEnv("   ")},
		{name: "both env vars unset", env: envWith("", "")},
	}
	for _, e := range envs {
		t.Run(e.name, func(t *testing.T) {
			baseline := &bytes.Buffer{}
			writeActionableError(baseline, errors.New(msg))
			got := &bytes.Buffer{}
			writeActionableErrorEnv(got, errors.New(msg), e.env, "zh-CN")

			want := baseline.String() + i18n.ErrorHintMessage("zh-CN", key) + "\n"
			if got.String() != want {
				t.Fatalf("stderr =\n%q\nwant   =\n%q", got.String(), want)
			}
			if !strings.HasPrefix(got.String(), baseline.String()) {
				t.Fatalf("existing lines changed: baseline %q, got %q", baseline.String(), got.String())
			}
		})
	}
}

// TestWriteActionableErrorEnvOptInAppendsHint 验证仅设置 ARGENT_OPT_INS=cli_hints
// 时同样会追加建议，且既有两行保持不变、按 locale 本地化。
func TestWriteActionableErrorEnvOptInAppendsHint(t *testing.T) {
	const msg = "provider returned 401 Unauthorized: invalid api key"
	const key = i18n.ErrorHintCredentials
	for _, locale := range []string{"zh-CN", "en-US", ""} {
		t.Run(locale, func(t *testing.T) {
			baseline := &bytes.Buffer{}
			writeActionableError(baseline, errors.New(msg))
			got := &bytes.Buffer{}
			writeActionableErrorEnv(got, errors.New(msg), envWith("", "cli_hints"), locale)

			want := baseline.String() + i18n.ErrorHintMessage(locale, key) + "\n"
			if got.String() != want {
				t.Fatalf("stderr =\n%q\nwant   =\n%q", got.String(), want)
			}
			if !strings.HasPrefix(got.String(), baseline.String()) {
				t.Fatalf("existing lines changed: baseline %q, got %q", baseline.String(), got.String())
			}
		})
	}
}

// TestCliHintMappingMatchesRules 锁定映射表与 cli_error_hint.go 四条规则、
// i18n 中文文案之间的一致性，防止任一侧改文案后接线静默失效。
func TestCliHintMappingMatchesRules(t *testing.T) {
	if len(cliHintToI18nKey) != len(cliErrorHintRules) {
		t.Fatalf("mapping has %d entries, rules have %d", len(cliHintToI18nKey), len(cliErrorHintRules))
	}
	seen := make(map[string]bool, len(cliHintI18nKeys))
	for _, key := range cliHintI18nKeys {
		seen[key] = true
	}
	for _, rule := range cliErrorHintRules {
		key, ok := cliHintToI18nKey[rule.hint]
		if !ok {
			t.Fatalf("rule hint %q has no i18n key", rule.hint)
		}
		if !seen[key] {
			t.Fatalf("rule hint %q mapped to unexpected key %q", rule.hint, key)
		}
		if got := i18n.ErrorHintMessage("zh-CN", key); got != rule.hint {
			t.Fatalf("i18n zh text drifted: key %q = %q, rule hint = %q", key, got, rule.hint)
		}
	}
}

// TestWriteActionableErrorEnvNilError 确认 nil 错误不产生任何输出。
func TestWriteActionableErrorEnvNilError(t *testing.T) {
	buf := &bytes.Buffer{}
	writeActionableErrorEnv(buf, nil, constEnv("1"), "zh-CN")
	if buf.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", buf.String())
	}
}

// TestRunPromptHintWiring 验证 cli.go 的错误分支确实接到了 writeActionableErrorEnv：
// 门控开启时按 locale 追加建议，关闭时不追加，成功路径 stderr 始终为空。
func TestRunPromptHintWiring(t *testing.T) {
	const msg = "provider returned 401 Unauthorized: invalid api key"
	factory := func(Options) (Runner, error) {
		return &stubRunner{fail: errors.New(msg)}, nil
	}
	for _, locale := range []string{"zh-CN", "en-US"} {
		t.Run(locale+"/on", func(t *testing.T) {
			stdio, _, errOut := newTestIO("", map[string]string{cliHintsEnvVar: "1"})
			code := RunWith(App{IO: stdio, NewRunner: factory}, []string{"--locale", locale, "hi"})
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr = %q", code, errOut.String())
			}
			if want := i18n.ErrorHintMessage(locale, i18n.ErrorHintCredentials); !strings.Contains(errOut.String(), want) {
				t.Fatalf("localized hint %q missing from stderr %q", want, errOut.String())
			}
		})
		t.Run(locale+"/off", func(t *testing.T) {
			stdio, _, errOut := newTestIO("", map[string]string{cliHintsEnvVar: "0"})
			code := RunWith(App{IO: stdio, NewRunner: factory}, []string{"--locale", locale, "hi"})
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr = %q", code, errOut.String())
			}
			for _, key := range cliHintI18nKeys {
				if strings.Contains(errOut.String(), i18n.ErrorHintMessage(locale, key)) {
					t.Fatalf("hint %q present with gate off: %q", key, errOut.String())
				}
			}
		})
	}

	t.Run("success", func(t *testing.T) {
		okFactory := func(Options) (Runner, error) {
			return &stubRunner{reply: "answer"}, nil
		}
		stdio, _, errOut := newTestIO("", map[string]string{cliHintsEnvVar: "1"})
		code := RunWith(App{IO: stdio, NewRunner: okFactory}, []string{"--locale", "zh-CN", "hi"})
		if code != 0 {
			t.Fatalf("exit = %d, want 0; stderr = %q", code, errOut.String())
		}
		if errOut.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", errOut.String())
		}
	})
}

// TestRunPromptHintWiringOptIn 验证默认开启以及 opt-in 列表含 cli_hints 时，
// cli.go 的错误分支接到建议输出；ARGENT_CLI_HINTS=0 时保持关闭。
func TestRunPromptHintWiringOptIn(t *testing.T) {
	const msg = "provider returned 401 Unauthorized: invalid api key"
	factory := func(Options) (Runner, error) {
		return &stubRunner{fail: errors.New(msg)}, nil
	}
	t.Run("cli_hints enabled", func(t *testing.T) {
		stdio, _, errOut := newTestIO("", map[string]string{optInEnvVar: "cli_hints"})
		code := RunWith(App{IO: stdio, NewRunner: factory}, []string{"--locale", "zh-CN", "hi"})
		if code != 1 {
			t.Fatalf("exit = %d, want 1; stderr = %q", code, errOut.String())
		}
		if want := i18n.ErrorHintMessage("zh-CN", i18n.ErrorHintCredentials); !strings.Contains(errOut.String(), want) {
			t.Fatalf("localized hint %q missing from stderr %q", want, errOut.String())
		}
	})
	t.Run("cli hints disabled via zero", func(t *testing.T) {
		stdio, _, errOut := newTestIO("", map[string]string{cliHintsEnvVar: "0", optInEnvVar: "cli_hints"})
		code := RunWith(App{IO: stdio, NewRunner: factory}, []string{"--locale", "zh-CN", "hi"})
		if code != 1 {
			t.Fatalf("exit = %d, want 1; stderr = %q", code, errOut.String())
		}
		for _, key := range cliHintI18nKeys {
			if strings.Contains(errOut.String(), i18n.ErrorHintMessage("zh-CN", key)) {
				t.Fatalf("hint %q present with cli hints disabled: %q", key, errOut.String())
			}
		}
	})
}
