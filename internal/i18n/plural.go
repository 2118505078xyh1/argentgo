package i18n

// Plural picks the grammatical form that matches count n for locale.
//
// Forms are passed in the order [singular, plural]. English — and any locale
// that Normalize falls back to — uses forms[0] for exactly one item and
// forms[1] for every other count (zero, negatives and plural alike). Chinese
// has no plural distinction, so forms[0] is returned for every count; callers
// normally pass a single neutral form for that locale.
//
// With no forms Plural returns "". A missing plural form falls back to
// forms[0], so a caller that provides only one form always gets that form.
// Forms beyond the first two are ignored.
//
// Selection is pure: it does no catalog lookup, records nothing in Missing and
// does not format the forms themselves. A caller that wants the count embedded
// typically follows with fmt.Sprintf, e.g.
//
//	form := Plural(locale, n,
//	    T(locale, "activity.command_one"),
//	    T(locale, "activity.command_many"))
//	line := fmt.Sprintf(form, n)
func Plural(locale string, n int, forms ...string) string {
	if len(forms) == 0 {
		return ""
	}
	if Normalize(locale) == ZhCN {
		return forms[0] // CJK has no plural: one neutral form covers every count.
	}
	if n == 1 || len(forms) < 2 {
		return forms[0]
	}
	return forms[1]
}
