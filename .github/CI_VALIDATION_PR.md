# CI validation PR

This branch exists only to keep a PR open against `main` so that
`pull_request`-triggered checks (CI, and anything chained off it via
`workflow_run`) can be observed running for real.

Workflow:
- Real changes land on `main` directly (fast feedback, no PR round-trip).
- After each push to `main`, this branch gets rebased onto the new tip and
  force-pushed, keeping this PR's diff to just this file.
- Do not merge this PR.
