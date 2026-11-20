package commands

// Built-in slash-command registration for the app layer.
//
// Discover only reads markdown files from disk; built-in descriptors such as
// /context and /review have no file, so callers must register them explicitly.
// NewSetWithBuiltins keeps that step in one place: app builds the set through
// this helper and routes the matching slash input to the built-in hook. The
// `argent commands` listing path in internal/cli stays unchanged and keeps
// listing only on-disk commands, so no user-visible output changes.

// NewSetWithBuiltins discovers commands from sources and registers the built-in
// descriptors (currently /context and /review). Explicit markdown commands win
// by name because Add never overwrites an existing entry.
func NewSetWithBuiltins(sources []Source) *Set {
	set := Discover(sources)
	set.Add(ContextCommand(), ReviewCommand())
	return set
}
