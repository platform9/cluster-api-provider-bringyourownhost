---
name: gh-actions-log-triage
description: Fast, targeted way to find the actual error in a failed GitHub Actions run, instead of dumping full noisy logs. Use whenever the user pastes a GitHub Actions run/job URL (`.../actions/runs/<run_id>/job/<job_id>`), a PR URL/number with a failing check, or asks to check/debug/investigate a CI run, a failing GitHub Actions job, or "why did this build fail". Also applies when comparing a failure against main to determine if it's pre-existing or introduced by the current PR.
---

# GH Actions log triage

Goal: get to the failing line(s) in 2-3 commands, without ever paging through a full raw log by eye or re-fetching the same log twice.

## Step 0 — find the run/job

- Given a run/job URL (`.../actions/runs/<run_id>/job/<job_id>`): parse `<run_id>` and `<job_id>` straight out of it. Don't ask the user to repeat numbers already in the URL.
- Given a PR (number or URL) instead: `gh pr view <n> --repo <owner>/<repo> --json statusCheckRollup` lists every check with its conclusion and a `detailsUrl` containing the run id and job id — use this to jump straight to the failing job without an extra `gh run view` round-trip.

## Step 1 — get the job tree

```
gh run view <run_id> --repo <owner>/<repo>
```

Cheap call. Shows ✓/✗ per job/step and each job's numeric ID. Confirms which job and which named step actually failed (e.g. "✗ Run e2e tests") before spending time on logs.

## Step 2 — save the log once

```
gh run view <run_id> --repo <owner>/<repo> --job=<job_id> --log > /tmp/<run_id>-<job_id>.log
```

Fetch to a file a single time, then grep the file as many times as needed. Don't re-run `gh run view --log` per grep attempt — it re-hits the API for no reason and is slow.

Don't reach for `--log-failed` as the first move: it dumps every step of every failed job (including verbose, irrelevant `actions/checkout` output) grouped under an unhelpful `UNKNOWN STEP` label, and when its output is long enough to redirect to a file, some terminals/wrappers only show a truncated preview rather than the full content — always grep the file you saved, not a preview.

## Step 3 — find the failure(s) in the saved log

Always use `grep -a` (or `LC_ALL=C grep`) on these logs, never plain `grep`. `gh run view --log` output carries ANSI escape codes / control bytes, so on macOS/BSD `grep` a saved log gets classified as binary and a plain `grep "FAILED" file.log` returns **zero matches with no warning or error**. That looks exactly like "no failures in this log" — a silent false negative that's easy to miss and worse than getting an obvious error. Every command below assumes `-a`.

**Ginkgo-based suites** (this repo's e2e, agent, and controller test suites all use Ginkgo/Gomega, and it's the dominant pattern for Go test suites generally):

```
grep -na '\[FAILED\]' /tmp/<run_id>-<job_id>.log
```

Each match is a failing spec with its file:line right there — a good anchor for `grep -a -A 30` if more context is needed. This also works for assertion failures that aren't really about Ginkgo itself (e.g. an RPM file-list mismatch, a diff comparison) — Gomega nests the underlying diff inside the same `[FAILED]` block, so the same grep still finds it.

To filter spec-level failures (the ones with a `_test.go` line, i.e. an actual test assertion) out from framework-internal failures (e.g. a failure inside `sigs.k8s.io/cluster-api/test@.../controlplane_helpers.go`, a helper timing out rather than a real assertion), narrow further:

```
grep -a 'in \[It\]' /tmp/<run_id>-<job_id>.log | grep -a '_test.go'
```

Near the very end of the run, Ginkgo also prints a "Summarizing N Failures" block — a compact index of every failure with its location:

```
grep -na -A "$((N*4))" 'Summarizing' /tmp/<run_id>-<job_id>.log
```

(check N from the summary line itself, or just use a generous fixed count like 60 if unsure).

**Non-Ginkgo output**: grep the literal error marker the tool actually emits — `Error:`, `FAIL`, `panic:`, `exit status` — rather than anchoring on the step name plus a guessed line count.

**Avoid** `awk -F'\t'` field-splitting on this log format. It looks like `<job>\t<step>\t<timestamp> <message>`, but ANSI color codes and embedded tabs in captured stdout make real lines not tokenize as expected — it can look reasonable and silently return empty, same failure mode as skipping `-a` on grep. Plain `grep -a` on the raw text is more reliable than trying to parse the tabular structure.

## Optional — is this failure pre-existing on main?

Don't assume a failure is caused by the PR — check main first. Two failures can look identical on a PR run and have completely different causes (one pre-existing/known-broken, one a flake that also failed on `main` yesterday, one actually new).

1. Find a comparable main run: `gh run list --branch main --workflow <workflow-name> --repo <owner>/<repo>`.
2. Check job existence, not just conclusion — a job can be new on the PR branch and never have run on `main` at all, which itself proves the failure was introduced by the PR (nothing to compare).
3. If the job existed on both: save both logs (Step 1-2 above) and grep both for the same `[FAILED]`/error-marker line, then diff the exact error strings — a matching string on repeated `main` runs means pre-existing; a string absent from `main` means PR-introduced. Don't just compare high-level pass/fail conclusions, since a flaky pre-existing failure and a newly introduced one can both show up as "✗" on the PR.
