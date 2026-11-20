package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/memory"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// buildSystem constructs the system prompt for existing callers (notably
// Resolve). Project instructions have always been injected unconditionally, so
// this wrapper keeps that legacy default: AGENTS.md/CLAUDE.md survive unless a
// caller explicitly opts into the project boundary through
// ARGENT_TRUST_PROJECT (see parseTrustEnv for the recognised false values).
// The trust-aware implementation lives in buildSystemWithTrust; see that
// function for the full semantic.
func buildSystem(root string, env func(string) string, thinking, schema string) string {
	return buildSystemWithTrust(root, env, trustFromEnv(env), thinking, schema)
}

// trustFromEnv derives the project-instruction trust decision for the legacy
// entry points. It delegates to parseTrustEnv so the prompt path and Resolve's
// Resolved.TrustProject decision share one interpretation of
// ARGENT_TRUST_PROJECT: 0/false/no/off are untrusted, 1/true/yes/on are
// trusted, and an unset value defaults to trusted to preserve the historical
// "always inject AGENTS.md" behaviour.
func trustFromEnv(env func(string) string) bool {
	if env == nil {
		return true
	}
	return parseTrustEnv(env("ARGENT_TRUST_PROJECT"))
}

// buildStaticSystem builds the cache-stable portion of the system prompt: the
// persona, tool-use guidance, project instructions and skill index. It
// deliberately excludes every process-specific value (cwd, reasoning effort,
// output schema) so the bytes the provider caches do not change when a session
// moves to a different directory or the thinking level is adjusted. Callers
// that inject those values as a user-role message pair this with
// buildDynamicContext; buildSystemWithTrust composes both for legacy callers.
func buildStaticSystem(root string, env func(string) string, trust bool) string {
	var b strings.Builder
	b.WriteString("You are argent, a concise coding assistant working on the user's machine.\n")
	b.WriteString("Use the available tools to inspect and change files. Read before writing, ")
	b.WriteString("batch independent calls, and stop once the request is satisfied.\n")

	if docs := projectMemoryDocs(root, env, trust); len(docs) > 0 {
		if block := memory.Block(docs); strings.TrimSpace(block) != "" {
			b.WriteString("\n")
			b.WriteString(block)
		}
	}
	// Skills are discovered through the shared cache; pass nil command roots
	// because only the registry is used here. The fingerprint re-discovers on
	// disk changes, so repeated prompt builds collapse to a stat-only hit.
	_, registry := sharedDefs().Get(nil, skills.DefaultRoots(root, homeDir(env)))
	if registry.Len() > 0 {
		b.WriteString("\n")
		b.WriteString(registry.IndexText())
	}
	return b.String()
}

// buildDynamicContext renders the per-turn values that must stay out of the
// cached system prefix: the working directory, the reasoning effort and the
// optional output schema. It is intentionally a user-role payload (no role is
// baked in): the caller injects it as its own message or prepends it to the
// latest user message, so only this short block is re-sent when a value changes.
// The wording matches buildSystemWithTrust exactly, so moving it does not
// change what the model is told.
func buildDynamicContext(root, thinking, schema string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Working directory: %s\n", root)
	if effort := strings.TrimSpace(thinking); effort != "" {
		fmt.Fprintf(&b, "Reasoning effort: %s. Spend accordingly.\n", effort)
	}
	if strings.TrimSpace(schema) != "" {
		b.WriteString("Reply with a single JSON document that conforms to this JSON Schema:\n")
		b.WriteString(strings.TrimSpace(schema))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildSystemWithTrust is the trust-aware prompt builder. When trust is false,
// memory.LoadWithBoundary collects no project documents (only user-level ones);
// when true, it collects AGENTS.md/CLAUDE.md from the project root down to the
// working directory, honouring AGENTS.override.md. .argentignore rules are
// applied afterwards as an advisory context-size filter, never as a security
// control.
//
// It composes buildStaticSystem and buildDynamicContext so both call sites keep
// a single source of truth for the wording. Callers that want a stable cache
// prefix should use the two helpers directly and inject the dynamic half as a
// user message instead of using this combined form.
func buildSystemWithTrust(root string, env func(string) string, trust bool, thinking, schema string) string {
	static := buildStaticSystem(root, env, trust)
	dynamic := buildDynamicContext(root, thinking, schema)
	if dynamic == "" {
		return static
	}
	return static + "\n" + dynamic + "\n"
}

// projectMemoryDocs loads, orders and filters the instruction documents that
// are injected into the system prompt. It is shared by the prompt builder and
// the /context report so both describe the same inputs.
func projectMemoryDocs(root string, env func(string) string, trust bool) []memory.Doc {
	insts, err := memory.LoadWithBoundary(root, trust, memory.LoadOptions{Home: homeDir(env)})
	if err != nil {
		return nil
	}
	docs := memory.InstructionsToDocs(insts)
	// LoadWithBoundary returns root-first, but Block's header promises entries
	// from the most specific (nearest) file to the least specific. Reverse so
	// the rendered block stays accurate.
	for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
		docs[i], docs[j] = docs[j], docs[i]
	}
	return filterIgnoredDocs(root, docs)
}

// filterIgnoredDocs drops documents whose path is excluded by an advisory
// .argentignore rule. This is a context-size convenience only: a false
// negative leaves extra text in the prompt, never a security hole. Discovery
// is best-effort, so an unreadable ignore file keeps the original documents.
func filterIgnoredDocs(root string, docs []memory.Doc) []memory.Doc {
	if len(docs) == 0 {
		return nil
	}
	rules, err := memory.LoadIgnore(root)
	if err != nil || rules == nil {
		return docs
	}
	out := docs[:0]
	for _, doc := range docs {
		rel, err := filepath.Rel(root, doc.Path)
		if err == nil && rules.Ignored(rel) {
			continue
		}
		out = append(out, doc)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// loadOutputSchema accepts inline schema text or a file path.
func loadOutputSchema(root, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	text := value
	if !strings.HasPrefix(value, "{") {
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("--output-schema: %w", err)
		}
		text = string(data)
	}
	if !json.Valid([]byte(text)) {
		return "", fmt.Errorf("--output-schema: the schema is not valid JSON")
	}
	return text, nil
}
