package i18n

// HasPlaceholderConflict reports whether text mixes the two placeholder
// styles this package supports: positional fmt verbs such as %s or %d, which
// T formats via fmt.Sprintf, and named {name} placeholders, which Format
// expands.
//
// A string containing both styles cannot be formatted correctly. Passing it
// through T leaves every {name} verbatim (and hands stray braces to
// Sprintf), while passing it through Format leaves every %s verbatim. A
// catalog entry that mixes them is therefore a translation bug; callers can
// use this predicate in a test or a doctor command to surface one before it
// reaches the UI. It is additive and does not change how T or Format behave.
//
// Escaping is respected, using the same scanners as the translation audit:
// "%%" is a literal percent, and "{{name}}" / "}}" are literal braces. Such
// text does not count as a placeholder. An invalid brace shape such as
// "{a-b}" likewise does not count.
//
// The check is pure: it reads no catalog or global state, and records nothing
// in Missing.
func HasPlaceholderConflict(text string) bool {
	return len(formatVerbs(text)) > 0 && len(namedPlaceholders(text)) > 0
}
