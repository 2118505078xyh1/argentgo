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
// caller explicitly opts into the project boundary with
// ARGENT_TRUST_PROJECT=0. The trust-aware implementation lives in
// buildSystemWithTrust; see that function for the full semantic.
func buildSystem(root string, env func(string) string, thinking, schema string) string {
	return buildSystemWithTrust(root, env, trustFromEnv(env), thinking, schema)
}

// trustFromEnv derives the project-instruction trust decision for the legacy
// entry points. The default is true so existing projects keep their AGENTS.md
// in the prompt (no regression). An explicit "0" opts into the trust boundary
// and drops project-level documents; "1" and every other value keep them.
// Resolve uses the same variable for Resolved.TrustProject, so the two paths
// agree on what "untrusted" means.
func trustFromEnv(env func(string) string) bool {
	if env == nil {
		return true
	}
	return strings.TrimSpace(env("ARGENT_TRUST_PROJECT")) != "0"
}

// buildSystemWithTrust is the trust-aware prompt builder. When trust is false,
// memory.LoadWithBoundary collects no project documents (only user-level ones);
// when true, it collects AGENTS.md/CLAUDE.md from the project root down to the
// working directory, honouring AGENTS.override.md. .argentignore rules are
// applied afterwards as an advisory context-size filter, never as a security
// control.
func buildSystemWithTrust(root string, env func(string) string, trust bool, thinking, schema string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are argent, a concise coding assistant working on the user's machine.\nWorking directory: %s\n", root)
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
	if effort := strings.TrimSpace(thinking); effort != "" {
		fmt.Fprintf(&b, "\nReasoning effort: %s. Spend accordingly.\n", effort)
	}
	if strings.TrimSpace(schema) != "" {
		b.WriteString("\nReply with a single JSON document that conforms to this JSON Schema:\n")
		b.WriteString(strings.TrimSpace(schema))
		b.WriteString("\n")
	}
	return b.String()
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
