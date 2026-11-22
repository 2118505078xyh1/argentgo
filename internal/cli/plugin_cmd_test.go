package cli

import (
	"bytes"
	"testing"
)

func newPluginApp(t *testing.T) (App, string) {
	t.Helper()
	home := t.TempDir()
	var out, errOut bytes.Buffer
	app := App{
		StorageDir: t.TempDir(),
		IO: IO{
			Out: &out, Err: &errOut,
			Env: func(k string) string {
				if k == "HOME" || k == "USERPROFILE" {
					return home
				}
				return ""
			},
		},
	}
	return app, home
}

func TestPluginUsageAndUnknown(t *testing.T) {
	app, _ := newPluginApp(t)
	if code := runPlugin(app, nil, nil); code != 2 {
		t.Fatalf("no args code = %d", code)
	}
	if code := runPlugin(app, nil, []string{"bogus"}); code != 2 {
		t.Fatalf("unknown code = %d", code)
	}
}

func TestPluginListOnEmptyDir(t *testing.T) {
	app, _ := newPluginApp(t)
	code := runPlugin(app, nil, []string{"list"})
	if code != 0 {
		t.Fatalf("list code = %d, err = %s", code, app.IO.Err.(*bytes.Buffer).String())
	}
}

func TestPluginEnableDisable(t *testing.T) {
	app, _ := newPluginApp(t)
	// SetEnabled 对不存在的插件是幂等的（只写 state），不应 panic。
	if code := runPlugin(app, nil, []string{"enable", "some-plugin"}); code != 0 {
		t.Fatalf("enable code = %d, err = %s", code, app.IO.Err.(*bytes.Buffer).String())
	}
	if code := runPlugin(app, nil, []string{"disable", "some-plugin"}); code != 0 {
		t.Fatalf("disable code = %d", code)
	}
}

func TestPluginSubcommandInSurface(t *testing.T) {
	found := false
	for _, name := range subcommandOrder {
		if name == "plugin" {
			found = true
		}
	}
	if !found {
		t.Fatal("plugin missing from subcommandOrder")
	}
}
