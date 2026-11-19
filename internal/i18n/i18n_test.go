package i18n

import (
	"strings"
	"testing"
)

func TestTranslatesBothLocales(t *testing.T) {
	if got := T(EnUS, "prompt.send"); got != "send" {
		t.Errorf("en = %q", got)
	}
	if got := T(ZhCN, "prompt.send"); got != "发送" {
		t.Errorf("zh = %q", got)
	}
	if got := T("zh-CN", "approval.denied"); got != "已拒绝" {
		t.Errorf("zh alias = %q", got)
	}
}

func TestUnknownLocaleFallsBackToDefault(t *testing.T) {
	if got := T("fr-FR", "prompt.send"); got != "send" {
		t.Errorf("got %q", got)
	}
	if got := T("", "status.ready"); got != "ready" {
		t.Errorf("got %q", got)
	}
}

func TestMissingKeyReturnsKeyAndIsRecorded(t *testing.T) {
	ResetMissing()
	if got := T(EnUS, "no.such.key"); got != "no.such.key" {
		t.Errorf("got %q", got)
	}
	// A second lookup must not duplicate the record.
	_ = T(ZhCN, "no.such.key")
	missing := Missing()
	if len(missing) != 1 || missing[0] != "no.such.key" {
		t.Errorf("missing = %#v", missing)
	}
	ResetMissing()
	if len(Missing()) != 0 {
		t.Errorf("reset failed: %#v", Missing())
	}
}

func TestFormattingArgs(t *testing.T) {
	if got := T(EnUS, "error.model", "boom"); got != "model error: boom" {
		t.Errorf("got %q", got)
	}
	if got := T(ZhCN, "info.skills_loaded", 3); got != "可用技能 3 个" {
		t.Errorf("got %q", got)
	}
}

func TestCatalogIsComplete(t *testing.T) {
	keys := Keys()
	if len(keys) < 60 {
		t.Fatalf("catalog has %d keys, want at least 60", len(keys))
	}
	for _, key := range keys {
		entry := catalog[key]
		if entry.en == "" {
			t.Errorf("%s: missing en", key)
		}
		if entry.zh == "" {
			t.Errorf("%s: missing zh", key)
		}
	}
}

func TestKnownLocales(t *testing.T) {
	known := Known()
	if len(known) != 2 || known[0] != EnUS || known[1] != ZhCN {
		t.Errorf("known = %#v", known)
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"argent locale wins", map[string]string{"ARGENT_LOCALE": "zh-CN", "LANG": "en_US.UTF-8"}, ZhCN},
		{"english lang", map[string]string{"LANG": "en_US.UTF-8"}, EnUS},
		{"chinese lang tag", map[string]string{"LANG": "zh_CN.UTF-8"}, ZhCN},
		{"lc_all first", map[string]string{"LC_ALL": "zh_CN.UTF-8", "LANG": "en_US.UTF-8"}, ZhCN},
		{"invalid argent locale falls through", map[string]string{"ARGENT_LOCALE": "klingon", "LANG": "zh_CN"}, ZhCN},
		{"nothing set", map[string]string{}, DefaultLocale},
		{"empty value", map[string]string{"ARGENT_LOCALE": "  "}, DefaultLocale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(name string) string { return tc.env[name] }
			if got := Detect(env); got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
	if got := Detect(nil); got != DefaultLocale {
		t.Errorf("nil env = %q", got)
	}
}

func TestCanonicalAliases(t *testing.T) {
	for _, alias := range []string{"en", "en-US", "en_us", "English"} {
		if code, ok := Canonical(alias); !ok || code != EnUS {
			t.Errorf("%q -> %q, %v", alias, code, ok)
		}
	}
	for _, alias := range []string{"zh", "zh-CN", "zh_cn", "中文"} {
		if code, ok := Canonical(alias); !ok || code != ZhCN {
			t.Errorf("%q -> %q, %v", alias, code, ok)
		}
	}
	if _, ok := Canonical("auto"); ok {
		t.Error("auto is not a locale")
	}
}

func TestNoCatalogKeyLooksLikeAProseSentence(t *testing.T) {
	for _, key := range Keys() {
		if strings.ContainsAny(key, " \t") {
			t.Errorf("key %q contains whitespace", key)
		}
	}
}
