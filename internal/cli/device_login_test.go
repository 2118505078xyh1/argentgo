package cli

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/auth"
)

// stubDeviceFlow 替换设备流程的两个接缝，并在测试结束时恢复原值。
func stubDeviceFlow(t *testing.T, start func(context.Context, auth.Config) (auth.DeviceAuth, error), poll func(context.Context, auth.Config, auth.DeviceAuth) (auth.Token, error)) {
	t.Helper()
	oldStart, oldPoll := deviceStart, devicePoll
	deviceStart, devicePoll = start, poll
	t.Cleanup(func() { deviceStart, devicePoll = oldStart, oldPoll })
}

// unexpectedDeviceStart 构造一个一旦被调用就令测试失败的设备启动桩。
func unexpectedDeviceStart(t *testing.T) func(context.Context, auth.Config) (auth.DeviceAuth, error) {
	t.Helper()
	return func(context.Context, auth.Config) (auth.DeviceAuth, error) {
		t.Error("deviceStart must not be called")
		return auth.DeviceAuth{}, errors.New("unexpected deviceStart")
	}
}

// unexpectedDevicePoll 构造一个一旦被调用就令测试失败的设备轮询桩。
func unexpectedDevicePoll(t *testing.T) func(context.Context, auth.Config, auth.DeviceAuth) (auth.Token, error) {
	t.Helper()
	return func(context.Context, auth.Config, auth.DeviceAuth) (auth.Token, error) {
		t.Error("devicePoll must not be called")
		return auth.Token{}, errors.New("unexpected devicePoll")
	}
}

func TestDeviceLoginPrintsCodeAndStoresToken(t *testing.T) {
	dir := t.TempDir()
	ioState, out, errOut := newTestIO("", nil)
	preset, _ := auth.Preset("github-copilot")

	var startCfg, pollCfg auth.Config
	calls := 0
	stubDeviceFlow(t,
		func(_ context.Context, cfg auth.Config) (auth.DeviceAuth, error) {
			calls++
			startCfg = cfg
			return auth.DeviceAuth{
				DeviceCode:      "secret-device-code",
				UserCode:        "ABCD-EFGH",
				VerificationURI: "https://github.com/login/device",
				Interval:        5,
				ExpiresIn:       900,
			}, nil
		},
		func(_ context.Context, cfg auth.Config, started auth.DeviceAuth) (auth.Token, error) {
			calls++
			pollCfg = cfg
			if started.DeviceCode != "secret-device-code" {
				t.Errorf("poll got device code %q", started.DeviceCode)
			}
			return auth.Token{
				Provider: "github-copilot",
				Kind:     auth.KindOAuth,
				Access:   "tok-secret",
				Refresh:  "ref-secret",
			}, nil
		},
	)

	code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"login", "github-copilot", "--device"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if calls != 2 {
		t.Fatalf("device flow calls = %d, want 2 (start + poll)", calls)
	}
	if !reflect.DeepEqual(startCfg, preset.Config) || !reflect.DeepEqual(pollCfg, preset.Config) {
		t.Fatalf("configs not taken from preset: start %+v poll %+v", startCfg, pollCfg)
	}

	text := out.String()
	for _, want := range []string{"ABCD-EFGH", "https://github.com/login/device", "github-copilot"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q missing %q", text, want)
		}
	}
	// 秘密绝不能出现在任何输出流里。
	for _, secret := range []string{"secret-device-code", "tok-secret", "ref-secret"} {
		if strings.Contains(text, secret) || strings.Contains(errOut.String(), secret) {
			t.Fatalf("secret %q echoed: out %q err %q", secret, text, errOut.String())
		}
	}

	store, err := auth.Open(filepath.Join(dir, "auth"))
	if err != nil {
		t.Fatal(err)
	}
	token, ok := store.Get("github-copilot")
	if !ok || token.Access != "tok-secret" || token.Refresh != "ref-secret" || token.Kind != auth.KindOAuth {
		t.Fatalf("stored token = %+v, present = %v", token, ok)
	}
}

func TestDeviceLoginRejectsNonDeviceProvider(t *testing.T) {
	stubDeviceFlow(t, unexpectedDeviceStart(t), unexpectedDevicePoll(t))
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login", "openai", "--device"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "does not support device login") {
		t.Fatalf("unexpected error: %q", errOut.String())
	}
}

func TestDeviceLoginRequiresProvider(t *testing.T) {
	stubDeviceFlow(t, unexpectedDeviceStart(t), unexpectedDevicePoll(t))
	ioState, _, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login", "--device"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestDeviceLoginStartErrorStopsBeforePolling(t *testing.T) {
	stubDeviceFlow(t,
		func(context.Context, auth.Config) (auth.DeviceAuth, error) {
			return auth.DeviceAuth{}, errors.New("device endpoint down")
		},
		unexpectedDevicePoll(t),
	)
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login", "github-copilot", "--device"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "device endpoint down") {
		t.Fatalf("unexpected error: %q", errOut.String())
	}
}

