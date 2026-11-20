package config

import "strings"

// replyLanguageDefault is the language used when neither the explicit locale
// nor any probed environment variable names a supported language.
const replyLanguageDefault = "en"

// replyLanguageEnvOrder is the order aider-style auto-detection probes the
// process environment. The first value that names a known language wins; an
// unknown value falls through to the next variable.
var replyLanguageEnvOrder = [...]string{"LANG", "LANGUAGE", "LC_ALL", "LC_MESSAGES"}

// replyLanguageAliases is a deliberately small, local table mapping a
// lower-cased locale, tag or language name to a short canonical code. config
// must not depend on internal/i18n, so this mirrors i18n.Canonical's idea
// without importing it. The short codes ("zh", "en", ...) are what the app
// turns into a human-readable "Reply in X" phrase.
var replyLanguageAliases = map[string]string{
	// English, including the C/POSIX pseudo-locales.
	"c": "en", "posix": "en", "en": "en", "eng": "en", "english": "en",
	// Chinese.
	"zh": "zh", "chi": "zh", "zho": "zh", "chinese": "zh",
	"中文": "zh", "简体中文": "zh", "繁體中文": "zh", "zh-hans": "zh", "zh-hant": "zh",
	// Japanese.
	"ja": "ja", "jpn": "ja", "japanese": "ja", "日本語": "ja",
	// Korean.
	"ko": "ko", "kor": "ko", "korean": "ko", "한국어": "ko",
	// A few common Western languages so the table is not CJK-only.
	"ru": "ru", "rus": "ru", "russian": "ru",
	"de": "de", "deu": "de", "ger": "de", "german": "de",
	"fr": "fr", "fra": "fr", "fre": "fr", "french": "fr",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"es": "es", "spa": "es", "spanish": "es",
	"ca": "ca", "cat": "ca", "catalan": "ca",
}

// canonicalReplyLanguage normalises one raw locale value into a short code.
// POSIX encoding/modifier suffixes (".UTF-8", "@euro") and BCP-47
// region/script subtags are stripped before the alias table is consulted, so
// values such as "zh_Hant_TW.UTF-8@euro" still map to "zh". "auto" and empty
// values are not languages and report ok == false.
func canonicalReplyLanguage(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.EqualFold(value, "auto") {
		return "", false
	}
	lower := strings.ToLower(value)
	if i := strings.IndexAny(lower, ".@"); i >= 0 {
		lower = lower[:i]
	}
	if code, ok := replyLanguageAliases[lower]; ok {
		return code, true
	}
	if i := strings.IndexAny(lower, "-_"); i >= 0 {
		if code, ok := replyLanguageAliases[lower[:i]]; ok {
			return code, true
		}
	}
	return "", false
}

// DetectReplyLanguage picks the language code for the app's "Reply in X"
// system-prompt injection. It is a pure function: it reads no config and no
// global state, touching only the two arguments it is given.
//
// An explicit locale wins. "auto", empty or unknown explicit locales fall
// through to the environment, probed in LANG, LANGUAGE, LC_ALL, LC_MESSAGES
// order. The first value naming a supported language is returned; if none
// does, replyLanguageDefault ("en") is returned. A nil env map is safe.
//
// This helper is intentionally config-local (no internal/i18n dependency) and
// does not change how Config.Locale is resolved. Wiring it into the app's
// prompt is left to the owner of internal/app.
func DetectReplyLanguage(locale string, env map[string]string) string {
	if code, ok := canonicalReplyLanguage(locale); ok {
		return code
	}
	for _, name := range replyLanguageEnvOrder {
		if code, ok := canonicalReplyLanguage(env[name]); ok {
			return code
		}
	}
	return replyLanguageDefault
}

// replyLanguageDisplayNames maps a canonical reply-language code to the
// language's native display name. config must not depend on internal/i18n, so
// these native names mirror i18n's output-language table locally; the codes are
// exactly the ones DetectReplyLanguage / canonicalReplyLanguage produce, which
// lets callers round-trip DetectReplyLanguage -> LocaleDisplayName.
var replyLanguageDisplayNames = map[string]string{
	"en": "English",
	"zh": "中文",
	"ja": "日本語",
	"ko": "한국어",
	"ru": "Русский",
	"de": "Deutsch",
	"fr": "Français",
	"pt": "Português",
	"es": "Español",
	"ca": "Català",
}

// LocaleDisplayName returns the native display name for a reply-language code
// or locale alias (e.g. "zh" -> "中文", "eng" -> "English", "中文" -> "中文").
// The app may inject the result into a system prompt as the language the model
// should reply in. It is a pure function: no config or global state is read.
//
// Input is normalised through canonicalReplyLanguage first, so aliases,
// encoding/script/region subtags and native names all resolve to the same
// display name. An empty, "auto", unknown or unsupported value yields "" so a
// caller can omit the directive rather than naming a language the user did not
// ask for.
func LocaleDisplayName(code string) string {
	canonical, ok := canonicalReplyLanguage(code)
	if !ok {
		return ""
	}
	return replyLanguageDisplayNames[canonical]
}
