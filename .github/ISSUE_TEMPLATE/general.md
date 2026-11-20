---
name: General issue
about: Report a bug, propose a feature, or flag a documentation problem
title: ''
labels: ''
assignees: ''
---

<!--
Pick the section that matches your issue and fill it in. Delete the others.
Redact secrets, tokens, real e-mail addresses and local/private paths before
posting. For questions and discussion, use the Discussions tab instead.
-->

## What kind of issue is this?

- [ ] **Bug** — something does not work as expected
- [ ] **Feature request** — an idea or improvement
- [ ] **Documentation / question** — a docs problem or a usage question

---

## Bug report

**Describe the bug**
A clear and concise description of what the bug is.

**To reproduce**
Steps to reproduce the behavior:
1. Run `argent ...` / open the native window and ...
2. ...
3. See error

**Expected behavior**
What you expected to happen.

**Actual behavior**
What actually happened. Include relevant log lines (redact any secrets, tokens,
emails or local paths).

---

## Feature request

**Problem**
What problem does this solve? What are you trying to do that is hard today?

**Proposed behavior**
Describe the behavior you would like. If it is a UI change in the native
client, mention where it appears (sidebar / transcript / composer / approval).

**Why it fits argentgo**
argentgo is a local, offline-first, stdlib-only Go harness shipped as a single
Windows binary. Explain how the idea fits those constraints. Ideas requiring a
third-party core dependency, cgo, or a hosted service are likely out of scope.

**Alternatives considered**
Other approaches you thought about.

---

## Documentation / question

**What is unclear or wrong**
Point to the file or the command / UI step, and describe the confusion.

**What you expected to find**
What you were looking for.

**Suggested improvement** (optional)
How you would word it or where you would add it.

---

## Environment (bugs and docs issues)

- argentgo version (`argent --version`):
- OS / Windows version:
- Go version (if building from source):
- Frontend: native MyGo window / CLI / TUI / `--rpc`:
- Where (docs issues): README / docs/ / CLI help / native window:

## Additional context

Anything else that may help — e.g. whether a session file was left open or a
checkpoint exists; links, examples from other tools, or screenshots (no personal
data).