func TestDeviceLoginDeniedOrExpiredDoesNotStore(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "denied", err: auth.ErrDeviceDenied},
		{name: "expired", err: auth.ErrDeviceExpired},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			ioState, _, errOut := newTestIO("", nil)
			stubDeviceFlow(t,
				func(context.Context, auth.Config) (auth.DeviceAuth, error) {
					return auth.DeviceAuth{DeviceCode: "dev", UserCode: "U", VerificationURI: "https://x", Interval: 1, ExpiresIn: 60}, nil
				},
				func(context.Context, auth.Config, auth.DeviceAuth) (auth.Token, error) {
					return auth.Token{}, test.err
				},
			)
			code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"login", "github-copilot", "--device"})
			if code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if !strings.Contains(errOut.String(), test.err.Error()) {
				t.Fatalf("error %q missing from %q", test.err, errOut.String())
			}
			store, err := auth.Open(filepath.Join(dir, "auth"))
			if err != nil {
				t.Fatal(err)
			}
			if tokens, err := store.List(); err != nil || len(tokens) != 0 {
				t.Fatalf("tokens = %+v, err = %v; want no stored token", tokens, err)
			}
		})
	}
}

func TestDeviceLoginAppliesTimeoutToPolling(t *testing.T) {
	stubDeviceFlow(t,
		func(ctx context.Context, _ auth.Config) (auth.DeviceAuth, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("device start context has no deadline")
			}
			return auth.DeviceAuth{DeviceCode: "dev", UserCode: "U", VerificationURI: "https://x", Interval: 1, ExpiresIn: 60}, nil
		},
		func(ctx context.Context, _ auth.Config, _ auth.DeviceAuth) (auth.Token, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("device poll context has no deadline")
			}
			return auth.Token{Provider: "github-copilot", Kind: auth.KindOAuth, Access: "tok"}, nil
		},
	)
	ioState, _, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login", "github-copilot", "--device"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
}

func TestDeviceLoginPreservesCJK(t *testing.T) {
	const userCode = "中文-代码-123"
	const uri = "https://example.com/验证?提示=请输入"
	stubDeviceFlow(t,
		func(context.Context, auth.Config) (auth.DeviceAuth, error) {
			return auth.DeviceAuth{DeviceCode: "dev-CJK", UserCode: userCode, VerificationURI: uri, Interval: 1, ExpiresIn: 60}, nil
		},
		func(context.Context, auth.Config, auth.DeviceAuth) (auth.Token, error) {
			return auth.Token{Provider: "github-copilot", Kind: auth.KindOAuth, Access: "tok"}, nil
		},
	)
	ioState, out, _ := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login", "github-copilot", "--device"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{userCode, uri} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q lost CJK text %q", out.String(), want)
		}
	}
}

func TestDeviceLoginHelpUsesDeviceUsage(t *testing.T) {
	stubDeviceFlow(t, unexpectedDeviceStart(t), unexpectedDevicePoll(t))
	ioState, out, _ := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login", "github-copilot", "--device", "--help"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "--device") || !strings.Contains(out.String(), "device-code") {
		t.Fatalf("device usage missing: %q", out.String())
	}
}

// TestLoginHelpUnchangedByDeviceFlag 守护默认行为：不带 --device 时帮助文本逐字节不变。
func TestLoginHelpUnchangedByDeviceFlag(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	RunWith(App{IO: ioState, StorageDir: t.TempDir()}, []string{"login", "--help"})
	const want = `usage: argentgo login <provider> [--api-key <key>] [--base-url <url>]

The key is read from --api-key, <PROVIDER>_API_KEY, ARGENT_API_KEY or stdin and
written to the config file. It is never echoed.
`
	if out.String() != want {
		t.Fatalf("login --help changed:\ngot  %q\nwant %q", out.String(), want)
	}
}

func TestLoginWithoutDeviceFlagKeepsAPIKeyFlow(t *testing.T) {
	stubDeviceFlow(t, unexpectedDeviceStart(t), unexpectedDevicePoll(t))
	dir := t.TempDir()
	ioState, _, _ := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: dir}, []string{"login", "openai", "--api-key", "sk-secret"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	// 原有路径仍把 key 写入配置文件，且从不触碰设备凭证库。
	cfg, err := loadConfigFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := cfg["providers"].(map[string]any)
	entry, _ := providers["openai"].(map[string]any)
	if entry["apiKey"] != "sk-secret" {
		t.Fatalf("config = %#v", cfg)
	}
	store, err := auth.Open(filepath.Join(dir, "auth"))
	if err != nil {
		t.Fatal(err)
	}
	if tokens, err := store.List(); err != nil || len(tokens) != 0 {
		t.Fatalf("device flow wrote tokens without --device: %+v, %v", tokens, err)
	}
}
