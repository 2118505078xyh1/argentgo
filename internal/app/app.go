// Package app wires the migrated packages into the CLI: configuration
// resolution, provider selection, tool registry, security chain, session
// storage and the agent loop.
//
// This is the only package that knows about every other package; keep it free
// of behaviour beyond construction.
//
// The implementation is split by responsibility across the app_*.go files:
//   - app_types.go: shared resolved-option and provider types
//   - app_resolve.go: configuration/env resolution and numeric helpers
//   - app_chatter.go: LLM client construction and fallback wrapper
//   - app_prompt.go: system prompt and output-schema loading
//   - app_runner.go: runner factory and cli.Runner implementation
//   - app_execute.go: the per-run Execute pipeline and approvers
package app
