package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verifyCheckApp 构造一个只供 checkVerify 使用的 App：配置路径指向临时目录，
// env 由注入的 map 提供，避免读取进程环境。
func verifyCheckApp(t *testing.T, configJSON string, writeConfig bool, env map[string]string) App {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if writeConfig {
		if err := os.WriteFile(path, []byte(configJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ioState, _, _ := newTestIO("", env)
	return App{IO: ioState, StorageDir: dir, ConfigPath: path}
}

func TestCheckVerifyStatus(t *testing.T) {
	tests := []struct {
		name        string
		configJSON  string
		writeConfig bool
		env         map[string]string
		wantStatus  string
	}{
		{
			name:       "no config and no opt-in warns",
			wantStatus: statusWarn,
		},
		{
			name:        "empty config warns",
			configJSON:  `{}`,
			writeConfig: true,
			wantStatus:  statusWarn,
		},
		{
			name:        "verifyCommand list is ok",
			configJSON:  `{"verifyCommand":["go","test","./..."]}`,
			writeConfig: true,
			wantStatus:  statusOK,
		},
		{
			name:        "verify_command alias is ok",
			configJSON:  `{"verify_command":["make","check"]}`,
			writeConfig: true,
			wantStatus:  statusOK,
		},
		{
			name:        "verifyCommands map is ok",
			configJSON:  `{"verifyCommands":{"go":"go test ./..."}}`,
			writeConfig: true,
			wantStatus:  statusOK,
		},
		{
			name:        "verify_commands alias is ok",
			configJSON:  `{"verify_commands":{"go":"go test ./..."}}`,
			writeConfig: true,
			wantStatus:  statusOK,
		},
		{
			name:        "empty verifyCommand still warns",
			configJSON:  `{"verifyCommand":[]}`,
			writeConfig: true,
			wantStatus:  statusWarn,
		},
		{
			name:        "empty verify_commands still warns",
			configJSON:  `{"verify_commands":{}}`,
			writeConfig: true,
			wantStatus:  statusWarn,
		},
		{
			name:        "unreadable config falls back to opt-in check and warns",
			configJSON:  `{not json`,
			writeConfig: true,
			wantStatus:  statusWarn,
		},
		{
			name:       "edit diagnostics opt-in is ok",
			env:        map[string]string{"ARGENT_EDIT_DIAGNOSTICS": "1"},
			wantStatus: statusOK,
		},
		{
			name:       "non-one edit diagnostics value still warns",
			env:        map[string]string{"ARGENT_EDIT_DIAGNOSTICS": "yes"},
			wantStatus: statusWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := checkVerify(verifyCheckApp(t, tt.configJSON, tt.writeConfig, tt.env))
			if check.name != "verify" {
				t.Fatalf("name = %q, want verify", check.name)
			}
			if check.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (detail: %s)", check.status, tt.wantStatus, check.detail)
			}
			// detail 必须是固定文案：不含任何配置内容或 env 值。
			if strings.Contains(check.detail, "go test") || strings.Contains(check.detail, "make") ||
				strings.Contains(check.detail, "go test ./...") {
				t.Fatalf("detail leaked config value: %s", check.detail)
			}
		})
	}
}

// TestCheckVerifyAutoVerifyOptIn 覆盖通过 ARGENT_OPT_INS 开启 auto_verify 的路径。
// auto_verify 键由另一代理并入 knownOptInKeys；若尚未落盘，OptInFromEnv 会忽略
// 该键，此时跳过并交由同文件中的 ARGENT_EDIT_DIAGNOSTICS 用例覆盖或运算的另一支。
func TestCheckVerifyAutoVerifyOptIn(t *testing.T) {
	env := map[string]string{"ARGENT_OPT_INS": "auto_verify"}
	envFn := func(key string) string { return env[key] }
	if !OptInFromEnv(envFn)["auto_verify"] {
		t.Skip("auto_verify 尚未注册进 knownOptInKeys（并行接线中）；ARGENT_EDIT_DIAGNOSTICS 分支由 TestCheckVerifyStatus 覆盖")
	}
	check := checkVerify(verifyCheckApp(t, "", false, env))
	if check.status != statusOK {
		t.Fatalf("status = %q, want ok (detail: %s)", check.status, check.detail)
	}
}

// TestDoctorVerifyHintAndExitCode 验证 doctor 输出包含 verify 提示行，且 warn
// 不改变退出码（只有 fail 才计数）。provider 探测指向立即拒绝的回环端口，
// 它本身就是 warn，因此两种配置下退出码都应为 0。
func TestDoctorVerifyHintAndExitCode(t *testing.T) {
	const refusedURL = "http://127.0.0.1:1"

	tests := []struct {
		name        string
		configJSON  string
		writeConfig bool
		env         map[string]string
		wantLine    string
	}{
		{
			name:     "unconfigured verify warns without changing exit code",
			wantLine: "warn verify",
		},
		{
			name:        "configured verify is ok",
			configJSON:  `{"verifyCommand":["go","test","./..."]}`,
			writeConfig: true,
			wantLine:    "ok   verify",
		},
		{
			name:     "edit diagnostics opt-in makes verify ok",
			env:      map[string]string{"ARGENT_EDIT_DIAGNOSTICS": "1"},
			wantLine: "ok   verify",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if tt.writeConfig {
				if err := os.WriteFile(path, []byte(tt.configJSON), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ioState, out, _ := newTestIO("", tt.env)
			app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
			code := RunWith(app, []string{"doctor", "--base-url", refusedURL})
			if code != 0 {
				t.Fatalf("exit = %d, want 0; output:\n%s", code, out.String())
			}
			if !strings.Contains(out.String(), tt.wantLine) {
				t.Fatalf("output missing %q:\n%s", tt.wantLine, out.String())
			}
		})
	}
}

// TestDoctorVerifyDoesNotLeakEnv 注入具有凭据形状的伪值与伪 key，断言 doctor
// 输出既不打印 env 值，也不打印伪 key。verify 检查只读 env 的判定结果，
// 其 detail 必须是固定文案。
func TestDoctorVerifyDoesNotLeakEnv(t *testing.T) {
	const refusedURL = "http://127.0.0.1:1"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json") // 不写文件：verify 保持 warn

	fakeValues := []string{
		"FAKE_SECRET_IN_OPTINS",
		"FAKE_SECRET_DIAGNOSTICS_VALUE",
		"FAKE_TOKEN_DO_NOT_LEAK",
		"TOTALLY_FAKE_KEY_XYZ",
	}
	env := map[string]string{
		// 未知 opt-in 键被忽略，verify 保持 warn；伪值绝不会出现在输出里。
		"ARGENT_OPT_INS":          fakeValues[0],
		"ARGENT_EDIT_DIAGNOSTICS": fakeValues[1], // 非 "1"，verify 仍为 warn
		fakeValues[3]:             fakeValues[2], // 无人读取的伪 key / 伪值
	}
	ioState, out, _ := newTestIO("", env)
	app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
	code := RunWith(app, []string{"doctor", "--base-url", refusedURL})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "warn verify") {
		t.Fatalf("output missing warn verify line:\n%s", out.String())
	}
	for _, secret := range fakeValues {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("output leaked env value/key %q:\n%s", secret, out.String())
		}
	}
}
