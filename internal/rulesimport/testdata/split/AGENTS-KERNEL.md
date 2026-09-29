# Synthetic kernel

## Git

- 🔴 Never force-push the default branch.
  Why: it rewrites shared history.
  The remote keeps the old commits.

- 🟡 Ask before rewriting local commits.
  Why: local history is recoverable.
  Details: Reset the branch to the previous commit, then cherry-pick.

## Hard safety

- 🟡 Ask before deleting generated files.
  Why: generated files can be rebuilt.

## Review

- Prefer small commits.
  Why: they are easier to read.
  roles: reviewer
  harness: codex
